package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const forwardSchema = `
CREATE TABLE IF NOT EXISTS forward_destinations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  filter TEXT NOT NULL DEFAULT '{}',
  settings TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS forward_stats (
  dest_id INTEGER NOT NULL,
  hour INTEGER NOT NULL,
  events INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  errors INTEGER NOT NULL DEFAULT 0,
  rejected INTEGER NOT NULL DEFAULT 0,
  dropped INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (dest_id, hour)
);
`

// Destination kinds.
const (
	FwdSentinel = "sentinel"
	FwdElastic  = "elasticsearch"
	FwdLogstash = "logstash"
	FwdSyslog   = "syslog"
	FwdWebhook  = "webhook"
	FwdSplunk   = "splunk"
)

var ForwardKinds = []string{FwdSentinel, FwdElastic, FwdLogstash, FwdSyslog, FwdWebhook, FwdSplunk}

// ForwardFilter selects the events a destination receives. All set parts
// must match (AND); within a list any value matches.
type ForwardFilter struct {
	Categories []string `json:"categories"`
	Sites      []string `json:"sites"`
	// Devices matches the hostname, MAC address or name of the sending device.
	Devices []string `json:"devices"`
	// MaxSeverity forwards events with this severity or more severe (lower
	// number); 7 (debug) forwards everything.
	MaxSeverity int `json:"max_severity"`
	// SavedSearch is the ID of a category whose query must match; 0 = none.
	SavedSearch int64 `json:"saved_search"`
	// Query is an extra filter in the search language.
	Query string `json:"query"`
}

// ForwardSettings holds the connection settings of every kind; each kind
// uses its own subset.
type ForwardSettings struct {
	// Microsoft Sentinel (Azure Monitor Logs Ingestion API).
	TenantID     string `json:"tenant_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	AuthMethod   string `json:"auth_method,omitempty"` // secret | certificate
	ClientSecret string `json:"client_secret,omitempty"`
	Certificate  string `json:"certificate,omitempty"` // PEM certificate plus private key
	Endpoint     string `json:"endpoint,omitempty"`    // logs ingestion endpoint of the DCE or DCR
	DCRID        string `json:"dcr_id,omitempty"`      // immutable ID, dcr-...
	Stream       string `json:"stream,omitempty"`
	CEFStream    string `json:"cef_stream,omitempty"`
	TableMode    string `json:"table_mode,omitempty"` // custom | split

	// Elasticsearch / OpenSearch, Splunk, webhook and Logstash over HTTP.
	URL        string `json:"url,omitempty"`
	ESAuth     string `json:"es_auth,omitempty"` // api_key | basic | none
	APIKey     string `json:"api_key,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	DataStream string `json:"data_stream,omitempty"`

	// Syslog and Logstash over TCP/UDP.
	Address   string `json:"address,omitempty"`   // host:port
	Transport string `json:"transport,omitempty"` // udp | tcp | tls | http
	Framing   string `json:"framing,omitempty"`   // octet | newline
	Format    string `json:"format,omitempty"`    // syslog: rfc5424 | cef; webhook: ecs | native

	// Splunk HTTP Event Collector.
	Token      string `json:"token,omitempty"`
	Index      string `json:"index,omitempty"`
	SourceType string `json:"sourcetype,omitempty"`

	// Webhook: extra request headers, one "Name: value" per line.
	Headers string `json:"headers,omitempty"`

	VerifyTLS bool   `json:"verify_tls"`
	CACert    string `json:"ca_cert,omitempty"` // extra trusted CA (PEM)

	// QueueMaxMB caps the on-disk queue; the oldest data is dropped beyond it.
	QueueMaxMB int `json:"queue_max_mb,omitempty"`
	// PricePerGB, when set, estimates the cost of the forwarded volume.
	PricePerGB float64 `json:"price_per_gb,omitempty"`
}

// secrets lists the settings stored encrypted and masked in the API.
func (s *ForwardSettings) secrets() []*string {
	return []*string{&s.ClientSecret, &s.Certificate, &s.APIKey, &s.Password, &s.Token, &s.Headers}
}

type ForwardDestination struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Enabled   bool            `json:"enabled"`
	Filter    ForwardFilter   `json:"filter"`
	Settings  ForwardSettings `json:"settings"`
	CreatedAt int64           `json:"created_at"`
}

// Mask replaces stored secrets with the Secret placeholder.
func (d ForwardDestination) Mask() ForwardDestination {
	for _, p := range d.Settings.secrets() {
		if *p != "" {
			*p = Secret
		}
	}
	return d
}

// KeepSecrets replaces Secret placeholders in d with the values of old.
func (d *ForwardDestination) KeepSecrets(old ForwardDestination) {
	ns, os := d.Settings.secrets(), old.Settings.secrets()
	for i, p := range ns {
		if *p == Secret {
			*p = *os[i]
		}
	}
}

const forwardCols = `id,name,kind,enabled,filter,settings,created_at`

func (m *Meta) scanForward(sc interface{ Scan(...any) error }) (ForwardDestination, error) {
	var d ForwardDestination
	var filter, settings string
	if err := sc.Scan(&d.ID, &d.Name, &d.Kind, &d.Enabled, &filter, &settings, &d.CreatedAt); err != nil {
		return d, err
	}
	d.Filter.MaxSeverity = 7
	json.Unmarshal([]byte(filter), &d.Filter)
	json.Unmarshal([]byte(settings), &d.Settings)
	for _, p := range d.Settings.secrets() {
		*p = m.Decrypt(*p)
	}
	return d, nil
}

