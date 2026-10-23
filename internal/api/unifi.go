package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/alerts"
	"github.com/rhisijmuiden/unify-syslog-server/internal/geoip"
	"github.com/rhisijmuiden/unify-syslog-server/internal/query"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifiapi"
)

// ---------------------------------------------------------------- UniFi controllers

func (s *Server) listControllers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Meta.Controllers()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	for i := range list {
		list[i] = store.MaskController(list[i])
	}
	jsonOut(w, list)
}

func validControllerURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (s *Server) saveController(w http.ResponseWriter, r *http.Request) {
	var c store.Controller
	if err := decode(r, &c); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	c.ID = idParam(r)
	c.Name, c.Site = strings.TrimSpace(c.Name), strings.TrimSpace(c.Site)
	if c.Name == "" {
		jerr(w, 400, "a name is required")
		return
	}
	if !validControllerURL(c.URL) {
		jerr(w, 400, "enter the address of the console, e.g. https://192.168.1.1")
		return
	}
	if c.APIKey == "" {
		jerr(w, 400, "an API key is required")
		return
	}
	if c.ID != 0 {
		if _, err := s.Meta.Controller(c.ID); err != nil {
			jerr(w, 404, "controller not found")
			return
		}
	}
	if err := s.Meta.SaveController(&c); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "controller_saved", c.Name)
	if c.Enabled && s.Syncer != nil {
		go s.Syncer.Sync(context.Background(), c)
	}
	jsonOut(w, store.MaskController(c))
}

func (s *Server) deleteController(w http.ResponseWriter, r *http.Request) {
	id := idParam(r)
	c, err := s.Meta.Controller(id)
	if err != nil {
		jerr(w, 404, "controller not found")
		return
	}
	if err := s.Meta.DeleteController(id); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if s.Enrich != nil {
		s.Enrich.Load()
	}
	s.audit_(r, "controller_deleted", c.Name)
	jsonOut(w, map[string]bool{"ok": true})
}

type syncResult struct {
	Sites   int `json:"sites"`
	Clients int `json:"clients"`
	Devices int `json:"devices"`
}

// testController reads a controller without storing anything.
func (s *Server) testController(w http.ResponseWriter, r *http.Request) {
	var c store.Controller
	if err := decode(r, &c); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if c.APIKey == store.Secret && c.ID != 0 {
		old, err := s.Meta.Controller(c.ID)
		if err != nil {
			jerr(w, 404, "controller not found")
			return
		}
		c.APIKey = old.APIKey
	}
	if !validControllerURL(c.URL) {
		jerr(w, 400, "enter the address of the console, e.g. https://192.168.1.1")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	snap, err := unifiapi.New(c.URL, c.APIKey, c.VerifyTLS).Fetch(ctx)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	res := syncResult{Sites: len(snap.Sites)}
	for _, l := range snap.Clients {
		res.Clients += len(l)
	}
	for _, l := range snap.Devices {
		res.Devices += len(l)
	}
	jsonOut(w, res)
}

func (s *Server) syncController(w http.ResponseWriter, r *http.Request) {
	c, err := s.Meta.Controller(idParam(r))
	if err != nil {
		jerr(w, 404, "controller not found")
		return
	}
	clients, devices, err := s.Syncer.Sync(r.Context(), c)
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	jsonOut(w, syncResult{Clients: clients, Devices: devices})
}

// listClients returns every known client name for the Clients page.
func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	if s.Enrich == nil {
		jsonOut(w, []store.ClientName{})
		return
	}
	u := user(r)
	jsonOut(w, slices.DeleteFunc(s.Enrich.Clients(), func(c store.ClientName) bool { return !u.SiteAllowed(c.Site) }))
}

func (s *Server) controllerDevice(mac string) (store.ControllerDevice, bool) {
	if s.Enrich == nil {
		return store.ControllerDevice{}, false
	}
	return s.Enrich.Device(mac)
}

func (s *Server) firmware(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	devs := slices.DeleteFunc(s.Meta.Devices(), func(d store.InvDevice) bool { return !u.SiteAllowed(d.Site) })
	jsonOut(w, alerts.Firmware(devs, s.controllerDevice, time.Now()))
}

