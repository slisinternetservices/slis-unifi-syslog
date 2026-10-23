package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probehub"
	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// enrollTTL is how long an enrollment code stays valid.
const enrollTTL = 7 * 24 * time.Hour

// ---------------------------------------------------------------- probe API

func (s *Server) probeAuth(w http.ResponseWriter, r *http.Request) *store.Probe {
	p, err := s.Meta.ProbeByKey(bearer(r))
	if err != nil {
		jerr(w, 401, "unknown or revoked probe key")
		return nil
	}
	return p
}

func (s *Server) probeEnroll(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.tooManyAttempts("probe:" + ip) {
		jerr(w, 429, "too many failed attempts, try again in 15 minutes")
		return
	}
	var req probeproto.EnrollRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	p, key, err := s.Meta.EnrollProbe(req.Code, req.Hostname, req.Version, ip)
	if err != nil {
		s.recordFail("probe:" + ip)
		if errors.Is(err, store.ErrNotFound) {
			jerr(w, 403, "unknown or already used enrollment code")
			return
		}
		jerr(w, 403, err.Error())
		return
	}
	s.Meta.AddAudit(store.AuditEntry{User: "system", Action: "probe_enrolled", Detail: fmt.Sprintf("%s (%s, %s %s)", p.Name, req.Hostname, req.OS, req.Arch), IP: ip})
	s.Log.Info("probe enrolled", "probe", p.Name, "host", req.Hostname, "ip", ip)
	jsonOut(w, probeproto.EnrollResponse{ID: p.ID, Name: p.Name, Site: p.Site, Key: key, Settings: p.Settings})
}

func (s *Server) probeHeartbeat(w http.ResponseWriter, r *http.Request) {
	p := s.probeAuth(w, r)
	if p == nil {
		return
	}
	var st probeproto.Status
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&st); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	jsonOut(w, s.Probes.Heartbeat(p, st, clientIP(r)))
}

func (s *Server) probeIngest(w http.ResponseWriter, r *http.Request) {
	p := s.probeAuth(w, r)
	if p == nil {
		return
	}
	epoch := r.Header.Get(probeproto.HeaderEpoch)
	if epoch == "" {
		jerr(w, 400, "missing "+probeproto.HeaderEpoch)
		return
	}
	resp, err := s.Probes.Ingest(r.Context(), p, epoch, http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		code := 400
		if errors.Is(err, probehub.ErrBusy) {
			code = 503
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "accepted": resp.Accepted, "seq": resp.Seq})
		return
	}
	jsonOut(w, resp)
}

// probeDownload serves the probe release of this server's version. It needs
// no key: the installers fetch it before enrollment, and it holds nothing
// that is not public on GitHub.
func (s *Server) probeDownload(w http.ResponseWriter, r *http.Request) {
	arch := r.URL.Query().Get("arch")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	a, err := s.Probes.Asset(ctx, arch)
	if err != nil {
		jerr(w, 404, err.Error())
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+probeproto.AssetName(a.Arch)+`"`)
	w.Header().Set("X-Checksum-SHA256", a.SHA256)
	w.Header().Set("X-Probe-Version", a.Version)
	http.ServeContent(w, r, probeproto.AssetName(a.Arch), time.Time{}, f)
}

// ---------------------------------------------------------------- admin API

type probeView struct {
	store.Probe
	Online   bool   `json:"online"`
	Outdated bool   `json:"outdated"`
	Ingested uint64 `json:"ingested"` // since the main server started
}

func (s *Server) probeViews() ([]probeView, error) {
	probes, err := s.Meta.Probes()
	if err != nil {
		return nil, err
	}
	out := make([]probeView, 0, len(probes))
	for _, p := range probes {
		out = append(out, probeView{Probe: p, Online: s.Probes.Online(p), Outdated: p.Enrolled && p.Version != "" && p.Version != version.Version, Ingested: s.Probes.Received(p.ID)})
	}
	return out, nil
}

