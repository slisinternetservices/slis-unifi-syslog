package store

import (
	"path/filepath"
	"testing"
)

func TestAlertRuleChannels(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	r := AlertRule{Name: "IPS", Query: "category:ips", Enabled: true, Channels: []int64{3, 7}, Severity: SevCritical}
	if err := m.SaveAlertRule(&r); err != nil {
		t.Fatal(err)
	}
	u := AlertRule{Name: "Odd", Query: "x", Severity: "bogus", Email: true}
	m.SaveAlertRule(&u)
	rules, _ := m.AlertRules()
	if len(rules) != 2 || len(rules[0].Channels) != 2 || rules[0].Channels[1] != 7 || rules[0].Severity != SevCritical || rules[0].Email {
		t.Fatalf("rule %+v", rules[0])
	}
	if rules[1].Severity != SevWarning || !rules[1].Email || len(rules[1].Channels) != 0 {
		t.Fatalf("rule %+v", rules[1])
	}
}

func TestNotifySecrets(t *testing.T) {
	dir := t.TempDir()
	m, _ := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	c := NotifyChannel{Name: "Hook", Kind: NotifyWebhook, Enabled: true, Settings: NotifySettings{URL: "https://example.com/h", Secret: "s3"}}
	m.SaveNotifyChannel(&c)
	masked := c.Mask()
	if masked.Settings.URL != "https://example.com/h" || masked.Settings.Secret != Secret {
		t.Fatalf("mask %+v", masked.Settings)
	}
	masked.Name = "Renamed"
	if err := m.SaveNotifyChannel(&masked); err != nil {
		t.Fatal(err)
	}
	got, _ := m.NotifyChannel(c.ID)
	if got.Name != "Renamed" || got.Settings.Secret != "s3" || got.Config.Events == nil {
		t.Fatalf("saved %+v", got)
	}
}
