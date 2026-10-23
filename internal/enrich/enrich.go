// Package enrich adds names and locations to parsed events: client and
// device names from UniFi controllers (or learnt from DHCP), and the
// country and network (AS) of public IP addresses.
package enrich

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/geoip"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

type Enricher struct {
	Meta *store.Meta
	Geo  *geoip.Manager

	mu      sync.RWMutex
	clients map[string]store.ClientName       // by MAC
	byIP    map[string]map[string]string      // site ("" = any) -> IP -> MAC
	devices map[string]store.ControllerDevice // by MAC
	dirty   map[string]store.ClientName
}

func New(meta *store.Meta, geo *geoip.Manager) *Enricher {
	return &Enricher{Meta: meta, Geo: geo, clients: map[string]store.ClientName{}, byIP: map[string]map[string]string{},
		devices: map[string]store.ControllerDevice{}, dirty: map[string]store.ClientName{}}
}

// Load reads the stored names, so enrichment works right after a restart.
func (x *Enricher) Load() error {
	names, err := x.Meta.ClientNames()
	if err != nil {
		return err
	}
	devs, err := x.Meta.ControllerDevices()
	if err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.clients = map[string]store.ClientName{}
	x.byIP = map[string]map[string]string{}
	for _, c := range names {
		x.putLocked(c)
	}
	x.devices = map[string]store.ControllerDevice{}
	for _, d := range devs {
		x.devices[d.MAC] = d
	}
	return nil
}

func (x *Enricher) putLocked(c store.ClientName) {
	if old, ok := x.clients[c.MAC]; ok && old.IP != "" && old.IP != c.IP {
		if m := x.byIP[old.Site]; m != nil && m[old.IP] == c.MAC {
			delete(m, old.IP)
		}
	}
	x.clients[c.MAC] = c
	if c.IP != "" {
		if x.byIP[c.Site] == nil {
			x.byIP[c.Site] = map[string]string{}
		}
		x.byIP[c.Site][c.IP] = c.MAC
	}
}

func displayName(c store.ClientName) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Hostname
}

// macByIP finds the client that holds ip at site; names synced without a
// site mapping match any site.
func (x *Enricher) macByIP(site, ip string) string {
	if m := x.byIP[site]; m != nil {
		if mac := m[ip]; mac != "" {
			return mac
		}
	}
	if site != "" {
		return x.byIP[""][ip]
	}
	return ""
}

// Apply enriches e in place. It runs for every message, so it only reads
// memory, apart from noting DHCP-learnt names for the next Flush.
func (x *Enricher) Apply(e *unifi.Event) {
	if e.Category == unifi.CatDHCP {
		x.learnDHCP(e)
	}
	x.mu.RLock()
	if d, ok := x.devices[e.DeviceMAC]; ok && e.DeviceMAC != "" && d.Name != "" {
		e.SetField("device_name", d.Name)
	}
	mac := e.ClientMAC
	if mac == "" && e.ClientIP != "" {
		mac = x.macByIP(e.Site, e.ClientIP)
	}
	if c, ok := x.clients[mac]; ok && mac != "" {
		if name := displayName(c); name != "" && name != e.ClientName {
			// A name from the controller wins over the DHCP hostname; a
			// learnt hostname only fills a gap.
			if e.ClientName == "" || c.Source == store.NameUniFi {
				if e.ClientName != "" {
					e.SetField("client_hostname", e.ClientName)
				}
				e.ClientName = name
			}
		}
	}
	if e.Category == unifi.CatFirewall || e.Category == unifi.CatIPS {
		for _, p := range []struct{ ip, key string }{{e.SrcIP, "src_name"}, {e.DstIP, "dst_name"}} {
			if p.ip == "" {
				continue
			}
			if c, ok := x.clients[x.macByIP(e.Site, p.ip)]; ok {
				if n := displayName(c); n != "" {
					e.SetField(p.key, n)
				}
			}
		}
	}
	x.mu.RUnlock()
	if x.Geo.Active() {
		geo(e, x.Geo, e.SrcIP, "src")
		geo(e, x.Geo, e.DstIP, "dst")
	}
}

func geo(e *unifi.Event, g *geoip.Manager, ip, prefix string) {
	if ip == "" {
		return
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return
	}
	info := g.Lookup(a)
	if info.Country != "" {
		e.SetField(prefix+"_country", info.Country)
	}
	if info.ASN != 0 {
		e.SetField(prefix+"_asn", strconv.FormatUint(uint64(info.ASN), 10))
		if info.Org != "" {
			e.SetField(prefix+"_as_org", info.Org)
		}
	}
}

// learnDHCP remembers hostnames and addresses handed out by the gateway.
func (x *Enricher) learnDHCP(e *unifi.Event) {
	if e.ClientMAC == "" || (e.EventType != "DHCPACK" && e.EventType != "LEASE_ALLOC" && e.EventType != "LEASE_RENEW") {
		return
	}
	x.mu.RLock()
	c, ok := x.clients[e.ClientMAC]
	x.mu.RUnlock()
	hostname := e.ClientName
	if ok && (hostname == "" || hostname == c.Hostname) && (e.ClientIP == "" || e.ClientIP == c.IP) && c.Site == x.siteFor(c, e.Site) {
		return
	}
	if !ok {
		c = store.ClientName{MAC: e.ClientMAC, Source: store.NameDHCP}
	}
	if hostname != "" {
		c.Hostname = hostname
	}
	if e.ClientIP != "" {
		c.IP = e.ClientIP
	}
	c.Site = x.siteFor(c, e.Site)
	c.Updated = time.Now().Unix()
	x.mu.Lock()
	x.putLocked(c)
	x.dirty[c.MAC] = c
	x.mu.Unlock()
}

// siteFor keeps the site of controller names that were synced without a
// site mapping; learnt names take the site of the log line.
func (x *Enricher) siteFor(c store.ClientName, site string) string {
	if c.Source == store.NameUniFi {
		return c.Site
	}
	return site
}

// Flush stores DHCP-learnt names.
func (x *Enricher) Flush() error {
	x.mu.Lock()
	if len(x.dirty) == 0 {
		x.mu.Unlock()
		return nil
	}
	list := make([]store.ClientName, 0, len(x.dirty))
	for _, c := range x.dirty {
		list = append(list, c)
	}
	x.dirty = map[string]store.ClientName{}
	x.mu.Unlock()
	return x.Meta.SaveClientNames(list)
}

// Clients lists every known client name, sorted by name.
func (x *Enricher) Clients() []store.ClientName {
	x.mu.RLock()
	out := make([]store.ClientName, 0, len(x.clients))
	for _, c := range x.clients {
		out = append(out, c)
	}
	x.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(displayName(out[i])), strings.ToLower(displayName(out[j]))
		if a != b {
			return a < b
		}
		return out[i].MAC < out[j].MAC
	})
	return out
}

// Device returns what a controller reports about the device with mac.
func (x *Enricher) Device(mac string) (store.ControllerDevice, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	d, ok := x.devices[strings.ToLower(mac)]
	return d, ok
}