func (s *Server) listProbes(w http.ResponseWriter, r *http.Request) {
	list, err := s.probeViews()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	jsonOut(w, map[string]any{
		"probes":         list,
		"server_version": version.Version,
		"server_url":     s.probeServerURL(r),
		"pin":            s.TLSPin,
		"repo":           version.Repo,
		"settings":       s.Meta.ProbeSettings(),
	})
}

// probeSummary is the probe overview for the dashboard; everyone sees the
// probes of the sites they may see.
func (s *Server) probeSummary(w http.ResponseWriter, r *http.Request) {
	list, err := s.probeViews()
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	u := user(r)
	type item struct {
		ID            int64   `json:"id"`
		Name          string  `json:"name"`
		Site          string  `json:"site"`
		Enrolled      bool    `json:"enrolled"`
		Online        bool    `json:"online"`
		Outdated      bool    `json:"outdated"`
		Version       string  `json:"version"`
		LastSeen      int64   `json:"last_seen"`
		Rate          float64 `json:"rate"`
		BufferRecords int64   `json:"buffer_records"`
		BufferBytes   int64   `json:"buffer_bytes"`
		BufferMax     int64   `json:"buffer_max"`
		Devices       int     `json:"devices"`
	}
	out := []item{}
	for _, p := range list {
		if !u.SiteAllowed(p.Site) {
			continue
		}
		out = append(out, item{p.ID, p.Name, p.Site, p.Enrolled, p.Online, p.Outdated, p.Version, p.LastSeen, p.Status.Rate,
			p.Status.BufferRecords, p.Status.BufferBytes, p.Status.BufferMax, p.Status.Devices})
	}
	jsonOut(w, out)
}

// probeServerURL is the address put in enrollment keys: the configured
// one, else the address the administrator uses, with https (the web port
// also speaks TLS).
func (s *Server) probeServerURL(r *http.Request) string {
	if u := s.Meta.ProbeSettings().ServerURL; u != "" {
		return strings.TrimRight(u, "/")
	}
	b := s.baseURL(r)
	if u, err := url.Parse(b); err == nil {
		u.Scheme = "https"
		u.Path = ""
		return u.String()
	}
	return b
}

type probeRequest struct {
	Name     string              `json:"name"`
	Site     string              `json:"site"`
	Settings probeproto.Settings `json:"settings"`
	// ServerURL overrides the address in the enrollment key.
	ServerURL string `json:"server_url"`
}

func validProbe(req *probeRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	req.Site = strings.TrimSpace(req.Site)
	if req.Name == "" {
		return errors.New("enter a name")
	}
	if req.Settings.UDP == "" && req.Settings.TCP == "" {
		return errors.New("enable UDP or TCP")
	}
	for _, a := range []string{req.Settings.UDP, req.Settings.TCP} {
		if a == "" {
			continue
		}
		if _, port, ok := strings.Cut(a, ":"); !ok || port == "" {
			return fmt.Errorf("listen address %q must look like :514 or 0.0.0.0:514", a)
		}
	}
	if req.Settings.BufferMB < 16 || req.Settings.BufferMB > 1<<20 {
		return errors.New("buffer size must be between 16 MB and 1 TB")
	}
	if req.Settings.BufferHours < 0 {
		return errors.New("buffer age cannot be negative")
	}
	return nil
}

type enrollView struct {
	Probe     probeView `json:"probe"`
	Code      string    `json:"code"`
	Key       string    `json:"key"`
	ServerURL string    `json:"server_url"`
	Expires   int64     `json:"expires"`
}

func (s *Server) enrollKey(r *http.Request, p *store.Probe, code, serverURL string) enrollView {
	if serverURL == "" {
		serverURL = s.probeServerURL(r)
	}
	serverURL = strings.TrimRight(serverURL, "/")
	k := probeproto.EnrollKey{URL: serverURL, Code: code, Pin: s.TLSPin}
	return enrollView{Probe: probeView{Probe: *p}, Code: code, Key: k.String(), ServerURL: serverURL, Expires: time.Now().Add(enrollTTL).Unix()}
}

