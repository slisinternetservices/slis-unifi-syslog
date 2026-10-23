package store

import (
 "path/filepath"
 "testing"
)

func TestSLISDefaultsAndOverrides(t *testing.T) {
 dir := t.TempDir()
 m, err := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
 if err != nil { t.Fatal(err) }
 defer m.DB.Close()
 if g := m.General(); g.InstanceName != "SLIS UniFi Syslog" || g.Language != "en" { t.Fatalf("unexpected defaults: %+v", g) }
 if b := m.Branding(); b.CompanyName != "SLIS Internet Services" { t.Fatalf("unexpected branding: %+v", b) }
 if err := m.SaveBranding(Branding{CompanyName: "Customer override"}); err != nil { t.Fatal(err) }
 if b := m.Branding(); b.CompanyName != "Customer override" { t.Fatalf("override lost: %+v", b) }
}
