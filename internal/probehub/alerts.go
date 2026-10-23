package probehub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// Run checks every minute for probes that went offline or whose buffer is
// filling up, and mails when that changes. It also keeps the probe
// release of the running version cached once probes exist.
func (h *Hub) Run(ctx context.Context) {
	h.init()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.check(ctx)
		}
	}
}

type change struct {
	p    store.Probe
	kind string // offline, online, buffer, buffer_ok
}

func (h *Hub) check(ctx context.Context) {
	cfg := h.Meta.ProbeSettings()
	probes, err := h.Meta.Probes()
	if err != nil {
		return
	}
	// Wait one offline window after a restart of the main server itself,
	// so its own downtime does not flag every probe.
	grace := time.Since(h.started) < time.Duration(cfg.OfflineMinutes)*time.Minute
	var changes []change
	for _, p := range probes {
		if !p.Enrolled {
			continue
		}
		offline := !h.Online(p)
		if grace && offline {
			continue
		}
		full := false
		if s := p.Status; s.BufferMax > 0 {
			full = s.BufferBytes*100 >= s.BufferMax*int64(cfg.BufferAlertPercent)
		}
		if offline == p.OfflineAlerted && full == p.BufferAlerted {
			continue
		}
		if offline != p.OfflineAlerted {
			changes = append(changes, change{p, map[bool]string{true: "offline", false: "online"}[offline]})
		}
		// The buffer state of an offline probe is stale.
		if full != p.BufferAlerted && !offline {
			changes = append(changes, change{p, map[bool]string{true: "buffer", false: "buffer_ok"}[full]})
		} else {
			full = p.BufferAlerted
		}
		h.Meta.SetProbeAlerted(p.ID, offline, full)
		if offline != p.OfflineAlerted {
			h.Log.Info("probe status changed", "probe", p.Name, "offline", offline)
		}
	}
	if len(changes) == 0 {
		return
	}
	compose := func(t i18n.Translator) (string, string) { return probeMail(t, changes, cfg) }
	sev := store.SevInfo
	for _, c := range changes {
		if c.kind == "offline" || c.kind == "buffer" {
			sev = store.SevWarning
		}
	}
	h.Notify.Notify(notify.Notification{Source: store.SourceProbes, Severity: sev, Compose: compose})
	if !cfg.Notify || h.Mailer == nil || !h.Mailer.Configured() {
		return
	}
	rcpt := cfg.Recipients
	if rcpt == "" {
		rcpt = h.Meta.Inventory().Recipients
	}
	if strings.TrimSpace(rcpt) == "" {
		return
	}
	mctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := h.Mailer.Send(mctx, []string{rcpt}, compose); err != nil {
		h.Log.Warn("probe mail", "err", err)
	}
}

func probeMail(t i18n.Translator, changes []change, cfg store.ProbeSettings) (subject, body string) {
	var b strings.Builder
	var offline, problems int
	for _, c := range changes {
		site := c.p.Site
		if site == store.DefaultSite {
			site = t("Default")
		}
		seen := t("never")
		if c.p.LastSeen > 0 {
			seen = time.Unix(c.p.LastSeen, 0).Local().Format("2006-01-02 15:04")
		}
		switch c.kind {
		case "offline":
			offline++
			fmt.Fprintf(&b, "%s\n", t("Probe %s (site %s) is offline: no contact for more than %d minutes, last contact %s.", c.p.Name, site, cfg.OfflineMinutes, seen))
		case "online":
			fmt.Fprintf(&b, "%s\n", t("Probe %s (site %s) is back online.", c.p.Name, site))
		case "buffer":
			problems++
			fmt.Fprintf(&b, "%s\n", t("The buffer of probe %s (site %s) is %d%% full (%d messages waiting). The oldest messages are dropped when it is full.",
				c.p.Name, site, c.p.Status.BufferBytes*100/max(c.p.Status.BufferMax, 1), c.p.Status.BufferRecords))
		case "buffer_ok":
			fmt.Fprintf(&b, "%s\n", t("The buffer of probe %s (site %s) is emptying again.", c.p.Name, site))
		}
	}
	switch {
	case offline > 0:
		subject = t("Probe offline: %d", offline)
	case problems > 0:
		subject = t("Probe buffer filling up")
	default:
		subject = t("Probes back to normal")
	}
	return subject, b.String()
}
