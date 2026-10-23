// Package forward sends selected UniFi events to Microsoft Sentinel,
// Elasticsearch/OpenSearch, Logstash, syslog receivers, Splunk and JSON
// webhooks. Every destination has its own filter, on-disk queue and
// delivery loop with batching and retries, so a slow or unreachable
// destination never holds up ingestion or the other destinations.
package forward

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/query"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

func basic(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

const defaultQueueMB = 1024

// Manager owns the destinations and their delivery loops.
type Manager struct {
	Meta *store.Meta
	Dir  string // <data_dir>/forward
	Log  *slog.Logger

	in     chan []*unifi.Event
	active atomic.Int32

	mu      sync.RWMutex
	runners map[int64]*runner
	ctx     context.Context
}

// NewManager prepares a manager; Run starts it.
func NewManager(meta *store.Meta, dir string, log *slog.Logger) *Manager {
	return &Manager{Meta: meta, Dir: dir, Log: log, in: make(chan []*unifi.Event, 64), runners: map[int64]*runner{}}
}

// Submit hands a batch of parsed events to the destinations. It only
// blocks when the destinations' queues cannot keep up with writing.
func (m *Manager) Submit(batch []*unifi.Event) {
	if m == nil || m.active.Load() == 0 || len(batch) == 0 {
		return
	}
	m.in <- batch
}

// InputDone tells Run that the pipeline submits nothing more.
func (m *Manager) InputDone() { close(m.in) }

// Run loads the destinations and distributes submitted events until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	if err := m.Reload(); err != nil {
		m.Log.Warn("forwarding destinations", "err", err)
	}
	flush := time.NewTicker(30 * time.Second)
	defer flush.Stop()
	purge := time.NewTicker(6 * time.Hour)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			// Queue what the pipeline still flushes while shutting down.
			deadline := time.After(5 * time.Second)
		drain:
			for {
				select {
				case batch, ok := <-m.in:
					if !ok {
						break drain
					}
					m.distribute(batch)
				case <-deadline:
					break drain
				}
			}
			m.stopAll()
			m.flushStats()
			return
		case batch, ok := <-m.in:
			if !ok {
				m.in = nil
				continue
			}
			m.distribute(batch)
		case <-flush.C:
			m.flushStats()
		case <-purge.C:
			m.Meta.PurgeForwardStats()
		}
	}
}

func (m *Manager) distribute(batch []*unifi.Event) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.runners {
		if !r.dest.Enabled {
			continue
		}
		var recs [][]byte
		for _, e := range batch {
			if r.match(e) {
				rec := FromEvent(e)
				b, err := json.Marshal(&rec)
				if err == nil {
					recs = append(recs, b)
				}
			}
		}
		if err := r.queue.Append(recs); err != nil {
			r.fail(fmt.Errorf("queue: %w", err))
			r.addStat(func(s *store.ForwardStat) { s.Dropped += int64(len(recs)) })
		}
	}
}

// Reload re-reads the destinations after a change. Unchanged destinations
// keep running; changed ones restart with their queue intact; removed ones
// stop and their queue is deleted.
func (m *Manager) Reload() error {
	dests, err := m.Meta.ForwardDestinations()
	if err != nil {
		return err
	}
	saved, _ := m.Meta.Categories()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return nil
	}
	seen := map[int64]bool{}
	enabled := int32(0)
	for _, d := range dests {
		seen[d.ID] = true
		if d.Enabled {
			enabled++
		}
		match, err := compileFilter(d.Filter, saved)
		old := m.runners[d.ID]
		if old != nil && sameConfig(old.dest, d) && old.matchErr == errString(err) {
			old.match = match
			continue
		}
		var q *Queue
		var st *runnerStats
		if old != nil {
			old.stop()
			q, st = old.queue, old.stats
		} else {
			q, err = OpenQueue(filepath.Join(m.Dir, strconv.FormatInt(d.ID, 10)), 0)
			if err != nil {
				m.Log.Error("forwarding queue", "destination", d.Name, "err", err)
				continue
			}
			st = &runnerStats{}
		}
		q.SetLimit(int64(cmp(d.Settings.QueueMaxMB, defaultQueueMB)) << 20)
		r := &runner{m: m, dest: d, queue: q, stats: st, match: match, matchErr: errString(err)}
		if err != nil {
			r.match = func(*unifi.Event) bool { return false }
			r.fail(fmt.Errorf("filter: %w", err))
		}
		m.runners[d.ID] = r
		if d.Enabled {
			r.start(m.ctx)
		}
	}
	for id, r := range m.runners {
		if !seen[id] {
			r.stop()
			r.queue.Close()
			os.RemoveAll(filepath.Join(m.Dir, strconv.FormatInt(id, 10)))
			delete(m.runners, id)
		}
	}
	m.active.Store(enabled)
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cmp(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func sameConfig(a, b store.ForwardDestination) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runners {
		r.stop()
		r.queue.Close()
	}
	m.active.Store(0)
}

