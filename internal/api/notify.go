package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// ---------------------------------------------------------------- notification channels

type notifyView struct {
	store.NotifyChannel
	// Queued is the number of notifications waiting (quiet hours, batching
	// or retries).
	Queued int `json:"queued"`
}

func (s *Server) listNotify(w http.ResponseWriter, r *http.Request) {
	chans, err := s.Meta.NotifyChannels()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	counts := s.Meta.NotifyQueueCounts()
	out := make([]notifyView, 0, len(chans))
	for _, c := range chans {
		out = append(out, notifyView{NotifyChannel: c.Mask(), Queued: counts[c.ID]})
	}
	jsonOut(w, out)
}

// prepareNotify validates a channel from the request; existing channels
// keep their stored secrets for placeholder values.
func (s *Server) prepareNotify(w http.ResponseWriter, r *http.Request) (store.NotifyChannel, bool) {
	var c store.NotifyChannel
	if err := decode(r, &c); err != nil {
		jerr(w, 400, "invalid request")
		return c, false
	}
	if id := idParam(r); id > 0 {
		c.ID = id
	}
	c.Name = strings.TrimSpace(c.Name)
	if !slices.Contains(store.NotifyKinds, c.Kind) {
		jerr(w, 400, "choose a channel type")
		return c, false
	}
	if c.ID != 0 {
		old, err := s.Meta.NotifyChannel(c.ID)
		if err != nil {
			jerr(w, 404, "channel not found")
			return c, false
		}
		c.KeepSecrets(old)
	}
	cfg := &c.Config
	if cfg.MinSeverity != "" && !slices.Contains(store.NotifySeverities, cfg.MinSeverity) {
		jerr(w, 400, "unknown severity")
		return c, false
	}
	events := []string{}
	for _, e := range cfg.Events {
		if slices.Contains(store.NotifySources, e) && !slices.Contains(events, e) {
			events = append(events, e)
		}
	}
	cfg.Events = events
	if cfg.Language != "" {
		if cfg.Language = i18n.Normalize(cfg.Language); !i18n.Has(cfg.Language) {
			jerr(w, 400, "unknown language")
			return c, false
		}
	}
	if cfg.QuietEnabled && (!notify.ValidClock(cfg.QuietStart) || !notify.ValidClock(cfg.QuietEnd)) {
		jerr(w, 400, "quiet hours need a start and end time as HH:MM")
		return c, false
	}
	cfg.BatchMinutes = min(max(cfg.BatchMinutes, 0), 24*60)
	if err := notify.Validate(c); err != nil {
		jerr(w, 400, err.Error())
		return c, false
	}
	return c, true
}

func (s *Server) saveNotify(w http.ResponseWriter, r *http.Request) {
	c, ok := s.prepareNotify(w, r)
	if !ok {
		return
	}
	if c.Name == "" {
		jerr(w, 400, "a name is required")
		return
	}
	if err := s.Meta.SaveNotifyChannel(&c); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "notify_saved", c.Name+" ("+c.Kind+")")
	saved, _ := s.Meta.NotifyChannel(c.ID)
	jsonOut(w, notifyView{NotifyChannel: saved.Mask()})
}

func (s *Server) deleteNotify(w http.ResponseWriter, r *http.Request) {
	c, err := s.Meta.NotifyChannel(idParam(r))
	if err != nil {
		jerr(w, 404, "channel not found")
		return
	}
	if err := s.Meta.DeleteNotifyChannel(c.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "notify_deleted", c.Name)
	jsonOut(w, map[string]bool{"ok": true})
}

// testNotify sends a test message with the settings from the request.
func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	c, ok := s.prepareNotify(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := notify.Test(ctx, c, s.Meta.General())
	if c.ID != 0 {
		s.Meta.SetNotifyResult(c.ID, time.Now(), err)
	}
	if err != nil {
		jerr(w, 502, err.Error())
		return
	}
	jsonOut(w, map[string]bool{"ok": true})
}
