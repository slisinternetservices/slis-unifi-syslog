// Package ingest parses incoming syslog messages and writes them to
// ClickHouse in large batches.
package ingest

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/enrich"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

type Stats struct {
	Parsed     atomic.Uint64
	Inserted   atomic.Uint64
	InsertErrs atomic.Uint64
	Discarded  atomic.Uint64
	LastInsert atomic.Int64
	LastError  atomic.Value // string
	Pending    atomic.Int64
}

type Pipeline struct {
	In        <-chan syslogd.Message
	Meta      *store.Meta
	Logs      *store.Logs
	Workers   int
	BatchSize int
	Flush     time.Duration
	Location  func() *time.Location
	Stats     *Stats
	Log       *slog.Logger
	// Enrich adds client, device and GeoIP information; optional.
	Enrich *enrich.Enricher
	// Forward hands every batch to the forwarding destinations; optional.
	Forward func([]*unifi.Event)

	rangesMu sync.RWMutex
	ranges   []store.SiteRange
}

func (p *Pipeline) ReloadRanges() {
	r, err := p.Meta.SiteRanges()
	if err != nil {
		return
	}
	p.rangesMu.Lock()
	p.ranges = r
	p.rangesMu.Unlock()
}

func (p *Pipeline) Run(ctx context.Context) {
	p.ReloadRanges()
	out := make(chan *unifi.Event, p.BatchSize*2)
	var wg sync.WaitGroup
	for i := 0; i < max(1, p.Workers); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.parseLoop(ctx, out)
		}()
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				p.Meta.FlushSources()
				p.Meta.FlushDevices()
				if p.Enrich != nil {
					p.Enrich.Flush()
				}
				return
			case <-t.C:
				if err := p.Meta.FlushSources(); err != nil {
					p.Log.Warn("flush sources", "err", err)
				}
				if err := p.Meta.FlushDevices(); err != nil {
					p.Log.Warn("flush devices", "err", err)
				}
				if p.Enrich != nil {
					if err := p.Enrich.Flush(); err != nil {
						p.Log.Warn("flush client names", "err", err)
					}
				}
			}
		}
	}()
	done := make(chan struct{})
	go func() { p.batchLoop(out); close(done) }()
	wg.Wait()
	close(out)
	<-done
}

func (p *Pipeline) parseLoop(ctx context.Context, out chan<- *unifi.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-p.In:
			loc := p.Location()
			e := unifi.Parse(m.Data, m.SourceIP, m.Received, loc)
			p.rangesMu.RLock()
			key := m.SourceIP
			if m.Probe != "" {
				key += "@" + m.Probe
			}
			site, name := p.Meta.Resolve(key, e.Hostname, e.DeviceModel, e.DeviceMAC, e.DeviceVersion, p.ranges, m.Site)
			p.rangesMu.RUnlock()
			e.Site = site
			if m.Probe != "" {
				e.SetField("probe", m.Probe)
			}
			p.Meta.TouchDevice(e.Hostname, e.DeviceModel, e.DeviceMAC, e.DeviceVersion, site, m.SourceIP, m.Received)
			// Remote locations usually arrive NATed from one WAN address, so the
			// source name labels the sender, it does not replace device hostnames.
			if name != "" {
				e.SetField("source_name", name)
			}
			if e.Hostname == "" {
				e.Hostname = first(name, m.SourceIP)
			}
			if p.Enrich != nil {
				p.Enrich.Apply(e)
			}
			p.Stats.Parsed.Add(1)
			out <- e
		}
	}
}

func (p *Pipeline) batchLoop(in <-chan *unifi.Event) {
	batch := make([]*unifi.Event, 0, p.BatchSize)
	t := time.NewTicker(p.Flush)
	defer t.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Forwarding queues on disk first, so a ClickHouse outage does not
		// delay delivery to the destinations.
		if p.Forward != nil {
			p.Forward(batch)
		}
		p.write(batch)
		batch = make([]*unifi.Event, 0, p.BatchSize)
	}
	for {
		select {
		case e, ok := <-in:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			p.Stats.Pending.Store(int64(len(batch)))
			if len(batch) >= p.BatchSize {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// write retries for up to ~2 minutes so a ClickHouse restart does not lose
// data; meanwhile the bounded queues fill and new UDP packets are counted as
// dropped rather than blocking the kernel socket.
func (p *Pipeline) write(batch []*unifi.Event) {
	backoff := 250 * time.Millisecond
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := p.Logs.Insert(ctx, batch)
		cancel()
		if err == nil {
			p.Stats.Inserted.Add(uint64(len(batch)))
			p.Stats.LastInsert.Store(time.Now().Unix())
			p.Stats.Pending.Store(0)
			return
		}
		p.Stats.InsertErrs.Add(1)
		p.Stats.LastError.Store(err.Error())
		p.Log.Error("clickhouse insert failed", "rows", len(batch), "err", err)
		if time.Now().After(deadline) {
			p.Stats.Discarded.Add(uint64(len(batch)))
			return
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, 10*time.Second)
	}
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