func (s *Server) createProbe(w http.ResponseWriter, r *http.Request) {
	var req probeRequest
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	req.Settings = req.Settings.Normalize()
	if err := validProbe(&req); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	if req.ServerURL != "" {
		if u, err := url.Parse(req.ServerURL); err != nil || u.Scheme != "https" || u.Host == "" {
			jerr(w, 400, "the server address must start with https://")
			return
		}
	}
	p := &store.Probe{Name: req.Name, Site: req.Site, Settings: req.Settings}
	code, err := s.Meta.CreateProbe(p, enrollTTL)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "probe_created", fmt.Sprintf("%s (site %s)", p.Name, p.Site))
	jsonOut(w, s.enrollKey(r, p, code, req.ServerURL))
}

func (s *Server) updateProbe(w http.ResponseWriter, r *http.Request) {
	p, err := s.Meta.Probe(idParam(r))
	if err != nil {
		jerr(w, 404, "probe not found")
		return
	}
	var req probeRequest
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	req.Settings = req.Settings.Normalize()
	if err := validProbe(&req); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	p.Name, p.Site, p.Settings = req.Name, req.Site, req.Settings
	if err := s.Meta.UpdateProbe(p); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "probe_updated", fmt.Sprintf("%s (site %s, buffer %d MB, udp %q, tcp %q)", p.Name, p.Site, p.Settings.BufferMB, p.Settings.UDP, p.Settings.TCP))
	jsonOut(w, p)
}

func (s *Server) deleteProbe(w http.ResponseWriter, r *http.Request) {
	p, err := s.Meta.Probe(idParam(r))
	if err != nil {
		jerr(w, 404, "probe not found")
		return
	}
	if err := s.Meta.DeleteProbe(p.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "probe_deleted", p.Name)
	jsonOut(w, map[string]bool{"ok": true})
}

// probeCode issues a new enrollment key, e.g. to reinstall a probe.
func (s *Server) probeCode(w http.ResponseWriter, r *http.Request) {
	p, err := s.Meta.Probe(idParam(r))
	if err != nil {
		jerr(w, 404, "probe not found")
		return
	}
	var req struct {
		ServerURL string `json:"server_url"`
	}
	decode(r, &req)
	code, err := s.Meta.NewProbeCode(p.ID, enrollTTL)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "probe_code", p.Name)
	jsonOut(w, s.enrollKey(r, p, code, req.ServerURL))
}

func (s *Server) updateProbes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if req.All {
		probes, _ := s.Meta.Probes()
		req.IDs = req.IDs[:0]
		for _, p := range probes {
			req.IDs = append(req.IDs, p.ID)
		}
	}
	n, err := s.Probes.RequestUpdate(req.IDs)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if n > 0 {
		s.audit_(r, "probe_update", fmt.Sprintf("%d probes to %s", n, version.Version))
	}
	jsonOut(w, map[string]any{"requested": n, "version": version.Version})
}

func (s *Server) getProbeSettings(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, s.Meta.ProbeSettings())
}

func (s *Server) putProbeSettings(w http.ResponseWriter, r *http.Request) {
	var ps store.ProbeSettings
	if err := decode(r, &ps); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	ps.ServerURL = strings.TrimRight(strings.TrimSpace(ps.ServerURL), "/")
	if ps.ServerURL != "" {
		if u, err := url.Parse(ps.ServerURL); err != nil || u.Scheme != "https" || u.Host == "" {
			jerr(w, 400, "the server address must start with https://")
			return
		}
	}
	if err := s.Meta.SetProbeSettings(ps); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "settings_probes", fmt.Sprintf("auto update %v, offline after %d min", ps.AutoUpdate, ps.OfflineMinutes))
	jsonOut(w, s.Meta.ProbeSettings())
}
