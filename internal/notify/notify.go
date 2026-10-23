// Package notify delivers notifications to chat and messaging channels:
// Microsoft Teams, Slack, Telegram, SMS (MessageBird / Bird), Pushover and
// generic webhooks. Notifications are rendered in the channel's language,
// queued in the settings database and sent by one background loop that
// applies each channel's quiet hours, batching and retries.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// Compose renders a notification in one language.
type Compose func(t i18n.Translator) (title, body string)

// Notification is an event to tell people about.
type Notification struct {
	// Source is one of the store.Source* values.
	Source   string
	Severity string
	// Channels, when not nil, are the only channels that receive the
	// notification (alert rules pick their own); otherwise every channel
	// subscribed to Source does.
	Channels []int64
	Compose  Compose
}

// Message is what a sender delivers: one notification or a batch of them.
type Message struct {
	Severity string
	Title    string
	Body     string
	Time     time.Time
	// Count is the number of notifications combined into this message.
	Count    int
	Instance string
	// Link opens the web interface, when its address is known.
	Link string
}

const (
	maxAge      = 36 * time.Hour
	maxBackoff  = 30 * time.Minute
	tick        = 20 * time.Second
	sendTimeout = 30 * time.Second
)

type Dispatcher struct {
	Meta *store.Meta
	Log  *slog.Logger
	// Loc returns the server time zone for quiet hours; nil = local time.
	Loc func() *time.Location

	once sync.Once
	kick chan struct{}
	// sending serialises delivery so a test or a kick never races the loop.
	sending sync.Mutex
}

func (d *Dispatcher) init() { d.once.Do(func() { d.kick = make(chan struct{}, 1) }) }

// Notify queues n for every matching channel and returns how many channels
// will receive it. It is safe to call on a nil Dispatcher.
func (d *Dispatcher) Notify(n Notification) int {
	if d == nil || d.Meta == nil || n.Compose == nil {
		return 0
	}
	d.init()
	chans, err := d.Meta.NotifyChannels()
	if err != nil {
		d.Log.Warn("notification channels", "err", err)
		return 0
	}
	def := d.Meta.General().Language
	now := time.Now().Unix()
	queued := 0
	for _, c := range chans {
		if !Wants(c, n) {
			continue
		}
		lang := c.Config.Language
		if lang == "" {
			lang = def
		}
		title, body := n.Compose(i18n.For(lang))
		err := d.Meta.EnqueueNotify(store.NotifyItem{ChannelID: c.ID, Severity: severity(n.Severity), Source: n.Source, Title: title, Body: body, CreatedAt: now})
		if err != nil {
			d.Log.Warn("queue notification", "channel", c.Name, "err", err)
			continue
		}
		queued++
	}
	if queued > 0 {
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
	return queued
}

// Wants reports whether channel c receives notification n.
func Wants(c store.NotifyChannel, n Notification) bool {
	if !c.Enabled {
		return false
	}
	if n.Channels != nil {
		if !slices.Contains(n.Channels, c.ID) {
			return false
		}
	} else if !slices.Contains(c.Config.Events, n.Source) {
		return false
	}
	min := c.Config.MinSeverity
	if min == "" {
		min = store.SevInfo
	}
	return store.SeverityRank(severity(n.Severity)) >= store.SeverityRank(min)
}

func severity(s string) string {
	if slices.Contains(store.NotifySeverities, s) {
		return s
	}
	return store.SevWarning
}

// Run delivers queued notifications until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	d.init()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		d.Flush(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.kick:
		}
	}
}

