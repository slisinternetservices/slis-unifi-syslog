// Package update checks GitHub Releases for a newer version and replaces
// the running binary in place. systemd (Restart=always) brings the new
// version up after the process exits.
//
// Releases come in two channels: stable (production) and test (GitHub
// pre-releases such as v0.4.0-beta.1). Before every install the running
// binary and a snapshot of the settings database are kept in HistoryDir, so
// an update can be rolled back by hand or automatically when the new
// version fails to start (see history.go).
package update

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

// Release channels.
const (
	ChannelStable = "stable"
	ChannelTest   = "test"
)

// NormalizeChannel maps anything unknown to the stable channel.
func NormalizeChannel(c string) string {
	if c == ChannelTest {
		return ChannelTest
	}
	return ChannelStable
}

type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

type CheckResult struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Channel   string `json:"channel"`
	Available bool   `json:"available"`
	// Prerelease marks Latest as a test version.
	Prerelease bool `json:"prerelease"`
	// Downgrade is set when Latest is lower than Current: switching from the
	// test channel back to stable offers the newest stable release.
	Downgrade bool      `json:"downgrade"`
	Notes     string    `json:"notes"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	CheckedAt time.Time `json:"checked_at"`
}

type Updater struct {
	Repo   string
	API    string // GitHub API base URL
	Client *http.Client
	// Exe is the binary that gets replaced; empty means the running one.
	Exe string
	// HistoryDir keeps previous versions for rollback; empty disables it.
	HistoryDir string
	// MetaPath is the settings database; a rollback stages its snapshot as
	// MetaPath+".restore", which store.OpenMeta applies on the next start.
	MetaPath string
	// Snapshot writes a consistent copy of the settings database to dst.
	Snapshot func(dst string) error
	// Keep is how many previous versions stay in HistoryDir.
	Keep int

	mu      sync.Mutex
	running bool
	state   string
}

func New() *Updater {
	return &Updater{Repo: version.Repo, API: "https://api.github.com", Client: &http.Client{Timeout: 5 * time.Minute}, Keep: 3}
}

func (u *Updater) State() (bool, string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running, u.state
}

func (u *Updater) setState(s string) {
	u.mu.Lock()
	u.state = s
	u.mu.Unlock()
}

// begin marks an update or rollback as running; the returned func ends it.
func (u *Updater) begin() (func(), error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return nil, fmt.Errorf("an update is already running")
	}
	u.running = true
	return func() {
		u.mu.Lock()
		u.running = false
		u.mu.Unlock()
	}, nil
}

func (u *Updater) fail(err error) error {
	u.setState("error: " + err.Error())
	return err
}

func (u *Updater) exe() (string, error) {
	exe := u.Exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return "", err
		}
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	return exe, nil
}

// Releases lists the published releases (drafts excluded), newest first.
func (u *Updater) Releases(ctx context.Context) ([]Release, error) {
	url := u.API + "/repos/" + u.Repo + "/releases?per_page=50"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "unify-syslog-server/"+version.Version)
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: %s", resp.Status)
	}
	var rs []Release
	if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
		return nil, err
	}
	out := rs[:0]
	for _, r := range rs {
		if !r.Draft {
			out = append(out, r)
		}
	}
	return out, nil
}

// Pick returns the highest release on the channel: stable only considers
// releases without a pre-release suffix, test considers all of them.
func Pick(rs []Release, channel string) *Release {
	var best *Release
	for i := range rs {
		r := &rs[i]
		if NormalizeChannel(channel) == ChannelStable && (r.Prerelease || IsPrerelease(r.Tag)) {
			continue
		}
		if best == nil || Newer(r.Tag, best.Tag) {
			best = r
		}
	}
	return best
}

// Offer decides whether release tag should be offered to a server running
// current on channel. Newer versions always are; on the stable channel a
// server running a test version is also offered the newest stable release
// when that is lower, so it can leave the test channel.
func Offer(tag, current, channel string) (available, downgrade bool) {
	switch c := Compare(tag, current); {
	case c > 0:
		return true, false
	case c < 0 && NormalizeChannel(channel) == ChannelStable && IsPrerelease(current):
		return true, true
	}
	return false, false
}

func (u *Updater) target(ctx context.Context, channel string) (*Release, error) {
	rs, err := u.Releases(ctx)
	if err != nil {
		return nil, err
	}
	r := Pick(rs, channel)
	if r == nil {
		if NormalizeChannel(channel) == ChannelStable {
			return nil, fmt.Errorf("no stable releases published yet on github.com/%s", u.Repo)
		}
		return nil, fmt.Errorf("no releases published yet on github.com/%s", u.Repo)
	}
	return r, nil
}

func (u *Updater) Check(ctx context.Context, channel string) (*CheckResult, error) {
	channel = NormalizeChannel(channel)
	r, err := u.target(ctx, channel)
	if err != nil {
		return nil, err
	}
	avail, down := Offer(r.Tag, version.Version, channel)
	return &CheckResult{
		Current: version.Version, Latest: strings.TrimPrefix(r.Tag, "v"), Channel: channel,
		Available: avail, Downgrade: down, Prerelease: r.Prerelease || IsPrerelease(r.Tag),
		Notes: r.Body, URL: r.HTMLURL, Published: r.PublishedAt, CheckedAt: time.Now(),
	}, nil
}

// AssetName is the release tarball for this platform.
func AssetName() string {
	return fmt.Sprintf("unify-syslog-server_linux_%s.tar.gz", runtime.GOARCH)
}

// Apply downloads, verifies (checksums.txt and a trial run) and installs
// the release the channel offers, then calls restart. The running version
// is kept in HistoryDir first.
func (u *Updater) Apply(ctx context.Context, channel string, restart func()) error {
	done, err := u.begin()
	if err != nil {
		return err
	}
	defer done()

	u.setState("fetching release")
	r, err := u.target(ctx, channel)
	if err != nil {
		return u.fail(err)
	}
	if avail, _ := Offer(r.Tag, version.Version, channel); !avail {
		u.setState("already up to date")
		return fmt.Errorf("version %s is already the latest", version.Version)
	}
	var assetURL, sumURL string
	for _, a := range r.Assets {
		switch a.Name {
		case AssetName():
			assetURL = a.URL
		case "checksums.txt":
			sumURL = a.URL
		}
	}
	if assetURL == "" {
		return u.fail(fmt.Errorf("release %s does not contain %s", r.Tag, AssetName()))
	}
	exe, err := u.exe()
	if err != nil {
		return u.fail(err)
	}
	dir := filepath.Dir(exe)

	u.setState("downloading " + r.Tag)
	tmp, err := os.CreateTemp(dir, ".update-*.tar.gz")
	if err != nil {
		return u.fail(fmt.Errorf("no write permission in %s: %w", dir, err))
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if err := u.download(ctx, assetURL, io.MultiWriter(tmp, h)); err != nil {
		tmp.Close()
		return u.fail(err)
	}
	tmp.Close()
	if sumURL != "" {
		u.setState("verifying checksum")
		want, err := u.checksum(ctx, sumURL, AssetName())
		if err != nil {
			return u.fail(err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			return u.fail(fmt.Errorf("checksum mismatch (%s != %s)", got, want))
		}
	}
	u.setState("installing")
	newBin := exe + ".new"
	defer os.Remove(newBin)
	if err := extractBinary(tmp.Name(), "unify-syslog", newBin); err != nil {
		return u.fail(err)
	}
	if err := trialRun(ctx, newBin, r.Tag); err != nil {
		return u.fail(err)
	}
	if u.HistoryDir != "" {
		u.setState("keeping version " + version.Version + " for rollback")
		if err := u.keep(exe, version.Version, "update"); err != nil {
			return u.fail(fmt.Errorf("keep current version: %w", err))
		}
	}
	if err := os.Rename(newBin, exe); err != nil {
		return u.fail(err)
	}
	if u.HistoryDir != "" {
		u.writePending(pending{From: version.Version, To: strings.TrimPrefix(r.Tag, "v"), StartedAt: time.Now()})
		u.prune()
	}
	u.setState("restarting into " + r.Tag)
	if restart != nil {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			restart()
		}()
	}
	return nil
}

// trialRun executes "<bin> version" so a binary that cannot run on this
// machine (wrong architecture, damaged download) is never installed.
func trialRun(ctx context.Context, bin, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("new version does not run on this machine: %v %s", err, strings.TrimSpace(string(out)))
	}
	if want != "" && !strings.Contains(string(out), " "+strings.TrimPrefix(want, "v")+" ") {
		return fmt.Errorf("new binary reports %q, expected version %s", strings.TrimSpace(string(out)), want)
	}
	return nil
}

func (u *Updater) download(ctx context.Context, url string, w io.Writer) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "unify-syslog-server/"+version.Version)
	resp, err := u.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: %s", resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (u *Updater) checksum(ctx context.Context, url, name string) (string, error) {
	var b strings.Builder
	if err := u.download(ctx, url, &b); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s", name)
}

func extractBinary(tgz, want, dst string) error {
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
