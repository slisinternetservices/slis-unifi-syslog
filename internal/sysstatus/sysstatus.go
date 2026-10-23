// Package sysstatus watches the health of the server itself: messages per
// second, dropped packets, disk space and the ClickHouse log store. It keeps
// an hour of samples for the System status page and notifies when a problem
// starts or clears.
package sysstatus

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/ingest"
	"github.com/rhisijmuiden/unify-syslog-server/internal/mail"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
)

const (
	sampleEvery = 10 * time.Second
	keep        = 361 // one hour of samples
	checkEvery  = time.Minute
	// recent is the period in which new drops or errors raise an issue.
	recent = 5 * time.Minute
	// Too many active parts in one partition means merges lag behind.
	maxParts = 300
)

// Monitor samples the counters and checks the health of the server.
type Monitor struct {
	Sys    *syslogd.Stats
	Ing    *ingest.Stats
	Filter *syslogd.Filter
	Logs   *store.Logs
	Notify *notify.Dispatcher
	Mailer *mail.Mailer
	Log    *slog.Logger
	// DataDir is the server's own data directory (settings, queues, backups).
	DataDir string
	// Queue returns the length and capacity of the ingest queue.
	Queue   func() (int, int)
	Started time.Time

	mu      sync.Mutex
	samples []sample
	health  store.LogHealth
	checked time.Time
	issues  map[string]Issue
}

// sample is a snapshot of the cumulative counters.
type sample struct {
	t           time.Time
	received    uint64 // UDP packets + TCP messages
	parsed      uint64 // all sources, probes included
	inserted    uint64
	dropped     uint64 // ingest queue full
	kernel      uint64 // UDP receive buffer errors in the kernel
	insertErrs  uint64
	discarded   uint64
	rejected    uint64
	kernelKnown bool
}

func (m *Monitor) take() sample {
	s := sample{t: time.Now()}
	if m.Sys != nil {
		s.received = m.Sys.UDPPackets.Load() + m.Sys.TCPMsgs.Load()
		s.dropped = m.Sys.Dropped.Load()
	}
	if m.Ing != nil {
		s.parsed = m.Ing.Parsed.Load()
		s.inserted = m.Ing.Inserted.Load()
		s.insertErrs = m.Ing.InsertErrs.Load()
		s.discarded = m.Ing.Discarded.Load()
	}
	if m.Filter != nil {
		s.rejected = m.Filter.Rejected.Load()
	}
	if u, err := readUDP(); err == nil {
		s.kernel, s.kernelKnown = u["RcvbufErrors"], true
	}
	return s
}

// Run samples every ten seconds and checks health every minute.
func (m *Monitor) Run(ctx context.Context) {
	m.record(m.take())
	m.check(ctx)
	st := time.NewTicker(sampleEvery)
	defer st.Stop()
	ct := time.NewTicker(checkEvery)
	defer ct.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-st.C:
			m.record(m.take())
		case <-ct.C:
			m.check(ctx)
		}
	}
}

func (m *Monitor) record(s sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.samples = append(m.samples, s)
	if len(m.samples) > keep {
		m.samples = slices.Delete(m.samples, 0, len(m.samples)-keep)
	}
}

// since returns the oldest sample at most d old and the newest one.
func (m *Monitor) since(d time.Duration) (sample, sample, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) < 2 {
		return sample{}, sample{}, false
	}
	last := m.samples[len(m.samples)-1]
	first := last
	for i := len(m.samples) - 2; i >= 0 && last.t.Sub(m.samples[i].t) <= d+time.Second; i-- {
		first = m.samples[i]
	}
	return first, last, first.t.Before(last.t)
}

// Point is the per second rate between two samples.
type Point struct {
	T        int64   `json:"t"`
	Received float64 `json:"received"`
	Stored   float64 `json:"stored"`
	Dropped  float64 `json:"dropped"`
}

func rate(a, b uint64, sec float64) float64 {
	if b < a || sec <= 0 {
		return 0
	}
	return float64(b-a) / sec
}

func point(a, b sample) Point {
	sec := b.t.Sub(a.t).Seconds()
	return Point{T: b.t.Unix(), Received: rate(a.parsed, b.parsed, sec), Stored: rate(a.inserted, b.inserted, sec),
		Dropped: rate(a.dropped, b.dropped, sec) + rate(a.kernel, b.kernel, sec)}
}

