package alerts

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func TestDeviceMail(t *testing.T) {
	ap := store.InvDevice{Hostname: "U6-Pro", Name: "AP Reception", Model: "U6-Pro", MAC: "f4:e2:c6:00:00:01", Site: "Office", LastSeen: 1_800_000_000}
	sw := store.InvDevice{Hostname: "USW-24", Site: "default", LastSeen: 1_800_000_000}

	subj, body := deviceMail(i18n.For("en"), []store.InvDevice{ap}, nil, 60)
	if subj != "Device silent: AP Reception" {
		t.Errorf("subject %q", subj)
	}
	if !strings.Contains(body, "more than 60 minutes") || !strings.Contains(body, "site Office") {
		t.Errorf("body %q", body)
	}

	subj, body = deviceMail(i18n.For("en"), []store.InvDevice{ap}, []store.InvDevice{sw}, 60)
	if subj != "1 silent, 1 back online" || !strings.Contains(body, "site Default") {
		t.Errorf("mixed: %q %q", subj, body)
	}
}

func TestCheckDevices(t *testing.T) {
	dir := t.TempDir()
	m, err := store.OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	m.TouchDevice("U6-Pro", "U6-Pro", "aa:00:00:00:00:01", "6.7.10", "Office", "10.0.0.1", now.Add(-2*time.Hour))
	m.TouchDevice("USW-24", "USW-24", "aa:00:00:00:00:02", "7.1.26", "Office", "10.0.0.1", now)
	m.FlushDevices()
	e := &Engine{Meta: m, Log: slog.New(slog.DiscardHandler), Started: now}

	silent := func() map[string]bool {
		out := map[string]bool{}
		for _, d := range m.Devices() {
			out[d.Key] = d.Silent
		}
		return out
	}
	// Server only just started: nothing is judged yet.
	e.checkDevices(context.Background(), now)
	if s := silent(); s["aa:00:00:00:00:01"] {
		t.Fatal("judged during start-up grace period")
	}
	e.Started = now.Add(-2 * time.Hour)
	e.checkDevices(context.Background(), now)
	if s := silent(); !s["aa:00:00:00:00:01"] || s["aa:00:00:00:00:02"] {
		t.Fatalf("after check: %v", s)
	}
	// The device comes back.
	m.TouchDevice("U6-Pro", "U6-Pro", "aa:00:00:00:00:01", "6.7.10", "Office", "10.0.0.1", now)
	e.checkDevices(context.Background(), now)
	if s := silent(); s["aa:00:00:00:00:01"] {
		t.Fatal("device that came back is still silent")
	}
}

func TestDeviceMailDutch(t *testing.T) {
	ap := store.InvDevice{Hostname: "U6-Pro", Site: "default", LastSeen: 1_800_000_000}
	subj, body := deviceMail(i18n.For("nl"), []store.InvDevice{ap}, nil, 30)
	if subj != "Apparaat stil: U6-Pro" || !strings.Contains(body, "langer dan 30 minuten") {
		t.Errorf("%q %q", subj, body)
	}
}
