// Package probe is the remote probe: it receives syslog at a site, buffers
// every message on disk and forwards them in order over HTTPS to the main
// server. It keeps buffering while the main server is unreachable or the
// probe is not yet enrolled.
package probe

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/diskqueue"
	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
	"github.com/rhisijmuiden/unify-syslog-server/internal/tlscert"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

const (
	DefaultDataDir = "/var/lib/unify-syslog-probe"
	DefaultUIAddr  = ":8080"

	heartbeatEvery = 30 * time.Second
	batchRecords   = 5000
	batchBytes     = 4 << 20
)

// BootFiles may hold an enrollment key, e.g. written to the FAT boot
// partition of the Raspberry Pi image before first boot.
var BootFiles = []string{"/boot/firmware/uss-probe.txt", "/boot/uss-probe.txt"}

type Agent struct {
	DataDir string
	UIAddr  string
	Log     *slog.Logger
	Level   *slog.LevelVar
	// Restart ends the process so systemd starts the (new) binary.
	Restart func()

	mu    sync.Mutex
	st    *State
	httpc *http.Client

	seq     *seqFile
	queue   *diskqueue.Queue
	msgs    chan syslogd.Message
	sysStat syslogd.Stats
	// wakeSend and wakeBeat are signalled after enrollment.
	wakeSend chan struct{}
	wakeBeat chan struct{}
	relisten chan struct{}

	started  time.Time
	received atomic.Uint64
	sent     atomic.Uint64
	dropped  atomic.Uint64
	expired  atomic.Uint64
	oldest   atomic.Int64

	statMu    sync.Mutex
	perSecond [60]uint64
	secAt     int64
	devices   map[string]int64
	listen    string
	lastErr   string
	lastOK    time.Time // last successful contact with the server
	serverVer string
	updating  atomic.Bool
	sys       sysinfo
}

func (a *Agent) state() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.st
}

func (a *Agent) setError(err error) {
	a.statMu.Lock()
	if err == nil {
		a.lastErr = ""
		a.lastOK = time.Now()
	} else {
		a.lastErr = err.Error()
	}
	a.statMu.Unlock()
}

// Open loads the state and the buffer; Run then starts the work.
func (a *Agent) Open() error {
	if a.DataDir == "" {
		a.DataDir = DefaultDataDir
	}
	if err := os.MkdirAll(a.DataDir, 0o750); err != nil {
		return err
	}
	st, err := loadState(a.DataDir)
	if err != nil {
		return err
	}
	a.st = st
	if a.seq, err = openSeq(a.DataDir); err != nil {
		return err
	}
	if a.queue, err = diskqueue.Open(filepath.Join(a.DataDir, "buffer"), int64(st.Settings.BufferMB)<<20); err != nil {
		return err
	}
	a.msgs = make(chan syslogd.Message, 50000)
	a.wakeSend = make(chan struct{}, 1)
	a.wakeBeat = make(chan struct{}, 1)
	a.relisten = make(chan struct{}, 1)
	a.devices = map[string]int64{}
	a.started = time.Now()
	a.applyLevel(st.Settings.LogLevel)
	a.httpc = newClient(st.Server, st.Pin)
	return nil
}

func newClient(server, pin string) *http.Client {
	host := ""
	if u, err := url.Parse(server); err == nil {
		host = u.Hostname()
	}
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlscert.ClientConfig(host, pin),
		TLSHandshakeTimeout: 15 * time.Second, IdleConnTimeout: 90 * time.Second, ForceAttemptHTTP2: true,
	}}
}

