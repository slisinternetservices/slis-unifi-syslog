package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// peerKey holds the address of the TCP peer, before RealIP replaces
// RemoteAddr with a forwarded address (which a client can make up).
const peerKey ctxKey = 100

func keepPeer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), peerKey, r.RemoteAddr)))
	})
}

// peerIP is the address of the TCP peer.
func peerIP(r *http.Request) string {
	a, _ := r.Context().Value(peerKey).(string)
	if a == "" {
		a = r.RemoteAddr
	}
	return clientIP(&http.Request{RemoteAddr: a})
}

// grafanaUser authenticates the Grafana ClickHouse data source: the
// password (or a bearer header) is an API token of an administrator.
func (s *Server) grafanaUser(r *http.Request) (*store.User, bool) {
	tok := bearer(r)
	if tok == "" {
		_, tok, _ = r.BasicAuth()
	}
	if tok == "" {
		tok = r.Header.Get("X-ClickHouse-Key")
	}
	if tok == "" {
		return nil, false
	}
	t, u, err := s.Meta.TokenUser(tok)
	if err != nil {
		return nil, false
	}
	// Only administrators: a read query can still use table functions such
	// as url() with the rights of the ClickHouse user.
	if u.Role != store.RoleAdmin {
		return nil, false
	}
	s.Meta.TouchToken(t, clientIP(r))
	return u, true
}

// grafanaSettings are query parameters that may not come from the client.
var grafanaBlocked = map[string]bool{"user": true, "password": true, "readonly": true, "query_id": false}

// clickhouseProxy serves the ClickHouse HTTP interface read-only under
// /clickhouse/, for the Grafana ClickHouse data source (protocol HTTP,
// path /clickhouse). ClickHouse itself stays unreachable from the network.
func (s *Server) clickhouseProxy(w http.ResponseWriter, r *http.Request) {
	if !s.Meta.Monitoring().GrafanaEnabled {
		http.NotFound(w, r)
		return
	}
	u, ok := s.grafanaUser(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="unify-syslog"`)
		http.Error(w, "Code: 516. Authentication failed: use an API token of an administrator as the password", http.StatusUnauthorized)
		return
	}
	cfg := s.Logs.Config()
	target, err := url.Parse(cfg.HTTPURL)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	q := url.Values{}
	for k, v := range r.URL.Query() {
		if lk := strings.ToLower(k); grafanaBlocked[lk] || strings.HasPrefix(lk, "param_readonly") {
			continue
		}
		q[k] = v
	}
	// readonly=2 allows reads and setting changes, never writes or DDL, and
	// a query cannot lower it.
	q.Set("readonly", "2")
	if q.Get("database") == "" {
		q.Set("database", cfg.Database)
	}
	if q.Get("max_execution_time") == "" {
		q.Set("max_execution_time", "60")
	}
	sub := strings.TrimPrefix(r.URL.Path, "/clickhouse")
	if sub == "" {
		sub = "/"
	}
	switch sub {
	case "/", "/ping", "/query":
	default:
		http.NotFound(w, r)
		return
	}
	target.Path = strings.TrimRight(target.Path, "/") + sub
	target.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, h := range []string{"Content-Type", "Content-Encoding", "Accept-Encoding", "X-ClickHouse-Format", "X-ClickHouse-Database", "X-ClickHouse-Query-Id"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.ContentLength = r.ContentLength
	req.SetBasicAuth(cfg.Username, cfg.Password)
	resp, err := s.Logs.HTTPClient().Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		if strings.HasPrefix(k, "X-Clickhouse") || k == "Content-Type" || k == "Content-Encoding" {
			w.Header()[k] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	s.auditView(r, "grafana_query", u.Username)
}
