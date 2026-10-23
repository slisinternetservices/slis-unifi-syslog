package api

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// ---------------------------------------------------------------- devices / site overview

type deviceView struct {
	store.InvDevice
	Status string `json:"status"` // active, silent or inactive (silent but not monitored)
	Count  uint64 `json:"count"`
	Errors uint64 `json:"errors"`
	// From a UniFi controller, when one is connected.
	ControllerName string `json:"controller_name,omitempty"`
	Updatable      bool   `json:"updatable,omitempty"`
}

func deviceStatus(d store.InvDevice, now time.Time, silentAfter time.Duration) string {
	switch {
	case now.Sub(time.Unix(d.LastSeen, 0)) < silentAfter:
		return "active"
	case d.Monitored:
		return "silent"
	default:
		return "inactive"
	}
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 7
	}
	counts := map[string]store.Device{}
	if c, err := s.Logs.Devices(r.Context(), time.Now().AddDate(0, 0, -days)); err == nil {
		for _, d := range c {
			counts[store.DeviceKey(d.MAC, d.Hostname, d.SourceIP)] = d
		}
	}
	now := time.Now()
	silentAfter := time.Duration(s.Meta.Inventory().SilentMinutes) * time.Minute
	out := []deviceView{}
	u := user(r)
	for _, d := range s.Meta.Devices() {
		if !u.SiteAllowed(d.Site) {
			continue
		}
		c := counts[d.Key]
		v := deviceView{InvDevice: d, Status: deviceStatus(d, now, silentAfter), Count: c.Count, Errors: c.Errors}
		if cd, ok := s.controllerDevice(d.MAC); ok && d.MAC != "" {
			v.ControllerName, v.Updatable = cd.Name, cd.Updatable
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Site != out[j].Site {
			return out[i].Site < out[j].Site
		}
		return strings.ToLower(first(out[i].Name, out[i].Hostname)) < strings.ToLower(first(out[j].Name, out[j].Hostname))
	})
	jsonOut(w, out)
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name      string `json:"name"`
		Notes     string `json:"notes"`
		Monitored bool   `json:"monitored"`
	}
	if err := decode(r, &b); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	key, _ := url.PathUnescape(chi.URLParam(r, "key"))
	if err := s.Meta.UpdateDevice(key, strings.TrimSpace(b.Name), strings.TrimSpace(b.Notes), b.Monitored); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "device_updated", key)
	jsonOut(w, map[string]bool{"ok": true})
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	key, _ := url.PathUnescape(chi.URLParam(r, "key"))
	if err := s.Meta.DeleteDevice(key); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "device_deleted", key)
	jsonOut(w, map[string]bool{"ok": true})
}

type siteView struct {
	Site     string   `json:"site"`
	Ranges   []string `json:"ranges"`
	Devices  int      `json:"devices"`
	Active   int      `json:"active"`
	Silent   int      `json:"silent"`
	Senders  int      `json:"senders"`
	LastSeen int64    `json:"last_seen"`
	Total    uint64   `json:"total"`
	Blocked  uint64   `json:"blocked"`
	Threats  uint64   `json:"threats"`
	Errors   uint64   `json:"errors"`
}

// siteOverview summarises every site for the last 24 hours.
func (s *Server) siteOverview(w http.ResponseWriter, r *http.Request) {
	sites := map[string]*siteView{}
	get := func(name string) *siteView {
		if name == "" {
			name = store.DefaultSite
		}
		v := sites[name]
		if v == nil {
			v = &siteView{Site: name, Ranges: []string{}}
			sites[name] = v
		}
		return v
	}
	for _, name := range s.Meta.Sites() {
		get(name)
	}
	if rs, err := s.Meta.SiteRanges(); err == nil {
		for _, rg := range rs {
			v := get(rg.Site)
			v.Ranges = append(v.Ranges, rg.CIDR)
		}
	}
	for _, src := range s.Meta.Sources() {
		v := get(src.Site)
		v.Senders++
		v.LastSeen = max(v.LastSeen, src.LastSeen)
	}
	now := time.Now()
	silentAfter := time.Duration(s.Meta.Inventory().SilentMinutes) * time.Minute
	for _, d := range s.Meta.Devices() {
		v := get(d.Site)
		v.Devices++
		switch deviceStatus(d, now, silentAfter) {
		case "active":
			v.Active++
		case "silent":
			v.Silent++
		}
		v.LastSeen = max(v.LastSeen, d.LastSeen)
	}
	if st, err := s.Logs.SiteStats(r.Context(), now.Add(-24*time.Hour)); err == nil {
		for _, x := range st {
			v := get(x.Site)
			v.Total, v.Blocked, v.Threats, v.Errors = x.Total, x.Blocked, x.Threats, x.Errors
		}
	}
	out := make([]*siteView, 0, len(sites))
	u := user(r)
	for _, v := range sites {
		if !u.SiteAllowed(v.Site) {
			continue
		}
		// The default site only matters while something actually lands there.
		if v.Site == store.DefaultSite && v.Senders == 0 && v.Devices == 0 && v.Total == 0 {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Site == store.DefaultSite) != (out[j].Site == store.DefaultSite) {
			return out[j].Site == store.DefaultSite
		}
		return strings.ToLower(out[i].Site) < strings.ToLower(out[j].Site)
	})
	jsonOut(w, out)
}

func (s *Server) getInventory(w http.ResponseWriter, r *http.Request) { jsonOut(w, s.Meta.Inventory()) }

func (s *Server) putInventory(w http.ResponseWriter, r *http.Request) {
	var b store.InventorySettings
	if err := decode(r, &b); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if b.SilentMinutes < 5 || b.SilentMinutes > 7*24*60 {
		jerr(w, 400, "the wait time must be between 5 minutes and 7 days")
		return
	}
	b.Recipients = strings.TrimSpace(b.Recipients)
	// Empty recipients fall back to the SMTP default recipient.
	for _, p := range strings.FieldsFunc(b.Recipients, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if !strings.Contains(p, "@") {
			jerr(w, 400, "invalid email address: "+p)
			return
		}
	}
	if err := s.Meta.SetSetting("inventory", b); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "inventory_settings", "")
	jsonOut(w, b)
}

func first(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
