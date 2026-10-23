package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/probeproto"
)

const probeSchema = `
CREATE TABLE IF NOT EXISTS probes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  site TEXT NOT NULL DEFAULT 'default',
  key_hash TEXT NOT NULL DEFAULT '',
  code_hash TEXT NOT NULL DEFAULT '',
  code_expires INTEGER NOT NULL DEFAULT 0,
  settings TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT '{}',
  version TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  remote_ip TEXT NOT NULL DEFAULT '',
  target_version TEXT NOT NULL DEFAULT '',
  update_state TEXT NOT NULL DEFAULT '',
  update_error TEXT NOT NULL DEFAULT '',
  epoch TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL DEFAULT 0,
  offline_alerted INTEGER NOT NULL DEFAULT 0,
  buffer_alerted INTEGER NOT NULL DEFAULT 0,
  enrolled_at INTEGER NOT NULL DEFAULT 0,
  last_seen INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS probes_key ON probes(key_hash) WHERE key_hash != '';
`

// Probe is a remote probe that receives syslog at a site and forwards it.
type Probe struct {
	ID       int64               `json:"id"`
	Name     string              `json:"name"`
	Site     string              `json:"site"`
	Settings probeproto.Settings `json:"settings"`
	Status   probeproto.Status   `json:"status"`
	Version  string              `json:"version"`
	Hostname string              `json:"hostname"`
	RemoteIP string              `json:"remote_ip"`
	// TargetVersion is the version the probe was asked to install.
	TargetVersion string `json:"target_version"`
	// UpdateState is "", "pending", "installed", "failed" or "rolled_back".
	UpdateState string `json:"update_state"`
	UpdateError string `json:"update_error"`
	Enrolled    bool   `json:"enrolled"`
	// CodeExpires is set while an enrollment code is open.
	CodeExpires    int64 `json:"code_expires"`
	EnrolledAt     int64 `json:"enrolled_at"`
	LastSeen       int64 `json:"last_seen"`
	CreatedAt      int64 `json:"created_at"`
	OfflineAlerted bool  `json:"-"`
	BufferAlerted  bool  `json:"-"`
	epoch          string
	seq            uint64
}

// ProbeSettings are the server-wide probe options.
type ProbeSettings struct {
	// AutoUpdate keeps every probe on the main server's version.
	AutoUpdate bool `json:"auto_update"`
	// OfflineMinutes after the last heartbeat a probe counts as offline.
	OfflineMinutes int `json:"offline_minutes"`
	// BufferAlertPercent of the buffer limit triggers a warning.
	BufferAlertPercent int `json:"buffer_alert_percent"`
	// Notify mails Recipients when a probe goes offline or its buffer fills.
	Notify     bool   `json:"notify"`
	Recipients string `json:"recipients"`
	// ServerURL is put in enrollment keys; empty means the address the
	// administrator uses, with https.
	ServerURL string `json:"server_url"`
}

func (m *Meta) ProbeSettings() ProbeSettings {
	s := ProbeSettings{AutoUpdate: true, OfflineMinutes: 5, BufferAlertPercent: 80, Notify: true}
	m.GetSetting("probes", &s)
	if s.OfflineMinutes < 1 {
		s.OfflineMinutes = 5
	}
	if s.BufferAlertPercent < 1 || s.BufferAlertPercent > 100 {
		s.BufferAlertPercent = 80
	}
	return s
}

func (m *Meta) SetProbeSettings(s ProbeSettings) error { return m.SetSetting("probes", s) }

const probeCols = `id,name,site,settings,status,version,hostname,remote_ip,target_version,update_state,update_error,key_hash!='',code_expires,enrolled_at,last_seen,created_at,offline_alerted,buffer_alerted,epoch,seq`

func scanProbe(r scanner) (*Probe, error) {
	var p Probe
	var settings, status string
	err := r.Scan(&p.ID, &p.Name, &p.Site, &settings, &status, &p.Version, &p.Hostname, &p.RemoteIP, &p.TargetVersion, &p.UpdateState, &p.UpdateError,
		&p.Enrolled, &p.CodeExpires, &p.EnrolledAt, &p.LastSeen, &p.CreatedAt, &p.OfflineAlerted, &p.BufferAlerted, &p.epoch, &p.seq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(settings), &p.Settings)
	json.Unmarshal([]byte(status), &p.Status)
	p.Settings = p.Settings.Normalize()
	if p.Site == "" {
		p.Site = DefaultSite
	}
	return &p, nil
}

