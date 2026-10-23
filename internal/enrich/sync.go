package enrich

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifiapi"
)

// SyncInterval is how often controllers are read.
const SyncInterval = 5 * time.Minute

// Syncer reads every enabled controller periodically and feeds the names
// into the Enricher.
type Syncer struct {
	Enricher *Enricher
	Log      *slog.Logger

	mu      sync.Mutex
	running map[int64]bool
	kick    chan struct{}
}

func (s *Syncer) Run(ctx context.Context) {
	s.mu.Lock()
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
	}
	s.mu.Unlock()
	t := time.NewTicker(SyncInterval)
	defer t.Stop()
	s.syncAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncAll(ctx)
		case <-s.kick:
			s.syncAll(ctx)
		}
	}
}

// Kick asks for a sync of all controllers soon, e.g. after a change.
func (s *Syncer) Kick() {
	s.mu.Lock()
	k := s.kick
	s.mu.Unlock()
	if k == nil {
		return
	}
	select {
	case k <- struct{}{}:
	default:
	}
}

func (s *Syncer) syncAll(ctx context.Context) {
	ctrls, err := s.Enricher.Meta.Controllers()
	if err != nil {
		return
	}
	for _, c := range ctrls {
		if c.Enabled {
			s.Sync(ctx, c)
		}
	}
}

// Sync reads one controller now and stores the result.
func (s *Syncer) Sync(ctx context.Context, c store.Controller) (clients, devices int, err error) {
	s.mu.Lock()
	if s.running == nil {
		s.running = map[int64]bool{}
	}
	if s.running[c.ID] {
		s.mu.Unlock()
		return 0, 0, nil
	}
	s.running[c.ID] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, c.ID)
		s.mu.Unlock()
	}()
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	snap, err := unifiapi.New(c.URL, c.APIKey, c.VerifyTLS).Fetch(cctx)
	meta := s.Enricher.Meta
	if err != nil {
		if s.Log != nil {
			s.Log.Warn("unifi controller sync", "controller", c.Name, "err", err)
		}
		meta.SetControllerSync(c.ID, time.Now(), err.Error(), 0, 0)
		return 0, 0, err
	}
	clients, devices, err = s.Enricher.ApplySnapshot(c, snap, time.Now())
	if err != nil {
		meta.SetControllerSync(c.ID, time.Now(), err.Error(), 0, 0)
		return 0, 0, err
	}
	meta.SetControllerSync(c.ID, time.Now(), "", clients, devices)
	return clients, devices, nil
}

// ApplySnapshot stores the clients and devices read from controller c.
// Clients that are offline are not in the snapshot, so names are kept and
// only overwritten, never dropped.
func (x *Enricher) ApplySnapshot(c store.Controller, snap *unifiapi.Snapshot, now time.Time) (clients, devices int, err error) {
	var names []store.ClientName
	var devs []store.ControllerDevice
	x.mu.RLock()
	for _, site := range snap.Sites {
		for _, cl := range snap.Clients[site.ID] {
			mac := strings.ToLower(cl.MACAddress)
			if mac == "" {
				continue
			}
			n := store.ClientName{MAC: mac, Name: strings.TrimSpace(cl.Name), IP: cl.IPAddress, Site: c.Site,
				Kind: strings.ToLower(cl.Type), Source: store.NameUniFi, ControllerID: c.ID, Updated: now.Unix()}
			if old, ok := x.clients[mac]; ok {
				n.Hostname = old.Hostname
				if n.IP == "" {
					n.IP = old.IP
				}
			}
			names = append(names, n)
		}
		for _, d := range snap.Devices[site.ID] {
			mac := strings.ToLower(d.MACAddress)
			if mac == "" {
				continue
			}
			devs = append(devs, store.ControllerDevice{MAC: mac, Name: strings.TrimSpace(d.Name), Model: d.Model, Firmware: d.FirmwareVersion,
				Updatable: d.FirmwareUpdatable, State: strings.ToLower(d.State), IP: d.IPAddress, Site: c.Site, ControllerID: c.ID, Updated: now.Unix()})
		}
	}
	x.mu.RUnlock()
	if err := x.Meta.SaveClientNames(names); err != nil {
		return 0, 0, err
	}
	if err := x.Meta.ReplaceControllerDevices(c.ID, devs); err != nil {
		return 0, 0, err
	}
	x.mu.Lock()
	for _, n := range names {
		x.putLocked(n)
	}
	for mac, d := range x.devices {
		if d.ControllerID == c.ID {
			delete(x.devices, mac)
		}
	}
	for _, d := range devs {
		x.devices[d.MAC] = d
	}
	x.mu.Unlock()
	return len(names), len(devs), nil
}
