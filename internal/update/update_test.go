package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/version"
)

func TestCompare(t *testing.T) {
	ordered := []string{"0.3.0", "v0.3.1", "0.4.0-alpha", "0.4.0-alpha.1", "0.4.0-beta", "0.4.0-beta.2", "0.4.0-beta.11", "0.4.0-rc.1", "0.4.0", "0.10.0"}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(ordered[i], ordered[j]); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if Compare("v1.2.3+build.5", "1.2.3") != 0 {
		t.Error("build metadata must be ignored")
	}
	if !IsPrerelease("v0.4.0-beta.1") || IsPrerelease("v0.4.0") {
		t.Error("IsPrerelease")
	}
}

func TestPickAndOffer(t *testing.T) {
	rs := []Release{
		{Tag: "v0.4.0-beta.2", Prerelease: true},
		{Tag: "v0.3.1"},
		{Tag: "v0.4.0-beta.1", Prerelease: true},
		{Tag: "v0.3.0"},
	}
	if r := Pick(rs, ChannelStable); r == nil || r.Tag != "v0.3.1" {
		t.Errorf("stable picked %v", r)
	}
	if r := Pick(rs, ChannelTest); r == nil || r.Tag != "v0.4.0-beta.2" {
		t.Errorf("test picked %v", r)
	}
	if Pick(rs[:1], ChannelStable) != nil {
		t.Error("stable must ignore pre-releases")
	}
	cases := []struct {
		tag, current, channel string
		avail, down           bool
	}{
		{"v0.3.1", "0.3.0", ChannelStable, true, false},
		{"v0.3.1", "0.3.1", ChannelStable, false, false},
		{"v0.3.1", "0.4.0-beta.2", ChannelStable, true, true}, // back to production
		{"v0.4.0-beta.2", "0.4.0-beta.1", ChannelTest, true, false},
		{"v0.4.0-beta.1", "0.4.0-beta.2", ChannelTest, false, false},
		{"v0.3.1", "0.4.0", ChannelStable, false, false},
	}
	for _, c := range cases {
		a, d := Offer(c.tag, c.current, c.channel)
		if a != c.avail || d != c.down {
			t.Errorf("Offer(%s, %s, %s) = %v,%v want %v,%v", c.tag, c.current, c.channel, a, d, c.avail, c.down)
		}
	}
}

// fakeBinary writes a script that answers "version" like the real binary.
func fakeBinary(t *testing.T, path, ver string) {
	t.Helper()
	script := "#!/bin/sh\necho \"unify-syslog " + ver + " (commit test, built now)\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T, running string) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	old := version.Version
	version.Version = running
	t.Cleanup(func() { version.Version = old })
	exe := filepath.Join(dir, "unify-syslog")
	fakeBinary(t, exe, running)
	meta := filepath.Join(dir, "meta.db")
	os.WriteFile(meta, []byte("settings of "+running), 0o640)
	u := New()
	u.Exe, u.HistoryDir, u.MetaPath, u.Keep = exe, filepath.Join(dir, "versions"), meta, 3
	u.Snapshot = func(dst string) error {
		b, err := os.ReadFile(meta)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o640)
	}
	return u, exe
}

