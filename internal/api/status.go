package api

import (
	"context"
	"net/http"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/sysstatus"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// systemStatus is the System status page: the monitor's view plus the state
// of probes, forwarding, notifications and backups.
func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	if s.Status == nil {
		jerr(w, 503, "system status is not available")
		return
	}
	st := s.Status.Status(r.Context())
	type component struct {
		Key   string `json:"key"`
		State string `json:"state"` // ok | warning | error | off
		// Detail is an English text with {name} placeholders from Params.
		Detail string         `json:"detail"`
		Params map[string]any `json:"params,omitempty"`
		Link   string         `json:"link,omitempty"`
	}
	comps := []component{}

	if probes, err := s.probeViews(); err == nil {
		c := component{Key: "probes", State: "off", Detail: "No remote probes", Link: "/probes"}
		if len(probes) > 0 {
			on := 0
			for _, p := range probes {
				if p.Online {
					on++
				}
			}
			c.State, c.Detail, c.Params = "ok", "{online} of {total} online", map[string]any{"online": on, "total": len(probes)}
			if on < len(probes) {
				c.State = "warning"
			}
		}
		comps = append(comps, c)
	}
	if s.Forward != nil {
		c := component{Key: "forwarding", State: "off", Detail: "No forwarding destinations", Link: "/forwarding"}
		sts := s.Forward.Statuses()
		if len(sts) > 0 {
			var failing int
			var queued int64
			for _, x := range sts {
				if x.State == "error" {
					failing++
				}
				queued += x.QueueRecs
			}
			c.State, c.Detail, c.Params = "ok", "{total} destinations, {queued} events queued", map[string]any{"total": len(sts), "queued": queued}
			if failing > 0 {
				c.State, c.Detail, c.Params["failing"] = "error", "{failing} of {total} destinations failing, {queued} events queued", failing
			}
		}
		comps = append(comps, c)
	}
	{
		c := component{Key: "notifications", State: "off", Detail: "No notification channels", Link: "/notifications"}
		if chans, err := s.Meta.NotifyChannels(); err == nil && len(chans) > 0 {
			queued := 0
			for _, n := range s.Meta.NotifyQueueCounts() {
				queued += n
			}
			c.State, c.Detail, c.Params = "ok", "{total} channels, {queued} messages waiting", map[string]any{"total": len(chans), "queued": queued}
			if queued > 0 {
				c.State = "warning"
			}
		}
		comps = append(comps, c)
	}
	if s.Backup != nil {
		b := s.Backup.Status()
		c := component{Key: "backup", State: "off", Detail: "No backup yet", Link: "/backup"}
		switch {
		case b.Running:
			c.State, c.Detail, c.Params = "ok", "Running: {job}", map[string]any{"job": b.Job}
		case b.LastErr != "":
			c.State, c.Detail, c.Params = "error", "Last backup failed: {error}", map[string]any{"error": b.LastErr}
		case !b.LastDone.IsZero():
			c.State, c.Detail, c.Params = "ok", "Last backup {time}", map[string]any{"time": b.LastDone.Unix()}
		}
		if !s.Meta.Backup().Enabled && !b.Running && b.LastErr == "" {
			c.State = "off"
		}
		comps = append(comps, c)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	storage, _ := s.Logs.Storage(ctx)
	if storage == nil {
		storage = &store.StorageInfo{}
	}
	jsonOut(w, struct {
		sysstatus.Status
		Storage    *store.StorageInfo `json:"storage"`
		Components []component        `json:"components"`
		Version    string             `json:"version"`
	}{st, storage, comps, version.Version})
}

func (s *Server) retentionUsage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	u, err := s.Logs.CategoryUsage(ctx)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	jsonOut(w, u)
}
