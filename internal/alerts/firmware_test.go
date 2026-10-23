package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"v6.5.28.14491", "6.5.28", 0},
		{"6.6.55", "6.5.28.14491", 1},
		{"v7.0.9", "7.0.10", -1},
		{"4.0.66.10832", "4.0.66.10832", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d", c.a, c.b, got)
		}
	}
}

func TestFirmwareReport(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	seen := now.Add(-time.Hour).Unix()
	devs := []store.InvDevice{
		{Key: "a", MAC: "aa", Hostname: "U6-Pro", Model: "U6-Pro", Version: "6.6.55", LastSeen: seen},
		{Key: "b", MAC: "bb", Hostname: "U6-Pro", Name: "AP Hall", Model: "U6-Pro", Version: "6.5.28", LastSeen: seen},
		{Key: "c", MAC: "cc", Hostname: "USW-24", Model: "USW-24", Version: "7.1.26", LastSeen: seen},
		{Key: "d", MAC: "dd", Hostname: "U6-Lite", Model: "U6-Lite", Version: "6.0.1", LastSeen: now.AddDate(0, -3, 0).Unix()}, // gone
	}
	ctrl := func(mac string) (store.ControllerDevice, bool) {
		if mac == "cc" {
			return store.ControllerDevice{Name: "Core switch", Updatable: true}, true
		}
		return store.ControllerDevice{}, false
	}
	rep := Firmware(devs, ctrl, now)
	if len(rep.Models) != 2 || !rep.Models[0].Mixed || rep.Models[0].Latest != "6.6.55" || rep.Models[1].Mixed {
		t.Fatalf("models %+v", rep.Models)
	}
	if len(rep.Outdated) != 2 {
		t.Fatalf("outdated %+v", rep.Outdated)
	}
	byKey := map[string]FirmwareDevice{}
	for _, d := range rep.Outdated {
		byKey[d.Key] = d
	}
	if byKey["b"].Reason != "newer" || byKey["b"].Name != "AP Hall" || byKey["c"].Reason != "controller" || byKey["c"].Name != "Core switch" {
		t.Fatalf("outdated %+v", rep.Outdated)
	}
	subj, body := firmwareMail(i18n.For("nl"), rep.Outdated, rep.Models)
	if subj != "2 apparaten draaien verouderde firmware" || !strings.Contains(body, "U6-Pro: 6.6.55 ×1, 6.5.28 ×1") {
		t.Fatalf("mail %q\n%s", subj, body)
	}
}
