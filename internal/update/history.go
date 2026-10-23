package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// Layout of HistoryDir:
//
//	<version>/unify-syslog   the binary as it ran
//	<version>/meta.db        settings database snapshot taken when it was replaced
//	<version>/kept.json      Kept metadata
//	pending.json             an installed update that has not proven itself yet
//	last-rollback.json       the most recent rollback, shown in Info & updates

// Kept is a previous version that can be restored.
type Kept struct {
	Version string    `json:"version"`
	KeptAt  time.Time `json:"kept_at"`
	// Reason is "update" (replaced by an update), "rollback" (replaced by a
	// rollback) or "previous" (found as the .old binary of an older updater).
	Reason      string `json:"reason"`
	HasSnapshot bool   `json:"has_snapshot"`
}

// RollbackEvent records the last rollback.
type RollbackEvent struct {
	From   string    `json:"from"`
	To     string    `json:"to"`
	At     time.Time `json:"at"`
	Auto   bool      `json:"auto"`
	Reason string    `json:"reason,omitempty"`
}

type pending struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	StartedAt time.Time `json:"started_at"`
	Attempts  int       `json:"attempts"`
}

// Limits for the automatic rollback of a freshly installed version.
var (
	// StartDeadline is how long a new version may take to become healthy.
	StartDeadline = 5 * time.Minute
	// MaxStarts is how often it may (re)start before it is given up on. It
	// stays below systemd's default start limit (5 starts in 10 seconds).
	MaxStarts = 3
	// HealthyAfter is how long a new version must run before it is confirmed.
	HealthyAfter = 30 * time.Second
)

// ErrRolledBack is returned by StartupCheck after it restored the previous
// version; the process should exit so systemd starts that version.
var ErrRolledBack = errors.New("new version failed to start; the previous version was restored")

func (u *Updater) path(name ...string) string {
	return filepath.Join(append([]string{u.HistoryDir}, name...)...)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// keep stores binary exe as version ver with a snapshot of the settings
// database, replacing an earlier copy of the same version.
func (u *Updater) keep(exe, ver, reason string) error {
	dir := u.path(ver)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := copyFile(exe, filepath.Join(dir, "unify-syslog"), 0o755); err != nil {
		return err
	}
	k := Kept{Version: ver, KeptAt: time.Now(), Reason: reason}
	if u.Snapshot != nil {
		if err := u.Snapshot(filepath.Join(dir, "meta.db")); err != nil {
			return fmt.Errorf("snapshot settings database: %w", err)
		}
		k.HasSnapshot = true
	}
	return writeJSON(filepath.Join(dir, "kept.json"), k)
}

// Kept lists the versions available for rollback, most recently kept first.
func (u *Updater) Kept() []Kept {
	if u.HistoryDir == "" {
		return nil
	}
	ents, _ := os.ReadDir(u.HistoryDir)
	var out []Kept
	for _, e := range ents {
		var k Kept
		if !e.IsDir() || readJSON(u.path(e.Name(), "kept.json"), &k) != nil {
			continue
		}
		if _, err := os.Stat(u.path(e.Name(), "unify-syslog")); err != nil {
			continue
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].KeptAt.After(out[j].KeptAt) })
	return out
}

// prune removes all but the Keep most recently kept versions.
func (u *Updater) prune() {
	ks := u.Kept()
	for i := u.Keep; u.Keep > 0 && i < len(ks); i++ {
		os.RemoveAll(u.path(ks[i].Version))
	}
}

func (u *Updater) LastRollback() *RollbackEvent {
	var ev RollbackEvent
	if u.HistoryDir == "" || readJSON(u.path("last-rollback.json"), &ev) != nil {
		return nil
	}
	return &ev
}

func (u *Updater) writePending(p pending) { writeJSON(u.path("pending.json"), p) }

func (u *Updater) readPending() (*pending, bool) {
	var p pending
	if u.HistoryDir == "" || readJSON(u.path("pending.json"), &p) != nil {
		return nil, false
	}
	return &p, true
}

func (u *Updater) clearPending() { os.Remove(u.path("pending.json")) }

// Rollback switches to kept version ver ("" = the most recently kept one)
// and calls restart. The running version is kept in its place so the
// rollback can be undone. The kept settings snapshot is staged as
// MetaPath+".restore", so settings return to how they were for that version.
func (u *Updater) Rollback(ctx context.Context, ver string, restart func()) (string, error) {
	done, err := u.begin()
	if err != nil {
		return "", err
	}
	defer done()
	if ver == "" {
		ks := u.Kept()
		if len(ks) == 0 {
			return "", u.fail(errors.New("no previous version is kept on this server"))
		}
		ver = ks[0].Version
	}
	u.setState("rolling back to " + ver)
	if err := u.activate(ctx, ver, true); err != nil {
		return "", u.fail(err)
	}
	writeJSON(u.path("last-rollback.json"), RollbackEvent{From: version.Version, To: ver, At: time.Now()})
	u.setState("restarting into v" + ver)
	if restart != nil {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			restart()
		}()
	}
	return ver, nil
}

