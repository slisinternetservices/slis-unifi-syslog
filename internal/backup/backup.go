// Package backup creates full and incremental backups (configuration
// database plus ClickHouse log data), ships them to remote targets (FTP,
// FTPS, SCP, local directory) and restores them.
//
// A backup is a plain tar containing manifest.json, meta.db and, when logs
// are included, logs.native.gz (ClickHouse Native format, gzip). A full
// backup contains every log row; an incremental contains rows received
// since the previous backup in the same chain. Restoring an incremental
// replays its full base and every incremental up to it.
package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/mail"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

type Manifest struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	BaseID      string    `json:"base_id"`
	ParentID    string    `json:"parent_id"`
	FromMS      int64     `json:"from_ms"`
	ToMS        int64     `json:"to_ms"`
	Rows        int64     `json:"rows"`
	IncludeLogs bool      `json:"include_logs"`
	AppVersion  string    `json:"app_version"`
	CreatedAt   time.Time `json:"created_at"`
}

type Status struct {
	Running  bool      `json:"running"`
	Job      string    `json:"job"`
	Step     string    `json:"step"`
	Started  time.Time `json:"started"`
	LastErr  string    `json:"last_error"`
	LastDone time.Time `json:"last_done"`
}

type Manager struct {
	Meta    *store.Meta
	Logs    *store.Logs
	Mailer  *mail.Mailer
	Notify  *notify.Dispatcher
	Dir     string
	KeyPath string // secret key that encrypts stored passwords; travels with meta.db
	Log     *slog.Logger
	Restart func()

	mu     sync.Mutex
	status Status
}

// Settle time: rows can reach ClickHouse a little after their receive
// timestamp (batching, insert retries), so the incremental window ends
// this far in the past.
const settle = 3 * time.Minute

var reName = regexp.MustCompile(`^unify-syslog-(\d{8}-\d{6})-(full|incr)-([0-9a-f]+)(?:-base-([0-9a-f]+))?\.tar(\.age)?$`)

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) begin(job string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Running {
		return fmt.Errorf("a job is already running: %s", m.status.Job)
	}
	m.status = Status{Running: true, Job: job, Started: time.Now(), LastDone: m.status.LastDone}
	return nil
}

func (m *Manager) step(s string) {
	m.mu.Lock()
	m.status.Step = s
	m.mu.Unlock()
	m.Log.Info("backup", "step", s)
}

func (m *Manager) end(err error) {
	m.mu.Lock()
	m.status.Running = false
	m.status.Step = ""
	m.status.LastDone = time.Now()
	if err != nil {
		m.status.LastErr = err.Error()
	} else {
		m.status.LastErr = ""
	}
	m.mu.Unlock()
}

// Start runs a backup in the background.
func (m *Manager) Start(kind string) error {
	if err := m.begin("backup " + kind); err != nil {
		return err
	}
	go func() {
		rec, err := m.run(context.Background(), kind)
		m.end(err)
		m.notify(rec, err)
	}()
	return nil
}

func (m *Manager) lastChain() (full *store.BackupRecord, last *store.BackupRecord) {
	recs, _ := m.Meta.BackupRecords(500)
	for i := range recs {
		r := recs[i]
		if r.Status != "ok" {
			continue
		}
		if last == nil {
			last = &r
		}
		if r.Kind == "full" {
			full = &r
			return
		}
	}
	return nil, nil
}

