package forward

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// Sender delivers batches of records to one destination.
type Sender interface {
	// Send delivers recs. rejected counts records the destination refused
	// for good (they are not retried); an error means retry the batch.
	Send(ctx context.Context, recs []Record) (rejected int, err error)
	// Limits is the largest batch to hand to Send.
	Limits() (records, bytes int)
	Close()
}

// errTooLarge asks the caller to split the batch.
var errTooLarge = errors.New("batch too large for the destination")

// NewSender builds the sender for a destination.
func NewSender(d store.ForwardDestination) (Sender, error) {
	s := d.Settings
	tlsCfg, err := tlsConfig(s)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsCfg,
		MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true}}
	switch d.Kind {
	case store.FwdSentinel:
		return newSentinel(s, hc)
	case store.FwdElastic:
		if err := needURL(s.URL); err != nil {
			return nil, err
		}
		return &elastic{s: s, hc: hc}, nil
	case store.FwdSplunk:
		if err := needURL(s.URL); err != nil {
			return nil, err
		}
		if s.Token == "" {
			return nil, errors.New("a HEC token is required")
		}
		return &splunk{s: s, hc: hc}, nil
	case store.FwdWebhook:
		if err := needURL(s.URL); err != nil {
			return nil, err
		}
		h, err := parseHeaders(s.Headers)
		if err != nil {
			return nil, err
		}
		return &webhook{url: s.URL, headers: h, ecs: s.Format != "native", hc: hc}, nil
	case store.FwdLogstash:
		if s.Transport == "http" {
			if err := needURL(s.URL); err != nil {
				return nil, err
			}
			h := http.Header{}
			if s.Username != "" {
				h.Set("Authorization", "Basic "+basic(s.Username, s.Password))
			}
			return &webhook{url: s.URL, headers: h, ecs: true, hc: hc}, nil
		}
		st, err := newStream(s, tlsCfg, "newline")
		if err != nil {
			return nil, err
		}
		st.format = func(r *Record) ([]byte, error) { return json.Marshal(ecsDoc(r, "")) }
		return st, nil
	case store.FwdSyslog:
		st, err := newStream(s, tlsCfg, s.Framing)
		if err != nil {
			return nil, err
		}
		if s.Format == "cef" {
			st.format = func(r *Record) ([]byte, error) { return []byte(cef(r)), nil }
		} else {
			st.format = func(r *Record) ([]byte, error) { return []byte(rfc5424(r)), nil }
		}
		return st, nil
	}
	return nil, fmt.Errorf("unknown destination type %q", d.Kind)
}

func needURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("enter a full URL, e.g. https://host:9200")
	}
	return nil
}

func tlsConfig(s store.ForwardSettings) (*tls.Config, error) {
	c := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !s.VerifyTLS}
	if strings.TrimSpace(s.CACert) != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(s.CACert)) {
			return nil, errors.New("the CA certificate is not valid PEM")
		}
		c.RootCAs = pool
	}
	return c, nil
}

func parseHeaders(s string) (http.Header, error) {
	h := http.Header{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("header %q must look like Name: value", line)
		}
		h.Add(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return h, nil
}

// httpError describes a failed request with a short excerpt of the body.
func httpError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		return errTooLarge
	}
	msg := strings.TrimSpace(string(b))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
}

func post(ctx context.Context, hc *http.Client, u string, body []byte, h http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	return hc.Do(req)
}

// ---------------------------------------------------------------- Elasticsearch / OpenSearch

type elastic struct {
	s  store.ForwardSettings
	hc *http.Client
}

func (e *elastic) dataStream() string { return first(e.s.DataStream, "logs-unifi.syslog-default") }

func (e *elastic) headers() http.Header {
	h := http.Header{"Content-Type": {"application/x-ndjson"}}
	switch e.s.ESAuth {
	case "api_key":
		h.Set("Authorization", "ApiKey "+e.s.APIKey)
	case "basic":
		h.Set("Authorization", "Basic "+basic(e.s.Username, e.s.Password))
	}
	return h
}

func (e *elastic) Limits() (int, int) { return 5000, 5 << 20 }
func (e *elastic) Close()             {}

func (e *elastic) Send(ctx context.Context, recs []Record) (int, error) {
	ds := e.dataStream()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for i := range recs {
		// A fixed _id makes a retried batch idempotent: documents that were
		// already indexed answer 409 instead of being stored twice.
		enc.Encode(obj{"create": obj{"_index": ds, "_id": recs[i].ID}})
		enc.Encode(ecsDoc(&recs[i], ds))
	}
	u := strings.TrimRight(e.s.URL, "/") + "/_bulk?filter_path=errors,items.*.status,items.*.error.type,items.*.error.reason"
	resp, err := post(ctx, e.hc, u, b.Bytes(), e.headers())
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, httpError(resp)
	}
	var out struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int `json:"status"`
			Error  struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"error"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return 0, fmt.Errorf("bulk response: %w", err)
	}
	if !out.Errors {
		return 0, nil
	}
	rejected, retry := 0, 0
	var reason string
	for _, it := range out.Items {
		for _, r := range it {
			switch {
			case r.Status < 300 || r.Status == http.StatusConflict:
			case r.Status == http.StatusTooManyRequests || r.Status >= 500:
				retry++
				reason = first(reason, r.Error.Type+": "+r.Error.Reason)
			default:
				rejected++
				reason = first(reason, r.Error.Type+": "+r.Error.Reason)
			}
		}
	}
	if retry > 0 {
		return 0, fmt.Errorf("%d of %d documents must be retried (%s)", retry, len(recs), reason)
	}
	return rejected, rejectedError(rejected, reason)
}

// rejectedError is returned together with a rejected count so the reason
// shows on the status page; the batch counts as delivered.
type rejectedErr struct{ msg string }