// Flush sends what is due on every channel.
func (d *Dispatcher) Flush(ctx context.Context, now time.Time) {
	d.sending.Lock()
	defer d.sending.Unlock()
	chans, err := d.Meta.NotifyChannels()
	if err != nil {
		return
	}
	g := d.Meta.General()
	for _, c := range chans {
		items, err := d.Meta.NotifyQueue(c.ID)
		if err != nil || len(items) == 0 {
			continue
		}
		var expired []int64
		live := items[:0]
		for _, it := range items {
			if now.Sub(time.Unix(it.CreatedAt, 0)) > maxAge || !c.Enabled {
				expired = append(expired, it.ID)
			} else {
				live = append(live, it)
			}
		}
		if len(expired) > 0 {
			d.Meta.DeleteNotifyItems(expired)
			if c.Enabled {
				d.Log.Warn("notifications expired", "channel", c.Name, "count", len(expired))
			}
		}
		due := Due(c.Config, live, d.local(now))
		if len(due) == 0 {
			continue
		}
		m := Combine(i18n.For(firstNonEmpty(c.Config.Language, g.Language)), due)
		m.Instance = firstNonEmpty(g.InstanceName, "UniFi Syslog Server")
		m.Link = strings.TrimRight(g.BaseURL, "/")
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		err = Send(sctx, c, m)
		cancel()
		ids := make([]int64, len(due))
		for i, it := range due {
			ids[i] = it.ID
		}
		d.Meta.SetNotifyResult(c.ID, now, err)
		if err != nil {
			d.Log.Warn("notification", "channel", c.Name, "kind", c.Kind, "err", err)
			wait := time.Minute << min(due[0].Attempts, 5)
			d.Meta.RetryNotifyItems(ids, now.Add(min(wait, maxBackoff)))
			continue
		}
		d.Meta.DeleteNotifyItems(ids)
	}
}

func (d *Dispatcher) local(t time.Time) time.Time {
	if d.Loc != nil {
		if l := d.Loc(); l != nil {
			return t.In(l)
		}
	}
	return t.Local()
}

// Due picks the queued items that should be sent now, applying quiet hours,
// batching and retry back-off. now must be in the server time zone.
func Due(cfg store.NotifyConfig, items []store.NotifyItem, now time.Time) []store.NotifyItem {
	var out []store.NotifyItem
	critical := false
	quiet := InQuietHours(cfg, now)
	for _, it := range items {
		if it.NextTry > now.Unix() {
			continue
		}
		crit := it.Severity == store.SevCritical
		if quiet && !(crit && cfg.QuietCritical) {
			continue
		}
		critical = critical || crit
		out = append(out, it)
	}
	if len(out) == 0 || critical || cfg.BatchMinutes <= 0 {
		return out
	}
	// Hold a batch until its oldest item has waited the batch window; a
	// retry (attempts > 0) has already waited.
	oldest := out[0]
	if oldest.Attempts == 0 && now.Sub(time.Unix(oldest.CreatedAt, 0)) < time.Duration(cfg.BatchMinutes)*time.Minute {
		return nil
	}
	return out
}

// InQuietHours reports whether now (server time) is within the quiet hours.
func InQuietHours(cfg store.NotifyConfig, now time.Time) bool {
	if !cfg.QuietEnabled {
		return false
	}
	start, ok1 := clock(cfg.QuietStart)
	end, ok2 := clock(cfg.QuietEnd)
	if !ok1 || !ok2 || start == end {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// ValidClock reports whether s is a time of day as HH:MM.
func ValidClock(s string) bool { _, ok := clock(s); return ok }

func clock(s string) (int, bool) {
	var h, m int
	if n, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || n != 2 || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Combine turns queued items into one message: a single item as is, several
// as a summary with every item in the body.
func Combine(t i18n.Translator, items []store.NotifyItem) Message {
	m := Message{Severity: store.SevInfo, Count: len(items), Time: time.Unix(items[len(items)-1].CreatedAt, 0)}
	for _, it := range items {
		if store.SeverityRank(it.Severity) > store.SeverityRank(m.Severity) {
			m.Severity = it.Severity
		}
	}
	if len(items) == 1 {
		m.Title, m.Body = items[0].Title, items[0].Body
		return m
	}
	m.Title = t("%d notifications", len(items))
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s · %s\n", time.Unix(it.CreatedAt, 0).Local().Format("15:04"), it.Title)
		if body := strings.TrimSpace(it.Body); body != "" {
			b.WriteString(body + "\n")
		}
	}
	m.Body = b.String()
	return m
}

// Test sends a test message to a channel right away, bypassing the queue.
func Test(ctx context.Context, c store.NotifyChannel, g store.GeneralSettings) error {
	t := i18n.For(firstNonEmpty(c.Config.Language, g.Language))
	m := Message{
		Severity: store.SevInfo, Count: 1, Time: time.Now(),
		Title:    t("Test message"),
		Body:     t("This channel is set up correctly. You will receive notifications from %s here.", firstNonEmpty(g.InstanceName, "UniFi Syslog Server")),
		Instance: firstNonEmpty(g.InstanceName, "UniFi Syslog Server"),
		Link:     strings.TrimRight(g.BaseURL, "/"),
	}
	return Send(ctx, c, m)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
