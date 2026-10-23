package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

func rule(cond string, threshold int) store.AlertRule {
	r := store.AlertRule{Condition: cond, Threshold: threshold, WindowMinutes: 5, BaselineHours: 24, SpikePercent: 200}
	r.Validate()
	return r
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	// First baseline event a full day before the five-minute window.
	day := now.Add(-5*time.Minute - 24*time.Hour)
	groups := []store.AlertGroup{{Key: "10.0.0.1", Current: 50}, {Key: "10.0.0.2", Current: 3}}
	if h := Evaluate(rule(store.CondAbove, 10), groups, now); len(h) != 1 || h[0].Key != "10.0.0.1" || h[0].Value != 50 {
		t.Errorf("above: %+v", h)
	}
	if h := Evaluate(rule(store.CondDistinct, 3), groups, now); len(h) != 2 {
		t.Errorf("distinct: %+v", h)
	}

	// Without group by, no rows at all means zero events.
	if h := Evaluate(rule(store.CondBelow, 1), nil, now); len(h) != 1 || h[0].Value != 0 {
		t.Errorf("below, no rows: %+v", h)
	}
	if h := Evaluate(rule(store.CondBelow, 1), []store.AlertGroup{{Current: 4}}, now); len(h) != 0 {
		t.Errorf("below, events: %+v", h)
	}
	quiet := rule(store.CondBelow, 1)
	quiet.GroupBy = "site"
	if h := Evaluate(quiet, []store.AlertGroup{{Key: "Office", Current: 0, Baseline: 2880, First: day}, {Key: "Shop", Current: 12, Baseline: 3000, First: day}}, now); len(h) != 1 || h[0].Key != "Office" || h[0].Normal != 10 {
		t.Errorf("below per site: %+v", h)
	}

	// 288 five-minute windows in 24 hours: a baseline of 2880 is 10 per window.
	spike := rule(store.CondSpike, 20)
	for _, c := range []struct {
		cur, base uint64
		first     time.Time
		want      bool
	}{
		{30, 2880, day, true},       // 3x normal
		{29, 2880, day, false},      // just under 3x
		{25, 0, time.Time{}, false}, // no history
		{900, 288000, day, false},
		// Two hours of history (less than a quarter of the baseline).
		{300, 24, now.Add(-5*time.Minute - 2*time.Hour), false},
		// Twelve hours of history: 1440 is 10 per window.
		{30, 1440, now.Add(-5*time.Minute - 12*time.Hour), true},
	} {
		h := Evaluate(spike, []store.AlertGroup{{Current: c.cur, Baseline: c.base, First: c.first}}, now)
		if (len(h) == 1) != c.want {
			t.Errorf("spike %d over %d since %v: %+v", c.cur, c.base, c.first, h)
		}
	}
}

func TestPlan(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	q := Plan(rule(store.CondAbove, 1), now)
	if !q.From.Equal(now.Add(-5*time.Minute)) || !q.WindowStart.Equal(q.From) {
		t.Errorf("above: %+v", q)
	}
	q = Plan(rule(store.CondSpike, 1), now)
	if !q.From.Equal(now.Add(-5*time.Minute - 24*time.Hour)) {
		t.Errorf("spike baseline: %v", q.From)
	}
	r := rule(store.CondBelow, 1)
	if q = Plan(r, now); !q.From.Equal(q.WindowStart) || !q.Fewest {
		t.Errorf("below: %+v", q)
	}
	r.GroupBy = "site"
	if q = Plan(r, now); !q.From.Before(q.WindowStart) {
		t.Errorf("below per site needs a baseline: %+v", q)
	}
}

func TestDescribe(t *testing.T) {
	r := rule(store.CondDistinct, 20)
	r.DistinctField, r.GroupBy = "dst_port", "src_ip"
	if got := Describe(i18n.For("en"), r); got != "At least 20 different values of dst_port in 5 minutes, per src_ip" {
		t.Errorf("en: %q", got)
	}
	if got := Describe(i18n.For("nl"), rule(store.CondSpike, 10)); !strings.Contains(got, "300%") && !strings.Contains(got, "200%") {
		t.Errorf("nl: %q", got)
	}
}
