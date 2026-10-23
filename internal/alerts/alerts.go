// Package alerts evaluates user-defined alert rules (a search query plus a
// condition within a time window: a threshold, too few events, distinct
// values or a spike, optionally per field value and on a schedule) and
// notifies by e-mail and notification channels when they fire.
package alerts

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/forward"
	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/mail"
	"github.com/rhisijmuiden/unify-syslog-server/internal/notify"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

type Engine struct {
	Meta   *store.Meta
	Logs   *store.Logs
	Mailer *mail.Mailer
	Log    *slog.Logger

	// Started is when the server came up; silent-device checks wait one
	// silence window after it.
	Started time.Time
	// Controller returns what a UniFi controller reports about a device.
	Controller func(mac string) (store.ControllerDevice, bool)
	// Forward delivers fired alerts to forwarding destinations; optional.
	Forward *forward.Manager
	// Notify delivers to notification channels (Teams, Slack, SMS…); optional.
	Notify *notify.Dispatcher
	// Loc returns the server time zone for rule schedules; nil = local.
	Loc func() *time.Location

	lastFirmware time.Time
}

func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			e.evaluate(ctx, now)
			e.checkDevices(ctx, now)
			e.checkFirmware(ctx, now)
		}
	}
}

func (e *Engine) evaluate(ctx context.Context, now time.Time) {
	rules, err := e.Meta.AlertRules()
	if err != nil {
		return
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if r.LastFired > 0 && now.Sub(time.Unix(r.LastFired, 0)) < time.Duration(max(r.CooldownMinutes, 1))*time.Minute {
			continue
		}
		if !r.Schedule.Active(now.In(e.loc())) {
			continue
		}
		// Right after a start "fewer than" would fire on the gap in the logs.
		if r.Condition == store.CondBelow && now.Sub(e.Started) < time.Duration(r.WindowMinutes)*time.Minute {
			continue
		}
		res, err := e.Check(ctx, r, now)
		if err != nil {
			e.Log.Warn("alert rule", "rule", r.Name, "err", err)
			continue
		}
		if len(res.Hits) == 0 {
			continue
		}
		e.Meta.MarkAlertFired(r.ID, now)
		e.fire(ctx, r, res)
	}
}

func (e *Engine) loc() *time.Location {
	if e.Loc != nil {
		if l := e.Loc(); l != nil {
			return l
		}
	}
	return time.Local
}

// Result is the outcome of evaluating a rule once.
type Result struct {
	Hits []Hit `json:"hits"`
	// Groups is how many groups (or 1 without group by) were evaluated.
	Groups int `json:"groups"`
	// Top is the group with the highest window value, also when it did
	// not fire, so a preview can show how close a rule is.
	Top *Hit `json:"top,omitempty"`
	req store.SearchReq
}

// Hit is one group that met the condition.
type Hit struct {
	Key   string `json:"key"`
	Value uint64 `json:"value"`
	// Normal is the average window value over the baseline (spike and
	// below rules).
	Normal float64 `json:"normal"`
}

// Plan returns the query that evaluates rule r at now.
func Plan(r store.AlertRule, now time.Time) store.AlertQuery {
	window := time.Duration(r.WindowMinutes) * time.Minute
	q := store.AlertQuery{SearchReq: store.SearchReq{Query: r.Query, From: now.Add(-window), To: now}, WindowStart: now.Add(-window), GroupBy: r.GroupBy, Distinct: r.DistinctField}
	switch r.Condition {
	case store.CondSpike:
		q.From = q.WindowStart.Add(-time.Duration(r.BaselineHours) * time.Hour)
	case store.CondBelow:
		q.Fewest = true
		// Groups seen during the baseline that went (almost) quiet.
		if r.GroupBy != "" {
			q.From = q.WindowStart.Add(-time.Duration(r.BaselineHours) * time.Hour)
		}
	}
	return q
}

// Evaluate applies the condition of r, evaluated at now, to the groups of
// its query.
func Evaluate(r store.AlertRule, groups []store.AlertGroup, now time.Time) []Hit {
	window := time.Duration(max(r.WindowMinutes, 1)) * time.Minute
	windowStart := now.Add(-window)
	if r.Condition == store.CondBelow && r.GroupBy == "" && len(groups) == 0 {
		groups = []store.AlertGroup{{}}
	}
	hits := []Hit{}
	for _, g := range groups {
		// Normal is the average window over the history there is, which
		// after a fresh install is shorter than BaselineHours.
		var normal float64
		covered := windowStart.Sub(g.First)
		if g.Baseline > 0 && covered > 0 {
			normal = float64(g.Baseline) / max(covered.Minutes()/window.Minutes(), 1)
		}
		var ok bool
		switch r.Condition {
		case store.CondBelow:
			ok = g.Current < uint64(r.Threshold)
		case store.CondSpike:
			// Without enough history there is no normal to compare with.
			enough := g.Baseline > 0 && covered >= time.Duration(r.BaselineHours)*time.Hour/4
			ok = enough && g.Current >= uint64(r.Threshold) && float64(g.Current) >= normal*(1+float64(r.SpikePercent)/100)
		default:
			ok = g.Current >= uint64(r.Threshold)
		}
		if ok {
			h := Hit{Key: g.Key, Value: g.Current}
			if r.Condition == store.CondSpike || (r.Condition == store.CondBelow && r.GroupBy != "") {
				h.Normal = normal
			}
			hits = append(hits, h)
		}
	}
	return hits
}