// compileFilter turns a destination filter into one matcher.
func compileFilter(f store.ForwardFilter, saved []store.Category) (query.Matcher, error) {
	var parts []query.Matcher
	if len(f.Categories) > 0 {
		cats := f.Categories
		parts = append(parts, func(e *unifi.Event) bool { return slices.Contains(cats, e.Category) })
	}
	if len(f.Sites) > 0 {
		sites := f.Sites
		parts = append(parts, func(e *unifi.Event) bool { return slices.Contains(sites, e.Site) })
	}
	if len(f.Devices) > 0 {
		devs := make([]string, len(f.Devices))
		for i, d := range f.Devices {
			devs[i] = strings.ToLower(strings.TrimSpace(d))
		}
		parts = append(parts, func(e *unifi.Event) bool {
			for _, v := range []string{e.Hostname, e.DeviceMAC, e.Fields["device_name"]} {
				if v != "" && slices.Contains(devs, strings.ToLower(v)) {
					return true
				}
			}
			return false
		})
	}
	if f.MaxSeverity >= 0 && f.MaxSeverity < 7 {
		maxSev := uint8(f.MaxSeverity)
		parts = append(parts, func(e *unifi.Event) bool { return e.Severity <= maxSev })
	}
	if f.SavedSearch != 0 {
		i := slices.IndexFunc(saved, func(c store.Category) bool { return c.ID == f.SavedSearch })
		if i < 0 {
			return nil, errors.New("the saved search no longer exists")
		}
		m, err := query.CompileMatcher(saved[i].Query)
		if err != nil {
			return nil, fmt.Errorf("saved search %q: %w", saved[i].Name, err)
		}
		parts = append(parts, m)
	}
	if strings.TrimSpace(f.Query) != "" {
		m, err := query.CompileMatcher(f.Query)
		if err != nil {
			return nil, err
		}
		parts = append(parts, m)
	}
	return func(e *unifi.Event) bool {
		for _, p := range parts {
			if !p(e) {
				return false
			}
		}
		return true
	}, nil
}

// ValidateFilter checks a filter before it is saved.
func ValidateFilter(f store.ForwardFilter, saved []store.Category) error {
	_, err := compileFilter(f, saved)
	return err
}

// Alert describes a fired alert rule for SendAlert.
type Alert struct {
	Rule      string
	Query     string
	Count     uint64
	Window    int
	Threshold int
}

// SendAlert queues an alert record for the given destinations, whatever
// their filters.
func (m *Manager) SendAlert(ids []int64, a Alert) {
	if m == nil || len(ids) == 0 {
		return
	}
	now := time.Now().UTC()
	rec := Record{ID: newID(), Kind: KindAlert, TS: now, Received: now, App: "unify-syslog", Facility: 13, Severity: 1,
		Category: "alert", EventType: "alert_fired", Action: "alert", Rule: a.Rule,
		Message: fmt.Sprintf("Alert %s: %d matches in %d min (threshold %d)", a.Rule, a.Count, a.Window, a.Threshold),
		Fields:  map[string]string{"alert_query": a.Query, "alert_count": strconv.FormatUint(a.Count, 10), "alert_window_minutes": strconv.Itoa(a.Window), "alert_threshold": strconv.Itoa(a.Threshold)}}
	b, _ := json.Marshal(&rec)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, id := range ids {
		if r := m.runners[id]; r != nil && r.dest.Enabled {
			if err := r.queue.Append([][]byte{b}); err != nil {
				r.fail(fmt.Errorf("queue: %w", err))
			}
		}
	}
}

// Test sends one test record straight to d, bypassing the queue.
func Test(ctx context.Context, d store.ForwardDestination, instance string) error {
	s, err := NewSender(d)
	if err != nil {
		return err
	}
	defer s.Close()
	rec := TestRecord(instance)
	if d.Kind == store.FwdSentinel && d.Settings.TableMode == "split" {
		// Exercise both streams.
		fw := rec
		fw.ID, fw.Kind, fw.Category, fw.Action, fw.Rule = newID(), KindEvent, unifi.CatFirewall, "allow", "TEST"
		fw.Message = rec.Message + " (CommonSecurityLog)"
		_, err = s.Send(ctx, []Record{rec, fw})
	} else {
		_, err = s.Send(ctx, []Record{rec})
	}
	return err
}
