package forward

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/query"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// runnerStats survive a restart of the runner after a settings change.
type runnerStats struct {
	mu        sync.Mutex
	hours     map[int64]*store.ForwardStat // not yet written to the database
	lastOK    time.Time
	lastErr   string
	lastErrAt time.Time
	failing   bool
	retryAt   time.Time
}

type runner struct {
	m        *Manager
	dest     store.ForwardDestination
	queue    *Queue
	stats    *runnerStats
	match    query.Matcher
	matchErr string

	cancel context.CancelFunc
	done   chan struct{}
}

func (r *runner) start(ctx context.Context) {
	ctx, r.cancel = context.WithCancel(ctx)
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		r.loop(ctx)
	}()
}

func (r *runner) stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
		r.cancel = nil
	}
}

func (r *runner) addStat(f func(*store.ForwardStat)) {
	hour := time.Now().Truncate(time.Hour).Unix()
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	if r.stats.hours == nil {
		r.stats.hours = map[int64]*store.ForwardStat{}
	}
	s := r.stats.hours[hour]
	if s == nil {
		s = &store.ForwardStat{DestID: r.dest.ID, Hour: hour}
		r.stats.hours[hour] = s
	}
	f(s)
}

func (r *runner) fail(err error) {
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	r.stats.lastErr, r.stats.lastErrAt, r.stats.failing = err.Error(), time.Now(), true
}

func (r *runner) ok() {
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	r.stats.lastOK, r.stats.failing, r.stats.retryAt = time.Now(), false, time.Time{}
}

func (r *runner) loop(ctx context.Context) {
	snd, err := NewSender(r.dest)
	if err != nil {
		r.fail(err)
		<-ctx.Done()
		return
	}
	defer snd.Close()
	maxRecs, maxBytes := snd.Limits()
	backoff := time.Second
	for {
		if n := r.queue.TakeDropped(); n > 0 {
			r.addStat(func(s *store.ForwardStat) { s.Dropped += n })
			r.m.Log.Warn("forwarding queue full, oldest events dropped", "destination", r.dest.Name, "events", n)
		}
		items, err := r.queue.Peek(maxRecs, maxBytes)
		if err != nil {
			r.fail(err)
		}
		if len(items) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-r.queue.Notify():
				// Give the batch a moment to fill up.
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			case <-time.After(5 * time.Second):
			}
			continue
		}
		sent, err := r.deliver(ctx, snd, items)
		if sent > 0 {
			r.queue.Ack(items[sent-1])
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.fail(err)
			r.addStat(func(s *store.ForwardStat) { s.Errors++ })
			r.stats.mu.Lock()
			r.stats.retryAt = time.Now().Add(backoff)
			r.stats.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff = time.Second
		r.ok()
	}
}

// deliver sends items and returns how many from the start were handled
// (delivered or rejected for good). A batch that is too large is split.
func (r *runner) deliver(ctx context.Context, snd Sender, items []Item) (int, error) {
	recs := make([]Record, 0, len(items))
	size := 0
	for _, it := range items {
		var rec Record
		if err := json.Unmarshal(it.Data, &rec); err != nil {
			continue // unreadable record; skipped when the batch is acknowledged
		}
		recs = append(recs, rec)
		size += len(it.Data)
	}
	if len(recs) == 0 {
		return len(items), nil
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	rejected, err := snd.Send(sctx, recs)
	cancel()
	if errors.Is(err, errTooLarge) {
		if len(items) == 1 {
			r.addStat(func(s *store.ForwardStat) { s.Rejected++ })
			r.fail(errors.New("an event larger than the destination accepts was skipped"))
			return 1, nil
		}
		half := len(items) / 2
		n, err := r.deliver(ctx, snd, items[:half])
		if err != nil || n < half {
			return n, err
		}
		m, err := r.deliver(ctx, snd, items[half:])
		return half + m, err
	}
	var rej rejectedErr
	if errors.As(err, &rej) {
		r.fail(err)
		err = nil
	}
	if err != nil {
		return 0, err
	}
	r.addStat(func(s *store.ForwardStat) {
		s.Events += int64(len(recs) - rejected)
		s.Bytes += int64(size)
		s.Rejected += int64(rejected)
	})
	return len(items), nil
}

func (m *Manager) flushStats() {
	m.mu.RLock()
	var all []store.ForwardStat
	for _, r := range m.runners {
		r.stats.mu.Lock()
		for _, s := range r.stats.hours {
			all = append(all, *s)
		}
		r.stats.hours = nil
		r.stats.mu.Unlock()
	}
	m.mu.RUnlock()
	if err := m.Meta.AddForwardStats(all); err != nil {
		m.Log.Warn("forwarding statistics", "err", err)
	}
}

// Status is the live state of a destination for the status page.
type Status struct {
	ID        int64  `json:"id"`
	State     string `json:"state"` // ok | error | idle | disabled
	QueueRecs int64  `json:"queue_events"`
	QueueSize int64  `json:"queue_bytes"`
	LastOK    int64  `json:"last_ok"`
	LastError string `json:"last_error"`
	LastErrAt int64  `json:"last_error_at"`
	RetryAt   int64  `json:"retry_at"`
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// Statuses returns the live state per destination ID. Counters not yet
// written to the database are written first, so ForwardStats is current.
func (m *Manager) Statuses() map[int64]Status {
	m.flushStats()
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[int64]Status{}
	for id, r := range m.runners {
		recs, size := r.queue.Pending()
		r.stats.mu.Lock()
		st := Status{ID: id, QueueRecs: recs, QueueSize: size, LastOK: unix(r.stats.lastOK), LastError: r.stats.lastErr, LastErrAt: unix(r.stats.lastErrAt), RetryAt: unix(r.stats.retryAt)}
		switch {
		case !r.dest.Enabled:
			st.State = "disabled"
		case r.stats.failing:
			st.State = "error"
		case r.stats.lastOK.IsZero():
			st.State = "idle"
		default:
			st.State = "ok"
		}
		r.stats.mu.Unlock()
		out[id] = st
	}
	return out
}