// Check evaluates rule r at now without sending anything.
func (e *Engine) Check(ctx context.Context, r store.AlertRule, now time.Time) (Result, error) {
	if err := r.Validate(); err != nil {
		return Result{}, err
	}
	q := Plan(r, now)
	qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	groups, err := e.Logs.AlertStats(qctx, q, 1000)
	if err != nil {
		return Result{}, err
	}
	res := Result{Hits: Evaluate(r, groups, now), Groups: len(groups), req: store.SearchReq{Query: r.Query, From: q.WindowStart, To: now}}
	if len(groups) > 0 {
		top := slices.MaxFunc(groups, func(a, b store.AlertGroup) int { return cmp.Compare(a.Current, b.Current) })
		res.Top = &Hit{Key: top.Key, Value: top.Current}
	}
	return res, nil
}

// Describe explains the condition of a rule in one line.
func Describe(t i18n.Translator, r store.AlertRule) string {
	var s string
	switch r.Condition {
	case store.CondBelow:
		s = t("Fewer than %d events in %d minutes", r.Threshold, r.WindowMinutes)
	case store.CondDistinct:
		s = t("At least %d different values of %s in %d minutes", r.Threshold, r.DistinctField, r.WindowMinutes)
	case store.CondSpike:
		s = t("%d%% more events than normal in %d minutes (normal = average of the last %d hours, at least %d events)", r.SpikePercent, r.WindowMinutes, r.BaselineHours, r.Threshold)
	default:
		s = t("At least %d events in %d minutes", r.Threshold, r.WindowMinutes)
	}
	if r.GroupBy != "" {
		s += ", " + t("per %s", r.GroupBy)
	}
	return s
}

const maxListed = 10

func (e *Engine) fire(ctx context.Context, r store.AlertRule, res Result) {
	hits := res.Hits
	slices.SortFunc(hits, func(a, b Hit) int {
		if r.Condition == store.CondBelow {
			return cmp.Compare(a.Value, b.Value)
		}
		return cmp.Compare(b.Value, a.Value)
	})
	e.Log.Info("alert fired", "rule", r.Name, "value", hits[0].Value, "groups", len(hits))
	e.Forward.SendAlert(r.Destinations, forward.Alert{Rule: r.Name, Query: r.Query, Count: hits[0].Value, Window: r.WindowMinutes, Threshold: r.Threshold})
	req := res.req
	req.Limit = 15
	rows, _ := e.Logs.Search(ctx, req)
	compose := func(t i18n.Translator) (string, string) {
		var b strings.Builder
		b.WriteString(t("Rule: %s\nQuery: %s\n", r.Name, r.Query))
		b.WriteString(t("Condition: %s\n", Describe(t, r)))
		if r.GroupBy == "" {
			b.WriteString(t("Value: %d\n", hits[0].Value))
			if hits[0].Normal > 0 {
				b.WriteString(t("Normal: %.1f\n", hits[0].Normal))
			}
		} else {
			b.WriteString(t("Matches (%d):\n", len(hits)))
			for i, h := range hits {
				if i == maxListed {
					b.WriteString(t("  … and %d more\n", len(hits)-maxListed))
					break
				}
				fmt.Fprintf(&b, "  %s: %d", h.Key, h.Value)
				if h.Normal > 0 {
					b.WriteString(" (" + t("normal %.1f", h.Normal) + ")")
				}
				b.WriteString("\n")
			}
		}
		if len(rows) > 0 {
			b.WriteString(t("\nLatest events:\n"))
		}
		for _, x := range rows {
			fmt.Fprintf(&b, "%s  %-15s %-10s %s\n", x.TS.In(e.loc()).Format("2006-01-02 15:04:05"), x.Hostname, x.Category, x.Message)
		}
		return t("Alert: %s", r.Name), b.String()
	}
	if len(r.Channels) > 0 {
		e.Notify.Notify(notify.Notification{Source: store.SourceAlert, Severity: r.Severity, Channels: r.Channels, Compose: compose})
	}
	if !r.Email || e.Mailer == nil || !e.Mailer.Configured() {
		return
	}
	mctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := e.Mailer.Send(mctx, []string{r.Recipients}, compose); err != nil {
		e.Log.Warn("alert mail", "err", err)
	}
}
