// Package probehub is the main server's side of remote probes: enrollment,
// heartbeats, receiving buffered syslog batches and handing out updates.
// Probes run the same version as the main server; their release tarballs
// are fetched once from GitHub and served to the probes from here, so
// probes never need internet access of their own.
package probehub

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/mail"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

type Hub struct {
	Meta *store.Meta
	// Out is the ingest queue shared with the local syslog listeners.
	Out chan<- syslogd.Message
	// Dir holds the cached probe releases (Dir/releases/<version>).
	Dir    string
	Client *http.Client
	Log    *slog.Logger
	Mailer *mail.Mailer
	Notify *notify.Dispatcher
	// Pin is the public key pin of the server certificate.
	Pin string

	mu       sync.Mutex
	assets   map[string]*Asset // by arch, for version.Version
	fetching map[string]bool
	fetchErr map[string]string
	seqMu    sync.Mutex // serialises ingest per probe
	seqLocks map[int64]*sync.Mutex
	started  time.Time
	ingested map[int64]*counter
	ingestMu sync.Mutex
}

type counter struct {
	total uint64
}

// Asset is a cached, verified probe release tarball.
type Asset struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Path    string `json:"-"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

func (h *Hub) init() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.assets == nil {
		h.assets, h.fetching, h.fetchErr = map[string]*Asset{}, map[string]bool{}, map[string]string{}
		h.seqLocks, h.ingested = map[int64]*sync.Mutex{}, map[int64]*counter{}
		h.started = time.Now()
	}
	if h.Client == nil {
		h.Client = &http.Client{Timeout: 10 * time.Minute}
	}
}

func (h *Hub) releaseDir() string { return filepath.Join(h.Dir, "releases", version.Version) }

// Asset returns the probe tarball for arch, downloading it from the GitHub
// release of this server's version when it is not cached yet. A tarball
// placed in the release directory by hand (with checksums.txt) is used as is.
func (h *Hub) Asset(ctx context.Context, arch string) (*Asset, error) {
	h.init()
	if !slices.Contains(probeproto.Archs, arch) {
		return nil, fmt.Errorf("unsupported architecture %q", arch)
	}
	h.mu.Lock()
	if a := h.assets[arch]; a != nil {
		h.mu.Unlock()
		return a, nil
	}
	h.mu.Unlock()
	name := probeproto.AssetName(arch)
	dir := h.releaseDir()
	path := filepath.Join(dir, name)
	if a, err := verifyLocal(dir, name, arch); err == nil {
		h.mu.Lock()
		h.assets[arch] = a
		h.mu.Unlock()
		return a, nil
	}
	if strings.Contains(version.Version, "dev") {
		return nil, fmt.Errorf("this is a development build (%s): there is no probe release to download; put %s and checksums.txt in %s", version.Version, name, dir)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	base := "https://github.com/" + version.Repo + "/releases/download/v" + version.Version + "/"
	sums, err := h.get(ctx, base+"checksums.txt")
	if err != nil {
		return nil, fmt.Errorf("probe release %s: %w", version.Version, err)
	}
	want := checksumFor(string(sums), name)
	if want == "" {
		return nil, fmt.Errorf("release v%s has no probe for %s", version.Version, arch)
	}
	tmp, err := os.CreateTemp(dir, ".dl-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	sum := sha256.New()
	err = h.download(ctx, base+name, io.MultiWriter(tmp, sum))
	tmp.Close()
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return nil, fmt.Errorf("checksum mismatch for %s", name)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	// checksums.txt last: it marks the directory complete.
	if err := appendChecksum(dir, name, want); err != nil {
		return nil, err
	}
	a, err := verifyLocal(dir, name, arch)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.assets[arch] = a
	h.mu.Unlock()
	h.pruneReleases()
	h.Log.Info("probe release cached", "version", version.Version, "arch", arch)
	return a, nil
}

// prepare fetches the asset for arch in the background.
func (h *Hub) prepare(arch string) {
	h.init()
	h.mu.Lock()
	if h.assets[arch] != nil || h.fetching[arch] {
		h.mu.Unlock()
		return
	}
	h.fetching[arch] = true
	h.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		_, err := h.Asset(ctx, arch)
		h.mu.Lock()
		h.fetching[arch] = false
		if err != nil {
			h.fetchErr[arch] = err.Error()
			h.Log.Warn("probe release", "arch", arch, "err", err)
		} else {
			delete(h.fetchErr, arch)
		}
		h.mu.Unlock()
	}()
}

// FetchError returns the last error preparing the release for arch.
func (h *Hub) FetchError(arch string) string {
	h.init()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fetchErr[arch]
}

func verifyLocal(dir, name, arch string) (*Asset, error) {
	sums, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		return nil, err
	}
	want := checksumFor(string(sums), name)
	if want == "" {
		return nil, errors.New("no checksum")
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, f)
	if err != nil {
		return nil, err
	}
	if hex.EncodeToString(sum.Sum(nil)) != want {
		return nil, errors.New("checksum mismatch")
	}
	return &Asset{Version: version.Version, Arch: arch, Path: f.Name(), SHA256: want, Size: n}, nil
}

func checksumFor(sums, name string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func appendChecksum(dir, name, sum string) error {
	p := filepath.Join(dir, "checksums.txt")
	old, _ := os.ReadFile(p)
	if checksumFor(string(old), name) == sum {
		return nil
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s  %s\n", sum, name)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// pruneReleases removes cached releases of other versions.
func (h *Hub) pruneReleases() {
	ents, err := os.ReadDir(filepath.Join(h.Dir, "releases"))
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() && e.Name() != version.Version {
			os.RemoveAll(filepath.Join(h.Dir, "releases", e.Name()))
		}
	}
}

func (h *Hub) get(ctx context.Context, url string) ([]byte, error) {
	var b strings.Builder
	if err := h.download(ctx, url, &b); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func (h *Hub) download(ctx context.Context, url string, w io.Writer) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "unify-syslog-server/"+version.Version)
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// ---------------------------------------------------------------- probes

// Online reports whether a probe was heard from recently.
func (h *Hub) Online(p store.Probe) bool {
	min := h.Meta.ProbeSettings().OfflineMinutes
	return p.LastSeen > 0 && time.Since(time.Unix(p.LastSeen, 0)) < time.Duration(min)*time.Minute
}

// target is the version a probe should run: the one an administrator
// asked for, or with automatic updates the server's own version.
func (h *Hub) target(p *store.Probe) string {
	if p.TargetVersion != "" {
		return p.TargetVersion
	}
	if h.Meta.ProbeSettings().AutoUpdate && p.Version != "" && p.Version != version.Version && p.UpdateState != "failed" && p.UpdateState != "rolled_back" {
		return version.Version
	}
	return ""
}

// Heartbeat stores a probe's status and answers with its settings and, when
// it runs another version than wanted, an update offer.
func (h *Hub) Heartbeat(p *store.Probe, st probeproto.Status, remoteIP string) probeproto.HeartbeatResponse {
	h.init()
	if err := h.Meta.ProbeHeartbeat(p.ID, st, remoteIP); err != nil {
		h.Log.Warn("probe heartbeat", "probe", p.Name, "err", err)
	}
	p.Version = st.Version
	// The outcome of an update the probe reports ends it.
	if r := st.Update; r != nil && r.To == p.TargetVersion && p.TargetVersion != "" {
		switch r.State {
		case "failed", "rolled_back":
			h.Meta.SetProbeUpdateResult(p.ID, r.State, r.Error)
			h.Meta.Audit("system", "probe_update_failed", fmt.Sprintf("%s: %s", p.Name, r.Error))
			p.TargetVersion, p.UpdateState = "", r.State
		}
	}
	if p.TargetVersion != "" && st.Version == p.TargetVersion {
		h.Meta.SetProbeUpdateResult(p.ID, "installed", "")
		p.TargetVersion, p.UpdateState = "", "installed"
	}
	resp := probeproto.HeartbeatResponse{Name: p.Name, Site: p.Site, ServerVersion: version.Version, Settings: p.Settings}
	want := h.target(p)
	if want == "" || want == st.Version {
		return resp
	}
	if want != version.Version {
		// Only the server's own version is ever cached.
		h.Meta.SetProbeUpdateResult(p.ID, "failed", "version "+want+" is not available on the main server")
		return resp
	}
	h.mu.Lock()
	a := h.assets[st.Arch]
	h.mu.Unlock()
	if a == nil {
		h.prepare(st.Arch)
		return resp
	}
	resp.Update = &probeproto.UpdateOffer{Version: a.Version, SHA256: a.SHA256, Size: a.Size, Path: probeproto.Prefix + "/download?arch=" + a.Arch}
	return resp
}

// RequestUpdate asks probes to install the server's version.
func (h *Hub) RequestUpdate(ids []int64) (int, error) {
	n := 0
	probes, err := h.Meta.Probes()
	if err != nil {
		return 0, err
	}
	for _, p := range probes {
		if !slices.Contains(ids, p.ID) || !p.Enrolled {
			continue
		}
		if p.Version == version.Version {
			continue
		}
		if err := h.Meta.SetProbeTarget(p.ID, version.Version); err != nil {
			return n, err
		}
		if p.Status.Arch != "" {
			h.prepare(p.Status.Arch)
		}
		n++
	}
	return n, nil
}

var ErrBusy = errors.New("the server is busy; the probe retries")

// Ingest stores a gzip NDJSON batch. Records the server already has
// (a retried batch) are skipped; the response says how far it got.
func (h *Hub) Ingest(ctx context.Context, p *store.Probe, epoch string, body io.Reader) (probeproto.IngestResponse, error) {
	h.init()
	h.seqMu.Lock()
	l := h.seqLocks[p.ID]
	if l == nil {
		l = &sync.Mutex{}
		h.seqLocks[p.ID] = l
	}
	h.seqMu.Unlock()
	l.Lock()
	defer l.Unlock()
	// Re-read under the lock: a parallel batch may have moved it on.
	cur, err := h.Meta.Probe(p.ID)
	if err != nil {
		return probeproto.IngestResponse{}, err
	}
	curEpoch, seq := cur.Seq()
	if curEpoch != epoch {
		seq = 0
	}
	start := seq
	var resp probeproto.IngestResponse
	defer func() {
		if seq != start || curEpoch != epoch {
			if err := h.Meta.SetProbeSeq(p.ID, epoch, seq); err != nil {
				h.Log.Warn("probe sequence", "probe", p.Name, "err", err)
			}
		}
	}()
	gz, err := gzip.NewReader(body)
	if err != nil {
		resp.Seq = seq
		return resp, fmt.Errorf("batch is not gzip: %w", err)
	}
	dec := json.NewDecoder(io.LimitReader(gz, 256<<20))
	now := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		var r probeproto.Record
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			resp.Seq = seq
			return resp, fmt.Errorf("damaged batch: %w", err)
		}
		if r.Seq <= seq {
			continue
		}
		ts := time.Unix(0, r.TS)
		// A probe without a real-time clock may report nonsense times.
		if ts.Year() < 2020 || ts.After(now.Add(5*time.Minute)) {
			ts = now
		}
		m := syslogd.Message{Data: []byte(r.Data), SourceIP: r.IP, Received: ts, Probe: cur.Name, Site: cur.Site}
		select {
		case h.Out <- m:
		default:
			// The queue is full: wait a little, else let the probe retry.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(5 * time.Second)
			select {
			case h.Out <- m:
			case <-timer.C:
				resp.Seq = seq
				return resp, ErrBusy
			case <-ctx.Done():
				resp.Seq = seq
				return resp, ErrBusy
			}
		}
		seq = r.Seq
		resp.Accepted++
	}
	resp.Seq = seq
	h.ingestMu.Lock()
	c := h.ingested[p.ID]
	if c == nil {
		c = &counter{}
		h.ingested[p.ID] = c
	}
	c.total += uint64(resp.Accepted)
	h.ingestMu.Unlock()
	return resp, nil
}

// Received is the number of messages stored from a probe since the server started.
func (h *Hub) Received(id int64) uint64 {
	h.init()
	h.ingestMu.Lock()
	defer h.ingestMu.Unlock()
	if c := h.ingested[id]; c != nil {
		return c.total
	}
	return 0
}
