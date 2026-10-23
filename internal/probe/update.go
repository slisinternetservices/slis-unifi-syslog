package probe

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// Updates come from the main server, never from the internet. Before the
// new binary replaces the running one it must match the checksum the
// server sent and pass a trial run. The previous binary stays next to it
// (".prev"); a version that keeps failing to start, or cannot reach the
// main server within a few minutes, is rolled back automatically.

const (
	maxStartAttempts = 3
	confirmWithin    = 5 * time.Minute
)

type pending struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Attempts int       `json:"attempts"`
	At       time.Time `json:"at"`
}

func pendingPath(dir string) string { return filepath.Join(dir, "update-pending.json") }

func exePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

func (a *Agent) report(r probeproto.UpdateReport) {
	r.At = time.Now().Unix()
	a.mu.Lock()
	a.st.LastUpdate = &r
	a.st.save(a.DataDir)
	a.mu.Unlock()
}

func (a *Agent) selfUpdate(ctx context.Context, offer probeproto.UpdateOffer) {
	fail := func(err error) {
		a.Log.Error("update failed", "to", offer.Version, "err", err)
		a.report(probeproto.UpdateReport{State: "failed", From: version.Version, To: offer.Version, Error: err.Error()})
	}
	if st := a.state(); st.LastUpdate != nil && st.LastUpdate.To == offer.Version && st.LastUpdate.State != "installed" &&
		time.Since(time.Unix(st.LastUpdate.At, 0)) < time.Hour {
		return // failed recently; the server stops offering it once it sees the report
	}
	a.Log.Info("installing update from main server", "from", version.Version, "to", offer.Version)
	exe, err := exePath()
	if err != nil {
		fail(err)
		return
	}
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".update-*.tar.gz")
	if err != nil {
		fail(fmt.Errorf("no write permission in %s: %w", dir, err))
		return
	}
	defer os.Remove(tmp.Name())
	st := a.state()
	dctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(dctx, http.MethodGet, st.Server+offer.Path, nil)
	req.Header.Set("Authorization", "Bearer "+st.Key)
	resp, err := a.client().Do(req)
	if err != nil {
		tmp.Close()
		fail(err)
		return
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	resp.Body.Close()
	tmp.Close()
	if err == nil && resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("download: %s", resp.Status)
	}
	if err != nil {
		fail(err)
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, offer.SHA256) {
		fail(fmt.Errorf("checksum mismatch (%s != %s)", got, offer.SHA256))
		return
	}
	newBin := exe + ".new"
	defer os.Remove(newBin)
	if err := extract(tmp.Name(), "unify-syslog-probe", newBin); err != nil {
		fail(err)
		return
	}
	if err := trialRun(ctx, newBin, offer.Version); err != nil {
		fail(err)
		return
	}
	if err := copyFile(exe, exe+".prev"); err != nil {
		fail(fmt.Errorf("keep current version: %w", err))
		return
	}
	b, _ := json.Marshal(pending{From: version.Version, To: offer.Version, At: time.Now()})
	if err := os.WriteFile(pendingPath(a.DataDir), b, 0o640); err != nil {
		fail(err)
		return
	}
	if err := os.Rename(newBin, exe); err != nil {
		os.Remove(pendingPath(a.DataDir))
		fail(err)
		return
	}
	a.Log.Info("update installed, restarting", "version", offer.Version)
	if a.Restart != nil {
		a.Restart()
	}
}

// StartupCheck runs before the agent starts. During an update it counts
// start attempts and rolls back to the previous binary after too many; it
// returns true when the process must exit so systemd starts that binary.
func StartupCheck(dataDir string, log func(msg string, args ...any)) (exit bool) {
	b, err := os.ReadFile(pendingPath(dataDir))
	if err != nil {
		return false
	}
	var p pending
	if json.Unmarshal(b, &p) != nil || p.To != version.Version {
		os.Remove(pendingPath(dataDir))
		return false
	}
	p.Attempts++
	if p.Attempts <= maxStartAttempts {
		b, _ := json.Marshal(p)
		os.WriteFile(pendingPath(dataDir), b, 0o640)
		return false
	}
	log("new version keeps failing, rolling back", "version", p.To, "to", p.From)
	if err := rollback(dataDir, p, "did not start"); err != nil {
		log("rollback failed", "err", err)
		return false
	}
	return true
}

func rollback(dataDir string, p pending, why string) error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(exe + ".prev"); err != nil {
		return errors.New("no previous version kept")
	}
	if err := os.Rename(exe+".prev", exe); err != nil {
		return err
	}
	os.Remove(pendingPath(dataDir))
	st, err := loadState(dataDir)
	if err == nil {
		st.LastUpdate = &probeproto.UpdateReport{State: "rolled_back", From: p.From, To: p.To, Error: "version " + p.To + " " + why + "; rolled back to " + p.From, At: time.Now().Unix()}
		st.save(dataDir)
	}
	return nil
}

// confirmUpdate is called after a successful heartbeat: the new version
// works, so the update is final.
func (a *Agent) confirmUpdate() {
	b, err := os.ReadFile(pendingPath(a.DataDir))
	if err != nil {
		return
	}
	var p pending
	json.Unmarshal(b, &p)
	os.Remove(pendingPath(a.DataDir))
	if p.To == version.Version {
		a.Log.Info("update confirmed", "version", p.To)
		a.report(probeproto.UpdateReport{State: "installed", From: p.From, To: p.To})
	}
}

// WatchUpdate rolls back when a freshly installed version has not reached
// the main server within confirmWithin.
func (a *Agent) WatchUpdate(ctx context.Context) {
	b, err := os.ReadFile(pendingPath(a.DataDir))
	if err != nil {
		return
	}
	var p pending
	if json.Unmarshal(b, &p) != nil || p.To != version.Version {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(confirmWithin):
	}
	if _, err := os.Stat(pendingPath(a.DataDir)); err != nil {
		return // confirmed
	}
	a.Log.Error("new version cannot reach the main server, rolling back", "version", p.To, "to", p.From)
	if err := rollback(a.DataDir, p, "could not reach the main server"); err != nil {
		a.Log.Error("rollback failed", "err", err)
		return
	}
	if st, err := loadState(a.DataDir); err == nil {
		a.mu.Lock()
		a.st.LastUpdate = st.LastUpdate
		a.mu.Unlock()
	}
	if a.Restart != nil {
		a.Restart()
	}
}

func trialRun(ctx context.Context, bin, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new version does not run on this machine: %v %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), " "+strings.TrimPrefix(want, "v")+" ") {
		return fmt.Errorf("new binary reports %q, expected version %s", strings.TrimSpace(string(out)), want)
	}
	return nil
}

func extract(tgz, want, dst string) error {
	f, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in archive", want)
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) != want || h.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
