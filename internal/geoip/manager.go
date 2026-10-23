package geoip

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Settings as stored by the web interface.
type Settings struct {
	Enabled    bool
	AutoUpdate bool
	Source     Source
}

// Manager owns the open databases and refreshes them in the background.
type Manager struct {
	Dir      string
	Settings func() Settings
	Client   *http.Client
	Log      *slog.Logger

	db atomic.Pointer[DB]

	mu        sync.Mutex
	running   bool
	lastCheck time.Time
	lastError string
}

// Status is shown on the GeoIP settings page.
type Status struct {
	Loaded    bool      `json:"loaded"`
	Databases []Meta    `json:"databases"`
	Updating  bool      `json:"updating"`
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error"`
}

// Load opens whatever is on disk; a missing database is not an error.
func (m *Manager) Load() {
	db, err := Open(m.Dir)
	if err != nil {
		return
	}
	if old := m.db.Swap(db); old != nil {
		// Readers in flight may still hold the old one; give them time.
		time.AfterFunc(time.Minute, old.Close)
	}
}

// Lookup returns nothing while GeoIP is off or no database is loaded.
func (m *Manager) Lookup(ip netip.Addr) Info {
	if m == nil {
		return Info{}
	}
	return m.db.Load().Lookup(ip)
}

// Active reports whether lookups currently return data.
func (m *Manager) Active() bool { return m != nil && m.db.Load() != nil }

// Disable drops the open databases so lookups stop at once.
func (m *Manager) Disable() {
	if old := m.db.Swap(nil); old != nil {
		time.AfterFunc(time.Minute, old.Close)
	}
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{Loaded: m.Active(), Databases: Describe(m.Dir), Updating: m.running, LastCheck: m.lastCheck, LastError: m.lastError}
}

// maxAge is how old a database may get before it is refreshed. DB-IP Lite
// is published monthly, GeoLite2 twice a week.
func maxAge(provider string) time.Duration {
	if provider == MaxMind {
		return 7 * 24 * time.Hour
	}
	return 30 * 24 * time.Hour
}

func (m *Manager) stale(s Settings, now time.Time) bool {
	for _, f := range []string{CountryFile, ASNFile} {
		st, err := os.Stat(filepath.Join(m.Dir, f))
		if err != nil || now.Sub(st.ModTime()) > maxAge(s.Source.Provider) {
			return true
		}
	}
	return false
}

// Update downloads fresh databases now and loads them.
func (m *Manager) Update(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = true
	m.mu.Unlock()
	err := Download(ctx, m.Client, m.Settings().Source, m.Dir, time.Now())
	m.mu.Lock()
	m.running = false
	m.lastCheck = time.Now()
	m.lastError = ""
	if err != nil {
		m.lastError = err.Error()
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if m.Settings().Enabled {
		m.Load()
	}
	return nil
}

// Run loads the databases and keeps them current while GeoIP is enabled.
func (m *Manager) Run(ctx context.Context) {
	if m.Client == nil {
		m.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if m.Settings().Enabled {
		m.Load()
	}
	check := func() {
		s := m.Settings()
		if !s.Enabled {
			m.Disable()
			return
		}
		if !m.Active() {
			m.Load()
		}
		if (s.AutoUpdate || !m.Active()) && m.stale(s, time.Now()) {
			m.mu.Lock()
			recent := time.Since(m.lastCheck) < 6*time.Hour && m.lastError != ""
			m.mu.Unlock()
			if recent {
				return // do not hammer the provider after a failure
			}
			if err := m.Update(ctx); err != nil && m.Log != nil {
				m.Log.Warn("geoip update", "err", err)
			}
		}
	}
	check()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}
