package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/ha"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// ---------------------------------------------------------------- peer API

func (s *Server) haPeerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.HA == nil || !s.HA.Enabled() || !s.HA.Authorized(r) {
			jerr(w, 401, "not a paired node")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) haPeerStatus(w http.ResponseWriter, r *http.Request) { jsonOut(w, s.HA.LocalStatus()) }

func (s *Server) haPeerSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.HA.Active() {
		jerr(w, 409, "this node is standby")
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	if err := s.HA.WriteSnapshot(w); err != nil {
		s.Log.Warn("ha snapshot", "err", err)
	}
}

func (s *Server) haPeerJoin(w http.ResponseWriter, r *http.Request) {
	var j ha.JoinRequest
	if err := decode(r, &j); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	resp, err := s.HA.Accept(j, s.Logs.Config())
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	s.Meta.AddAudit(store.AuditEntry{User: "system", Action: "ha_peer_joined", Detail: j.Name + " " + j.URL, IP: clientIP(r)})
	jsonOut(w, resp)
}

// ---------------------------------------------------------------- admin API

func (s *Server) haDefaultURL(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	_, port, err := net.SplitHostPort(s.Cfg.HTTPListen)
	if err != nil || port == "" {
		port = "8080"
	}
	return "https://" + net.JoinHostPort(host, port)
}

func (s *Server) getHA(w http.ResponseWriter, r *http.Request) {
	st := s.HA.State()
	out := map[string]any{
		"state":       st,
		"interfaces":  ha.Interfaces(),
		"default_url": s.haDefaultURL(r),
		"clickhouse":  map[string]any{"local": isLocalCH(s.Logs.Config()), "addr": s.Logs.Config().Addr},
	}
	if st.Enabled {
		if code, err := s.HA.Code(); err == nil && st.Config.PeerURL == "" {
			out["code"] = code
		}
		_, port, _ := net.SplitHostPort(s.Cfg.HTTPListen)
		if conf, err := s.HA.Keepalived(port); err == nil {
			out["keepalived"] = conf
		}
	}
	jsonOut(w, out)
}

func (s *Server) putHA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ha.Setup
		Action string `json:"action"` // start | join | update | leave | sync
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	var err error
	restart := false
	switch req.Action {
	case "start", "join":
		if s.HA.Enabled() {
			jerr(w, 409, "high availability is already set up on this node")
			return
		}
		if req.Action == "start" && isLocalCH(s.Logs.Config()) {
			jerr(w, 400, "both nodes must store logs in the same ClickHouse; connect this node to an external ClickHouse server or cluster first (Administration > Storage)")
			return
		}
		if req.Action == "start" {
			err = s.HA.Start(req.Setup)
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			err = s.HA.Join(ctx, req.Setup)
			cancel()
			// The ClickHouse connection of the first node applies after a restart,
			// and the settings are copied right after it.
			restart = err == nil
		}
	case "update":
		err = s.HA.Update(req.Setup)
	case "leave":
		err = s.HA.Leave()
	case "sync":
		if !s.HA.Standby() {
			jerr(w, 409, "only the standby node copies settings")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		err = s.HA.SyncNow(ctx)
		cancel()
	default:
		jerr(w, 400, "unknown action")
		return
	}
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	s.audit_(r, "settings_ha", req.Action)
	if restart {
		go func() { time.Sleep(time.Second); s.Restart() }()
	}
	jsonOut(w, map[string]any{"state": s.HA.State(), "restart": restart})
}

// standbyGuard refuses changes on the standby node: they would be
// overwritten by the next copy from the active node. Node-local settings
// (storage, high availability, running an update) stay open; personal
// settings live in the shared users table and are changed on the active node.
func (s *Server) standbyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.HA != nil && r.Method != http.MethodGet && r.Method != http.MethodHead && s.HA.Standby() && !standbyAllowed(r.URL.Path) {
			msg := "this is the standby node; make changes on the active node"
			if u := s.HA.PeerURL(); u != "" {
				msg += " (" + u + ")"
			}
			jerr(w, 409, msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func standbyAllowed(p string) bool {
	if readOnlyPost(p) {
		return true
	}
	for _, pre := range []string{"/api/auth/", "/api/settings/ha", "/api/settings/storage", "/api/storage/", "/api/update/", "/api/backup/test-target", "/api/alerts/test", "/api/notify/test", "/api/forward/test", "/api/controllers/test", "/api/settings/smtp/test"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}