func (m *Manager) run(ctx context.Context, kind string) (*store.BackupRecord, error) {
	cfg := m.Meta.Backup()
	if err := os.MkdirAll(m.Dir, 0o750); err != nil {
		return nil, err
	}
	full, last := m.lastChain()
	if kind != "full" && (full == nil || last == nil) {
		kind = "full"
	}
	now := time.Now().UTC()
	man := Manifest{ID: store.RandomToken(4), Kind: kind, IncludeLogs: cfg.IncludeLogs, AppVersion: version.Version, CreatedAt: now}
	man.ToMS = now.Add(-settle).UnixMilli()
	if kind == "full" {
		man.BaseID = man.ID
	} else {
		man.BaseID = full.ID
		man.ParentID = last.ID
		man.FromMS = last.Watermark
	}
	name := fmt.Sprintf("unify-syslog-%s-full-%s.tar", now.Format("20060102-150405"), man.ID)
	if kind != "full" {
		name = fmt.Sprintf("unify-syslog-%s-incr-%s-base-%s.tar", now.Format("20060102-150405"), man.ID, man.BaseID)
	}
	rec := &store.BackupRecord{ID: man.ID, Kind: kind, BaseID: man.BaseID, File: name, Status: "running", CreatedAt: now.Unix()}
	m.Meta.SaveBackupRecord(rec)

	work, err := os.MkdirTemp(m.Dir, ".work-")
	if err != nil {
		return rec, err
	}
	defer os.RemoveAll(work)

	fail := func(err error) (*store.BackupRecord, error) {
		rec.Status, rec.Message = "failed", err.Error()
		m.Meta.SaveBackupRecord(rec)
		return rec, err
	}

	m.step("saving configuration")
	if err := m.Meta.Snapshot(filepath.Join(work, "meta.db")); err != nil {
		return fail(fmt.Errorf("snapshot: %w", err))
	}
	if m.KeyPath != "" {
		if k, err := os.ReadFile(m.KeyPath); err == nil {
			os.WriteFile(filepath.Join(work, "secret.key"), k, 0o600)
		}
	}
	if man.IncludeLogs {
		m.step("exporting log data")
		from := time.UnixMilli(man.FromMS)
		to := time.UnixMilli(man.ToMS)
		n, _ := m.Logs.CountReceivedBetween(ctx, from, to)
		man.Rows = int64(n)
		f, err := os.Create(filepath.Join(work, "logs.native.gz"))
		if err != nil {
			return fail(err)
		}
		_, err = m.Logs.Export(ctx, f, from, to)
		f.Close()
		if err != nil {
			return fail(fmt.Errorf("export: %w", err))
		}
	}
	mb, _ := json.MarshalIndent(man, "", "  ")
	os.WriteFile(filepath.Join(work, "manifest.json"), mb, 0o640)

	m.step("writing archive")
	archive := filepath.Join(m.Dir, name)
	if err := writeTar(archive, work, []string{"manifest.json", "meta.db", "secret.key", "logs.native.gz"}); err != nil {
		return fail(err)
	}
	if cfg.Encrypt {
		m.step("encrypting archive")
		if err := encryptFile(archive, archive+".age", cfg.Passphrase); err != nil {
			os.Remove(archive)
			return fail(fmt.Errorf("encrypt: %w", err))
		}
		os.Remove(archive)
		name += ".age"
		archive += ".age"
		rec.File = name
	}
	st, _ := os.Stat(archive)
	rec.Size = st.Size()
	rec.Rows = man.Rows
	rec.Watermark = man.ToMS

	var okTargets, errs []string
	for _, t := range cfg.Targets {
		if !t.Enabled {
			continue
		}
		m.step("uploading to " + t.Name)
		if err := m.upload(ctx, t, archive, name); err != nil {
			errs = append(errs, t.Name+": "+err.Error())
			m.Log.Error("backup upload", "target", t.Name, "err", err)
		} else {
			okTargets = append(okTargets, t.Name)
		}
	}
	rec.Targets = strings.Join(okTargets, ", ")
	rec.Status = "ok"
	if len(errs) > 0 {
		rec.Status = "partial"
		rec.Message = strings.Join(errs, "; ")
		if len(okTargets) == 0 && countEnabled(cfg.Targets) > 0 {
			rec.Status = "failed"
		}
	}
	if rec.Status == "partial" {
		// The local archive is complete, so the chain can continue.
		rec.Status = "ok"
	}
	m.Meta.SaveBackupRecord(rec)

	m.step("pruning old backups")
	m.prune(ctx, cfg)
	if len(errs) > 0 {
		return rec, fmt.Errorf("upload failed: %s", strings.Join(errs, "; "))
	}
	return rec, nil
}

