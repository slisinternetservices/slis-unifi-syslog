package auth

import (
	"slices"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func TestAccess(t *testing.T) {
	s := store.SSOSettings{DefaultRole: "viewer", GroupRoles: []store.GroupRole{
		{GroupID: "g-admin", Role: "admin"},
		{GroupID: "g-ams", Role: "analyst", Sites: []string{"amsterdam"}},
		{GroupID: "g-utr", Role: "analyst", Sites: []string{"utrecht"}},
		{GroupID: "g-read", Role: "viewer", Sites: []string{"rotterdam"}},
		{GroupID: "g-all", Role: "viewer"},
	}}
	cases := []struct {
		groups []string
		role   string
		sites  []string
	}{
		{[]string{"G-ADMIN", "g-ams"}, "admin", []string{}},
		{[]string{"g-ams", "g-utr", "g-read"}, "analyst", []string{"amsterdam", "utrecht"}},
		{[]string{"g-read"}, "viewer", []string{"rotterdam"}},
		{[]string{"g-read", "g-all"}, "viewer", []string{}},
		{[]string{"other"}, "viewer", []string{}},
	}
	for _, c := range cases {
		role, sites, managed := Access(s, c.groups)
		if role != c.role || !slices.Equal(sites, c.sites) || !managed {
			t.Errorf("%v: got %s %v %v, want %s %v", c.groups, role, sites, managed, c.role, c.sites)
		}
	}
	if _, _, managed := Access(store.SSOSettings{DefaultRole: "admin"}, []string{"x"}); managed {
		t.Error("no mappings should not manage roles")
	}
	if role, _, _ := Access(store.SSOSettings{DefaultRole: "owner"}, nil); role != "viewer" {
		t.Errorf("invalid default role gave %s", role)
	}
}

func TestCheckTOTP(t *testing.T) {
	enr, err := NewTOTP("UniFi Syslog Server", "admin")
	if err != nil || enr.Secret == "" || enr.QR == "" {
		t.Fatalf("enroll: %v %+v", err, enr)
	}
	now := time.Now()
	code, _ := totp.GenerateCode(enr.Secret, now)
	if _, ok := CheckTOTP(enr.Secret, code, now); !ok {
		t.Fatal("current code rejected")
	}
	old, _ := totp.GenerateCode(enr.Secret, now.Add(-2*time.Minute))
	if _, ok := CheckTOTP(enr.Secret, old, now); ok && old != code {
		t.Fatal("old code accepted")
	}
	if _, ok := CheckTOTP(enr.Secret, "12345", now); ok {
		t.Fatal("short code accepted")
	}
}
