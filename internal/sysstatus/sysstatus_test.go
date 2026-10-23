package sysstatus

import (
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func TestParseSNMP(t *testing.T) {
	in := `Ip: Forwarding DefaultTTL
Ip: 1 64
Udp: InDatagrams NoPorts InErrors OutDatagrams RcvbufErrors SndbufErrors
Udp: 1000 2 7 50 5 0
UdpLite: InDatagrams NoPorts
UdpLite: 0 0
`
	u, err := parseSNMP(strings.NewReader(in), "Udp:")
	if err != nil || u["InDatagrams"] != 1000 || u["RcvbufErrors"] != 5 || u["InErrors"] != 7 {
		t.Fatalf("%v %v", u, err)
	}
}

func TestFindIssues(t *testing.T) {
	m := &Monitor{}
	now := time.Now()
	m.record(sample{t: now.Add(-2 * time.Minute), kernel: 10, kernelKnown: true, dropped: 1})
	m.record(sample{t: now, kernel: 25, kernelKnown: true, dropped: 1, insertErrs: 2})
	h := store.LogHealth{OK: true, MaxPartsInDay: 500, Disks: []store.Disk{
		{Name: "default", Path: "/var/lib/clickhouse/", Total: 100 << 30, Free: 3 << 30},
		{Name: "cold", Path: "/mnt/cold/", Total: 100 << 30, Free: 10 << 30},
		{Name: "big", Path: "/mnt/big/", Total: 100 << 30, Free: 80 << 30},
	}}
	got := map[string]string{}
	for _, is := range m.findIssues(h, now) {
		got[is.Key] = is.Severity
	}
	want := map[string]string{"parts": "warning", "disk:/var/lib/clickhouse/": "critical", "disk:/mnt/cold/": "warning", "udp_drops": "warning", "insert_errors": "critical"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["queue_full"]; ok || len(got) != len(want) {
		t.Errorf("unexpected issues: %v", got)
	}
	// Two paths on one filesystem raise one issue.
	tmp := t.TempDir()
	same := store.LogHealth{OK: true, Disks: []store.Disk{{Name: "default", Path: tmp, Total: 100 << 30, Free: 1 << 30}}}
	m.DataDir = tmp
	if is := m.findIssues(same, now); len(is) != 3 {
		t.Errorf("same filesystem: %+v", is)
	}
	m.DataDir = ""
	if is := m.findIssues(store.LogHealth{Error: "connection refused"}, now); is[0].Code != "clickhouse_down" || is[0].Params["error"] != "connection refused" {
		t.Errorf("down: %+v", is)
	}
}

func TestSeries(t *testing.T) {
	m := &Monitor{}
	now := time.Now()
	for i := 0; i < keep+20; i++ {
		m.record(sample{t: now.Add(time.Duration(i) * sampleEvery), parsed: uint64(i) * 1000, inserted: uint64(i) * 900})
	}
	s := m.series()
	if len(s) != keep-1 || s[0].Received != 100 || s[0].Stored != 90 {
		t.Fatalf("%d points, first %+v", len(s), s[0])
	}
}