func (m *Meta) Probes() ([]Probe, error) {
	rows, err := m.DB.Query(`SELECT ` + probeCols + ` FROM probes ORDER BY site, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Probe{}
	for rows.Next() {
		p, err := scanProbe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (m *Meta) Probe(id int64) (*Probe, error) {
	return scanProbe(m.DB.QueryRow(`SELECT `+probeCols+` FROM probes WHERE id=?`, id))
}

// ProbeByKey resolves a probe key to its probe.
func (m *Meta) ProbeByKey(key string) (*Probe, error) {
	if !strings.HasPrefix(key, probeproto.KeyPrefix) {
		return nil, ErrNotFound
	}
	return scanProbe(m.DB.QueryRow(`SELECT `+probeCols+` FROM probes WHERE key_hash=?`, hashToken(key)))
}

// Seq returns the epoch and the highest record number stored.
func (p *Probe) Seq() (string, uint64) { return p.epoch, p.seq }

// CreateProbe stores a new probe and returns its one-time enrollment code.
func (m *Meta) CreateProbe(p *Probe, ttl time.Duration) (string, error) {
	if p.Site == "" {
		p.Site = DefaultSite
	}
	p.Settings = p.Settings.Normalize()
	p.CreatedAt = time.Now().Unix()
	code := enrollCode()
	p.CodeExpires = time.Now().Add(ttl).Unix()
	s, _ := json.Marshal(p.Settings)
	res, err := m.DB.Exec(`INSERT INTO probes(name,site,settings,code_hash,code_expires,created_at) VALUES(?,?,?,?,?,?)`,
		p.Name, p.Site, string(s), hashToken(code), p.CodeExpires, p.CreatedAt)
	if err != nil {
		return "", err
	}
	p.ID, _ = res.LastInsertId()
	return code, nil
}

// NewProbeCode issues a fresh enrollment code for an existing probe, for
// re-installing it. The current key stays valid until the code is used.
func (m *Meta) NewProbeCode(id int64, ttl time.Duration) (string, error) {
	code := enrollCode()
	res, err := m.DB.Exec(`UPDATE probes SET code_hash=?, code_expires=? WHERE id=?`, hashToken(code), time.Now().Add(ttl).Unix(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return code, nil
}

// enrollCode is short enough to type: 4 groups of 4 characters without
// look-alikes.
func enrollCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	raw := make([]byte, 16)
	rand.Read(raw)
	var b strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(raw[i])%len(alphabet)])
	}
	return b.String()
}

// EnrollProbe exchanges a valid one-time code for a new probe key.
func (m *Meta) EnrollProbe(code, hostname, version, remoteIP string) (*Probe, string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	p, err := scanProbe(m.DB.QueryRow(`SELECT `+probeCols+` FROM probes WHERE code_hash=? AND code_hash!=''`, hashToken(code)))
	if err != nil {
		return nil, "", err
	}
	if p.CodeExpires < time.Now().Unix() {
		return nil, "", errors.New("the enrollment code has expired; create a new one on the main server")
	}
	key := probeproto.KeyPrefix + RandomToken(32)
	now := time.Now().Unix()
	// A new key starts a new record numbering.
	_, err = m.DB.Exec(`UPDATE probes SET key_hash=?, code_hash='', code_expires=0, hostname=?, version=?, remote_ip=?, enrolled_at=?, last_seen=?, epoch='', seq=0 WHERE id=?`,
		hashToken(key), hostname, version, remoteIP, now, now, p.ID)
	if err != nil {
		return nil, "", err
	}
	p.Enrolled, p.CodeExpires, p.Hostname, p.Version, p.RemoteIP, p.EnrolledAt, p.LastSeen = true, 0, hostname, version, remoteIP, now, now
	return p, key, nil
}

// UpdateProbe saves the name, site and settings.
func (m *Meta) UpdateProbe(p *Probe) error {
	if p.Site == "" {
		p.Site = DefaultSite
	}
	p.Settings = p.Settings.Normalize()
	s, _ := json.Marshal(p.Settings)
	_, err := m.DB.Exec(`UPDATE probes SET name=?, site=?, settings=? WHERE id=?`, p.Name, p.Site, string(s), p.ID)
	return err
}

func (m *Meta) DeleteProbe(id int64) error {
	_, err := m.DB.Exec(`DELETE FROM probes WHERE id=?`, id)
	return err
}

// ProbeHeartbeat stores the latest status report.
func (m *Meta) ProbeHeartbeat(id int64, st probeproto.Status, remoteIP string) error {
	b, _ := json.Marshal(st)
	_, err := m.DB.Exec(`UPDATE probes SET status=?, version=?, hostname=?, remote_ip=?, last_seen=? WHERE id=?`,
		string(b), st.Version, st.Hostname, remoteIP, time.Now().Unix(), id)
	return err
}

// SetProbeSeq records the highest stored record number of an epoch.
func (m *Meta) SetProbeSeq(id int64, epoch string, seq uint64) error {
	_, err := m.DB.Exec(`UPDATE probes SET epoch=?, seq=?, last_seen=? WHERE id=?`, epoch, seq, time.Now().Unix(), id)
	return err
}

// SetProbeTarget asks a probe to install version ("" cancels).
func (m *Meta) SetProbeTarget(id int64, version string) error {
	state := ""
	if version != "" {
		state = "pending"
	}
	_, err := m.DB.Exec(`UPDATE probes SET target_version=?, update_state=?, update_error='' WHERE id=?`, version, state, id)
	return err
}

// SetProbeUpdateResult ends an update: state "installed", "failed" or
// "rolled_back". The target is cleared so it is not offered again.
func (m *Meta) SetProbeUpdateResult(id int64, state, msg string) error {
	_, err := m.DB.Exec(`UPDATE probes SET target_version='', update_state=?, update_error=? WHERE id=?`, state, msg, id)
	return err
}

func (m *Meta) SetProbeAlerted(id int64, offline, buffer bool) error {
	_, err := m.DB.Exec(`UPDATE probes SET offline_alerted=?, buffer_alerted=? WHERE id=?`, offline, buffer, id)
	return err
}

// ProbeSites lists the sites probes are assigned to.
func (m *Meta) probeSites() []string {
	rows, err := m.DB.Query(`SELECT DISTINCT site FROM probes`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}
