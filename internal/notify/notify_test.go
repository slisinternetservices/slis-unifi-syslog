package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (c *capture) server(t *testing.T, status int, reply string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.reqs = append(c.reqs, r)
		c.body = append(c.body, string(b))
		c.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (c *capture) count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.reqs) }

var msg = Message{Severity: store.SevCritical, Title: "Alert: <SSH>", Body: "line 1\nline 2", Count: 1, Time: time.Unix(1700000000, 0), Instance: "Syslog", Link: "https://syslog.example.com"}

func TestSenders(t *testing.T) {
	var c capture
	srv := c.server(t, 200, `{"ok":true,"status":1}`)
	slackAPI, telegramAPI, messagebirdAPI, birdAPI, pushoverAPI = srv.URL, srv.URL, srv.URL, srv.URL, srv.URL
	cases := []struct {
		ch    store.NotifyChannel
		path  string
		check func(t *testing.T, r *http.Request, body string)
	}{
		{store.NotifyChannel{Kind: store.NotifyTeams, Settings: store.NotifySettings{URL: srv.URL + "/teams"}}, "/teams", func(t *testing.T, r *http.Request, body string) {
			var p struct {
				Attachments []struct {
					ContentType string `json:"contentType"`
					Content     struct {
						Body []struct{ Text, Color string } `json:"body"`
					} `json:"content"`
				} `json:"attachments"`
			}
			json.Unmarshal([]byte(body), &p)
			if len(p.Attachments) != 1 || p.Attachments[0].ContentType != "application/vnd.microsoft.card.adaptive" || p.Attachments[0].Content.Body[0].Color != "Attention" || !strings.Contains(p.Attachments[0].Content.Body[0].Text, "Alert: <SSH>") {
				t.Errorf("teams payload %s", body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifySlack, Settings: store.NotifySettings{URL: srv.URL + "/slack"}}, "/slack", func(t *testing.T, r *http.Request, body string) {
			if dec := jsonText(body); !strings.Contains(dec, "Alert: &lt;SSH&gt;") || !strings.Contains(dec, "#dc2626") {
				t.Errorf("slack payload %s", body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifySlack, Settings: store.NotifySettings{Mode: "bot", Token: "xoxb-1", Channel: "#noc"}}, "/chat.postMessage", func(t *testing.T, r *http.Request, body string) {
			if r.Header.Get("Authorization") != "Bearer xoxb-1" || !strings.Contains(body, `"channel":"#noc"`) {
				t.Errorf("slack bot %v %s", r.Header, body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifyTelegram, Settings: store.NotifySettings{Token: "123:abc", ChatID: "-100", ThreadID: "7"}}, "/bot123:abc/sendMessage", func(t *testing.T, r *http.Request, body string) {
			var p map[string]any
			json.Unmarshal([]byte(body), &p)
			if p["parse_mode"] != "HTML" || p["chat_id"] != "-100" || p["message_thread_id"] != "7" || !strings.Contains(p["text"].(string), "&lt;SSH&gt;") || !strings.Contains(p["text"].(string), "<pre>line 1\nline 2</pre>") {
				t.Errorf("telegram payload %s", body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifySMS, Settings: store.NotifySettings{Token: "key", Originator: "Syslog", Recipients: "+31 6 1234 5678, 0031687654321", MaxLength: 30}}, "/messages", func(t *testing.T, r *http.Request, body string) {
			var p struct {
				Recipients []string `json:"recipients"`
				Body       string   `json:"body"`
			}
			json.Unmarshal([]byte(body), &p)
			if r.Header.Get("Authorization") != "AccessKey key" || strings.Join(p.Recipients, ",") != "31612345678,0031687654321" || p.Body != "[CRITICAL] Alert: <SSH>: line…" {
				t.Errorf("sms payload %s", body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifySMS, Settings: store.NotifySettings{Provider: "bird", Token: "key", Workspace: "ws", ChannelID: "ch", Recipients: "+31612345678"}}, "/workspaces/ws/channels/ch/messages", func(t *testing.T, r *http.Request, body string) {
			if !strings.Contains(body, `"identifierValue":"+31612345678"`) {
				t.Errorf("bird payload %s", body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifyPushover, Settings: store.NotifySettings{Token: "app", User: "usr"}}, "/messages.json", func(t *testing.T, r *http.Request, body string) {
			v, _ := url.ParseQuery(body)
			if v.Get("token") != "app" || v.Get("user") != "usr" || v.Get("priority") != "1" {
				t.Errorf("pushover payload %s", body)
			}
		}},
		{store.NotifyChannel{Kind: store.NotifyWebhook, Settings: store.NotifySettings{URL: srv.URL + "/hook", Secret: "s3", Headers: "X-Env: test"}}, "/hook", func(t *testing.T, r *http.Request, body string) {
			if !strings.HasPrefix(r.Header.Get("X-Uss-Signature"), "sha256=") || r.Header.Get("X-Env") != "test" || !strings.Contains(body, `"severity":"critical"`) {
				t.Errorf("webhook %v %s", r.Header, body)
			}
		}},
	}
	for _, tc := range cases {
		before := c.count()
		if err := Send(context.Background(), tc.ch, msg); err != nil {
			t.Fatalf("%s: %v", tc.ch.Kind, err)
		}
		if c.count() != before+1 {
			t.Fatalf("%s: %d requests", tc.ch.Kind, c.count()-before)
		}
		r, body := c.reqs[before], c.body[before]
		if r.URL.Path != tc.path {
			t.Errorf("%s: path %s, want %s", tc.ch.Kind, r.URL.Path, tc.path)
		}
		tc.check(t, r, body)
	}
}

// jsonText re-encodes a JSON document without HTML escaping.
func jsonText(s string) string {
	var v any
	json.Unmarshal([]byte(s), &v)
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return b.String()
}

func TestSendErrors(t *testing.T) {
	var c capture
	srv := c.server(t, 400, `{"ok":false,"description":"Bad Request: chat not found"}`)
	telegramAPI = srv.URL
	err := Send(context.Background(), store.NotifyChannel{Kind: store.NotifyTelegram, Settings: store.NotifySettings{Token: "secret-token", ChatID: "1"}}, msg)
	if err == nil || !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("telegram error %v", err)
	}
	if err := Validate(store.NotifyChannel{Kind: store.NotifyTeams, Settings: store.NotifySettings{URL: "ftp://x"}}); err == nil {
		t.Fatal("teams with ftp URL accepted")
	}
	if err := Validate(store.NotifyChannel{Kind: store.NotifySMS, Settings: store.NotifySettings{Provider: "bird", Token: "k", Recipients: "+1"}}); err == nil {
		t.Fatal("bird without workspace accepted")
	}
}

func TestQuietHoursAndBatching(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 18, h, m, 0, 0, time.UTC) }
	night := store.NotifyConfig{QuietEnabled: true, QuietStart: "22:00", QuietEnd: "07:00"}
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(23, 0), true}, {at(3, 0), true}, {at(7, 0), false}, {at(12, 0), false}, {at(22, 0), true}} {
		if got := InQuietHours(night, c.t); got != c.want {
			t.Errorf("quiet at %s = %v", c.t.Format("15:04"), got)
		}
	}
	day := store.NotifyConfig{QuietEnabled: true, QuietStart: "09:00", QuietEnd: "17:00"}
	if !InQuietHours(day, at(10, 0)) || InQuietHours(day, at(18, 0)) {
		t.Error("daytime quiet hours")
	}

	now := at(23, 0)
	items := []store.NotifyItem{
		{ID: 1, Severity: store.SevWarning, CreatedAt: now.Add(-2 * time.Minute).Unix()},
		{ID: 2, Severity: store.SevCritical, CreatedAt: now.Unix()},
	}
	if got := Due(night, items, now); len(got) != 0 {
		t.Errorf("quiet hours sent %d", len(got))
	}
	night.QuietCritical = true
	if got := Due(night, items, now); len(got) != 1 || got[0].ID != 2 {
		t.Errorf("critical during quiet hours: %+v", got)
	}

	batch := store.NotifyConfig{BatchMinutes: 5}
	if got := Due(batch, items[:1], now); len(got) != 0 {
		t.Error("batch sent before its window")
	}
	if got := Due(batch, items[:1], now.Add(3*time.Minute)); len(got) != 1 {
		t.Error("batch not sent after its window")
	}
	if got := Due(batch, items, now); len(got) != 2 {
		t.Error("a critical item should flush the batch")
	}
	retry := []store.NotifyItem{{ID: 3, Severity: store.SevWarning, CreatedAt: now.Unix(), NextTry: now.Add(time.Minute).Unix()}}
	if got := Due(store.NotifyConfig{}, retry, now); len(got) != 0 {
		t.Error("retry sent before its time")
	}
}

func TestCombine(t *testing.T) {
	items := []store.NotifyItem{
		{Severity: store.SevInfo, Title: "Device back online: AP", CreatedAt: 1700000000},
		{Severity: store.SevWarning, Title: "Probe offline: 1", Body: "Probe x is offline.", CreatedAt: 1700000060},
	}
	m := Combine(i18n.For("nl"), items)
	if m.Severity != store.SevWarning || m.Count != 2 || !strings.Contains(m.Title, "2") || !strings.Contains(m.Body, "Device back online: AP") || !strings.Contains(m.Body, "Probe x is offline.") {
		t.Fatalf("combine %+v", m)
	}
	if one := Combine(i18n.For("en"), items[:1]); one.Title != items[0].Title {
		t.Fatalf("single %+v", one)
	}
}

func TestDispatcher(t *testing.T) {
	dir := t.TempDir()
	m, err := store.OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	var c capture
	srv := c.server(t, 200, "")
	sms := store.NotifyChannel{Name: "SMS", Kind: store.NotifyWebhook, Enabled: true, Config: store.NotifyConfig{MinSeverity: store.SevCritical, Events: []string{store.SourceBackup}}, Settings: store.NotifySettings{URL: srv.URL + "/sms"}}
	teams := store.NotifyChannel{Name: "Teams", Kind: store.NotifyTeams, Enabled: true, Config: store.NotifyConfig{Events: []string{store.SourceDevices, store.SourceBackup}, Language: "nl"}, Settings: store.NotifySettings{URL: srv.URL + "/teams"}}
	for _, ch := range []*store.NotifyChannel{&sms, &teams} {
		if err := m.SaveNotifyChannel(ch); err != nil {
			t.Fatal(err)
		}
	}
	// The Teams webhook URL is a secret: stored encrypted, masked in the API.
	var raw string
	m.DB.QueryRow(`SELECT settings FROM notify_channels WHERE id=?`, teams.ID).Scan(&raw)
	if strings.Contains(raw, srv.URL) {
		t.Fatal("teams URL stored in plain text")
	}
	if got, _ := m.NotifyChannel(teams.ID); got.Settings.URL != srv.URL+"/teams" || got.Mask().Settings.URL != store.Secret {
		t.Fatalf("teams URL %q", got.Settings.URL)
	}

	d := &Dispatcher{Meta: m, Log: slog.New(slog.DiscardHandler)}
	compose := func(t i18n.Translator) (string, string) { return t("Backup FAILED"), "" }
	if n := d.Notify(Notification{Source: store.SourceBackup, Severity: store.SevCritical, Compose: compose}); n != 2 {
		t.Fatalf("critical backup queued for %d channels", n)
	}
	if n := d.Notify(Notification{Source: store.SourceDevices, Severity: store.SevWarning, Compose: compose}); n != 1 {
		t.Fatalf("device warning queued for %d channels", n)
	}
	// Alert rules address channels directly, regardless of subscriptions.
	if n := d.Notify(Notification{Source: store.SourceAlert, Severity: store.SevInfo, Channels: []int64{sms.ID, teams.ID}, Compose: compose}); n != 1 {
		t.Fatalf("info alert queued for %d channels", n)
	}
	d.Flush(context.Background(), time.Now())
	if c.count() != 2 {
		t.Fatalf("%d deliveries, want one per channel", c.count())
	}
	for i, r := range c.reqs {
		if r.URL.Path == "/teams" && !strings.Contains(c.body[i], "3 meldingen") && !strings.Contains(c.body[i], "Back-up MISLUKT") {
			t.Errorf("teams message not in Dutch: %s", c.body[i])
		}
	}
	if q := m.NotifyQueueCounts(); len(q) != 0 {
		t.Fatalf("queue not empty: %v", q)
	}
	if got, _ := m.NotifyChannel(teams.ID); got.LastOK == 0 {
		t.Fatal("last delivery not recorded")
	}

	// A failing channel keeps its notifications for a retry.
	bad := store.NotifyChannel{Name: "Down", Kind: store.NotifyWebhook, Enabled: true, Config: store.NotifyConfig{Events: []string{store.SourceProbes}}, Settings: store.NotifySettings{URL: "http://127.0.0.1:1/x"}}
	m.SaveNotifyChannel(&bad)
	d.Notify(Notification{Source: store.SourceProbes, Severity: store.SevWarning, Compose: compose})
	d.Flush(context.Background(), time.Now())
	items, _ := m.NotifyQueue(bad.ID)
	if len(items) != 1 || items[0].Attempts != 1 || items[0].NextTry <= time.Now().Unix() {
		t.Fatalf("retry state %+v", items)
	}
	if got, _ := m.NotifyChannel(bad.ID); got.LastError == "" {
		t.Fatal("error not recorded")
	}
}
