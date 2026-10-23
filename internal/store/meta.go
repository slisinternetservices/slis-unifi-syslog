// Package store holds the metadata database (SQLite) and the log store (ClickHouse).
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Meta struct {
	DB   *sql.DB
	path string
	key  []byte

	mu      sync.RWMutex
	sources map[string]Source    // by IP
	devices map[string]InvDevice // by DeviceKey
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  email TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT 'viewer',
  provider TEXT NOT NULL DEFAULT 'local',
  external_id TEXT NOT NULL DEFAULT '',
  disabled INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_login INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sources (
  ip TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  site TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  mac TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
  key TEXT PRIMARY KEY,
  mac TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  prev_version TEXT NOT NULL DEFAULT '',
  version_changed INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL DEFAULT '',
  notes TEXT NOT NULL DEFAULT '',
  site TEXT NOT NULL DEFAULT '',
  source_ip TEXT NOT NULL DEFAULT '',
  monitored INTEGER NOT NULL DEFAULT 1,
  silent INTEGER NOT NULL DEFAULT 0,
  first_seen INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS site_ranges (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site TEXT NOT NULL,
  cidr TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS categories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  color TEXT NOT NULL DEFAULT '#6366f1',
  icon TEXT NOT NULL DEFAULT 'filter',
  query TEXT NOT NULL,
  pinned INTEGER NOT NULL DEFAULT 1,
  sort INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  query TEXT NOT NULL,
  threshold INTEGER NOT NULL DEFAULT 1,
  window_minutes INTEGER NOT NULL DEFAULT 5,
  cooldown_minutes INTEGER NOT NULL DEFAULT 30,
  recipients TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  last_fired INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS backups (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  base_id TEXT NOT NULL DEFAULT '',
  file TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  rows INTEGER NOT NULL DEFAULT 0,
  watermark INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  targets TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  username TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT ''
);
`

// OpenMeta opens (and migrates) the SQLite database. If a staged restore
// file "<path>.restore" exists it replaces the database first.
func OpenMeta(path, keyPath string) (*Meta, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path + ".restore"); err == nil {
		for _, suf := range []string{"-wal", "-shm"} {
			os.Remove(path + suf)
		}
		if err := os.Rename(path+".restore", path); err != nil {
			return nil, fmt.Errorf("apply staged restore: %w", err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	for _, sc := range []string{schema, unifiSchema, securitySchema, forwardSchema, probeSchema, notifySchema, searchSchema} {
		if _, err := db.Exec(sc); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	if err := migrateColumns(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	key, err := loadKey(keyPath)
	if err != nil {
		return nil, err
	}
	m := &Meta{DB: db, path: path, key: key}
	if err := m.reloadSources(); err != nil {
		return nil, err
	}
	if err := m.reloadDevices(); err != nil {
		return nil, err
	}
	m.seedCategories()
	return m, nil
}

// columns added after the first release; CREATE TABLE IF NOT EXISTS does not
// add them to existing databases.
var addedColumns = []struct{ table, column, def string }{
	{"users", "language", "TEXT NOT NULL DEFAULT ''"},
	{"users", "language_manual", "INTEGER NOT NULL DEFAULT 0"},
	{"devices", "firmware_alerted", "TEXT NOT NULL DEFAULT ''"},
	{"users", "sites", "TEXT NOT NULL DEFAULT ''"},
	{"users", "role_managed", "INTEGER NOT NULL DEFAULT 0"},
	{"users", "totp_secret", "TEXT NOT NULL DEFAULT ''"},
	{"users", "totp_enabled", "INTEGER NOT NULL DEFAULT 0"},
	{"users", "totp_last", "INTEGER NOT NULL DEFAULT 0"},
	{"users", "recovery_codes", "TEXT NOT NULL DEFAULT ''"},
	{"audit", "ip", "TEXT NOT NULL DEFAULT ''"},
	{"audit", "kind", "TEXT NOT NULL DEFAULT ''"},
	{"audit", "via", "TEXT NOT NULL DEFAULT ''"},
	{"alert_rules", "forward_to", "TEXT NOT NULL DEFAULT ''"},
	{"alert_rules", "channels", "TEXT NOT NULL DEFAULT ''"},
	{"alert_rules", "severity", "TEXT NOT NULL DEFAULT 'warning'"},
	{"alert_rules", "email", "INTEGER NOT NULL DEFAULT 1"},
	{"alert_rules", "condition", "TEXT NOT NULL DEFAULT 'above'"},
	{"alert_rules", "group_by", "TEXT NOT NULL DEFAULT ''"},
	{"alert_rules", "distinct_field", "TEXT NOT NULL DEFAULT ''"},
	{"alert_rules", "spike_percent", "INTEGER NOT NULL DEFAULT 200"},
	{"alert_rules", "baseline_hours", "INTEGER NOT NULL DEFAULT 24"},
	{"alert_rules", "schedule", "TEXT NOT NULL DEFAULT ''"},
}

func migrateColumns(db *sql.DB) error {
	for _, c := range addedColumns {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.column, c.def)); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadKey(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if err == nil {
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err == nil && len(k) == 32 {
			return k, nil
		}
		return nil, errors.New("invalid secret key file " + p)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(k)), 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func (m *Meta) Path() string { return m.path }

// Snapshot writes a consistent copy of the database to dst.
func (m *Meta) Snapshot(dst string) error {
	os.Remove(dst)
	_, err := m.DB.Exec(`VACUUM INTO ?`, dst)
	return err
}

// ---------------------------------------------------------------- secrets

// Encrypt protects secrets (SMTP/SSO/FTP passwords) at rest with AES-GCM.
func (m *Meta) Encrypt(plain string) string {
	if plain == "" {
		return ""
	}
	blk, _ := aes.NewCipher(m.key)
	gcm, _ := cipher.NewGCM(blk)
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return "enc:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil))
}

func (m *Meta) Decrypt(s string) string {
	if !strings.HasPrefix(s, "enc:") {
		return s
	}
	raw, err := base64.StdEncoding.DecodeString(s[4:])
	if err != nil {
		return ""
	}
	blk, _ := aes.NewCipher(m.key)
	gcm, _ := cipher.NewGCM(blk)
	if len(raw) < gcm.NonceSize() {
		return ""
	}
	out, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return ""
	}
	return string(out)
}

// ---------------------------------------------------------------- settings

func (m *Meta) GetSetting(key string, v any) error {
	var s string
	err := m.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), v)
}

func (m *Meta) SetSetting(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = m.DB.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(b))
	return err
}

// ---------------------------------------------------------------- categories

type Category struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	Icon        string `json:"icon"`
	Query       string `json:"query"`
	Pinned      bool   `json:"pinned"`
	Sort        int    `json:"sort"`
}

func (m *Meta) seedCategories() {
	var n int
	m.DB.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&n)
	var seeded bool
	m.GetSetting("categories_seeded", &seeded)
	if n > 0 || seeded {
		return
	}
	defaults := []Category{
		{Name: "Blocked traffic", Description: "All packets blocked by the firewall", Color: "#ef4444", Icon: "shield-x", Query: "category:firewall action:block"},
		{Name: "Threats (IPS)", Description: "Intrusion detection / prevention alerts", Color: "#f97316", Icon: "siren", Query: "category:ips"},
		{Name: "Wi-Fi connections", Description: "Clients connecting or disconnecting", Color: "#0ea5e9", Icon: "wifi", Query: "category:wifi"},
		{Name: "DHCP leases", Description: "Assigned IP addresses", Color: "#10b981", Icon: "network", Query: "category:dhcp event:DHCPACK"},
		{Name: "Login attempts", Description: "SSH and admin sign-ins", Color: "#a855f7", Icon: "key-round", Query: "category:auth OR category:admin"},
		{Name: "VPN", Description: "VPN tunnels and clients", Color: "#14b8a6", Icon: "lock", Query: "category:vpn"},
		{Name: "Switch ports", Description: "Link up/down and STP", Color: "#eab308", Icon: "cable", Query: "category:switch"},
		{Name: "Errors", Description: "Everything with severity 'error' or higher", Color: "#dc2626", Icon: "triangle-alert", Query: "severity:<=3"},
	}
	for i, c := range defaults {
		c.Sort = i
		c.Pinned = true
		m.SaveCategory(&c)
	}
	m.SetSetting("categories_seeded", true)
}

func (m *Meta) Categories() ([]Category, error) {
	rows, err := m.DB.Query(`SELECT id,name,description,color,icon,query,pinned,sort FROM categories ORDER BY sort,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Color, &c.Icon, &c.Query, &c.Pinned, &c.Sort); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Meta) SaveCategory(c *Category) error {
	if c.ID == 0 {
		res, err := m.DB.Exec(`INSERT INTO categories(name,description,color,icon,query,pinned,sort,created_at) VALUES(?,?,?,?,?,?,?,?)`,
			c.Name, c.Description, c.Color, c.Icon, c.Query, c.Pinned, c.Sort, time.Now().Unix())
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := m.DB.Exec(`UPDATE categories SET name=?,description=?,color=?,icon=?,query=?,pinned=?,sort=? WHERE id=?`,
		c.Name, c.Description, c.Color, c.Icon, c.Query, c.Pinned, c.Sort, c.ID)
	return err
}

func (m *Meta) DeleteCategory(id int64) error {
	_, err := m.DB.Exec(`DELETE FROM categories WHERE id=?`, id)
	return err
}

// ---------------------------------------------------------------- alert rules

// Alert rule conditions.
const (
	// CondAbove fires when at least Threshold events match in the window.
	CondAbove = "above"
	// CondBelow fires when fewer than Threshold events match, e.g. a site
	// or device that went quiet.
	CondBelow = "below"
	// CondDistinct fires when DistinctField has at least Threshold
	// different values, e.g. many destination ports from one source.
	CondDistinct = "distinct"
	// CondSpike fires when the window has SpikePercent more events than
	// the average window of the last BaselineHours (and at least Threshold).
	CondSpike = "spike"
)

// AlertSchedule limits when a rule is evaluated, in server time.
type AlertSchedule struct {
	// Days are weekdays, 0 = Sunday; empty = every day.
	Days []int `json:"days"`
	// From and To are "HH:MM"; empty = all day. From after To spans
	// midnight, e.g. 22:00 to 06:00.
	From string `json:"from"`
	To   string `json:"to"`
}

// Active reports whether t (in server time) falls in the schedule.
func (s AlertSchedule) Active(t time.Time) bool {
	if len(s.Days) > 0 && !slices.Contains(s.Days, int(t.Weekday())) {
		return false
	}
	from, okF := parseHHMM(s.From)
	to, okT := parseHHMM(s.To)
	if !okF || !okT || from == to {
		return true
	}
	now := t.Hour()*60 + t.Minute()
	if from < to {
		return now >= from && now < to
	}
	return now >= from || now < to
}

func parseHHMM(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

type AlertRule struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Query           string `json:"query"`
	Threshold       int    `json:"threshold"`
	WindowMinutes   int    `json:"window_minutes"`
	CooldownMinutes int    `json:"cooldown_minutes"`
	Recipients      string `json:"recipients"`
	Enabled         bool   `json:"enabled"`
	LastFired       int64  `json:"last_fired"`
	// Destinations are forwarding destinations that also receive the alert.
	Destinations []int64 `json:"destinations"`
	// Channels are notification channels (Teams, Slack, SMS…) for the alert.
	Channels []int64 `json:"channels"`
	// Severity is info, warning or critical; channels can skip lower ones.
	Severity string `json:"severity"`
	// Email sends the alert by e-mail to Recipients.
	Email bool `json:"email"`
	// Condition is one of the Cond* values; empty means CondAbove.
	Condition string `json:"condition"`
	// GroupBy evaluates the rule per value of a field, e.g. per source IP.
	GroupBy string `json:"group_by"`
	// DistinctField is the field counted by CondDistinct.
	DistinctField string        `json:"distinct_field"`
	SpikePercent  int           `json:"spike_percent"`
	BaselineHours int           `json:"baseline_hours"`
	Schedule      AlertSchedule `json:"schedule"`
}

// Validate normalises a rule and checks its condition.
func (a *AlertRule) Validate() error {
	if a.Threshold < 1 {
		a.Threshold = 1
	}
	if a.WindowMinutes < 1 {
		a.WindowMinutes = 5
	}
	if a.WindowMinutes > 7*24*60 {
		return errors.New("the window can be at most 7 days")
	}
	if a.SpikePercent < 1 {
		a.SpikePercent = 200
	}
	if a.BaselineHours < 1 {
		a.BaselineHours = 24
	}
	if a.BaselineHours > 7*24 {
		return errors.New("the baseline can be at most 7 days")
	}
	if a.Schedule.Days == nil {
		a.Schedule.Days = []int{}
	}
	for _, d := range a.Schedule.Days {
		if d < 0 || d > 6 {
			return errors.New("invalid weekday")
		}
	}
	for _, t := range []string{a.Schedule.From, a.Schedule.To} {
		if _, ok := parseHHMM(t); t != "" && !ok {
			return errors.New("times must look like 08:00")
		}
	}
	switch a.Condition {
	case "":
		a.Condition = CondAbove
	case CondAbove, CondBelow, CondSpike:
	case CondDistinct:
		if !AlertFieldOK(a.DistinctField) {
			return errors.New("choose the field to count distinct values of")
		}
	default:
		return errors.New("unknown condition")
	}
	if a.Condition != CondDistinct {
		a.DistinctField = ""
	}
	if a.GroupBy != "" && !AlertFieldOK(a.GroupBy) {
		return errors.New("unknown field to group by")
	}
	if a.GroupBy != "" && a.GroupBy == a.DistinctField {
		return errors.New("group by and distinct field must differ")
	}
	return nil
}

const alertCols = `id,name,query,threshold,window_minutes,cooldown_minutes,recipients,enabled,last_fired,forward_to,channels,severity,email,
	condition,group_by,distinct_field,spike_percent,baseline_hours,schedule`

func (m *Meta) AlertRules() ([]AlertRule, error) {
	rows, err := m.DB.Query(`SELECT ` + alertCols + ` FROM alert_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertRule{}
	for rows.Next() {
		var a AlertRule
		var fwd, chans, sched string
		if err := rows.Scan(&a.ID, &a.Name, &a.Query, &a.Threshold, &a.WindowMinutes, &a.CooldownMinutes, &a.Recipients, &a.Enabled, &a.LastFired, &fwd, &chans, &a.Severity, &a.Email,
			&a.Condition, &a.GroupBy, &a.DistinctField, &a.SpikePercent, &a.BaselineHours, &sched); err != nil {
			return nil, err
		}
		a.Destinations = splitIDs(fwd)
		a.Channels = splitIDs(chans)
		if sched != "" {
			json.Unmarshal([]byte(sched), &a.Schedule)
		}
		if a.Schedule.Days == nil {
			a.Schedule.Days = []int{}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (m *Meta) SaveAlertRule(a *AlertRule) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if SeverityRank(a.Severity) == 1 {
		a.Severity = SevWarning
	}
	sched := ""
	if len(a.Schedule.Days) > 0 || a.Schedule.From != "" || a.Schedule.To != "" {
		b, _ := json.Marshal(a.Schedule)
		sched = string(b)
	}
	if a.ID == 0 {
		res, err := m.DB.Exec(`INSERT INTO alert_rules(name,query,threshold,window_minutes,cooldown_minutes,recipients,enabled,forward_to,channels,severity,email,
			condition,group_by,distinct_field,spike_percent,baseline_hours,schedule,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.Name, a.Query, a.Threshold, a.WindowMinutes, a.CooldownMinutes, a.Recipients, a.Enabled, joinIDs(a.Destinations), joinIDs(a.Channels), a.Severity, a.Email,
			a.Condition, a.GroupBy, a.DistinctField, a.SpikePercent, a.BaselineHours, sched, time.Now().Unix())
		if err != nil {
			return err
		}
		a.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := m.DB.Exec(`UPDATE alert_rules SET name=?,query=?,threshold=?,window_minutes=?,cooldown_minutes=?,recipients=?,enabled=?,forward_to=?,channels=?,severity=?,email=?,
		condition=?,group_by=?,distinct_field=?,spike_percent=?,baseline_hours=?,schedule=? WHERE id=?`,
		a.Name, a.Query, a.Threshold, a.WindowMinutes, a.CooldownMinutes, a.Recipients, a.Enabled, joinIDs(a.Destinations), joinIDs(a.Channels), a.Severity, a.Email,
		a.Condition, a.GroupBy, a.DistinctField, a.SpikePercent, a.BaselineHours, sched, a.ID)
	return err
}

func (m *Meta) MarkAlertFired(id int64, t time.Time) {
	m.DB.Exec(`UPDATE alert_rules SET last_fired=? WHERE id=?`, t.Unix(), id)
}

func (m *Meta) DeleteAlertRule(id int64) error {
	_, err := m.DB.Exec(`DELETE FROM alert_rules WHERE id=?`, id)
	return err
}
