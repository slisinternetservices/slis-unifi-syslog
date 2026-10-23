package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
)

func jsonCall(t *testing.T, h http.Handler, tok, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := call(h, tok, method, path, "application/json", []byte(body))
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func bearerCall(h http.Handler, secret, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func sessionCookie(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

func addUser(t *testing.T, m *store.Meta, name, role string, sites ...string) string {
	t.Helper()
	u := &store.User{Username: name, Role: role, Sites: sites}
	if err := m.CreateUser(u, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	tok, err := m.CreateSession(u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTOTPSignIn(t *testing.T) {
	s, h, admin := testServer(t)
	if w, _ := jsonCall(t, h, admin, "PUT", "/api/settings/security", `{"require_totp":true,"audit_retention_days":365}`); w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}

	// The policy makes a user without 2FA set it up while signing in.
	w, out := jsonCall(t, h, "", "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`)
	if w.Code != 200 || out["totp_setup"] != true || sessionCookie(w) != "" {
		t.Fatalf("login should ask for setup: %d %s", w.Code, w.Body)
	}
	secret := out["enrollment"].(map[string]any)["secret"].(string)
	ticket := out["ticket"].(string)
	if w, _ := jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"`+ticket+`","code":"000000"}`); w.Code != 401 {
		t.Fatalf("wrong code accepted: %d", w.Code)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	w, out = jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"`+ticket+`","code":"`+code+`"}`)
	if w.Code != 200 || sessionCookie(w) == "" {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	codes := out["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", codes)
	}

	login := func() string {
		w, out := jsonCall(t, h, "", "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`)
		if w.Code != 200 || out["totp_required"] != true {
			t.Fatalf("login should ask for a code: %d %s", w.Code, w.Body)
		}
		return out["ticket"].(string)
	}
	// The code used for setup cannot be replayed.
	if w, _ := jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"`+login()+`","code":"`+code+`"}`); w.Code != 401 {
		t.Fatalf("replayed code accepted: %d", w.Code)
	}
	next, _ := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	if w, _ := jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"`+login()+`","code":"`+next+`"}`); w.Code != 200 {
		t.Fatalf("next code: %d %s", w.Code, w.Body)
	}
	// A recovery code works once.
	rc := codes[0].(string)
	if w, _ := jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"`+login()+`","code":"`+strings.ToUpper(rc)+`"}`); w.Code != 200 {
		t.Fatalf("recovery code: %d %s", w.Code, w.Body)
	}
	if w, _ := jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"`+login()+`","code":"`+rc+`"}`); w.Code != 401 {
		t.Fatalf("recovery code reused: %d", w.Code)
	}
	// An unknown ticket is refused.
	if w, _ := jsonCall(t, h, "", "POST", "/api/auth/login/totp", `{"ticket":"nope","code":"`+next+`"}`); w.Code != 401 {
		t.Fatalf("unknown ticket: %d", w.Code)
	}
	if u, _ := s.Meta.UserByName("admin"); !u.TOTPEnabled || u.RecoveryLeft != 9 {
		t.Fatalf("user state: %+v", u)
	}

	// Audit log has the sign-ins with kind "auth" and the client IP.
	w, out = jsonCall(t, h, admin, "GET", "/api/audit?kind=auth", "")
	rows := out["rows"].([]any)
	if w.Code != 200 || len(rows) < 3 || rows[0].(map[string]any)["ip"] == "" {
		t.Fatalf("audit: %d %s", w.Code, w.Body)
	}
}

func TestAPITokens(t *testing.T) {
	s, h, admin := testServer(t)
	viewer := addUser(t, s.Meta, "viewer", store.RoleViewer)

	w, out := jsonCall(t, h, viewer, "POST", "/api/tokens", `{"name":"grafana","read_only":true,"days":30}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	ro := out["secret"].(string)
	if !strings.HasPrefix(ro, store.TokenPrefix) {
		t.Fatalf("secret %q", ro)
	}
	if w := bearerCall(h, ro, "GET", "/api/me", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"viewer"`) {
		t.Fatalf("token me: %d %s", w.Code, w.Body)
	}
	if w := bearerCall(h, ro, "GET", "/api/me", "x"); w.Code != 200 {
		t.Fatalf("no CSRF header needed: %d", w.Code)
	}
	if w := bearerCall(h, "uss_wrong", "GET", "/api/me", ""); w.Code != 401 {
		t.Fatalf("bad token: %d", w.Code)
	}
	// Tokens cannot manage tokens or the account.
	if w := bearerCall(h, ro, "GET", "/api/tokens", ""); w.Code != 403 {
		t.Fatalf("token lists tokens: %d", w.Code)
	}
	// Read-only tokens can still search (POST), but not change anything.
	if w := bearerCall(h, ro, "POST", "/api/categories", `{"name":"x","query":""}`); w.Code != 403 || !strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("read-only write: %d %s", w.Code, w.Body)
	}
	if !readOnlyPost("/api/search") {
		t.Fatal("search should be allowed for read-only tokens")
	}
	// A read-write token keeps the role of its user.
	_, out = jsonCall(t, h, viewer, "POST", "/api/tokens", `{"name":"script"}`)
	rw := out["secret"].(string)
	if w := bearerCall(h, rw, "POST", "/api/categories", `{"name":"x","query":""}`); w.Code != 403 || strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("viewer token write: %d %s", w.Code, w.Body)
	}
	_, out = jsonCall(t, h, admin, "POST", "/api/tokens", `{"name":"admin script"}`)
	adm := out["secret"].(string)
	if w := bearerCall(h, adm, "POST", "/api/categories", `{"name":"From API","query":"category:firewall"}`); w.Code != 200 {
		t.Fatalf("admin token write: %d %s", w.Code, w.Body)
	}
	// Admins see and revoke everyone's tokens; viewers only their own.
	w, _ = jsonCall(t, h, admin, "GET", "/api/tokens?all=1", "")
	var all []store.APIToken
	json.Unmarshal(w.Body.Bytes(), &all)
	if len(all) != 3 {
		t.Fatalf("all tokens: %s", w.Body)
	}
	var adminTok int64
	for _, x := range all {
		if x.Username == "admin" {
			adminTok = x.ID
		}
	}
	if w, _ := jsonCall(t, h, viewer, "DELETE", "/api/tokens/"+itoa(adminTok), ""); w.Code != 404 {
		t.Fatalf("viewer revoked admin token: %d", w.Code)
	}
	if w, _ := jsonCall(t, h, admin, "DELETE", "/api/tokens/"+itoa(all[len(all)-1].ID), ""); w.Code != 200 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := bearerCall(h, ro, "GET", "/api/me", ""); w.Code != 401 {
		t.Fatalf("revoked token still works: %d", w.Code)
	}
	// Disabled users' tokens stop working.
	v, _ := s.Meta.UserByName("viewer")
	v.Disabled = true
	s.Meta.UpdateUser(v)
	if w := bearerCall(h, rw, "GET", "/api/me", ""); w.Code != 401 {
		t.Fatalf("disabled user's token works: %d", w.Code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestRolesAndSites(t *testing.T) {
	s, h, admin := testServer(t)
	for _, r := range []store.SiteRange{{Site: "amsterdam", CIDR: "10.1.0.0/16"}, {Site: "utrecht", CIDR: "10.2.0.0/16"}} {
		s.Meta.SaveSiteRange(&r)
	}
	viewer := addUser(t, s.Meta, "viewer", store.RoleViewer, "amsterdam")
	analyst := addUser(t, s.Meta, "analyst", store.RoleAnalyst)

	var sites []string
	w := call(h, viewer, "GET", "/api/sites", "", nil)
	json.Unmarshal(w.Body.Bytes(), &sites)
	if len(sites) != 1 || sites[0] != "amsterdam" {
		t.Fatalf("viewer sites: %s", w.Body)
	}
	w = call(h, admin, "GET", "/api/sites", "", nil)
	json.Unmarshal(w.Body.Bytes(), &sites)
	if len(sites) != 3 {
		t.Fatalf("admin sites: %s", w.Body)
	}
	if w := call(h, viewer, "GET", "/api/search/export", "", nil); w.Code != 403 {
		t.Fatalf("viewer export: %d", w.Code)
	}
	if w := call(h, viewer, "POST", "/api/categories", "application/json", []byte(`{"name":"x","query":""}`)); w.Code != 403 {
		t.Fatalf("viewer category: %d", w.Code)
	}
	if w := call(h, analyst, "POST", "/api/categories", "application/json", []byte(`{"name":"x","query":"category:vpn"}`)); w.Code != 200 {
		t.Fatalf("analyst category: %d %s", w.Code, w.Body)
	}
	if w := call(h, analyst, "GET", "/api/users", "", nil); w.Code != 403 {
		t.Fatalf("analyst users: %d", w.Code)
	}
	// Admins assign roles and sites; admins themselves always see all sites.
	v, _ := s.Meta.UserByName("viewer")
	body := `{"role":"analyst","sites":["utrecht"," amsterdam","utrecht"]}`
	if w := call(h, admin, "PUT", "/api/users/"+itoa(v.ID), "application/json", []byte(body)); w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	v, _ = s.Meta.UserByName("viewer")
	if v.Role != "analyst" || len(v.Sites) != 2 || v.Sites[0] != "amsterdam" {
		t.Fatalf("stored: %+v", v)
	}
	if w := call(h, admin, "PUT", "/api/users/"+itoa(v.ID), "application/json", []byte(`{"role":"owner"}`)); w.Code != 400 {
		t.Fatalf("invalid role accepted: %d", w.Code)
	}
	// Users whose access comes from Entra groups cannot be changed by hand.
	v.RoleManaged = true
	s.Meta.UpdateUser(v)
	if w := call(h, admin, "PUT", "/api/users/"+itoa(v.ID), "application/json", []byte(`{"role":"admin"}`)); w.Code != 400 {
		t.Fatalf("managed role changed: %d", w.Code)
	}
}

func TestAllowlistAPI(t *testing.T) {
	s, h, admin := testServer(t)
	s.Filter = &syslogd.Filter{}
	if w := call(h, admin, "PUT", "/api/settings/allowlist", "application/json", []byte(`{"enabled":true,"entries":[]}`)); w.Code != 400 {
		t.Fatalf("empty enabled list accepted: %d", w.Code)
	}
	if w := call(h, admin, "PUT", "/api/settings/allowlist", "application/json", []byte(`{"enabled":true,"entries":[{"cidr":"10.0.0.0/8"}]}`)); w.Code != 200 {
		t.Fatalf("reject-all test setup: %d", w.Code)
	}
	if s.Filter.Allow("192.168.1.10", "udp") {
		t.Fatal("outside sender allowed")
	}
	if w := call(h, admin, "PUT", "/api/settings/allowlist", "application/json", []byte(`{"enabled":true,"entries":[{"cidr":"bad"}]}`)); w.Code != 400 {
		t.Fatalf("bad entry accepted: %d", w.Code)
	}
	body := `{"enabled":true,"entries":[{"cidr":"10.1.2.3/16","note":"HQ"},{"cidr":"192.168.1.10"},{"cidr":"10.1.0.0/16"}]}`
	w := call(h, admin, "PUT", "/api/settings/allowlist", "application/json", []byte(body))
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	a := s.Meta.Allowlist()
	if len(a.Entries) != 2 || a.Entries[0].CIDR != "10.1.0.0/16" || a.Entries[1].CIDR != "192.168.1.10" {
		t.Fatalf("normalized: %+v", a.Entries)
	}
	if !s.Filter.Allow("10.1.200.1", "udp") || !s.Filter.Allow("::ffff:192.168.1.10", "tcp") || s.Filter.Allow("10.2.0.1", "udp") {
		t.Fatal("filter not applied")
	}
	if rej := s.Filter.RecentRejected(); len(rej) != 1 || rej[0].IP != "10.2.0.1" {
		t.Fatalf("rejected list (192.168.1.10 should be forgotten once allowed): %+v", rej)
	}
	call(h, admin, "PUT", "/api/settings/allowlist", "application/json", []byte(`{"enabled":false,"entries":[{"cidr":"10.1.0.0/16"}]}`))
	if !s.Filter.Allow("8.8.8.8", "udp") {
		t.Fatal("disabled allowlist still filters")
	}
}
