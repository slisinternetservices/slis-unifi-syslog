package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForwardDestinations(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	d := ForwardDestination{Name: "Sentinel", Kind: FwdSentinel, Enabled: true, Filter: ForwardFilter{Categories: []string{"ips"}, MaxSeverity: 3},
		Settings: ForwardSettings{TenantID: "t", ClientID: "c", ClientSecret: "s3cret"}}
	if err := m.SaveForwardDestination(&d); err != nil {
		t.Fatal(err)
	}
	var raw string
	m.DB.QueryRow(`SELECT settings FROM forward_destinations WHERE id=?`, d.ID).Scan(&raw)
	if strings.Contains(raw, "s3cret") {
		t.Fatal("secret stored in plain text")
	}
	masked := d.Mask()
	if masked.Settings.ClientSecret != Secret {
		t.Fatal("not masked")
	}
	masked.Name = "Renamed"
	if err := m.SaveForwardDestination(&masked); err != nil {
		t.Fatal(err)
	}
	got, _ := m.ForwardDestination(d.ID)
	if got.Name != "Renamed" || got.Settings.ClientSecret != "s3cret" || got.Filter.MaxSeverity != 3 {
		t.Fatalf("got %+v", got)
	}

	a := AlertRule{Name: "x", Query: "category:ips", Destinations: []int64{d.ID}}
	m.SaveAlertRule(&a)
	rules, _ := m.AlertRules()
	if len(rules) != 1 || len(rules[0].Destinations) != 1 || rules[0].Destinations[0] != d.ID {
		t.Fatalf("alert destinations %+v", rules)
	}

	old := time.Now().AddDate(0, 0, -100).Truncate(time.Hour).Unix()
	now := time.Now().Truncate(time.Hour).Unix()
	m.AddForwardStats([]ForwardStat{{DestID: d.ID, Hour: old, Events: 5, Bytes: 50}, {DestID: d.ID, Hour: now, Events: 2, Bytes: 20}})
	m.AddForwardStats([]ForwardStat{{DestID: d.ID, Hour: now, Events: 1}})
	if err := m.PurgeForwardStats(); err != nil {
		t.Fatal(err)
	}
	tot, _ := m.ForwardTotals()
	if tot[d.ID].Events != 8 || tot[d.ID].Bytes != 70 {
		t.Fatalf("totals %+v", tot[d.ID])
	}
	recent, _ := m.ForwardStats(time.Now().Add(-time.Hour))
	if len(recent) != 1 || recent[0].Events != 3 {
		t.Fatalf("recent %+v", recent)
	}
	m.DeleteForwardDestination(d.ID)
	if _, err := m.ForwardDestination(d.ID); err != ErrNotFound {
		t.Fatal("not deleted")
	}
}
