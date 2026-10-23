package probe

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// The probe's own page on port 8080: enrollment while the probe is not
// enrolled, afterwards a read-only status page. Everything else is managed
// on the main server.

//go:embed page.html
var page []byte

type LocalStatus struct {
	Enrolled      bool              `json:"enrolled"`
	Revoked       bool              `json:"revoked"`
	Server        string            `json:"server"`
	Name          string            `json:"name"`
	Site          string            `json:"site"`
	Version       string            `json:"version"`
	ServerVersion string            `json:"server_version"`
	LastContact   int64             `json:"last_contact"`
	LastError     string            `json:"last_error"`
	Status        probeproto.Status `json:"status"`
}

func (a *Agent) Local() LocalStatus {
	st := a.state()
	a.statMu.Lock()
	ls := LocalStatus{Enrolled: st.Enrolled(), Revoked: st.Revoked, Server: st.Server, Name: st.Name, Site: st.Site, Version: version.Version,
		ServerVersion: a.serverVer, LastError: a.lastErr}
	if !a.lastOK.IsZero() {
		ls.LastContact = a.lastOK.Unix()
	}
	a.statMu.Unlock()
	ls.Status = a.Status()
	return ls
}

func (a *Agent) uiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Write(page)
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.Local())
	})
	mux.HandleFunc("POST /api/enroll", func(w http.ResponseWriter, r *http.Request) {
		// Browsers cannot send this header cross-site without a preflight.
		if r.Header.Get("X-USS") != "1" {
			writeJSON(w, 403, map[string]string{"error": "missing X-USS header"})
			return
		}
		if st := a.state(); st.Enrolled() && !st.Revoked {
			writeJSON(w, 409, map[string]string{"error": "this probe is already enrolled; run 'unify-syslog-probe reset' on the probe first"})
			return
		}
		var req struct{ Key string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		if _, err := a.Enroll(ctx, req.Key); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, a.Local())
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
