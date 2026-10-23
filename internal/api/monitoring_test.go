package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/rhisijmuiden/unify-syslog-server/internal/config"
	"github.com/rhisijmuiden/unify-syslog-server/internal/ha"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func tokenFor(t *testing.T, m *store.Meta, name string) string {
	t.Helper()
	u, err := m.UserByName(name)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := m.CreateToken(&store.APIToken{UserID: u.ID, Name: "t", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestMetricsAndGrafanaAccess(t *testing.T) {
	s, h, _ := testServer(t)
	addUser(t, s.Meta, "analyst", store.RoleAnalyst)
	admin, analyst := tokenFor(t, s.Meta, "admin"), tokenFor(t, s.Meta, "analyst")

	req := func(path, auth, remote string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if remote != "" {
			r.RemoteAddr = remote
		}
		return r
	}
	if !s.metricsAuth(req("/metrics", "Bearer "+admin, "")) {
		t.Fatal("administrator token refused")
	}
	if s.metricsAuth(req("/metrics", "Bearer "+analyst, "")) {
		t.Fatal("analyst token accepted for metrics")
	}
	if s.metricsAuth(req("/metrics", "", "10.0.0.5:4000")) {
		t.Fatal("anonymous access without an allowed network")
	}
	s.Meta.SetSetting("monitoring", store.MonitoringSettings{MetricsEnabled: true, AnonymousCIDRs: []string{"10.0.0.0/24"}})
	r := req("/metrics", "", "10.0.0.5:4000")
	keep := keepPeer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.metricsAuth(r) {
			t.Error("allowed network refused")
		}
	}))
	keep.ServeHTTP(httptest.NewRecorder(), r)
	// X-Forwarded-For does not count as the source of the request.
	r = req("/metrics", "", "192.0.2.9:4000")
	r.Header.Set("X-Forwarded-For", "10.0.0.5")
	keep = keepPeer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.metricsAuth(r) {
			t.Error("forwarded address trusted")
		}
	}))
	keep.ServeHTTP(httptest.NewRecorder(), r)

	s.Meta.SetSetting("monitoring", store.MonitoringSettings{MetricsEnabled: false})
	if w := bearerCall(h, admin, "GET", "/metrics", ""); w.Code != 404 {
		t.Fatalf("metrics while off: %d", w.Code)
	}
	if w := bearerCall(h, admin, "GET", "/clickhouse/?query=SELECT+1", ""); w.Code != 404 {
		t.Fatalf("clickhouse proxy while off: %d", w.Code)
	}
	s.Meta.SetSetting("monitoring", store.MonitoringSettings{GrafanaEnabled: true})
	r = httptest.NewRequest("POST", "/clickhouse/", strings.NewReader("SELECT 1"))
	r.SetBasicAuth("grafana", analyst)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("analyst token on the clickhouse proxy: %d", w.Code)
	}
}

func TestStandbyGuard(t *testing.T) {
	s, h, tok := testServer(t)
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.HA = config.HA{Enabled: true, VIP: "192.0.2.50/24", PeerURL: "https://192.0.2.11:8080"}
	s.HA = &ha.Node{Cfg: cfg, Meta: s.Meta, LocalAddrs: func() ([]netip.Addr, error) { return nil, nil }}
	if !s.HA.Standby() {
		t.Fatal("node without the virtual IP is not standby")
	}
	w := call(h, tok, "PUT", "/api/settings/branding", "application/json", []byte(`{"company_name":"x"}`))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "https://192.0.2.11:8080") {
		t.Fatalf("change on standby: %d %s", w.Code, w.Body)
	}
	if w := call(h, tok, "PUT", "/api/me/language", "application/json", []byte(`{"language":"nl"}`)); w.Code != 409 {
		t.Fatalf("personal change on standby: %d", w.Code)
	}
	if w := call(h, tok, "GET", "/api/settings/branding", "", nil); w.Code != 200 {
		t.Fatalf("read on standby: %d", w.Code)
	}
	if w := call(h, tok, "GET", "/api/auth/status", "", nil); !strings.Contains(w.Body.String(), `"standby"`) {
		t.Fatalf("status lacks the standby flag: %s", w.Body)
	}
}