// activate installs kept version ver over the running binary and stages its
// settings snapshot. keepCurrent stores the running version first.
func (u *Updater) activate(ctx context.Context, ver string, keepCurrent bool) error {
	ver = strings.TrimPrefix(ver, "v")
	if ver == "" || strings.ContainsAny(ver, `/\`) || ver == "." || ver == ".." {
		return fmt.Errorf("invalid version %q", ver)
	}
	var k Kept
	if err := readJSON(u.path(ver, "kept.json"), &k); err != nil {
		return fmt.Errorf("version %s is not kept on this server", ver)
	}
	exe, err := u.exe()
	if err != nil {
		return err
	}
	newBin := exe + ".new"
	defer os.Remove(newBin)
	if err := copyFile(u.path(ver, "unify-syslog"), newBin, 0o755); err != nil {
		return err
	}
	if err := trialRun(ctx, newBin, ver); err != nil {
		return err
	}
	if keepCurrent && ver != version.Version {
		if err := u.keep(exe, version.Version, "rollback"); err != nil {
			return fmt.Errorf("keep current version: %w", err)
		}
	}
	if k.HasSnapshot && u.MetaPath != "" {
		if err := copyFile(u.path(ver, "meta.db"), u.MetaPath+".restore", 0o640); err != nil {
			return fmt.Errorf("stage settings snapshot: %w", err)
		}
	}
	if err := os.Rename(newBin, exe); err != nil {
		os.Remove(u.MetaPath + ".restore")
		return err
	}
	u.clearPending()
	os.RemoveAll(u.path(ver))
	return nil
}

// StartupCheck runs before the settings database is opened. When this
// process is a freshly installed update that keeps failing to start (more
// than MaxStarts starts, or StartDeadline passed), it restores the previous
// version and returns ErrRolledBack.
func (u *Updater) StartupCheck(ctx context.Context) error {
	p, ok := u.readPending()
	if !ok {
		return nil
	}
	if p.To != version.Version {
		u.clearPending() // started some other way, e.g. a reinstall
		return nil
	}
	p.Attempts++
	u.writePending(*p)
	if p.Attempts <= MaxStarts && time.Since(p.StartedAt) < StartDeadline {
		return nil
	}
	return u.autoRollback(ctx, p, fmt.Sprintf("not healthy after %d starts", p.Attempts))
}

func (u *Updater) autoRollback(ctx context.Context, p *pending, reason string) error {
	if err := u.activate(ctx, p.From, false); err != nil {
		u.clearPending()
		return fmt.Errorf("automatic rollback to %s failed: %w", p.From, err)
	}
	writeJSON(u.path("last-rollback.json"), RollbackEvent{From: p.To, To: p.From, At: time.Now(), Auto: true, Reason: reason})
	return ErrRolledBack
}

// WatchStartup confirms a freshly installed update once healthy() has held
// for HealthyAfter, and rolls back (then calls restart) when that does not
// happen before StartDeadline. It returns at once when no update is pending.
func (u *Updater) WatchStartup(ctx context.Context, healthy func() bool, restart func()) {
	p, ok := u.readPending()
	if !ok || p.To != version.Version {
		return
	}
	deadline := time.NewTimer(time.Until(p.StartedAt.Add(StartDeadline)))
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if !healthy() {
				since = time.Time{}
				continue
			}
			if since.IsZero() {
				since = time.Now()
			}
			if time.Since(since) >= HealthyAfter {
				u.clearPending()
				return
			}
		case <-deadline.C:
			if err := u.autoRollback(ctx, p, "not healthy within "+StartDeadline.String()); errors.Is(err, ErrRolledBack) && restart != nil {
				restart()
			}
			return
		}
	}
}

// ImportPrevious keeps the "<exe>.old" binary that updaters before release
// channels left behind, so the first update to this version can be rolled
// back too. It only runs while nothing is kept yet.
func (u *Updater) ImportPrevious(ctx context.Context) {
	if u.HistoryDir == "" || len(u.Kept()) > 0 {
		return
	}
	exe, err := u.exe()
	if err != nil {
		return
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, old, "version").Output()
	if err != nil {
		return
	}
	f := strings.Fields(string(out)) // "unify-syslog 0.3.0 (commit ...)"
	if len(f) < 2 || Compare(f[1], version.Version) == 0 {
		return
	}
	if err := u.keep(old, f[1], "previous"); err == nil {
		os.Remove(old)
	}
}
