package api

import (
	"context"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rhisijmuiden/unify-syslog-server/integrations"
	"github.com/rhisijmuiden/unify-syslog-server/internal/forward"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// ---------------------------------------------------------------- forwarding

type forwardTotals struct {
	Events   int64 `json:"events"`
	Bytes    int64 `json:"bytes"`
	Errors   int64 `json:"errors"`
	Rejected int64 `json:"rejected"`
	Dropped  int64 `json:"dropped"`
}

type forwardView struct {
	store.ForwardDestination
	Status forward.Status `json:"status"`
	Total  forwardTotals  `json:"total"`
	Day    forwardTotals  `json:"day"`
	// Hourly holds events per hour for the last 24 hours, oldest first.
	Hourly []int64 `json:"hourly"`
	// DailyBytes estimates the volume per day from the last 24 hours.
	DailyBytes int64 `json:"daily_bytes"`
}

func (s *Server) listForward(w http.ResponseWriter, r *http.Request) {
	dests, err := s.Meta.ForwardDestinations()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	statuses := map[int64]forward.Status{}
	if s.Forward != nil {
		statuses = s.Forward.Statuses()
	}
	now := time.Now()
	start := now.Truncate(time.Hour).Add(-23 * time.Hour)
	hours, _ := s.Meta.ForwardStats(start)
	totals, _ := s.Meta.ForwardTotals()
	out := make([]forwardView, 0, len(dests))
	for _, d := range dests {
		v := forwardView{ForwardDestination: d.Mask(), Status: statuses[d.ID], Hourly: make([]int64, 24)}
		if v.Status.ID == 0 {
			v.Status = forward.Status{ID: d.ID, State: "disabled"}
		}
		t := totals[d.ID]
		v.Total = forwardTotals{t.Events, t.Bytes, t.Errors, t.Rejected, t.Dropped}
		for _, h := range hours {
			if h.DestID != d.ID {
				continue
			}
			if i := int((h.Hour - start.Unix()) / 3600); i >= 0 && i < 24 {
				v.Hourly[i] += h.Events
			}
			v.Day.Events += h.Events
			v.Day.Bytes += h.Bytes
			v.Day.Errors += h.Errors
			v.Day.Rejected += h.Rejected
			v.Day.Dropped += h.Dropped
		}
		// A destination younger than a day is extrapolated from its age.
		age := now.Sub(time.Unix(d.CreatedAt, 0)).Hours()
		v.DailyBytes = v.Day.Bytes
		if age < 24 {
			v.DailyBytes = int64(float64(v.Day.Bytes) * 24 / max(age, 1))
		}
		out = append(out, v)
	}
	jsonOut(w, out)
}

// prepareForward validates a destination from the request; existing
// destinations keep their stored secrets for placeholder values.
func (s *Server) prepareForward(w http.ResponseWriter, r *http.Request) (store.ForwardDestination, bool) {
	var d store.ForwardDestination
	if err := decode(r, &d); err != nil {
		jerr(w, 400, "invalid request")
		return d, false
	}
	if id := idParam(r); id > 0 {
		d.ID = id
	}
	d.Name = strings.TrimSpace(d.Name)
	if !slices.Contains(store.ForwardKinds, d.Kind) {
		jerr(w, 400, "choose a destination type")
		return d, false
	}
	if d.ID != 0 {
		old, err := s.Meta.ForwardDestination(d.ID)
		if err != nil {
			jerr(w, 404, "destination not found")
			return d, false
		}
		d.KeepSecrets(old)
		d.CreatedAt = old.CreatedAt
	}
	if _, err := forward.NewSender(d); err != nil {
		jerr(w, 400, err.Error())
		return d, false
	}
	cats, _ := s.Meta.Categories()
	if err := forward.ValidateFilter(d.Filter, cats); err != nil {
		jerr(w, 400, "filter: "+err.Error())
		return d, false
	}
	return d, true
}

func (s *Server) saveForward(w http.ResponseWriter, r *http.Request) {
	d, ok := s.prepareForward(w, r)
	if !ok {
		return
	}
	if d.Name == "" {
		jerr(w, 400, "a name is required")
		return
	}
	if err := s.Meta.SaveForwardDestination(&d); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.reloadForward()
	s.audit_(r, "forward_saved", d.Name+" ("+d.Kind+")")
	saved, _ := s.Meta.ForwardDestination(d.ID)
	jsonOut(w, saved.Mask())
}

func (s *Server) reloadForward() {
	if s.Forward != nil {
		if err := s.Forward.Reload(); err != nil {
			s.Log.Warn("reload forwarding", "err", err)
		}
	}
}

func (s *Server) deleteForward(w http.ResponseWriter, r *http.Request) {
	d, err := s.Meta.ForwardDestination(idParam(r))
	if err != nil {
		jerr(w, 404, "destination not found")
		return
	}
	if err := s.Meta.DeleteForwardDestination(d.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.reloadForward()
	s.audit_(r, "forward_deleted", d.Name)
	jsonOut(w, map[string]bool{"ok": true})
}

// testForward sends one test event with the settings from the request.
func (s *Server) testForward(w http.ResponseWriter, r *http.Request) {
	d, ok := s.prepareForward(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	instance := s.Meta.General().InstanceName
	if instance == "" {
		instance = "UniFi Syslog Server"
	}
	if err := forward.Test(ctx, d, instance); err != nil {
		jerr(w, 502, err.Error())
		return
	}
	jsonOut(w, map[string]bool{"ok": true})
}

// installTemplate puts the bundled index template into Elasticsearch or
// OpenSearch for the destination's data stream.
func (s *Server) installTemplate(w http.ResponseWriter, r *http.Request) {
	d, ok := s.prepareForward(w, r)
	if !ok {
		return
	}
	tpl, err := fs.ReadFile(integrations.FS, "elastic/index-template.json")
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	name, err := forward.InstallTemplate(ctx, d, tpl)
	if err != nil {
		jerr(w, 502, err.Error())
		return
	}
	s.audit_(r, "forward_template_installed", d.Name+": "+name)
	jsonOut(w, map[string]string{"template": name})
}

type contentFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// forwardContent lists the bundled Sentinel, Elastic and Logstash content.
func (s *Server) forwardContent(w http.ResponseWriter, r *http.Request) {
	out := []contentFile{}
	fs.WalkDir(integrations.FS, ".", func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() && (p == "grafana" || p == "prometheus") {
			return fs.SkipDir
		}
		if err == nil && !e.IsDir() {
			info, _ := e.Info()
			out = append(out, contentFile{Path: p, Size: info.Size()})
		}
		return nil
	})
	jsonOut(w, out)
}

func (s *Server) forwardContentFile(w http.ResponseWriter, r *http.Request) {
	p := path.Clean(chi.URLParam(r, "*"))
	b, err := fs.ReadFile(integrations.FS, p)
	if err != nil {
		jerr(w, 404, "not found")
		return
	}
	ct := mime.TypeByExtension(path.Ext(p))
	switch path.Ext(p) {
	case ".ndjson":
		ct = "application/x-ndjson"
	case ".kql", ".conf", ".md":
		ct = "text/plain; charset=utf-8"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(p)+`"`)
	w.Write(b)
}