func (m *Monitor) series() []Point {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Point{}
	for i := 1; i < len(m.samples); i++ {
		out = append(out, point(m.samples[i-1], m.samples[i]))
	}
	return out
}

// Issue is a problem found by the health check.
type Issue struct {
	Key      string `json:"key"`
	Code     string `json:"code"`
	Severity string `json:"severity"` // warning | critical
	// Message is an English text with {name} placeholders filled from
	// Params, for the interface to translate.
	Message string            `json:"message"`
	Params  map[string]string `json:"params"`
	Since   int64             `json:"since"`
}

func pct(free, total uint64) float64 {
	if total == 0 {
		return 100
	}
	return float64(free) / float64(total) * 100
}

// Disks returns the log store's disks and the server data directory.
func (m *Monitor) disks(h store.LogHealth) []store.Disk {
	out := []store.Disk{}
	for _, d := range h.Disks {
		d.Name = "ClickHouse: " + d.Name
		out = append(out, d)
	}
	if m.DataDir != "" {
		var st syscall.Statfs_t
		if syscall.Statfs(m.DataDir, &st) == nil {
			out = append(out, store.Disk{Name: "Server data", Path: m.DataDir, Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize)})
		}
	}
	return out
}

// fsID identifies the filesystem of a local path.
func fsID(path string) (string, bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return "", false
	}
	return fmt.Sprint(st.Fsid, st.Blocks), true
}

// Issues finds the current problems from the health of the log store and
// the counters of the last few minutes.
func (m *Monitor) findIssues(h store.LogHealth, now time.Time) []Issue {
	var out []Issue
	add := func(key, code, sev, msg string, params map[string]string) {
		out = append(out, Issue{Key: key, Code: code, Severity: sev, Message: msg, Params: params})
	}
	if !h.OK {
		add("clickhouse", "clickhouse_down", store.SevCritical, "ClickHouse is not reachable: {error}", map[string]string{"error": h.Error})
	} else if h.MaxPartsInDay > maxParts {
		add("parts", "too_many_parts", store.SevWarning, "ClickHouse has {n} data parts in one day; merges are falling behind", map[string]string{"n": strconv.FormatUint(h.MaxPartsInDay, 10)})
	}
	// The same filesystem often holds both ClickHouse and the server data.
	seen := map[string]bool{}
	for _, d := range m.disks(h) {
		if fs, ok := fsID(d.Path); ok {
			if seen[fs] {
				continue
			}
			seen[fs] = true
		}
		free := pct(d.Free, d.Total)
		p := map[string]string{"name": d.Name, "path": d.Path, "free": fmt.Sprintf("%.0f", free)}
		switch {
		case free < 5:
			add("disk:"+d.Path, "disk_low", store.SevCritical, "Disk almost full: {name} ({path}) has {free}% free", p)
		case free < 15:
			add("disk:"+d.Path, "disk_low", store.SevWarning, "Disk filling up: {name} ({path}) has {free}% free", p)
		}
	}
	if a, b, ok := m.since(recent); ok {
		if b.kernelKnown && a.kernelKnown && b.kernel > a.kernel {
			add("udp_drops", "udp_drops", store.SevWarning, "The kernel dropped {n} syslog packets in the last 5 minutes (UDP receive buffer full)", map[string]string{"n": strconv.FormatUint(b.kernel-a.kernel, 10)})
		}
		if b.dropped > a.dropped {
			add("queue_full", "queue_full", store.SevWarning, "{n} messages were dropped in the last 5 minutes because the ingest queue was full", map[string]string{"n": strconv.FormatUint(b.dropped-a.dropped, 10)})
		}
		if b.insertErrs > a.insertErrs {
			var le string
			if m.Ing != nil {
				le, _ = m.Ing.LastError.Load().(string)
			}
			add("insert_errors", "insert_errors", store.SevCritical, "Storing logs in ClickHouse failed {n} times in the last 5 minutes: {error}", map[string]string{"n": strconv.FormatUint(b.insertErrs-a.insertErrs, 10), "error": le})
		}
	}
	return out
}

