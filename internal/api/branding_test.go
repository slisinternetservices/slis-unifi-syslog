package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func testServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := store.OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	u := &store.User{Username: "admin", Role: "admin"}
	if err := m.CreateUser(u, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	tok, err := m.CreateSession(u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Meta: m}
	return s, s.Handler(), tok
}

func call(h http.Handler, tok, method, path, ctype string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("X-USS", "1")
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if tok != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var png1x1 = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func TestBranding(t *testing.T) {
	_, h, tok := testServer(t)

	if w := call(h, tok, "PUT", "/api/settings/branding", "application/json", []byte(`{"company_name":"  Contoso B.V. "}`)); w.Code != 200 {
		t.Fatalf("save name: %d %s", w.Code, w.Body)
	}
	if w := call(h, tok, "PUT", "/api/settings/branding/logo_light", "image/png", png1x1); w.Code != 200 {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	if w := call(h, tok, "PUT", "/api/settings/branding/logo_dark", "text/plain", []byte("hello")); w.Code != 400 {
		t.Fatalf("non-image accepted: %d", w.Code)
	}
	if w := call(h, tok, "PUT", "/api/settings/branding/favicon", "image/x-icon", []byte{0, 0, 1, 0, 1, 0}); w.Code != 200 {
		t.Fatalf("ico favicon: %d %s", w.Code, w.Body)
	}
	if w := call(h, tok, "PUT", "/api/settings/branding/logo_dark", "image/x-icon", []byte{0, 0, 1, 0, 1, 0}); w.Code != 400 {
		t.Fatalf("ico logo accepted: %d", w.Code)
	}
	if w := call(h, tok, "PUT", "/api/settings/branding/other", "image/png", png1x1); w.Code != 404 {
		t.Fatalf("unknown slot: %d", w.Code)
	}

	// Public status shows the branding to the sign-in page.
	w := call(h, "", "GET", "/api/auth/status", "", nil)
	var st struct {
		Language string         `json:"language"`
		Branding store.Branding `json:"branding"`
	}
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Branding.CompanyName != "Contoso B.V." || !st.Branding.Assets["logo_light"] || st.Branding.Assets["logo_dark"] || st.Branding.Version == "" || st.Language != "en" {
		t.Fatalf("status: %s", w.Body)
	}

	// Images are public but sandboxed.
	w = call(h, "", "GET", "/api/branding/logo_light", "", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || !bytes.Equal(w.Body.Bytes(), png1x1) {
		t.Fatalf("serve: %d %v", w.Code, w.Header())
	}
	r := httptest.NewRequest("GET", "/api/branding/logo_light", nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r)
	if w2.Code != 304 {
		t.Fatalf("etag: %d", w2.Code)
	}

	// Only admins may change branding.
	if w := call(h, "", "PUT", "/api/settings/branding/logo_dark", "image/png", png1x1); w.Code != 401 {
		t.Fatalf("anonymous upload: %d", w.Code)
	}

	old := st.Branding.Version
	if w := call(h, tok, "DELETE", "/api/settings/branding/logo_light", "", nil); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := call(h, "", "GET", "/api/branding/logo_light", "", nil); w.Code != 404 {
		t.Fatalf("deleted asset still served: %d", w.Code)
	}
	w = call(h, "", "GET", "/api/auth/status", "", nil)
	st.Branding = store.Branding{}
	json.Unmarshal(w.Body.Bytes(), &st)
	if st.Branding.Version == old || st.Branding.Assets["logo_light"] {
		t.Fatalf("version not bumped: %s", w.Body)
	}
}

func TestLanguage(t *testing.T) {
	s, h, tok := testServer(t)
	w := call(h, tok, "PUT", "/api/me/language", "application/json", []byte(`{"language":"nl-NL"}`))
	var u store.User
	json.Unmarshal(w.Body.Bytes(), &u)
	if w.Code != 200 || u.Language != "nl" || !u.LanguageManual {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	if got := s.Meta.LanguageForEmail("ADMIN"); got != "nl" {
		t.Fatalf("language by username: %q", got)
	}
	if w := call(h, tok, "PUT", "/api/me/language", "application/json", []byte(`{"language":"<script>"}`)); w.Code != 400 {
		t.Fatalf("invalid language accepted: %d", w.Code)
	}
	w = call(h, tok, "PUT", "/api/me/language", "application/json", []byte(`{"language":""}`))
	json.Unmarshal(w.Body.Bytes(), &u)
	if u.Language != "" || u.LanguageManual {
		t.Fatalf("reset: %s", w.Body)
	}
}