func countEnabled(ts []store.BackupTarget) int {
	n := 0
	for _, t := range ts {
		if t.Enabled {
			n++
		}
	}
	return n
}

func (m *Manager) upload(ctx context.Context, t store.BackupTarget, local, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Hour)
	defer cancel()
	r, err := Connect(ctx, t)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.Upload(ctx, local, name)
}

func writeTar(dst, dir string, names []string) error {
	f, err := os.Create(dst + ".part")
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	for _, n := range names {
		p := filepath.Join(dir, n)
		st, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			f.Close()
			return err
		}
		hdr := &tar.Header{Name: n, Mode: 0o640, Size: st.Size(), ModTime: st.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			f.Close()
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			f.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(dst+".part", dst)
}

// prune keeps the newest KeepChains full backups and their incrementals,
// locally and on every target.
func (m *Manager) prune(ctx context.Context, cfg store.BackupSettings) {
	keep := cfg.KeepChains
	if keep < 1 {
		keep = 1
	}
	recs, _ := m.Meta.BackupRecords(10000)
	var fulls []string
	for _, r := range recs {
		if r.Kind == "full" && r.Status == "ok" {
			fulls = append(fulls, r.ID)
		}
	}
	if len(fulls) <= keep {
		return
	}
	keepBase := map[string]bool{}
	for _, id := range fulls[:keep] {
		keepBase[id] = true
	}
	var drop []store.BackupRecord
	for _, r := range recs {
		if !keepBase[r.BaseID] && r.Status != "running" {
			drop = append(drop, r)
		}
	}
	if len(drop) == 0 {
		return
	}
	for _, t := range cfg.Targets {
		if !t.Enabled {
			continue
		}
		r, err := Connect(ctx, t)
		if err != nil {
			continue
		}
		for _, d := range drop {
			r.Delete(ctx, d.File)
		}
		r.Close()
	}
	for _, d := range drop {
		os.Remove(filepath.Join(m.Dir, d.File))
		m.Meta.DeleteBackupRecord(d.ID)
	}
}

func (m *Manager) notify(rec *store.BackupRecord, err error) {
	cfg := m.Meta.Backup()
	compose := func(t i18n.Translator) (string, string) {
		subject := t("Backup succeeded")
		body := ""
		if rec != nil {
			body = t("Type: %s\nFile: %s\nSize: %d bytes\nRows: %d\nTargets: %s\n", t(rec.Kind), rec.File, rec.Size, rec.Rows, rec.Targets)
		}
		if err != nil {
			subject = t("Backup FAILED")
			body += "\n" + t("Error: %s", err.Error())
		}
		return subject, body
	}
	sev := store.SevInfo
	if err != nil {
		sev = store.SevCritical
	}
	m.Notify.Notify(notify.Notification{Source: store.SourceBackup, Severity: sev, Compose: compose})
	if cfg.NotifyOn == "never" || cfg.NotifyOn == "" || m.Mailer == nil || !m.Mailer.Configured() {
		return
	}
	if err == nil && cfg.NotifyOn != "always" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if e := m.Mailer.Send(ctx, []string{cfg.NotifyTo}, compose); e != nil {
		m.Log.Warn("backup notification", "err", e)
	}
}

// ---------------------------------------------------------------- listing

type Entry struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	ID       string    `json:"id"`
	BaseID   string    `json:"base_id"`
	Time     time.Time `json:"time"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func parseName(f RemoteFile) (Entry, bool) {
	mm := reName.FindStringSubmatch(f.Name)
	if mm == nil {
		return Entry{}, false
	}
	t, _ := time.Parse("20060102-150405", mm[1])
	e := Entry{Name: f.Name, ID: mm[3], Time: t, Size: f.Size, Modified: f.Modified, Kind: "full", BaseID: mm[3]}
	if mm[2] == "incr" {
		e.Kind = "incremental"
		e.BaseID = mm[4]
	}
	return e, true
}

// List returns the backups on a target ("local" means the app's own dir).
func (m *Manager) List(ctx context.Context, targetID string) ([]Entry, error) {
	r, err := m.remote(ctx, targetID)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	files, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, f := range files {
		if e, ok := parseName(f); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *Manager) remote(ctx context.Context, targetID string) (Remote, error) {
	if targetID == "" || targetID == "local" {
		return Connect(ctx, store.BackupTarget{Type: "local", Path: m.Dir})
	}
	for _, t := range m.Meta.Backup().Targets {
		if t.ID == targetID {
			return Connect(ctx, t)
		}
	}
	return nil, fmt.Errorf("target not found")
}

// Test connects to a target and lists it, returning a short description.
func Test(ctx context.Context, t store.BackupTarget) (string, error) {
	r, err := Connect(ctx, t)
	if err != nil {
		return "", err
	}
	defer r.Close()
	files, err := r.List(ctx)
	if err != nil {
		return "", err
	}
	// Verify write access with a small probe file.
	tmp, _ := os.CreateTemp("", "probe")
	tmp.WriteString("unify-syslog write test\n")
	tmp.Close()
	defer os.Remove(tmp.Name())
	probe := ".unify-syslog-probe"
	if err := r.Upload(ctx, tmp.Name(), probe); err != nil {
		return "", fmt.Errorf("connected, but write failed: %w", err)
	}
	r.Delete(ctx, probe)
	return fmt.Sprintf("Connection OK, write OK, %d backup(s) found", len(files)), nil
}

// ---------------------------------------------------------------- restore

type RestoreOptions struct {
	Target string `json:"target"`
	Name   string `json:"name"`
	Config bool   `json:"config"`
	Logs   bool   `json:"logs"`
	// Passphrase decrypts encrypted backups; empty uses the one in the
	// backup settings.
	Passphrase string `json:"passphrase"`
}

func (m *Manager) StartRestore(opts RestoreOptions) error {
	if err := m.begin("restore " + opts.Name); err != nil {
		return err
	}
	go func() {
		err := m.restore(context.Background(), opts)
		m.end(err)
		if err != nil {
			m.Log.Error("restore failed", "err", err)
			return
		}
		if opts.Config && m.Restart != nil {
			m.Log.Info("restore done, restarting to load configuration")
			m.Restart()
		}
	}()
	return nil
}

func (m *Manager) restore(ctx context.Context, opts RestoreOptions) error {
	entries, err := m.List(ctx, opts.Target)
	if err != nil {
		return err
	}
	var sel *Entry
	for i := range entries {
		if entries[i].Name == opts.Name {
			sel = &entries[i]
		}
	}
	if sel == nil {
		return fmt.Errorf("backup %s not found", opts.Name)
	}
	// Build the chain: full base + incrementals up to the selected one.
	var chain []Entry
	for _, e := range entries {
		if e.BaseID == sel.BaseID && !e.Time.After(sel.Time) {
			chain = append(chain, e)
		}
	}
	sort.Slice(chain, func(i, j int) bool {
		if chain[i].Kind != chain[j].Kind {
			return chain[i].Kind == "full"
		}
		return chain[i].Time.Before(chain[j].Time)
	})
	if len(chain) == 0 || chain[0].Kind != "full" {
		return fmt.Errorf("full base backup %s is missing", sel.BaseID)
	}
	work, err := os.MkdirTemp(m.Dir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	r, err := m.remote(ctx, opts.Target)
	if err != nil {
		return err
	}
	defer r.Close()
	var dirs []string
	for i, e := range chain {
		if !opts.Logs && e.Name != sel.Name {
			continue
		}
		m.step(fmt.Sprintf("downloading %d/%d: %s", i+1, len(chain), e.Name))
		local := filepath.Join(work, e.Name)
		if err := r.Download(ctx, e.Name, local); err != nil {
			return fmt.Errorf("download %s: %w", e.Name, err)
		}
		if strings.HasSuffix(e.Name, ".age") {
			m.step(fmt.Sprintf("decrypting %d/%d: %s", i+1, len(chain), e.Name))
			pass := opts.Passphrase
			if pass == "" {
				pass = m.Meta.Backup().Passphrase
			}
			plain := strings.TrimSuffix(local, ".age")
			if err := decryptFile(local, plain, pass); err != nil {
				return fmt.Errorf("decrypt %s: %w", e.Name, err)
			}
			os.Remove(local)
			local = plain
		}
		d := filepath.Join(work, fmt.Sprintf("%03d", i))
		if err := extractTar(local, d); err != nil {
			return fmt.Errorf("extracting %s: %w", e.Name, err)
		}
		os.Remove(local)
		dirs = append(dirs, d)
	}
	if opts.Logs {
		m.step("clearing current log data")
		if err := m.Logs.Truncate(ctx); err != nil {
			return err
		}
		for i, d := range dirs {
			p := filepath.Join(d, "logs.native.gz")
			f, err := os.Open(p)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			m.step(fmt.Sprintf("importing log data %d/%d", i+1, len(dirs)))
			err = m.Logs.Import(ctx, f)
			f.Close()
			if err != nil {
				return fmt.Errorf("import: %w", err)
			}
		}
	}
	if opts.Config {
		m.step("staging configuration")
		last := dirs[len(dirs)-1]
		src, err := os.Open(filepath.Join(last, "meta.db"))
		if err != nil {
			return err
		}
		defer src.Close()
		if err := writeFile(m.Meta.Path()+".restore", src); err != nil {
			return err
		}
		if k, err := os.ReadFile(filepath.Join(last, "secret.key")); err == nil && m.KeyPath != "" {
			if err := os.WriteFile(m.KeyPath, k, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractTar(file, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Base(h.Name)
		switch name {
		case "manifest.json", "meta.db", "secret.key", "logs.native.gz":
		default:
			continue
		}
		out, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	}
}

// ---------------------------------------------------------------- scheduler

func (m *Manager) Scheduler(ctx context.Context, loc func() *time.Location) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.tick(now.In(loc()))
		}
	}
}

func (m *Manager) tick(now time.Time) {
	cfg := m.Meta.Backup()
	if !cfg.Enabled {
		return
	}
	recs, _ := m.Meta.BackupRecords(200)
	var lastAny, lastFull time.Time
	for _, r := range recs {
		if r.Status != "ok" {
			continue
		}
		t := time.Unix(r.CreatedAt, 0)
		if lastAny.IsZero() {
			lastAny = t
		}
		if r.Kind == "full" && lastFull.IsZero() {
			lastFull = t
		}
	}
	hhmm := now.Format("15:04")
	daily := hhmm == cfg.Time && now.Sub(lastAny) > 30*time.Minute
	extra := cfg.IncrementalHours > 0 && !lastAny.IsZero() && now.Sub(lastAny) >= time.Duration(cfg.IncrementalHours)*time.Hour
	if !daily && !extra {
		return
	}
	kind := "incremental"
	every := cfg.FullEveryDays
	if every < 1 {
		every = 7
	}
	if lastFull.IsZero() || (daily && now.Sub(lastFull) >= time.Duration(every)*24*time.Hour-time.Hour) {
		kind = "full"
	}
	if err := m.Start(kind); err != nil {
		m.Log.Warn("scheduled backup", "err", err)
	}
}
