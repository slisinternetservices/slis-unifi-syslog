package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const notifySchema = `
CREATE TABLE IF NOT EXISTS notify_channels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  config TEXT NOT NULL DEFAULT '{}',
  settings TEXT NOT NULL DEFAULT '{}',
  last_ok INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  last_error_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS notify_queue (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  channel_id INTEGER NOT NULL,
  severity TEXT NOT NULL,
  source TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_try INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS notify_queue_channel ON notify_queue(channel_id, id);
`

// Notification channel kinds.
const (
	NotifyTeams    = "teams"
	NotifySlack    = "slack"
	NotifyTelegram = "telegram"
	NotifySMS      = "sms"
	NotifyPushover = "pushover"
	NotifyWebhook  = "webhook"
)

var NotifyKinds = []string{NotifyTeams, NotifySlack, NotifyTelegram, NotifySMS, NotifyPushover, NotifyWebhook}

// Notification severities, least severe first.
const (
	SevInfo     = "info"
	SevWarning  = "warning"
	SevCritical = "critical"
)

var NotifySeverities = []string{SevInfo, SevWarning, SevCritical}

// SeverityRank orders severities; unknown values count as warning.
func SeverityRank(s string) int {
	switch s {
	case SevInfo:
		return 0
	case SevCritical:
		return 2
	}
	return 1
}

// Notification sources a channel can subscribe to. Alert rules are not a
// subscription: each rule picks its channels itself.
const (
	SourceAlert    = "alert"
	SourceDevices  = "devices"
	SourceFirmware = "firmware"
	SourceProbes   = "probes"
	SourceBackup   = "backup"
	// SourceSystem is the server's own health: disk space, ClickHouse,
	// dropped messages.
	SourceSystem = "system"
)

var NotifySources = []string{SourceDevices, SourceFirmware, SourceProbes, SourceBackup, SourceSystem}

// NotifyConfig decides what a channel receives and when.
type NotifyConfig struct {
	// MinSeverity drops notifications below it, e.g. critical for SMS.
	MinSeverity string `json:"min_severity"`
	// Events are the system notifications the channel receives.
	Events []string `json:"events"`
	// Language of the messages; empty = server default.
	Language string `json:"language"`
	// Quiet hours: between QuietStart and QuietEnd ("HH:MM", server time)
	// notifications are held and sent as one summary afterwards. Critical
	// ones still go out right away when QuietCritical is set.
	QuietEnabled  bool   `json:"quiet_enabled"`
	QuietStart    string `json:"quiet_start"`
	QuietEnd      string `json:"quiet_end"`
	QuietCritical bool   `json:"quiet_critical"`
	// BatchMinutes collects notifications for this long after the first one
	// and sends them as one message; 0 sends right away. Critical
	// notifications are never delayed.
	BatchMinutes int `json:"batch_minutes"`
}

