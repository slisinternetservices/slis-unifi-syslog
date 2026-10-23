package store

import (
	"path/filepath"
	"testing"
)

func TestSavedSearches(t *testing.T) {
	dir := t.TempDir()
	m, err := OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	alice, bob := &User{Username: "alice", Role: RoleAnalyst}, &User{Username: "bob", Role: RoleViewer}
	for _, u := range []*User{alice, bob} {
		if err := m.CreateUser(u, "correct horse battery"); err != nil {
			t.Fatal(err)
		}
	}
	private := &SavedSearch{OwnerID: alice.ID, Name: "Mine", Query: "category:wifi", Preset: "24h"}
	shared := &SavedSearch{OwnerID: alice.ID, Name: "Team", Query: "category:ips", Shared: true}
	for _, s := range []*SavedSearch{private, shared} {
		if err := m.SaveSearch(s); err != nil {
			t.Fatal(err)
		}
	}
	if l, _ := m.SavedSearches(alice.ID); len(l) != 2 {
		t.Fatalf("alice sees %d", len(l))
	}
	l, _ := m.SavedSearches(bob.ID)
	if len(l) != 1 || l[0].Name != "Team" || l[0].Owner != "alice" || l[0].Pinned {
		t.Fatalf("bob sees %+v", l)
	}
	m.PinSearch(bob.ID, shared.ID, true)
	if s, _ := m.SavedSearch(shared.ID, bob.ID); !s.Pinned {
		t.Error("pin for bob")
	}
	if s, _ := m.SavedSearch(shared.ID, alice.ID); s.Pinned {
		t.Error("pins are per user")
	}
	m.UnshareSearch(shared.ID, alice.ID)
	if s, _ := m.SavedSearch(shared.ID, bob.ID); s.Pinned {
		t.Error("unshare keeps other users' pins")
	}
	m.DeleteUser(alice.ID)
	if l, _ := m.SavedSearches(bob.ID); len(l) != 0 {
		t.Errorf("searches of a deleted user remain: %+v", l)
	}
}