func reported(t *testing.T, exe string) string {
	t.Helper()
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRollbackRestoresBinaryAndSettings(t *testing.T) {
	u, exe := setup(t, "0.3.0")
	ctx := context.Background()
	// Simulate Apply: keep the running version, then install 0.4.0.
	if err := u.keep(exe, "0.3.0", "update"); err != nil {
		t.Fatal(err)
	}
	fakeBinary(t, exe, "0.4.0")
	version.Version = "0.4.0"
	os.WriteFile(u.MetaPath, []byte("settings of 0.4.0"), 0o640)

	ks := u.Kept()
	if len(ks) != 1 || ks[0].Version != "0.3.0" || !ks[0].HasSnapshot {
		t.Fatalf("kept = %+v", ks)
	}
	ver, err := u.Rollback(ctx, "", nil)
	if err != nil || ver != "0.3.0" {
		t.Fatalf("rollback = %q, %v", ver, err)
	}
	if got := reported(t, exe); !strings.Contains(got, "0.3.0") {
		t.Errorf("binary not restored: %s", got)
	}
	if b, _ := os.ReadFile(u.MetaPath + ".restore"); string(b) != "settings of 0.3.0" {
		t.Errorf("staged restore = %q", b)
	}
	ks = u.Kept()
	if len(ks) != 1 || ks[0].Version != "0.4.0" || ks[0].Reason != "rollback" {
		t.Errorf("after rollback kept = %+v (want only 0.4.0)", ks)
	}
	if ev := u.LastRollback(); ev == nil || ev.From != "0.4.0" || ev.To != "0.3.0" || ev.Auto {
		t.Errorf("event = %+v", ev)
	}
}

func TestRollbackRefusesBrokenBinary(t *testing.T) {
	u, exe := setup(t, "0.4.0")
	if err := u.keep(exe, "0.3.0", "update"); err != nil { // binary reports 0.4.0, not 0.3.0
		t.Fatal(err)
	}
	if _, err := u.Rollback(context.Background(), "0.3.0", nil); err == nil {
		t.Fatal("rollback to a binary reporting the wrong version must fail")
	}
	if !strings.Contains(reported(t, exe), "0.4.0") {
		t.Error("running binary must stay in place")
	}
	if _, err := u.Rollback(context.Background(), "../etc", nil); err == nil {
		t.Error("path traversal must be refused")
	}
}

func TestAutoRollbackAfterFailedStarts(t *testing.T) {
	u, exe := setup(t, "0.3.0")
	ctx := context.Background()
	u.keep(exe, "0.3.0", "update")
	fakeBinary(t, exe, "0.4.0")
	version.Version = "0.4.0"
	u.writePending(pending{From: "0.3.0", To: "0.4.0", StartedAt: time.Now()})

	for i := 0; i < MaxStarts; i++ {
		if err := u.StartupCheck(ctx); err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
	}
	if err := u.StartupCheck(ctx); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("start %d: %v, want ErrRolledBack", MaxStarts+1, err)
	}
	if !strings.Contains(reported(t, exe), "0.3.0") {
		t.Error("previous binary not restored")
	}
	if _, ok := u.readPending(); ok {
		t.Error("pending must be cleared")
	}
	if ev := u.LastRollback(); ev == nil || !ev.Auto || ev.To != "0.3.0" {
		t.Errorf("event = %+v", ev)
	}
	if len(u.Kept()) != 0 {
		t.Error("the failed version must not be kept")
	}
}

func TestWatchStartupConfirms(t *testing.T) {
	u, _ := setup(t, "0.4.0")
	old := HealthyAfter
	HealthyAfter = 0
	t.Cleanup(func() { HealthyAfter = old })
	u.writePending(pending{From: "0.3.0", To: "0.4.0", StartedAt: time.Now()})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u.WatchStartup(ctx, func() bool { return true }, nil)
	if _, ok := u.readPending(); ok {
		t.Error("a healthy start must confirm the update")
	}
}

func TestPrune(t *testing.T) {
	u, exe := setup(t, "0.5.0")
	for i, v := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
		u.keep(exe, v, "update")
		k := Kept{Version: v, KeptAt: time.Now().Add(time.Duration(i) * time.Minute), Reason: "update"}
		writeJSON(u.path(v, "kept.json"), k)
	}
	u.prune()
	ks := u.Kept()
	if len(ks) != 3 || ks[0].Version != "0.4.0" || ks[2].Version != "0.2.0" {
		t.Errorf("kept after prune = %+v", ks)
	}
}

func TestApplyKeepsCurrentAndMarksPending(t *testing.T) {
	u, exe := setup(t, "0.3.0")
	// Release tarball with a binary that reports 0.4.0-beta.1.
	bin := filepath.Join(t.TempDir(), "unify-syslog")
	fakeBinary(t, bin, "0.4.0-beta.1")
	tgz := filepath.Join(t.TempDir(), AssetName())
	if out, err := exec.Command("tar", "-C", filepath.Dir(bin), "-czf", tgz, "unify-syslog").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	data, _ := os.ReadFile(tgz)
	sum := sha256.Sum256(data)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + u.Repo + "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v0.4.0-beta.1","prerelease":true,"assets":[{"name":%q,"browser_download_url":"%s/a.tgz"},{"name":"checksums.txt","browser_download_url":"%s/sums"}]},{"tag_name":"v0.3.0"}]`, AssetName(), srv.URL, srv.URL)
		case "/a.tgz":
			w.Write(data)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), AssetName())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u.API = srv.URL
	ctx := context.Background()

	if err := u.Apply(ctx, ChannelStable, nil); err == nil {
		t.Fatal("stable channel must not install a pre-release")
	}
	res, err := u.Check(ctx, ChannelTest)
	if err != nil || !res.Available || !res.Prerelease || res.Latest != "0.4.0-beta.1" {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if err := u.Apply(ctx, ChannelTest, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reported(t, exe), "0.4.0-beta.1") {
		t.Error("new binary not installed")
	}
	if ks := u.Kept(); len(ks) != 1 || ks[0].Version != "0.3.0" || !ks[0].HasSnapshot {
		t.Errorf("kept = %+v", ks)
	}
	if p, ok := u.readPending(); !ok || p.From != "0.3.0" || p.To != "0.4.0-beta.1" {
		t.Errorf("pending = %+v", p)
	}
}
