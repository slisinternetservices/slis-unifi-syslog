package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probe"
	"github.com/rhisijmuiden/unify-syslog-server/internal/probehub"
	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlscert"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlsmux"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

func freeUDP(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().String()
}

// probeServer serves the API like the real server: plain HTTP and HTTPS
// with a self-signed certificate on one port.
func probeServer(t *testing.T) (*Server, string, string, chan syslogd.Message) {
	s, _, admin := testServer(t)
	dir := t.TempDir()
	cert, err := tlscert.LoadOrCreate(filepath.Join(dir, "tls"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan syslogd.Message, 1000)
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	s.TLSPin = tlscert.LeafPin(cert)
	s.Probes = &probehub.Hub{Meta: s.Meta, Out: out, Dir: filepath.Join(dir, "probes"), Log: s.Log, Pin: s.TLSPin}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	plain, secure := tlsmux.Split(ln)
	srv := &http.Server{Handler: s.Handler()}
	go srv.Serve(plain)
	go srv.Serve(tls.NewListener(secure, &tls.Config{Certificates: []tls.Certificate{cert}}))
	t.Cleanup(func() { srv.Close() })
	return s, ln.Addr().String(), admin, out
}

func TestProbeEnrollIngestAndRevoke(t *testing.T) {
	s, addr, admin, out := probeServer(t)
	h := s.Handler()

	// Plain HTTP still works on the same port.
	if resp, err := http.Get("http://" + addr + "/api/auth/status"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("plain http: %v %v", resp, err)
	}

	udp := freeUDP(t)
	body := fmt.Sprintf(`{"name":"Branch office","site":"amsterdam","server_url":"https://%s","settings":{"udp":%q,"tcp":"","buffer_mb":64,"buffer_hours":24}}`, addr, udp)
	w, created := jsonCall(t, h, admin, "POST", "/api/probes", body)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	key := created["key"].(string)
	k, err := probeproto.ParseEnrollKey(key)
	if err != nil || k.URL != "https://"+addr || k.Pin != s.TLSPin {
		t.Fatalf("key %+v %v", k, err)
	}

	a := &probe.Agent{DataDir: t.TempDir(), Log: s.Log}
	if err := a.Open(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	// A wrong pin is refused.
	bad := probeproto.EnrollKey{URL: k.URL, Code: k.Code, Pin: "AAAA"}
	if _, err := a.Enroll(ctx, bad.String()); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("enroll with wrong pin: %v", err)
	}
	if _, err := a.Enroll(ctx, key); err != nil {
		t.Fatal(err)
	}
	// The one-time code cannot be used twice.
	if _, err := a.Enroll(ctx, key); err == nil {
		t.Fatal("code accepted twice")
	}

	// Settings from the server opened the listener; send it messages.
	var c net.Conn
	for i := 0; i < 50; i++ {
		if c, err = net.Dial("udp", udp); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	got := map[string]syslogd.Message{}
	deadline := time.After(20 * time.Second)
	for i := 0; len(got) < 5; i++ {
		if i < 400 && i%40 == 0 {
			for n := 0; n < 5; n++ {
				fmt.Fprintf(c, "<14>Oct 18 10:00:00 U6-Pro,f4e2c6000001,v6.6.55 hostapd: message %d", n)
			}
		}
		select {
		case m := <-out:
			got[string(m.Data)] = m
			if m.Probe != "Branch office" || m.Site != "amsterdam" || m.SourceIP != "127.0.0.1" {
				t.Fatalf("message %+v", m)
			}
		case <-time.After(250 * time.Millisecond):
		case <-deadline:
			t.Fatalf("only %d messages arrived", len(got))
		}
	}

	// A retried batch is not stored twice.
	// The sequence is stored after the batch is handed on, so wait for it.
	var p *store.Probe
	var epoch string
	var seq uint64
	for i := 0; i < 100 && seq < 5; i++ {
		p, _ = s.Meta.Probe(int64(created["probe"].(map[string]any)["id"].(float64)))
		epoch, seq = p.Seq()
		time.Sleep(50 * time.Millisecond)
	}
	if seq == 0 || epoch == "" {
		t.Fatalf("sequence not stored: %q %d", epoch, seq)
	}
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	json.NewEncoder(gz).Encode(probeproto.Record{Seq: seq, TS: time.Now().UnixNano(), IP: "10.0.0.1", Data: "dup"})
	json.NewEncoder(gz).Encode(probeproto.Record{Seq: seq + 1000, TS: time.Now().UnixNano(), IP: "10.0.0.1", Data: "new"})
	gz.Close()
	res, err := s.Probes.Ingest(ctx, p, epoch, &raw)
	if err != nil || res.Accepted != 1 || res.Seq != seq+1000 {
		t.Fatalf("dedup: %+v %v", res, err)
	}
	if m := <-out; string(m.Data) != "new" {
		t.Fatalf("got %q", m.Data)
	}

	// The probe shows up with its status after a heartbeat.
	var view struct {
		Probes []probeView `json:"probes"`
	}
	for i := 0; i < 100; i++ {
		w := call(h, admin, "GET", "/api/probes", "", nil)
		json.Unmarshal(w.Body.Bytes(), &view)
		if len(view.Probes) == 1 && view.Probes[0].Status.Version != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(view.Probes) != 1 || !view.Probes[0].Online || view.Probes[0].Hostname == "" || view.Probes[0].Ingested < 6 {
		t.Fatalf("probe view %+v", view.Probes)
	}

	// Deleting the probe revokes its key.
	if w := call(h, admin, "DELETE", fmt.Sprintf("/api/probes/%d", p.ID), "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	fmt.Fprintf(c, "<14>after delete")
	for i := 0; i < 100 && !a.Local().Revoked; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !a.Local().Revoked {
		t.Fatal("probe did not notice the revoked key")
	}
	cancel()
	<-done
}

func TestProbeUpdateOffer(t *testing.T) {
	s, addr, admin, _ := probeServer(t)
	h := s.Handler()
	w, created := jsonCall(t, h, admin, "POST", "/api/probes", `{"name":"p1","site":"","settings":{"udp":":514","tcp":":514","buffer_mb":64}}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	k, _ := probeproto.ParseEnrollKey(created["key"].(string))
	p, key, err := s.Meta.EnrollProbe(k.Code, "host", "0.0.1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	st := probeproto.Status{Version: "0.0.1", Arch: runtime.GOARCH}

	// Without a cached release nothing is offered.
	if hr := s.Probes.Heartbeat(p, st, ""); hr.Update != nil {
		t.Fatalf("offer without release: %+v", hr.Update)
	}
	// A release placed in the cache is offered and served.
	dir := filepath.Join(s.Probes.Dir, "releases", version.Version)
	os.MkdirAll(dir, 0o755)
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "unify-syslog-probe", Mode: 0o755, Size: 4})
	tw.Write([]byte("test"))
	tw.Close()
	gz.Close()
	name := probeproto.AssetName(runtime.GOARCH)
	os.WriteFile(filepath.Join(dir, name), tgz.Bytes(), 0o644)
	sum := sha256.Sum256(tgz.Bytes())
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o644)
	if _, err := s.Probes.Asset(context.Background(), runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	hr := s.Probes.Heartbeat(p, st, "")
	if hr.Update == nil || hr.Update.Version != version.Version || hr.Update.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("offer: %+v", hr.Update)
	}
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: tlscert.ClientConfig("127.0.0.1", s.TLSPin)}}
	resp, err := cl.Get("https://" + addr + hr.Update.Path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(b, tgz.Bytes()) {
		t.Fatalf("download: %d %d bytes", resp.StatusCode, len(b))
	}

	// A rolled back update is not offered again.
	s.Meta.SetProbeTarget(p.ID, version.Version)
	p, _ = s.Meta.ProbeByKey(key)
	st.Update = &probeproto.UpdateReport{State: "rolled_back", From: "0.0.1", To: version.Version, Error: "did not start"}
	if hr := s.Probes.Heartbeat(p, st, ""); hr.Update != nil {
		t.Fatal("offered after rollback")
	}
	p, _ = s.Meta.ProbeByKey(key)
	if p.UpdateState != "rolled_back" || p.TargetVersion != "" {
		t.Fatalf("state %q target %q", p.UpdateState, p.TargetVersion)
	}
	if hr := s.Probes.Heartbeat(p, st, ""); hr.Update != nil {
		t.Fatal("auto update retried a rolled back version")
	}
	// Asking again explicitly does.
	if w, _ := jsonCall(t, h, admin, "POST", "/api/probes/update", `{"all":true}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	p, _ = s.Meta.ProbeByKey(key)
	st.Update = nil
	if hr := s.Probes.Heartbeat(p, st, ""); hr.Update == nil {
		t.Fatal("not offered after an explicit request")
	}
	// Running the target version completes the update.
	st.Version = version.Version
	s.Probes.Heartbeat(p, st, "")
	p, _ = s.Meta.ProbeByKey(key)
	if p.UpdateState != "installed" || p.TargetVersion != "" {
		t.Fatalf("after install: %q %q", p.UpdateState, p.TargetVersion)
	}
}
