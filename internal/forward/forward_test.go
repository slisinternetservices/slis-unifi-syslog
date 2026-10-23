package forward

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

func sampleEvent() *unifi.Event {
	ts := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	return &unifi.Event{TS: ts, Received: ts, SourceIP: "10.0.0.1", Site: "HQ", Hostname: "UDM-Pro", DeviceMAC: "f4:e2:c6:00:00:01", DeviceVersion: "4.1.13",
		Facility: 4, Severity: 4, App: "kernel", Category: unifi.CatFirewall, EventType: "packet", Action: "block", Rule: "WAN_IN-4000",
		SrcIP: "203.0.113.9", DstIP: "192.168.1.10", SrcPort: 51515, DstPort: 22, Proto: "tcp", InIf: "eth8",
		Message: "Blocked SSH", Raw: "<12>raw line", Fields: map[string]string{"src_country": "CN", "src_asn": "4134", "device_name": "Gateway", "custom": "x"}}
}

func TestFormats(t *testing.T) {
	r := FromEvent(sampleEvent())
	row := sentinelRow(&r)
	if row["SrcCountry"] != "CN" || row["DstPort"] != uint16(22) || row["DeviceName"] != "Gateway" || row["Fields"].(map[string]string)["custom"] != "x" {
		t.Fatalf("sentinel row %v", row)
	}
	if _, ok := row["Fields"].(map[string]string)["src_country"]; ok {
		t.Fatal("known fields must not be repeated in Fields")
	}
	csl := cslRow(&r)
	if csl["SimplifiedDeviceAction"] != "Deny" || csl["LogSeverity"] != "5" || csl["Protocol"] != "TCP" || !toCSL(&r) {
		t.Fatalf("csl row %v", csl)
	}
	doc := ecsDoc(&r, "logs-unifi.syslog-default")
	b, _ := json.Marshal(doc)
	for _, want := range []string{`"source":{"as":{"number":4134}`, `"data_stream":{"dataset":"unifi.syslog","namespace":"default","type":"logs"}`,
		`"type":["connection","denied"]`, `"observer":{`, `"original":"\u003c12\u003eraw line"`, `"fields":{"custom":"x"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("ECS document lacks %s:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), `"client"`) {
		t.Errorf("empty values must be left out: %s", b)
	}
	msg := rfc5424(&r)
	if !strings.HasPrefix(msg, "<36>1 2026-10-18T12:00:00.000Z UDM-Pro kernel - packet [unifi@32473 site=\"HQ\"") || !strings.HasSuffix(msg, "] Blocked SSH") {
		t.Fatalf("rfc5424 %s", msg)
	}
	c := cef(&r)
	if !strings.Contains(c, "CEF:0|Ubiquiti|UniFi|4.1.13|WAN_IN-4000|WAN_IN-4000 block|5|") || !strings.Contains(c, "src=203.0.113.9") || !strings.Contains(c, "cs1=HQ") {
		t.Fatalf("cef %s", c)
	}
}

func openMeta(t *testing.T) *store.Meta {
	dir := t.TempDir()
	m, err := store.OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The Elasticsearch destination keeps events queued while the cluster
// fails, retries with the same _id and counts per-document rejections.
func TestElasticsearchDelivery(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var docs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.Header.Get("Authorization") != "ApiKey secret" {
			w.WriteHeader(401)
			return
		}
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		sc := bufio.NewScanner(r.Body)
		sc.Buffer(nil, 1<<20)
		var items []string
		for sc.Scan() {
			var action map[string]map[string]string
			json.Unmarshal(sc.Bytes(), &action)
			sc.Scan()
			var d map[string]any
			json.Unmarshal(sc.Bytes(), &d)
			if action["create"]["_index"] != "logs-unifi.syslog-default" || action["create"]["_id"] == "" {
				t.Errorf("bad action %v", action)
			}
			if d["message"] == "reject me" {
				items = append(items, `{"create":{"status":400,"error":{"type":"mapper_parsing_exception","reason":"bad"}}}`)
				continue
			}
			docs = append(docs, d)
			items = append(items, `{"create":{"status":201}}`)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":true,"items":[`+strings.Join(items, ",")+`]}`)
	}))
	defer srv.Close()

	meta := openMeta(t)
	d := store.ForwardDestination{Name: "Elastic", Kind: store.FwdElastic, Enabled: true,
		Filter:   store.ForwardFilter{Categories: []string{"firewall"}, MaxSeverity: 7, Query: "action:block"},
		Settings: store.ForwardSettings{URL: srv.URL, ESAuth: "api_key", APIKey: "secret"}}
	if err := meta.SaveForwardDestination(&d); err != nil {
		t.Fatal(err)
	}
	m := NewManager(meta, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	waitFor(t, "destination loaded", func() bool { return m.active.Load() == 1 })

	block := sampleEvent()
	allow := sampleEvent()
	allow.Action = "allow"
	wifi := sampleEvent()
	wifi.Category = unifi.CatWiFi
	bad := sampleEvent()
	bad.Message = "reject me"
	m.Submit([]*unifi.Event{block, allow, wifi, bad})

	waitFor(t, "delivery after retry", func() bool { mu.Lock(); defer mu.Unlock(); return len(docs) == 1 })
	waitFor(t, "statistics", func() bool {
		m.Statuses() // writes pending counters
		st, _ := meta.ForwardTotals()
		return st[d.ID].Events == 1 && st[d.ID].Rejected == 1 && st[d.ID].Errors == 1
	})
	s := m.Statuses()[d.ID]
	if s.State != "ok" || !strings.Contains(s.LastError, "mapper_parsing_exception") || s.QueueRecs != 0 {
		t.Fatalf("status %+v", s)
	}
	if docs[0]["unifi"].(map[string]any)["action"] != "block" {
		t.Fatalf("wrong document %v", docs[0])
	}

	// Alerts reach the destination whatever its filter.
	m.SendAlert([]int64{d.ID}, Alert{Rule: "SSH", Count: 3, Window: 5, Threshold: 1})
	waitFor(t, "alert", func() bool { mu.Lock(); defer mu.Unlock(); return len(docs) == 2 })
	if docs[1]["event"].(map[string]any)["kind"] != "alert" {
		t.Fatalf("alert document %v", docs[1])
	}

	// Removing the destination stops it and deletes its queue.
	meta.DeleteForwardDestination(d.ID)
	m.Reload()
	if len(m.Statuses()) != 0 || m.active.Load() != 0 {
		t.Fatal("destination still running")
	}
	cancel()
	m.InputDone()
	<-done
}