func (e rejectedErr) Error() string { return e.msg }

func rejectedError(n int, reason string) error {
	if n == 0 {
		return nil
	}
	return rejectedErr{fmt.Sprintf("%d documents rejected: %s", n, reason)}
}

// InstallTemplate creates or updates the index template for the data
// stream of an Elasticsearch or OpenSearch destination.
func InstallTemplate(ctx context.Context, d store.ForwardDestination, template []byte) (string, error) {
	snd, err := NewSender(d)
	if err != nil {
		return "", err
	}
	e, ok := snd.(*elastic)
	if !ok {
		return "", errors.New("only Elasticsearch and OpenSearch destinations use an index template")
	}
	var tpl obj
	if err := json.Unmarshal(template, &tpl); err != nil {
		return "", err
	}
	ds := e.dataStream()
	name, pattern := "logs-unifi.syslog", "logs-unifi.syslog-*"
	if t, dset, _, ok := dataStreamParts(ds); ok {
		name, pattern = t+"-"+dset, t+"-"+dset+"-*"
	} else {
		name, pattern = ds, ds+"*"
	}
	tpl["index_patterns"] = []string{pattern}
	body, _ := json.Marshal(tpl)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(e.s.URL, "/")+"/_index_template/"+url.PathEscape(name), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header = e.headers()
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", httpError(resp)
	}
	return name, nil
}

// ---------------------------------------------------------------- Splunk HEC

type splunk struct {
	s  store.ForwardSettings
	hc *http.Client
}

func (sp *splunk) Limits() (int, int) { return 2000, 800 << 10 }
func (sp *splunk) Close()             {}

func (sp *splunk) Send(ctx context.Context, recs []Record) (int, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	for i := range recs {
		r := &recs[i]
		ev := obj{"time": float64(r.TS.UnixMilli()) / 1000, "host": first(r.Hostname, r.SourceIP), "source": "unifi:" + first(r.Category, "syslog"),
			"sourcetype": first(sp.s.SourceType, "unifi:syslog"), "event": r}
		if sp.s.Index != "" {
			ev["index"] = sp.s.Index
		}
		enc.Encode(ev)
	}
	u := strings.TrimRight(sp.s.URL, "/")
	if !strings.Contains(u, "/services/collector") {
		u += "/services/collector/event"
	}
	resp, err := post(ctx, sp.hc, u, b.Bytes(), http.Header{"Authorization": {"Splunk " + sp.s.Token}, "Content-Type": {"application/json"}})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, httpError(resp)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return 0, nil
}

// ---------------------------------------------------------------- JSON webhook / Logstash HTTP

type webhook struct {
	url     string
	headers http.Header
	ecs     bool
	hc      *http.Client
}

func (w *webhook) Limits() (int, int) { return 1000, 1 << 20 }
func (w *webhook) Close()             {}

func (w *webhook) Send(ctx context.Context, recs []Record) (int, error) {
	docs := make([]any, len(recs))
	for i := range recs {
		if w.ecs {
			docs[i] = ecsDoc(&recs[i], "")
		} else {
			docs[i] = &recs[i]
		}
	}
	body, err := json.Marshal(docs)
	if err != nil {
		return 0, err
	}
	h := w.headers.Clone()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	resp, err := post(ctx, w.hc, w.url, body, h)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, httpError(resp)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return 0, nil
}

// ---------------------------------------------------------------- syslog / Logstash TCP

// stream writes one message per record over UDP, TCP or TLS.
type stream struct {
	network, addr string
	tls           *tls.Config
	framing       string
	format        func(*Record) ([]byte, error)

	mu   sync.Mutex
	conn net.Conn
}

func newStream(s store.ForwardSettings, tlsCfg *tls.Config, framing string) (*stream, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s.Address))
	if err != nil || host == "" {
		return nil, errors.New("enter the address as host:port, e.g. graylog.example.com:6514")
	}
	if _, err := strconv.Atoi(port); err != nil {
		return nil, errors.New("enter the address as host:port, e.g. graylog.example.com:6514")
	}
	st := &stream{addr: net.JoinHostPort(host, port), framing: framing}
	switch s.Transport {
	case "udp":
		st.network = "udp"
	case "tls":
		st.network = "tcp"
		st.tls = tlsCfg.Clone()
		st.tls.ServerName = host
	default:
		st.network = "tcp"
	}
	return st, nil
}

func (s *stream) Limits() (int, int) { return 1000, 1 << 20 }

func (s *stream) dial(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if s.tls != nil {
		return (&tls.Dialer{NetDialer: d, Config: s.tls}).DialContext(ctx, s.network, s.addr)
	}
	return d.DialContext(ctx, s.network, s.addr)
}

func (s *stream) Send(ctx context.Context, recs []Record) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		c, err := s.dial(ctx)
		if err != nil {
			return 0, err
		}
		s.conn = c
	}
	var b bytes.Buffer
	for i := range recs {
		msg, err := s.format(&recs[i])
		if err != nil {
			return 0, err
		}
		if s.network == "udp" {
			if len(msg) > 65000 {
				msg = msg[:65000]
			}
			s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := s.conn.Write(msg); err != nil {
				s.reset()
				return 0, err
			}
			continue
		}
		msg = bytes.ReplaceAll(msg, []byte("\n"), []byte(" "))
		if s.framing == "octet" {
			b.WriteString(strconv.Itoa(len(msg)))
			b.WriteByte(' ')
			b.Write(msg)
		} else {
			b.Write(msg)
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		return 0, nil
	}
	s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := s.conn.Write(b.Bytes()); err != nil {
		s.reset()
		return 0, err
	}
	return 0, nil
}

func (s *stream) reset() {
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

func (s *stream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
