package ha

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlscert"
)

type testNode struct {
	*Node
	srv  *httptest.Server
	vip  atomic.Bool
	rest atomic.Int32
}

func newTestNode(t *testing.T, name string) *testNode {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	meta, err := store.OpenMeta(cfg.MetaDBPath(), cfg.SecretKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	tn := &testNode{}
	tlsDir := filepath.Join(dir, "tls")
	if _, err := tlscert.LoadOrCreate(tlsDir, "", ""); err != nil {
		t.Fatal(err)
	}
	tn.Node = &Node{Cfg: cfg, Meta: meta, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), TLSDir: tlsDir,
		Restart: func() { tn.rest.Add(1) },
		LocalAddrs: func() ([]netip.Addr, error) {
			if tn.vip.Load() {
				return []netip.Addr{netip.MustParseAddr("192.0.2.50")}, nil
			}
			return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
		}}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !tn.Enabled() || !tn.Authorized(r) {
				http.Error(w, `{"error":"not a paired node"}`, 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/ha/v1/status", auth(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(tn.LocalStatus()) }))
	mux.HandleFunc("/api/ha/v1/snapshot", auth(func(w http.ResponseWriter, r *http.Request) { tn.WriteSnapshot(w) }))
	mux.HandleFunc("/api/ha/v1/join", auth(func(w http.ResponseWriter, r *http.Request) {
		var j JoinRequest
		json.NewDecoder(r.Body).Decode(&j)
		resp, err := tn.Accept(j, config.ClickHouse{Addr: "ch.example:9000", HTTPURL: "http://ch.example:8123", Database: "unify_syslog", Username: "u", Password: "p"})
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		json.NewEncoder(w).Encode(resp)
	}))
	tn.srv = httptest.NewTLSServer(mux)
	t.Cleanup(tn.srv.Close)
	tn.Pin = tlscert.Pin(tn.srv.Certificate())
	return tn
}

func TestPairAndSync(t *testing.T) {
	a, b := newTestNode(t, "a"), newTestNode(t, "b")
	a.vip.Store(true)
	if err := a.Start(Setup{NodeName: "node-a", SelfURL: a.srv.URL, VIP: "192.0.2.50/24", Interface: "eth0", Priority: 110}); err != nil {
		t.Fatal(err)
	}
	if !a.Active() || !a.Enabled() {
		t.Fatal("first node holding the VIP must be active")
	}
	code, err := a.Code()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.Join(ctx, Setup{NodeName: "node-b", SelfURL: b.srv.URL, Code: "USSHA1.bogus"}); err == nil {
		t.Fatal("bogus code accepted")
	}
	if err := b.Join(ctx, Setup{NodeName: "node-b", SelfURL: b.srv.URL, Code: code, Interface: "eth0", Priority: 100}); err != nil {
		t.Fatal(err)
	}
	if b.Active() || !b.Standby() {
		t.Fatal("second node without the VIP must be standby")
	}
	// b took over the ClickHouse connection of a, in node.yaml.
	cfg := config.Default()
	cfg.DataDir = b.Cfg.DataDir
	n, _ := cfg.LoadNode()
	if n.ClickHouse == nil || n.ClickHouse.Addr != "ch.example:9000" || n.HA == nil || n.HA.PeerURL != a.srv.URL {
		t.Fatalf("node.yaml of b: %+v", n)
	}
	if a.PeerURL() != b.srv.URL {
		t.Fatalf("a did not register b: %q", a.PeerURL())
	}
	// A setting changed on the active node reaches the standby.
	g := a.Meta.General()
	g.InstanceName = "Pair"
	a.Meta.SetSetting("general", g)
	b.poll(ctx, b.cfg)
	if b.rest.Load() != 1 {
		t.Fatalf("standby did not restart after copying (%d), state %+v", b.rest.Load(), b.State())
	}
	if _, err := os.Stat(b.Cfg.MetaDBPath() + ".restore"); err != nil {
		t.Fatal("no staged database")
	}
	ka, _ := os.ReadFile(a.Cfg.SecretKeyPath())
	kb, _ := os.ReadFile(b.Cfg.SecretKeyPath())
	if string(ka) != string(kb) {
		t.Fatal("secret key not copied")
	}
	ca, _ := os.ReadFile(filepath.Join(a.TLSDir, "cert.pem"))
	cb, _ := os.ReadFile(filepath.Join(b.TLSDir, "cert.pem"))
	if string(ca) != string(cb) {
		t.Fatal("certificate not copied")
	}
	// The staged database is the one of a.
	m2, err := store.OpenMeta(b.Cfg.MetaDBPath(), b.Cfg.SecretKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	if m2.General().InstanceName != "Pair" {
		t.Fatal("copied database lacks the change")
	}
	fa, _ := a.Meta.ConfigFingerprint()
	fb, _ := m2.ConfigFingerprint()
	if fa != fb {
		t.Fatal("fingerprints differ after copy")
	}
	// The active node never copies from a standby.
	a.poll(ctx, a.cfg)
	if a.rest.Load() != 0 {
		t.Fatal("active node restarted")
	}
	conf, err := a.Keepalived("8080")
	if err != nil || !strings.Contains(conf, "virtual_router_id") || !strings.Contains(conf, "192.0.2.50/24 dev eth0") || !strings.Contains(conf, "unicast_peer") || !strings.Contains(conf, "nopreempt") {
		t.Fatalf("keepalived.conf:\n%s\n%v", conf, err)
	}
}

func TestWrongSecret(t *testing.T) {
	a := newTestNode(t, "a")
	a.vip.Store(true)
	a.Start(Setup{NodeName: "a", SelfURL: a.srv.URL, VIP: "192.0.2.50/24"})
	c := a.cfg
	c.PeerURL, c.Secret, c.PeerPin = a.srv.URL, "wrong", a.Pin
	var st Status
	if err := a.call(context.Background(), c, http.MethodGet, "/api/ha/v1/status", nil, &st); err == nil {
		t.Fatal("wrong secret accepted")
	}
	// An unknown certificate is refused even with the right secret.
	c.Secret, c.PeerPin = a.cfg.Secret, "AAAA"
	b := &Node{Pin: "BBBB"}
	if err := b.call(context.Background(), c, http.MethodGet, "/api/ha/v1/status", nil, &st); err == nil {
		t.Fatal("unpinned certificate accepted")
	}
}

func TestGate(t *testing.T) {
	a := newTestNode(t, "a")
	a.Start(Setup{NodeName: "a", SelfURL: a.srv.URL, VIP: "192.0.2.50/24"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var running atomic.Int32
	var runs atomic.Int32
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.Gate(ctx, "job", func(ctx context.Context) {
			runs.Add(1)
			running.Add(1)
			<-ctx.Done()
			running.Add(-1)
		})
	}()
	wait := func(want int32) {
		deadline := time.Now().Add(2 * time.Second)
		for running.Load() != want {
			if time.Now().After(deadline) {
				t.Fatalf("running = %d, want %d", running.Load(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(0) // standby: not running
	a.vip.Store(true)
	a.setActive(a.holdsVIP(a.cfg.VIP))
	wait(1)
	a.vip.Store(false)
	a.setActive(a.holdsVIP(a.cfg.VIP))
	wait(0)
	a.Leave() // without high availability the job always runs
	wait(1)
	cancel()
	wg.Wait()
	if runs.Load() != 2 {
		t.Fatalf("runs = %d", runs.Load())
	}
}