func testCert(t *testing.T) string {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "unifi"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalPKCS8PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}))
}

// Sentinel signs in with a certificate and splits firewall events into
// CommonSecurityLog.
func TestSentinelSplitWithCertificate(t *testing.T) {
	var mu sync.Mutex
	streams := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			r.ParseForm()
			parts := strings.Split(r.Form.Get("client_assertion"), ".")
			if r.URL.Path != "/tenant-1/oauth2/v2.0/token" || len(parts) != 3 || r.Form.Get("scope") != "https://monitor.azure.com//.default" || r.Form.Get("client_secret") != "" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_request","error_description":"bad"}`)
				return
			}
			io.WriteString(w, `{"access_token":"tok","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Encoding") != "gzip" || r.URL.Query().Get("api-version") != "2023-01-01" {
			w.WriteHeader(403)
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		streams[name]++
		w.WriteHeader(204)
	}))
	defer srv.Close()
	old := loginBase
	loginBase = srv.URL
	defer func() { loginBase = old }()

	d := store.ForwardDestination{Kind: store.FwdSentinel, Settings: store.ForwardSettings{TenantID: "tenant-1", ClientID: "app", AuthMethod: "certificate",
		Certificate: testCert(t), Endpoint: srv.URL, DCRID: "dcr-0123", TableMode: "split"}}
	if err := Test(context.Background(), d, "Lab"); err != nil {
		t.Fatal(err)
	}
	if streams[DefaultSentinelStream] != 1 || streams[DefaultCEFStream] != 1 {
		t.Fatalf("streams %v", streams)
	}
}

// Syslog over TCP uses octet counting.
func TestSyslogTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 4096)
		n, _ := c.Read(b)
		got <- string(b[:n])
	}()
	d := store.ForwardDestination{Kind: store.FwdSyslog, Settings: store.ForwardSettings{Address: ln.Addr().String(), Transport: "tcp", Framing: "octet", Format: "rfc5424"}}
	if err := Test(context.Background(), d, "Lab"); err != nil {
		t.Fatal(err)
	}
	msg := <-got
	sp := strings.IndexByte(msg, ' ')
	if sp < 1 || !strings.HasPrefix(msg[sp+1:], "<14>1 ") || len(msg[sp+1:]) != atoi(msg[:sp]) {
		t.Fatalf("frame %q", msg)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