// ---------------------------------------------------------------- GeoIP

func (s *Server) getGeoIP(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"settings": store.MaskGeoIP(s.Meta.GeoIP())}
	if s.Geo != nil {
		out["status"] = s.Geo.Status()
	}
	jsonOut(w, out)
}

// geoIPState tells dashboard viewers (not only admins) why a map may be
// empty: GeoIP off, no database yet, or simply no matching traffic.
func (s *Server) geoIPState(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": s.Meta.GeoIP().Enabled, "loaded": false, "error": ""}
	if s.Geo != nil {
		st := s.Geo.Status()
		out["loaded"], out["error"] = st.Loaded, st.LastError
	}
	jsonOut(w, out)
}

func (s *Server) putGeoIP(w http.ResponseWriter, r *http.Request) {
	var v store.GeoIPSettings
	if err := decode(r, &v); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if v.Provider != geoip.MaxMind {
		v.Provider = geoip.DBIP
	}
	v.AccountID = strings.TrimSpace(v.AccountID)
	if v.Provider == geoip.MaxMind && (v.AccountID == "" || v.LicenseKey == "") {
		jerr(w, 400, "MaxMind needs an account ID and a license key")
		return
	}
	old := s.Meta.GeoIP()
	if err := s.Meta.SaveGeoIP(v); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "settings_geoip", "")
	if s.Geo != nil {
		if !v.Enabled {
			s.Geo.Disable()
		} else if !s.Geo.Active() || old.Provider != v.Provider {
			s.Geo.Load()
			// A new provider, or nothing downloaded yet: fetch now.
			if !s.Geo.Active() || old.Provider != v.Provider {
				go s.Geo.Update(context.Background())
			}
		}
	}
	s.getGeoIP(w, r)
}

func (s *Server) updateGeoIP(w http.ResponseWriter, r *http.Request) {
	if s.Geo == nil {
		jerr(w, 503, "GeoIP is not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if err := s.Geo.Update(ctx); err != nil {
		jerr(w, 502, err.Error())
		return
	}
	s.audit_(r, "geoip_updated", "")
	s.getGeoIP(w, r)
}

// ---------------------------------------------------------------- dashboard panels

type panelReq struct {
	Key   string `json:"key"`
	Query string `json:"query"`
	Field string `json:"field"` // empty: count only
	N     int    `json:"n"`
}

type panelRes struct {
	Count *uint64         `json:"count,omitempty"`
	Items []store.TopItem `json:"items,omitempty"`
	Error string          `json:"error,omitempty"`
}

// panels evaluates many counts and top lists for one time range in a
// single request; the ready-made dashboards are built from them.
func (s *Server) panels(w http.ResponseWriter, r *http.Request) {
	var b struct {
		store.SearchReq
		Panels []panelReq `json:"panels"`
	}
	if err := decode(r, &b); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if len(b.Panels) > 40 {
		jerr(w, 400, "too many panels")
		return
	}
	normalizeRange(&b.SearchReq)
	b.Sites = user(r).ScopeSites(b.Sites)
	type res struct {
		key string
		v   panelRes
	}
	ch := make(chan res, len(b.Panels))
	sem := make(chan struct{}, 6)
	for _, p := range b.Panels {
		go func(p panelReq) {
			sem <- struct{}{}
			defer func() { <-sem }()
			q := b.SearchReq
			q.Query = p.Query
			var out panelRes
			if _, err := query.Compile(p.Query); err != nil {
				out.Error = err.Error()
			} else if p.Field == "" {
				n, err := s.Logs.Count(r.Context(), q)
				if err != nil {
					out.Error = err.Error()
				}
				out.Count = &n
			} else {
				items, err := s.Logs.Top(r.Context(), q, p.Field, p.N)
				if err != nil {
					out.Error = err.Error()
				}
				if items == nil {
					items = []store.TopItem{}
				}
				out.Items = items
			}
			ch <- res{p.Key, out}
		}(p)
	}
	out := map[string]panelRes{}
	for range b.Panels {
		x := <-ch
		out[x.key] = x.v
	}
	jsonOut(w, out)
}