func (m *Monitor) check(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	h := m.Logs.Health(cctx)
	cancel()
	now := time.Now()
	found := m.findIssues(h, now)

	m.mu.Lock()
	m.health, m.checked = h, now
	prev := m.issues
	next := map[string]Issue{}
	var started, cleared []Issue
	for _, is := range found {
		if p, ok := prev[is.Key]; ok {
			is.Since = p.Since
			if store.SeverityRank(is.Severity) > store.SeverityRank(p.Severity) {
				started = append(started, is)
			}
		} else {
			is.Since = now.Unix()
			started = append(started, is)
		}
		next[is.Key] = is
	}
	for k, p := range prev {
		if _, ok := next[k]; !ok {
			cleared = append(cleared, p)
		}
	}
	m.issues = next
	m.mu.Unlock()

	for _, is := range started {
		m.Log.Warn("system status", "issue", is.Code, "severity", is.Severity, "detail", fill(is.Message, is.Params))
		m.tell(ctx, is, false)
	}
	for _, is := range cleared {
		m.Log.Info("system status resolved", "issue", is.Code)
		m.tell(ctx, is, true)
	}
}

// fill replaces {name} placeholders with params.
func fill(s string, params map[string]string) string {
	for k, v := range params {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func (m *Monitor) tell(ctx context.Context, is Issue, resolved bool) {
	sev := is.Severity
	if resolved {
		sev = store.SevInfo
	}
	compose := func(t i18n.Translator) (string, string) {
		// Messages use {name} placeholders, not fmt verbs, in every language.
		body := fill(t(is.Message), is.Params)
		if resolved {
			return t("Resolved: %s", t(titles[is.Code])), t("This problem is over:") + "\n" + body
		}
		return t("System status: %s", t(titles[is.Code])), body
	}
	m.Notify.Notify(notify.Notification{Source: store.SourceSystem, Severity: sev, Compose: compose})
	if m.Mailer == nil || !m.Mailer.Configured() {
		return
	}
	mctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := m.Mailer.Send(mctx, nil, compose); err != nil {
		m.Log.Warn("system status mail", "err", err)
	}
}

var titles = map[string]string{
	"clickhouse_down": "ClickHouse not reachable",
	"too_many_parts":  "ClickHouse merges falling behind",
	"disk_low":        "Low disk space",
	"udp_drops":       "Syslog packets dropped",
	"queue_full":      "Ingest queue full",
	"insert_errors":   "Storing logs fails",
}

// Status is what the System status page shows.
type Status struct {
	Health  string          `json:"health"` // ok | warning | critical
	Issues  []Issue         `json:"issues"`
	Checked time.Time       `json:"checked"`
	Ingest  Ingest          `json:"ingest"`
	Disks   []store.Disk    `json:"disks"`
	Store   store.LogHealth `json:"clickhouse"`
	Process Process         `json:"process"`
}

type Ingest struct {
	Rate        float64 `json:"rate"` // messages per second, last minute
	RateStored  float64 `json:"rate_stored"`
	Peak        float64 `json:"peak"` // highest 10 s rate of the last hour
	Series      []Point `json:"series"`
	Received    uint64  `json:"received"` // UDP packets and TCP messages
	UDPPackets  uint64  `json:"udp_packets"`
	TCPMessages uint64  `json:"tcp_messages"`
	TCPConns    int64   `json:"tcp_connections"`
	Parsed      uint64  `json:"parsed"`
	Inserted    uint64  `json:"inserted"`
	Dropped     uint64  `json:"dropped"`
	Rejected    uint64  `json:"rejected"`
	InsertErrs  uint64  `json:"insert_errors"`
	Discarded   uint64  `json:"discarded"`
	Pending     int64   `json:"pending"`
	QueueLen    int     `json:"queue_length"`
	QueueCap    int     `json:"queue_capacity"`
	LastInsert  int64   `json:"last_insert"`
	LastError   string  `json:"last_error"`
	// Kernel are the UDP counters of the network stack (/proc/net/snmp):
	// RcvbufErrors counts packets dropped because the socket buffer was full.
	Kernel map[string]uint64 `json:"kernel"`
	// DroppedHour are dropped messages (queue and kernel) in the last hour.
	DroppedHour uint64 `json:"dropped_hour"`
}

type Process struct {
	UptimeSec    int64      `json:"uptime_sec"`
	Goroutines   int        `json:"goroutines"`
	HeapBytes    uint64     `json:"heap_bytes"`
	RSSBytes     uint64     `json:"rss_bytes"`
	CPUs         int        `json:"cpus"`
	Load         [3]float64 `json:"load"`
	MemTotal     uint64     `json:"mem_total"`
	MemAvailable uint64     `json:"mem_available"`
}

// Status returns the current state. The log store is checked again when
// the last check is more than a few seconds old.
func (m *Monitor) Status(ctx context.Context) Status {
	m.mu.Lock()
	h, checked := m.health, m.checked
	m.mu.Unlock()
	if time.Since(checked) > 5*time.Second {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		h, checked = m.Logs.Health(cctx), time.Now()
		cancel()
		m.mu.Lock()
		m.health, m.checked = h, checked
		m.mu.Unlock()
	}
	st := Status{Health: "ok", Issues: []Issue{}, Checked: checked, Store: h, Disks: m.disks(h)}
	m.mu.Lock()
	for _, is := range m.issues {
		st.Issues = append(st.Issues, is)
	}
	m.mu.Unlock()
	// Issues found by the minute check, plus what this fresh check shows.
	seen := map[string]bool{}
	for _, is := range st.Issues {
		seen[is.Key] = true
	}
	for _, is := range m.findIssues(h, time.Now()) {
		if !seen[is.Key] {
			is.Since = time.Now().Unix()
			st.Issues = append(st.Issues, is)
		}
	}
	slices.SortFunc(st.Issues, func(a, b Issue) int {
		if d := store.SeverityRank(b.Severity) - store.SeverityRank(a.Severity); d != 0 {
			return d
		}
		return strings.Compare(a.Key, b.Key)
	})
	for _, is := range st.Issues {
		if is.Severity == store.SevCritical {
			st.Health = "critical"
			break
		}
		st.Health = "warning"
	}

	cur := m.take()
	in := Ingest{Series: m.series(), Parsed: cur.parsed, Inserted: cur.inserted, Dropped: cur.dropped, Rejected: cur.rejected,
		InsertErrs: cur.insertErrs, Discarded: cur.discarded, Received: cur.received}
	if m.Sys != nil {
		in.UDPPackets, in.TCPMessages, in.TCPConns = m.Sys.UDPPackets.Load(), m.Sys.TCPMsgs.Load(), m.Sys.TCPConns.Load()
	}
	if m.Ing != nil {
		in.Pending, in.LastInsert = m.Ing.Pending.Load(), m.Ing.LastInsert.Load()
		in.LastError, _ = m.Ing.LastError.Load().(string)
	}
	if m.Queue != nil {
		in.QueueLen, in.QueueCap = m.Queue()
	}
	if u, err := readUDP(); err == nil {
		in.Kernel = u
	}
	for _, p := range in.Series {
		in.Peak = max(in.Peak, p.Received)
	}
	if a, b, ok := m.since(time.Minute); ok {
		p := point(a, b)
		in.Rate, in.RateStored = p.Received, p.Stored
	}
	if a, b, ok := m.since(time.Hour); ok {
		in.DroppedHour = (b.dropped - a.dropped)
		if b.kernelKnown && a.kernelKnown && b.kernel >= a.kernel {
			in.DroppedHour += b.kernel - a.kernel
		}
	}
	st.Ingest = in

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st.Process = Process{UptimeSec: int64(time.Since(m.Started).Seconds()), Goroutines: runtime.NumGoroutine(), HeapBytes: ms.HeapAlloc, CPUs: runtime.NumCPU()}
	st.Process.RSSBytes = procKB("/proc/self/status", "VmRSS")
	st.Process.MemTotal = procKB("/proc/meminfo", "MemTotal")
	st.Process.MemAvailable = procKB("/proc/meminfo", "MemAvailable")
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			st.Process.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	return st
}

// readUDP parses the Udp lines of /proc/net/snmp.
func readUDP() (map[string]uint64, error) {
	f, err := os.Open("/proc/net/snmp")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseSNMP(f, "Udp:")
}

func parseSNMP(f interface{ Read([]byte) (int, error) }, prefix string) (map[string]uint64, error) {
	sc := bufio.NewScanner(f)
	var head []string
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, prefix))
		if head == nil {
			head = fields
			continue
		}
		out := map[string]uint64{}
		for i, k := range head {
			if i < len(fields) {
				out[k], _ = strconv.ParseUint(fields[i], 10, 64)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s not found", prefix)
}

// procKB reads a "Key:  123 kB" line from a /proc file, in bytes.
func procKB(path, key string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && k == key {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(f[0], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
}
