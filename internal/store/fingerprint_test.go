package store

import (
	"path/filepath"
	"testing"
)

func TestConfigFingerprint(t *testing.T) {
	d := t.TempDir()
	m, err := OpenMeta(filepath.Join(d, "m.db"), filepath.Join(d, "k"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.ConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	// What the server updates by itself does not count.
	m.DB.Exec(`INSERT INTO sessions(token_hash,user_id,expires_at,created_at) VALUES('x',1,1,1)`)
	m.SetSetting("update", UpdateSettings{Channel: "stable", LastVersion: "v9"})
	b, _ := m.ConfigFingerprint()
	m.SetSetting("update", UpdateSettings{Channel: "stable", LastVersion: "v10"})
	c, _ := m.ConfigFingerprint()
	if b != c {
		t.Errorf("last update check changed the fingerprint")
	}
	// Settings do.
	g := m.General()
	g.InstanceName = "Other"
	m.SetSetting("general", g)
	if e, _ := m.ConfigFingerprint(); e == a || e == c {
		t.Errorf("settings change did not change the fingerprint")
	}
}
