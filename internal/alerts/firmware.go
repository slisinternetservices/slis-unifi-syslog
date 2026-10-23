package alerts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// FirmwareDevice is a device that runs older firmware than it could.
type FirmwareDevice struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Model   string `json:"model"`
	Site    string `json:"site"`
	Version string `json:"version"`
	Latest  string `json:"latest"`
	// Reason is "controller" when UniFi Network offers an update, or
	// "newer" when another device of the same model runs a newer version.
	Reason string `json:"reason"`
}

type VersionCount struct {
	Version string `json:"version"`
	Count   int    `json:"count"`
}

type FirmwareModel struct {
	Model    string         `json:"model"`
	Latest   string         `json:"latest"`
	Mixed    bool           `json:"mixed"`
	Versions []VersionCount `json:"versions"`
}

type FirmwareReport struct {
	Models   []FirmwareModel  `json:"models"`
	Outdated []FirmwareDevice `json:"outdated"`
}

// firmwareWindow limits the report to devices that are still around.
const firmwareWindow = 30 * 24 * time.Hour

// Firmware compares the firmware of every recently seen device with the
// newest version seen for the same model, and with what the controller
// says about available updates.
func Firmware(devs []store.InvDevice, controller func(mac string) (store.ControllerDevice, bool), now time.Time) FirmwareReport {
	byModel := map[string]map[string]int{}
	var recent []store.InvDevice
	for _, d := range devs {
		if d.Model == "" || d.Version == "" || now.Sub(time.Unix(d.LastSeen, 0)) > firmwareWindow {
			continue
		}
		recent = append(recent, d)
		if byModel[d.Model] == nil {
			byModel[d.Model] = map[string]int{}
		}
		byModel[d.Model][d.Version]++
	}
	rep := FirmwareReport{Models: []FirmwareModel{}, Outdated: []FirmwareDevice{}}
	latest := map[string]string{}
	for model, vs := range byModel {
		m := FirmwareModel{Model: model}
		for v, n := range vs {
			m.Versions = append(m.Versions, VersionCount{v, n})
			if m.Latest == "" || CompareVersions(v, m.Latest) > 0 {
				m.Latest = v
			}
		}
		for _, v := range m.Versions {
			m.Mixed = m.Mixed || CompareVersions(v.Version, m.Latest) < 0
		}
		sort.Slice(m.Versions, func(i, j int) bool { return CompareVersions(m.Versions[i].Version, m.Versions[j].Version) > 0 })
		latest[model] = m.Latest
		rep.Models = append(rep.Models, m)
	}
	sort.Slice(rep.Models, func(i, j int) bool { return rep.Models[i].Model < rep.Models[j].Model })
	for _, d := range recent {
		f := FirmwareDevice{Key: d.Key, Name: firstNonEmpty(d.Name, d.Hostname), Model: d.Model, Site: d.Site, Version: d.Version, Latest: latest[d.Model]}
		if c, ok := controller(d.MAC); ok && d.MAC != "" {
			if f.Name == d.Hostname && c.Name != "" && d.Name == "" {
				f.Name = c.Name
			}
			if c.Updatable {
				f.Reason = "controller"
			}
		}
		if f.Reason == "" && CompareVersions(d.Version, f.Latest) < 0 {
			f.Reason = "newer"
		}
		if f.Reason != "" {
			rep.Outdated = append(rep.Outdated, f)
		}
	}
	sort.Slice(rep.Outdated, func(i, j int) bool {
		a, b := rep.Outdated[i], rep.Outdated[j]
		if a.Site != b.Site {
			return a.Site < b.Site
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return rep
}

// CompareVersions compares UniFi firmware versions such as "v6.5.28.14491"
// and "6.5.28". Only the numbers both versions carry are compared, so a
// version with a build number equals the same version without one.
func CompareVersions(a, b string) int {
	x, y := versionParts(a), versionParts(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	var out []int
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' }) {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out
}

// checkFirmware mails once per device and version when a device falls
// behind. It runs hourly.
func (e *Engine) checkFirmware(ctx context.Context, now time.Time) {
	if now.Sub(e.lastFirmware) < time.Hour {
		return
	}
	e.lastFirmware = now
	cfg := e.Meta.Inventory()
	lookup := func(string) (store.ControllerDevice, bool) { return store.ControllerDevice{}, false }
	if e.Controller != nil {
		lookup = e.Controller
	}
	rep := Firmware(e.Meta.Devices(), lookup, now)
	outdated := map[string]FirmwareDevice{}
	for _, d := range rep.Outdated {
		outdated[d.Key] = d
	}
	var fresh []FirmwareDevice
	for _, d := range e.Meta.Devices() {
		f, behind := outdated[d.Key]
		switch {
		case behind && d.FirmwareAlerted != d.Version:
			fresh = append(fresh, f)
		case !behind && d.FirmwareAlerted != "":
			e.Meta.SetFirmwareAlerted(d.Key, "")
		}
	}
	if len(fresh) == 0 {
		return
	}
	compose := func(t i18n.Translator) (string, string) { return firmwareMail(t, fresh, rep.Models) }
	sent := e.Notify.Notify(notify.Notification{Source: store.SourceFirmware, Severity: store.SevInfo, Compose: compose}) > 0
	if cfg.FirmwareNotify && e.Mailer != nil && e.Mailer.Configured() {
		mctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := e.Mailer.Send(mctx, []string{cfg.Recipients}, compose); err != nil {
			e.Log.Warn("firmware mail", "err", err)
		} else {
			sent = true
		}
	}
	// Unsent warnings are retried on the next run.
	if !sent {
		return
	}
	for _, f := range fresh {
		e.Meta.SetFirmwareAlerted(f.Key, f.Version)
	}
}

func firmwareMail(t i18n.Translator, devs []FirmwareDevice, models []FirmwareModel) (subject, body string) {
	var b strings.Builder
	b.WriteString(t("These devices run outdated firmware:") + "\n")
	for _, d := range devs {
		site := d.Site
		if site == "" || site == store.DefaultSite {
			site = t("Default")
		}
		why := t("newer version %s runs on other %s devices", d.Latest, d.Model)
		if d.Reason == "controller" {
			why = t("UniFi Network offers an update")
		}
		fmt.Fprintf(&b, "  - %s\n", t("%s (%s, site %s): %s, %s", d.Name, d.Model, site, d.Version, why))
	}
	var mixed []string
	for _, m := range models {
		if m.Mixed {
			vs := make([]string, len(m.Versions))
			for i, v := range m.Versions {
				vs[i] = fmt.Sprintf("%s ×%d", v.Version, v.Count)
			}
			mixed = append(mixed, fmt.Sprintf("  - %s: %s", m.Model, strings.Join(vs, ", ")))
		}
	}
	if len(mixed) > 0 {
		b.WriteString("\n" + t("Models with mixed firmware:") + "\n" + strings.Join(mixed, "\n") + "\n")
	}
	if len(devs) == 1 {
		subject = t("Outdated firmware: %s", devs[0].Name)
	} else {
		subject = t("%d devices run outdated firmware", len(devs))
	}
	return subject, b.String()
}
