package store

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestScopeSites(t *testing.T) {
	admin := &User{Role: RoleAdmin, Sites: []string{"a"}}
	if got := admin.ScopeSites([]string{"b"}); !slices.Equal(got, []string{"b"}) {
		t.Errorf("admin: %v", got)
	}
	u := &User{Role: RoleViewer, Sites: []string{"a", "b"}}
	if got := u.ScopeSites(nil); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("empty request: %v", got)
	}
	if got := u.ScopeSites([]string{"b", "c"}); !slices.Equal(got, []string{"b"}) {
		t.Errorf("intersection: %v", got)
	}
	if got := u.ScopeSites([]string{"c"}); len(got) != 1 || got[0] != NoSite {
		t.Errorf("no overlap must match nothing: %v", got)
	}
	if !(&User{Role: RoleViewer, Sites: []string{DefaultSite}}).SiteAllowed("") {
		t.Error("empty site is the default site")
	}
}

func TestUserColumnsAndAudit(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	u := &User{Username: "anna", Role: RoleAnalyst, Sites: []string{"b", "a", "a"}}
	if err := m.CreateUser(u, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	got, err := m.UserByName("anna")
	if err != nil || !slices.Equal(got.Sites, []string{"a", "b"}) || got.Role != RoleAnalyst {
		t.Fatalf("%v %+v", err, got)
	}
	if err := m.EnableTOTP(u.ID, "JBSWY3DPEHPK3PXP", 100, []string{"aaaa-bbbb"}); err != nil {
		t.Fatal(err)
	}
	got, _ = m.UserByID(u.ID)
	if !got.TOTPEnabled || got.TOTPSecret(m) != "JBSWY3DPEHPK3PXP" || got.RecoveryLeft != 1 {
		t.Fatalf("totp: %+v", got)
	}
	if m.UseTOTPStep(u.ID, 100) == nil || m.UseTOTPStep(u.ID, 101) != nil {
		t.Fatal("replay protection")
	}
	if !m.UseRecoveryCode(u.ID, "AAAA BBBB") || m.UseRecoveryCode(u.ID, "aaaabbbb") {
		t.Fatal("recovery code must work exactly once")
	}

	m.Audit("anna", "login", "")
	m.AddAudit(AuditEntry{User: "anna", Action: "export_logs", Detail: "csv, query: category:vpn", IP: "10.0.0.1"})
	m.Audit("bob", "settings_smtp", "")
	rows, _ := m.AuditEntries(AuditQuery{Kind: "export"})
	if len(rows) != 1 || rows[0].IP != "10.0.0.1" {
		t.Fatalf("kind filter: %+v", rows)
	}
	if rows, _ := m.AuditEntries(AuditQuery{Text: "vpn"}); len(rows) != 1 {
		t.Fatalf("text filter: %+v", rows)
	}
	if rows, _ := m.AuditEntries(AuditQuery{User: "ANNA"}); len(rows) != 2 {
		t.Fatalf("user filter: %+v", rows)
	}
	all, _ := m.AuditEntries(AuditQuery{Limit: 2})
	rest, _ := m.AuditEntries(AuditQuery{BeforeID: all[1].ID})
	if len(all) != 2 || len(rest) != 1 {
		t.Fatalf("paging: %d %d", len(all), len(rest))
	}
}