// NotifySettings holds the connection settings of every kind; each kind
// uses its own subset.
type NotifySettings struct {
	// Teams and Slack incoming webhook, generic webhook.
	URL string `json:"url,omitempty"`
	// Slack: webhook | bot.
	Mode string `json:"mode,omitempty"`
	// Slack bot token, Telegram bot token, Pushover application token,
	// MessageBird / Bird access key.
	Token string `json:"token,omitempty"`
	// Slack channel (bot mode).
	Channel string `json:"channel,omitempty"`
	// Telegram chat and optional forum topic.
	ChatID   string `json:"chat_id,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
	// Pushover user or group key.
	User string `json:"user,omitempty"`
	// SMS: provider messagebird (legacy REST API) | bird (Bird channels API).
	Provider   string `json:"provider,omitempty"`
	Workspace  string `json:"workspace,omitempty"`
	ChannelID  string `json:"channel_id,omitempty"`
	Originator string `json:"originator,omitempty"`
	// Recipients are phone numbers in international format, comma-separated.
	Recipients string `json:"recipients,omitempty"`
	// MaxLength caps an SMS; longer texts are cut.
	MaxLength int `json:"max_length,omitempty"`
	// Webhook: HMAC-SHA256 signing secret and extra headers.
	Secret  string `json:"secret,omitempty"`
	Headers string `json:"headers,omitempty"`
}

// secrets lists the settings stored encrypted and masked in the API. The
// webhook URLs of Teams and Slack carry their own signature.
func (s *NotifySettings) secrets(kind string) []*string {
	out := []*string{&s.Token, &s.Secret, &s.Headers}
	if kind == NotifyTeams || kind == NotifySlack {
		out = append(out, &s.URL)
	}
	return out
}

type NotifyChannel struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Enabled     bool           `json:"enabled"`
	Config      NotifyConfig   `json:"config"`
	Settings    NotifySettings `json:"settings"`
	LastOK      int64          `json:"last_ok"`
	LastError   string         `json:"last_error"`
	LastErrorAt int64          `json:"last_error_at"`
	CreatedAt   int64          `json:"created_at"`
}

// Mask replaces stored secrets with the Secret placeholder.
func (c NotifyChannel) Mask() NotifyChannel {
	for _, p := range c.Settings.secrets(c.Kind) {
		if *p != "" {
			*p = Secret
		}
	}
	return c
}

// KeepSecrets replaces Secret placeholders in c with the values of old.
func (c *NotifyChannel) KeepSecrets(old NotifyChannel) {
	ns, os := c.Settings.secrets(c.Kind), old.Settings.secrets(c.Kind)
	for i, p := range ns {
		if *p == Secret {
			*p = *os[i]
		}
	}
}

const notifyCols = `id,name,kind,enabled,config,settings,last_ok,last_error,last_error_at,created_at`

func (m *Meta) scanNotify(sc interface{ Scan(...any) error }) (NotifyChannel, error) {
	var c NotifyChannel
	var cfg, settings string
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &c.Enabled, &cfg, &settings, &c.LastOK, &c.LastError, &c.LastErrorAt, &c.CreatedAt); err != nil {
		return c, err
	}
	json.Unmarshal([]byte(cfg), &c.Config)
	if c.Config.Events == nil {
		c.Config.Events = []string{}
	}
	json.Unmarshal([]byte(settings), &c.Settings)
	for _, p := range c.Settings.secrets(c.Kind) {
		*p = m.Decrypt(*p)
	}
	return c, nil
}

// NotifyChannels returns all channels with secrets decrypted.
func (m *Meta) NotifyChannels() ([]NotifyChannel, error) {
	rows, err := m.DB.Query(`SELECT ` + notifyCols + ` FROM notify_channels ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyChannel{}
	for rows.Next() {
		c, err := m.scanNotify(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Meta) NotifyChannel(id int64) (NotifyChannel, error) {
	c, err := m.scanNotify(m.DB.QueryRow(`SELECT `+notifyCols+` FROM notify_channels WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SaveNotifyChannel stores c; secrets equal to the Secret placeholder keep
// their stored value.
func (m *Meta) SaveNotifyChannel(c *NotifyChannel) error {
	if c.ID != 0 {
		old, err := m.NotifyChannel(c.ID)
		if err != nil {
			return err
		}
		c.KeepSecrets(old)
	}
	s := c.Settings
	for _, p := range s.secrets(c.Kind) {
		*p = m.Encrypt(*p)
	}
	cb, _ := json.Marshal(c.Config)
	sb, _ := json.Marshal(s)
	if c.ID == 0 {
		c.CreatedAt = time.Now().Unix()
		res, err := m.DB.Exec(`INSERT INTO notify_channels(name,kind,enabled,config,settings,created_at) VALUES(?,?,?,?,?,?)`,
			c.Name, c.Kind, c.Enabled, string(cb), string(sb), c.CreatedAt)
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := m.DB.Exec(`UPDATE notify_channels SET name=?,kind=?,enabled=?,config=?,settings=? WHERE id=?`,
		c.Name, c.Kind, c.Enabled, string(cb), string(sb), c.ID)
	return err
}

func (m *Meta) DeleteNotifyChannel(id int64) error {
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM notify_channels WHERE id=?`, `DELETE FROM notify_queue WHERE channel_id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// SetNotifyResult records the outcome of a delivery attempt.
func (m *Meta) SetNotifyResult(id int64, at time.Time, err error) {
	if err == nil {
		m.DB.Exec(`UPDATE notify_channels SET last_ok=? WHERE id=?`, at.Unix(), id)
		return
	}
	m.DB.Exec(`UPDATE notify_channels SET last_error=?, last_error_at=? WHERE id=?`, err.Error(), at.Unix(), id)
}

// NotifyItem is a rendered notification waiting for its channel.
type NotifyItem struct {
	ID        int64  `json:"id"`
	ChannelID int64  `json:"channel_id"`
	Severity  string `json:"severity"`
	Source    string `json:"source"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
	Attempts  int    `json:"attempts"`
	NextTry   int64  `json:"next_try"`
}

func (m *Meta) EnqueueNotify(it NotifyItem) error {
	_, err := m.DB.Exec(`INSERT INTO notify_queue(channel_id,severity,source,title,body,created_at) VALUES(?,?,?,?,?,?)`,
		it.ChannelID, it.Severity, it.Source, it.Title, it.Body, it.CreatedAt)
	return err
}

// NotifyQueue returns the waiting notifications of a channel, oldest first.
func (m *Meta) NotifyQueue(channel int64) ([]NotifyItem, error) {
	rows, err := m.DB.Query(`SELECT id,channel_id,severity,source,title,body,created_at,attempts,next_try FROM notify_queue WHERE channel_id=? ORDER BY id`, channel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotifyItem{}
	for rows.Next() {
		var it NotifyItem
		if err := rows.Scan(&it.ID, &it.ChannelID, &it.Severity, &it.Source, &it.Title, &it.Body, &it.CreatedAt, &it.Attempts, &it.NextTry); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// NotifyQueueCounts returns the number of waiting notifications per channel.
func (m *Meta) NotifyQueueCounts() map[int64]int {
	out := map[int64]int{}
	rows, err := m.DB.Query(`SELECT channel_id, COUNT(*) FROM notify_queue GROUP BY channel_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if rows.Scan(&id, &n) == nil {
			out[id] = n
		}
	}
	return out
}

func (m *Meta) DeleteNotifyItems(ids []int64) {
	for _, id := range ids {
		m.DB.Exec(`DELETE FROM notify_queue WHERE id=?`, id)
	}
}

// RetryNotifyItems counts a failed attempt and schedules the next one.
func (m *Meta) RetryNotifyItems(ids []int64, next time.Time) {
	for _, id := range ids {
		m.DB.Exec(`UPDATE notify_queue SET attempts=attempts+1, next_try=? WHERE id=?`, next.Unix(), id)
	}
}