// ForwardDestinations returns all destinations with secrets decrypted.
func (m *Meta) ForwardDestinations() ([]ForwardDestination, error) {
	rows, err := m.DB.Query(`SELECT ` + forwardCols + ` FROM forward_destinations ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ForwardDestination{}
	for rows.Next() {
		d, err := m.scanForward(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (m *Meta) ForwardDestination(id int64) (ForwardDestination, error) {
	d, err := m.scanForward(m.DB.QueryRow(`SELECT `+forwardCols+` FROM forward_destinations WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// SaveForwardDestination stores d; secrets equal to the Secret placeholder
// keep their stored value.
func (m *Meta) SaveForwardDestination(d *ForwardDestination) error {
	if d.ID != 0 {
		old, err := m.ForwardDestination(d.ID)
		if err != nil {
			return err
		}
		d.KeepSecrets(old)
	}
	s := d.Settings
	for _, p := range s.secrets() {
		*p = m.Encrypt(*p)
	}
	fb, _ := json.Marshal(d.Filter)
	sb, _ := json.Marshal(s)
	if d.ID == 0 {
		d.CreatedAt = time.Now().Unix()
		res, err := m.DB.Exec(`INSERT INTO forward_destinations(name,kind,enabled,filter,settings,created_at) VALUES(?,?,?,?,?,?)`,
			d.Name, d.Kind, d.Enabled, string(fb), string(sb), d.CreatedAt)
		if err != nil {
			return err
		}
		d.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := m.DB.Exec(`UPDATE forward_destinations SET name=?,kind=?,enabled=?,filter=?,settings=? WHERE id=?`,
		d.Name, d.Kind, d.Enabled, string(fb), string(sb), d.ID)
	return err
}

func (m *Meta) DeleteForwardDestination(id int64) error {
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM forward_destinations WHERE id=?`, `DELETE FROM forward_stats WHERE dest_id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ForwardStat is one hour of delivery counters of a destination.
type ForwardStat struct {
	DestID   int64 `json:"dest_id"`
	Hour     int64 `json:"hour"` // unix time of the start of the hour
	Events   int64 `json:"events"`
	Bytes    int64 `json:"bytes"`
	Errors   int64 `json:"errors"`
	Rejected int64 `json:"rejected"`
	Dropped  int64 `json:"dropped"`
}

// AddForwardStats adds the counters to their hour.
func (m *Meta) AddForwardStats(stats []ForwardStat) error {
	if len(stats) == 0 {
		return nil
	}
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, s := range stats {
		_, err := tx.Exec(`INSERT INTO forward_stats(dest_id,hour,events,bytes,errors,rejected,dropped) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(dest_id,hour) DO UPDATE SET events=events+excluded.events, bytes=bytes+excluded.bytes,
			errors=errors+excluded.errors, rejected=rejected+excluded.rejected, dropped=dropped+excluded.dropped`,
			s.DestID, s.Hour, s.Events, s.Bytes, s.Errors, s.Rejected, s.Dropped)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ForwardStats returns the hourly counters since t, oldest first.
func (m *Meta) ForwardStats(since time.Time) ([]ForwardStat, error) {
	rows, err := m.DB.Query(`SELECT dest_id,hour,events,bytes,errors,rejected,dropped FROM forward_stats WHERE hour>=? ORDER BY hour`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ForwardStat{}
	for rows.Next() {
		var s ForwardStat
		if err := rows.Scan(&s.DestID, &s.Hour, &s.Events, &s.Bytes, &s.Errors, &s.Rejected, &s.Dropped); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ForwardTotals returns the all-time counters per destination.
func (m *Meta) ForwardTotals() (map[int64]ForwardStat, error) {
	rows, err := m.DB.Query(`SELECT dest_id,SUM(events),SUM(bytes),SUM(errors),SUM(rejected),SUM(dropped) FROM forward_stats GROUP BY dest_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ForwardStat{}
	for rows.Next() {
		var s ForwardStat
		if err := rows.Scan(&s.DestID, &s.Events, &s.Bytes, &s.Errors, &s.Rejected, &s.Dropped); err != nil {
			return nil, err
		}
		out[s.DestID] = s
	}
	return out, rows.Err()
}

// PurgeForwardStats keeps hourly counters for 90 days; totals are kept by
// folding older hours into one row per destination at hour 0.
func (m *Meta) PurgeForwardStats() error {
	cut := time.Now().AddDate(0, 0, -90).Unix()
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	stmts := []string{
		`INSERT INTO forward_stats(dest_id,hour,events,bytes,errors,rejected,dropped)
			SELECT dest_id,0,SUM(events),SUM(bytes),SUM(errors),SUM(rejected),SUM(dropped) FROM forward_stats WHERE hour>0 AND hour<? GROUP BY dest_id
			ON CONFLICT(dest_id,hour) DO UPDATE SET events=events+excluded.events, bytes=bytes+excluded.bytes,
			errors=errors+excluded.errors, rejected=rejected+excluded.rejected, dropped=dropped+excluded.dropped`,
		`DELETE FROM forward_stats WHERE hour>0 AND hour<?`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q, cut); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func joinIDs(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}

func splitIDs(s string) []int64 {
	out := []int64{}
	for _, p := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}
