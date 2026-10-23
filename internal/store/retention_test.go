package store

import (
	"testing"
	"time"
)

func TestRetentionTTL(t *testing.T) {
	if got := RetentionTTL(90, nil, ""); got != "toDateTime(ts) + INTERVAL 90 DAY DELETE" {
		t.Errorf("default only: %s", got)
	}
	got := RetentionTTL(90, map[string]int{"wifi": 30, "ips": 365}, "")
	want := "toDateTime(ts) + INTERVAL 365 DAY DELETE, " +
		"toDateTime(ts) + INTERVAL 365 DAY DELETE WHERE category = 'ips', " +
		"toDateTime(ts) + INTERVAL 30 DAY DELETE WHERE category = 'wifi', " +
		"toDateTime(ts) + INTERVAL 90 DAY DELETE WHERE category NOT IN ('ips', 'wifi')"
	if got != want {
		t.Errorf("per category:\n got %s\nwant %s", got, want)
	}
	move := ColdTTL(30, "cold")
	if got := RetentionTTL(90, nil, move); got != "toDateTime(ts) + INTERVAL 30 DAY TO VOLUME 'cold', toDateTime(ts) + INTERVAL 90 DAY DELETE" {
		t.Errorf("with cold storage: %s", got)
	}
}

func TestValidateRetention(t *testing.T) {
	for _, c := range []struct {
		days int
		per  map[string]int
		ok   bool
	}{
		{90, nil, true},
		{0, nil, false},
		{90, map[string]int{"wifi": 30}, true},
		{90, map[string]int{"wifi": 0}, false},
		{90, map[string]int{"wifi'; DROP": 30}, false},
		{90, map[string]int{"Wifi": 30}, false},
	} {
		if err := ValidateRetention(c.days, c.per); (err == nil) != c.ok {
			t.Errorf("%d %v: err %v", c.days, c.per, err)
		}
	}
}

func TestAlertSchedule(t *testing.T) {
	at := func(day time.Weekday, hm string) time.Time {
		// 2026-10-18 is a Sunday.
		d, _ := time.Parse("2006-01-02 15:04", "2026-10-18 "+hm)
		return d.AddDate(0, 0, int(day))
	}
	office := AlertSchedule{Days: []int{1, 2, 3, 4, 5}, From: "08:00", To: "18:00"}
	night := AlertSchedule{From: "22:00", To: "06:00"}
	for _, c := range []struct {
		s    AlertSchedule
		t    time.Time
		want bool
	}{
		{AlertSchedule{}, at(time.Sunday, "03:00"), true},
		{office, at(time.Monday, "09:00"), true},
		{office, at(time.Monday, "18:00"), false},
		{office, at(time.Saturday, "09:00"), false},
		{night, at(time.Tuesday, "23:30"), true},
		{night, at(time.Tuesday, "05:59"), true},
		{night, at(time.Tuesday, "12:00"), false},
	} {
		if got := c.s.Active(c.t); got != c.want {
			t.Errorf("%+v at %s: %v", c.s, c.t.Format("Mon 15:04"), got)
		}
	}
}

func TestAlertRuleValidate(t *testing.T) {
	ok := []AlertRule{
		{Query: "x"},
		{Condition: CondDistinct, DistinctField: "dst_port", GroupBy: "src_ip"},
		{Condition: CondBelow, GroupBy: "site"},
		{Condition: CondSpike, SpikePercent: 300, BaselineHours: 48},
	}
	for _, a := range ok {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	bad := []AlertRule{
		{Condition: "sometimes"},
		{Condition: CondDistinct},
		{Condition: CondDistinct, DistinctField: "src_ip", GroupBy: "src_ip"},
		{GroupBy: "password"},
		{Schedule: AlertSchedule{Days: []int{7}}},
		{Schedule: AlertSchedule{From: "8 o'clock"}},
		{BaselineHours: 1000},
	}
	for _, a := range bad {
		if err := a.Validate(); err == nil {
			t.Errorf("%+v: accepted", a)
		}
	}
	a := AlertRule{}
	a.Validate()
	if a.Condition != CondAbove || a.Threshold != 1 || a.SpikePercent != 200 || a.BaselineHours != 24 {
		t.Errorf("defaults: %+v", a)
	}
}
