package alerts

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// checkDevices mails when monitored devices stop sending logs and when they
// come back. Nothing is judged until the server itself has run for the
// silence window, so a restart after downtime does not flag every device.
func (e *Engine) checkDevices(ctx context.Context, now time.Time) {
	cfg := e.Meta.Inventory()
	window := time.Duration(cfg.SilentMinutes) * time.Minute
	if now.Sub(e.Started) < window {
		return
	}
	var silent, back []store.InvDevice
	for _, d := range e.Meta.Devices() {
		quiet := d.Monitored && now.Sub(time.Unix(d.LastSeen, 0)) >= window
		if quiet == d.Silent {
			continue
		}
		if err := e.Meta.SetDeviceSilent(d.Key, quiet); err != nil {
			e.Log.Warn("device state", "device", d.Key, "err", err)
			continue
		}
		if quiet {
			silent = append(silent, d)
		} else if d.Monitored {
			back = append(back, d)
		}
	}
	if len(silent)+len(back) == 0 {
		return
	}
	e.Log.Info("device status changed", "silent", len(silent), "back", len(back))
	compose := func(t i18n.Translator) (string, string) { return deviceMail(t, silent, back, cfg.SilentMinutes) }
	sev := store.SevInfo
	if len(silent) > 0 {
		sev = store.SevWarning
	}
	e.Notify.Notify(notify.Notification{Source: store.SourceDevices, Severity: sev, Compose: compose})
	if !cfg.Notify || e.Mailer == nil || !e.Mailer.Configured() {
		return
	}
	mctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := e.Mailer.Send(mctx, []string{cfg.Recipients}, compose); err != nil {
		e.Log.Warn("device mail", "err", err)
	}
}

func deviceMail(t i18n.Translator, silent, back []store.InvDevice, minutes int) (subject, body string) {
	label := func(d store.InvDevice) string {
		name := d.Name
		if name == "" {
			name = d.Hostname
		}
		site := d.Site
		if site == "" || site == store.DefaultSite {
			site = t("Default")
		}
		return t("%s (%s, site %s, last seen %s)", name, strings.TrimSpace(d.Model+" "+d.MAC), site, time.Unix(d.LastSeen, 0).Local().Format("2006-01-02 15:04"))
	}
	var b strings.Builder
	write := func(title string, list []store.InvDevice) {
		if len(list) == 0 {
			return
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Site+list[i].Hostname < list[j].Site+list[j].Hostname })
		fmt.Fprintf(&b, "%s\n", title)
		for _, d := range list {
			fmt.Fprintf(&b, "  - %s\n", label(d))
		}
		b.WriteString("\n")
	}
	write(t("No logs received (for more than %d minutes):", minutes), silent)
	write(t("Back online:"), back)
	switch {
	case len(silent) > 0 && len(back) > 0:
		subject = t("%d silent, %d back online", len(silent), len(back))
	case len(silent) == 1:
		subject = t("Device silent: %s", firstNonEmpty(silent[0].Name, silent[0].Hostname))
	case len(silent) > 1:
		subject = t("%d devices stopped sending logs", len(silent))
	case len(back) == 1:
		subject = t("Device back online: %s", firstNonEmpty(back[0].Name, back[0].Hostname))
	default:
		subject = t("%d devices back online", len(back))
	}
	return subject, b.String()
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
