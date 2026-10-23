package store

import (
	"net/netip"
	"time"
)

// Source is a device that sends syslog to us, keyed by its source IP.
// Each source belongs to a site (one of the user's locations).
type Source struct {
	IP        string `json:"ip"`
	Name      string `json:"name"`
	Site      string `json:"site"`
	Hostname  string `json:"hostname"`
	Model     string `json:"model"`
	MAC       string `json:"mac"`
	Version   string `json:"version"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	dirty     bool
	// siteChanged makes FlushSources store the site of an existing row.
	siteChanged bool
}

type SiteRange struct {
	ID   int64  `json:"id"`
	Site string `json:"site"`
	CIDR string `json:"cidr"`
	pfx  netip.Prefix
}

const DefaultSite = "default"

func (m *Meta) reloadSources() error {
	rows, err := m.DB.Query(`SELECT ip,name,site,hostname,model,mac,version,first_seen,last_seen FROM sources`)
	if err != nil {
		return err
	}
	defer rows.Close()
	src := map[string]Source{}
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.IP, &s.Name, &s.Site, &s.Hostname, &s.Model, &s.MAC, &s.Version, &s.FirstSeen, &s.LastSeen); err != nil {
			return err
		}
		src[s.IP] = s
	}
	m.mu.Lock()
	m.sources = src
	m.mu.Unlock()
	return rows.Err()
}

func (m *Meta) SiteRanges() ([]SiteRange, error) {
	rows, err := m.DB.Query(`SELECT id,site,cidr FROM site_ranges ORDER BY site,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteRange{}
	for rows.Next() {
		var r SiteRange
		if err := rows.Scan(&r.ID, &r.Site, &r.CIDR); err != nil {
			return nil, err
		}
		r.pfx, _ = netip.ParsePrefix(r.CIDR)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Resolve returns the site and display name for an incoming event and
// records the source if it is new. It is called on the hot ingest path and
// only touches memory; FlushSources persists the changes. key is the
// source IP, or "IP@probe" for messages relayed by a probe (addresses
// behind different probes may overlap); site is the probe's site then.
func (m *Meta) Resolve(key, hostname, model, mac, version string, ranges []SiteRange, site string) (string, string) {
	ip := key
	now := time.Now().Unix()
	m.mu.RLock()
	s, ok := m.sources[ip]
	m.mu.RUnlock()
	if ok && now-s.LastSeen < 30 && (site == "" || s.Site == site) {
		return s.Site, s.Name
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok = m.sources[ip]
	if !ok {
		s = Source{IP: ip, FirstSeen: now, Site: siteFor(ip, ranges)}
	}
	if site != "" && s.Site != site {
		// The probe's site decides; persisted below with the source.
		s.Site = site
		s.siteChanged = true
	}
	if hostname != "" {
		s.Hostname = hostname
	}
	if model != "" {
		s.Model, s.MAC, s.Version = model, mac, version
	}
	s.LastSeen = now
	s.dirty = true
	m.sources[ip] = s
	return s.Site, s.Name
}

func siteFor(ip string, ranges []SiteRange) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return DefaultSite
	}
	best, bestBits := DefaultSite, -1
	for _, r := range ranges {
		if r.pfx.IsValid() && r.pfx.Contains(addr.Unmap()) && r.pfx.Bits() > bestBits {
			best, bestBits = r.Site, r.pfx.Bits()
		}
	}
	return best
}

func (m *Meta) FlushSources() error {
	m.mu.Lock()
	var dirty []Source
	for k, s := range m.sources {
		if s.dirty {
			dirty = append(dirty, s)
			s.dirty, s.siteChanged = false, false
			m.sources[k] = s
		}
	}
	m.mu.Unlock()
	if len(dirty) == 0 {
		return nil
	}
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, s := range dirty {
		if s.siteChanged {
			if _, err := tx.Exec(`UPDATE sources SET site=? WHERE ip=?`, s.Site, s.IP); err != nil {
				tx.Rollback()
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO sources(ip,name,site,hostname,model,mac,version,first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(ip) DO UPDATE SET hostname=excluded.hostname, model=excluded.model, mac=excluded.mac, version=excluded.version, last_seen=excluded.last_seen`,
			s.IP, s.Name, s.Site, s.Hostname, s.Model, s.MAC, s.Version, s.FirstSeen, s.LastSeen)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (m *Meta) Sources() []Source {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Source, 0, len(m.sources))
	for _, s := range m.sources {
		out = append(out, s)
	}
	return out
}

func (m *Meta) UpdateSource(ip, name, site string) error {
	if site == "" {
		site = DefaultSite
	}
	m.FlushSources()
	if _, err := m.DB.Exec(`UPDATE sources SET name=?, site=? WHERE ip=?`, name, site, ip); err != nil {
		return err
	}
	return m.reloadSources()
}

func (m *Meta) DeleteSource(ip string) error {
	if _, err := m.DB.Exec(`DELETE FROM sources WHERE ip=?`, ip); err != nil {
		return err
	}
	return m.reloadSources()
}

func (m *Meta) SaveSiteRange(r *SiteRange) error {
	if _, err := netip.ParsePrefix(r.CIDR); err != nil {
		return err
	}
	if r.ID == 0 {
		res, err := m.DB.Exec(`INSERT INTO site_ranges(site,cidr) VALUES(?,?)`, r.Site, r.CIDR)
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
	} else if _, err := m.DB.Exec(`UPDATE site_ranges SET site=?, cidr=? WHERE id=?`, r.Site, r.CIDR, r.ID); err != nil {
		return err
	}
	return m.applyRanges()
}

func (m *Meta) DeleteSiteRange(id int64) error {
	if _, err := m.DB.Exec(`DELETE FROM site_ranges WHERE id=?`, id); err != nil {
		return err
	}
	return nil
}

// applyRanges assigns sources that are still on the default site to the
// site whose range now matches them.
func (m *Meta) applyRanges() error {
	ranges, err := m.SiteRanges()
	if err != nil {
		return err
	}
	m.FlushSources()
	for _, s := range m.Sources() {
		if s.Site == DefaultSite || s.Site == "" {
			if site := siteFor(s.IP, ranges); site != s.Site {
				m.DB.Exec(`UPDATE sources SET site=? WHERE ip=?`, site, s.IP)
			}
		}
	}
	return m.reloadSources()
}

// Sites lists all distinct site names.
func (m *Meta) Sites() []string {
	seen := map[string]bool{DefaultSite: true}
	out := []string{DefaultSite}
	for _, s := range m.Sources() {
		if s.Site != "" && !seen[s.Site] {
			seen[s.Site] = true
			out = append(out, s.Site)
		}
	}
	for _, site := range m.probeSites() {
		if !seen[site] {
			seen[site] = true
			out = append(out, site)
		}
	}
	if rs, err := m.SiteRanges(); err == nil {
		for _, r := range rs {
			if !seen[r.Site] {
				seen[r.Site] = true
				out = append(out, r.Site)
			}
		}
	}
	return out
}