func (a *Agent) applyLevel(l string) {
	if a.Level == nil {
		return
	}
	switch l {
	case "debug":
		a.Level.Set(slog.LevelDebug)
	case "warn":
		a.Level.Set(slog.LevelWarn)
	case "error":
		a.Level.Set(slog.LevelError)
	default:
		a.Level.Set(slog.LevelInfo)
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Run receives, buffers and forwards until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	defer a.queue.Close()
	a.Log.Info("probe starting", "version", version.Version, "data", a.DataDir, "enrolled", a.state().Enrolled())
	var wg sync.WaitGroup
	run := func(f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	appendDone := make(chan struct{})
	rctx, stopRecv := context.WithCancel(context.Background())
	go func() { a.receive(rctx); close(a.msgs) }()
	go func() { a.appendLoop(); close(appendDone) }()
	run(a.sendLoop)
	run(a.heartbeatLoop)
	run(a.bootEnroll)
	ui := &http.Server{Addr: a.UIAddr, Handler: a.uiHandler(), ReadHeaderTimeout: 10 * time.Second}
	if a.UIAddr != "" {
		go func() {
			if err := ui.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.Log.Warn("local web page", "addr", a.UIAddr, "err", err)
			}
		}()
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ui.Shutdown(sctx)
	cancel()
	// Stop receiving, then let the appender write what is in memory.
	stopRecv()
	<-appendDone
	wg.Wait()
	a.Log.Info("probe stopped")
	return nil
}

// receive runs the syslog listeners and restarts them when the ports change.
func (a *Agent) receive(ctx context.Context) {
	for ctx.Err() == nil {
		s := a.state().Settings
		if s.UDP == "" && s.TCP == "" {
			select {
			case <-ctx.Done():
			case <-a.relisten:
			}
			continue
		}
		lctx, cancel := context.WithCancel(ctx)
		srv := &syslogd.Server{UDPAddr: s.UDP, TCPAddr: s.TCP, ReadBuf: 4 << 20, Out: a.msgs, Stats: &a.sysStat, Log: a.Log}
		a.statMu.Lock()
		a.listen = fmt.Sprintf("udp %s, tcp %s", or(s.UDP, "off"), or(s.TCP, "off"))
		a.statMu.Unlock()
		errc := make(chan error, 1)
		go func() { errc <- srv.Run(lctx) }()
		select {
		case err := <-errc:
			cancel()
			if err != nil && ctx.Err() == nil {
				a.Log.Error("syslog listener", "err", err)
				a.setListenErr(err)
				select {
				case <-ctx.Done():
				case <-a.relisten:
				case <-time.After(10 * time.Second):
				}
			}
		case <-a.relisten:
			a.Log.Info("syslog ports changed, restarting listeners")
			cancel()
			<-errc
		case <-ctx.Done():
			cancel()
			<-errc
		}
	}
}

func (a *Agent) setListenErr(err error) {
	a.statMu.Lock()
	a.listen = "error: " + err.Error()
	a.statMu.Unlock()
}

func or(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// appendLoop writes received messages to the buffer in small batches.
func (a *Agent) appendLoop() {
	batch := make([]syslogd.Message, 0, 2000)
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := a.store(batch); err != nil {
			a.Log.Error("buffer write failed", "records", len(batch), "err", err)
			a.dropped.Add(uint64(len(batch)))
		}
		batch = batch[:0]
	}
	for {
		select {
		case m, ok := <-a.msgs:
			if !ok {
				flush()
				return
			}
			batch = append(batch, m)
			if len(batch) == cap(batch) {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

func (a *Agent) store(batch []syslogd.Message) error {
	first, err := a.seq.take(len(batch))
	if err != nil {
		return err
	}
	recs := make([][]byte, len(batch))
	now := time.Now().Unix()
	a.statMu.Lock()
	for i, m := range batch {
		b, _ := json.Marshal(probeproto.Record{Seq: first + uint64(i), TS: m.Received.UnixNano(), IP: m.SourceIP, Data: strings.TrimRight(string(m.Data), "\r\n\x00")})
		recs[i] = b
		a.devices[m.SourceIP] = now
		a.advanceLocked(now)
		a.perSecond[now%60]++
	}
	a.statMu.Unlock()
	a.received.Add(uint64(len(batch)))
	if err := a.queue.Append(recs); err != nil {
		return err
	}
	a.dropped.Add(uint64(a.queue.TakeDropped()))
	return nil
}

// advanceLocked clears the per-second counters of seconds that passed.
func (a *Agent) advanceLocked(now int64) {
	if now == a.secAt {
		return
	}
	for s := a.secAt + 1; s <= now && s-a.secAt <= 60; s++ {
		a.perSecond[s%60] = 0
	}
	a.secAt = now
}

func (a *Agent) rate() float64 {
	a.statMu.Lock()
	defer a.statMu.Unlock()
	now := time.Now().Unix()
	a.advanceLocked(now)
	var n uint64
	for i, v := range a.perSecond {
		if int64(i) != now%60 { // the current second is incomplete
			n += v
		}
	}
	return float64(n) / 59
}

// sendLoop forwards the buffer in order, retrying with backoff.
func (a *Agent) sendLoop(ctx context.Context) {
	backoff := time.Second
	wait := func(d time.Duration, extra <-chan struct{}) {
		select {
		case <-ctx.Done():
		case <-a.wakeSend:
		case <-extra:
		case <-time.After(d):
		}
	}
	for ctx.Err() == nil {
		st := a.state()
		items, err := a.queue.Peek(batchRecords, batchBytes)
		if err != nil {
			a.Log.Error("buffer read", "err", err)
		}
		if len(items) > 0 {
			var r probeproto.Record
			if json.Unmarshal(items[0].Data, &r) == nil {
				a.oldest.Store(r.TS / 1e9)
			}
		} else {
			a.oldest.Store(0)
		}
		if !st.Enrolled() || st.Revoked {
			wait(5*time.Second, nil)
			continue
		}
		if len(items) == 0 {
			wait(5*time.Second, a.queue.Notify())
			continue
		}
		n, err := a.deliver(ctx, st, items)
		if n > 0 {
			backoff = time.Second
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.Log.Warn("delivery failed, retrying", "in", backoff, "err", err)
			a.setError(err)
			wait(backoff, nil)
			backoff = min(backoff*2, time.Minute)
		}
	}
}

var errRevoked = errors.New("the main server no longer accepts this probe's key; enroll it again")

// deliver sends one batch and confirms what the server stored.
func (a *Agent) deliver(ctx context.Context, st State, items []diskqueue.Item) (int, error) {
	maxAge := time.Duration(st.Settings.BufferHours) * time.Hour
	cut := time.Now().Add(-maxAge).UnixNano()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	seqs := make([]uint64, len(items))
	send := 0
	for i, it := range items {
		var r probeproto.Record
		if err := json.Unmarshal(it.Data, &r); err != nil {
			a.Log.Warn("skipping damaged buffer record")
			continue
		}
		seqs[i] = r.Seq
		if maxAge > 0 && r.TS < cut {
			a.expired.Add(1)
			continue
		}
		gz.Write(it.Data)
		gz.Write([]byte{'\n'})
		send++
	}
	gz.Close()
	if send == 0 {
		a.queue.Ack(items[len(items)-1])
		return len(items), nil
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, st.Server+probeproto.Prefix+"/ingest", &raw)
	req.Header.Set("Authorization", "Bearer "+st.Key)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set(probeproto.HeaderEpoch, a.seq.epoch)
	resp, err := a.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var ir probeproto.IngestResponse
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	json.Unmarshal(body, &ir)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		a.markRevoked()
		return 0, errRevoked
	case resp.StatusCode == http.StatusOK:
		a.queue.Ack(items[len(items)-1])
		a.sent.Add(uint64(ir.Accepted))
		a.setError(nil)
		return len(items), nil
	}
	// Partly stored (the server was busy): confirm what it has.
	n := 0
	for i := range items {
		if seqs[i] != 0 && seqs[i] <= ir.Seq {
			n = i + 1
		}
	}
	if n > 0 {
		a.queue.Ack(items[n-1])
		a.sent.Add(uint64(ir.Accepted))
	}
	return n, fmt.Errorf("main server: %s %s", resp.Status, strings.TrimSpace(errText(body)))
}

func errText(b []byte) string {
	var e struct{ Error string }
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		return e.Error
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

func (a *Agent) client() *http.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.httpc
}

func (a *Agent) markRevoked() {
	a.mu.Lock()
	if !a.st.Revoked {
		a.st.Revoked = true
		a.st.save(a.DataDir)
		a.Log.Error(errRevoked.Error())
	}
	a.mu.Unlock()
}

// Status collects what the heartbeat and the local page report.
func (a *Agent) Status() probeproto.Status {
	st := a.state()
	recs, size := a.queue.Pending()
	mu, mt := memInfo()
	du, dt := diskInfo(a.DataDir)
	host, _ := os.Hostname()
	a.statMu.Lock()
	cut := time.Now().Add(-24 * time.Hour).Unix()
	for ip, t := range a.devices {
		if t < cut {
			delete(a.devices, ip)
		}
	}
	devices, listen, lastErr := len(a.devices), a.listen, a.lastErr
	cpu := a.sys.cpu()
	a.statMu.Unlock()
	return probeproto.Status{
		Version: version.Version, Arch: runtime.GOARCH, OS: osName(), Hostname: host, IPs: localIPs(),
		Uptime: int64(time.Since(a.started).Seconds()), Received: a.received.Load(), Rate: a.rate(), Sent: a.sent.Load(),
		BufferRecords: recs, BufferBytes: size, BufferMax: int64(st.Settings.BufferMB) << 20,
		Dropped: a.dropped.Load() + a.sysStat.Dropped.Load(), Expired: a.expired.Load(), Oldest: a.oldest.Load(), Devices: devices,
		CPU: cpu, Load1: load1(), MemUsed: mu, MemTotal: mt, DiskUsed: du, DiskTotal: dt,
		Listen: listen, LastError: lastErr, Update: st.LastUpdate,
	}
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	a.sys.cpu() // first sample
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		if st := a.state(); st.Enrolled() && !st.Revoked {
			if err := a.heartbeat(ctx); err != nil && ctx.Err() == nil {
				a.Log.Warn("heartbeat", "err", err)
				a.setError(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.wakeBeat:
		}
	}
}

func (a *Agent) heartbeat(ctx context.Context) error {
	st := a.state()
	b, _ := json.Marshal(a.Status())
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(hctx, http.MethodPost, st.Server+probeproto.Prefix+"/heartbeat", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+st.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		a.markRevoked()
		return errRevoked
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("main server: %s %s", resp.Status, errText(body))
	}
	var hr probeproto.HeartbeatResponse
	if err := json.Unmarshal(body, &hr); err != nil {
		return err
	}
	a.setError(nil)
	a.statMu.Lock()
	a.serverVer = hr.ServerVersion
	a.statMu.Unlock()
	a.confirmUpdate()
	a.apply(hr)
	if hr.Update != nil && hr.Update.Version != version.Version && a.updating.CompareAndSwap(false, true) {
		go func() {
			defer a.updating.Store(false)
			a.selfUpdate(ctx, *hr.Update)
		}()
	}
	return nil
}

// apply takes over name, site and settings from the server.
func (a *Agent) apply(hr probeproto.HeartbeatResponse) {
	s := hr.Settings.Normalize()
	a.mu.Lock()
	old := a.st.Settings
	changed := a.st.Name != hr.Name || a.st.Site != hr.Site || old != s
	a.st.Name, a.st.Site, a.st.Settings = hr.Name, hr.Site, s
	if changed {
		a.st.save(a.DataDir)
	}
	a.mu.Unlock()
	if !changed {
		return
	}
	if old.LogLevel != s.LogLevel {
		a.applyLevel(s.LogLevel)
	}
	if old.BufferMB != s.BufferMB {
		a.queue.SetLimit(int64(s.BufferMB) << 20)
	}
	if old.UDP != s.UDP || old.TCP != s.TCP {
		signal(a.relisten)
	}
	a.Log.Info("settings from main server applied", "udp", s.UDP, "tcp", s.TCP, "buffer_mb", s.BufferMB, "buffer_hours", s.BufferHours, "log_level", s.LogLevel)
}

// Enroll registers the probe with the main server using an enrollment key.
func (a *Agent) Enroll(ctx context.Context, key string) (*State, error) {
	k, err := probeproto.ParseEnrollKey(key)
	if err != nil {
		return nil, err
	}
	server := strings.TrimRight(k.URL, "/")
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the server address must start with https://")
	}
	host, _ := os.Hostname()
	b, _ := json.Marshal(probeproto.EnrollRequest{Code: k.Code, Hostname: host, Version: version.Version, Arch: runtime.GOARCH, OS: osName()})
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ectx, http.MethodPost, server+probeproto.Prefix+"/enroll", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := newClient(server, k.Pin).Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", server, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enrollment refused: %s", errText(body))
	}
	var er probeproto.EnrollResponse
	if err := json.Unmarshal(body, &er); err != nil {
		return nil, err
	}
	a.mu.Lock()
	old := a.st.Settings
	a.st.Server, a.st.Pin, a.st.Key, a.st.ID, a.st.Name, a.st.Site = server, k.Pin, er.Key, er.ID, er.Name, er.Site
	a.st.Settings, a.st.Revoked = er.Settings.Normalize(), false
	a.httpc = newClient(server, k.Pin)
	err = a.st.save(a.DataDir)
	st := *a.st
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	a.Log.Info("enrolled", "server", server, "name", er.Name, "site", er.Site)
	if a.queue != nil {
		a.queue.SetLimit(int64(st.Settings.BufferMB) << 20)
		if old.UDP != st.Settings.UDP || old.TCP != st.Settings.TCP {
			signal(a.relisten)
		}
		signal(a.wakeSend)
		signal(a.wakeBeat)
	}
	return &st, nil
}

// Reset forgets the enrollment; the buffer is kept.
func (a *Agent) Reset() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Server, a.st.Pin, a.st.Key, a.st.ID, a.st.Revoked = "", "", "", 0, false
	return a.st.save(a.DataDir)
}

// bootEnroll uses an enrollment key from the boot partition once.
func (a *Agent) bootEnroll(ctx context.Context) {
	for _, p := range BootFiles {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		key := strings.TrimSpace(string(b))
		if key == "" || a.state().Enrolled() {
			return
		}
		for attempt := 0; attempt < 20 && ctx.Err() == nil; attempt++ {
			if _, err := a.Enroll(ctx, key); err == nil {
				return
			} else {
				a.Log.Warn("enrollment with key from "+p, "err", err)
				if strings.Contains(err.Error(), "refused") {
					return
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(30 * time.Second):
			}
		}
		return
	}
}
