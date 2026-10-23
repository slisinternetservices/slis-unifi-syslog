package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const unifiSchema = `
CREATE TABLE IF NOT EXISTS unifi_controllers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  api_key TEXT NOT NULL DEFAULT '',
  site TEXT NOT NULL DEFAULT '',
  verify_tls INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  last_sync INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  clients INTEGER NOT NULL DEFAULT 0,
  devices INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS client_names (
  mac TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT '',
  site TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  controller_id INTEGER NOT NULL DEFAULT 0,
  updated INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS controller_devices (
  mac TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  firmware TEXT NOT NULL DEFAULT '',
  updatable INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT '',
  ip TEXT NOT NULL DEFAULT '',
  site TEXT NOT NULL DEFAULT '',
  controller_id INTEGER NOT NULL DEFAULT 0,
  updated INTEGER NOT NULL
);
`

// Controller is a UniFi Network application reached through its API key.
type Controller struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	APIKey    string `json:"api_key"`
	Site      string `json:"site"` // site in this server its clients belong to; empty = any
	VerifyTLS bool   `json:"verify_tls"`
	Enabled   bool   `json:"enabled"`
	LastSync  int64  `json:"last_sync"`
	LastError string `json:"last_error"`
	Clients   int    `json:"clients"`
	Devices   int    `json:"devices"`
}

const controllerCols = `id,name,url,api_key,site,verify_tls,enabled,last_sync,last_error,clients,devices`

// Controllers returns all controllers with their API keys decrypted.
func (m *Meta) Controllers() ([]Controller, error) {
	rows, err := m.DB.Query(`SELECT ` + controllerCols + ` FROM unifi_controllers ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Controller{}
	for rows.Next() {
		var c Controller
		if err := rows.Scan(&c.ID, &c.Name, &c.URL, &c.APIKey, &c.Site, &c.VerifyTLS, &c.Enabled, &c.LastSync, &c.LastError, &c.Clients, &c.Devices); err != nil {
			return nil, err
		}
		c.APIKey = m.Decrypt(c.APIKey)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Meta) Controller(id int64) (Controller, error) {
	var c Controller
	err := m.DB.QueryRow(`SELECT `+controllerCols+` FROM unifi_controllers WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &c.URL, &c.APIKey, &c.Site, &c.VerifyTLS, &c.Enabled, &c.LastSync, &c.LastError, &c.Clients, &c.Devices)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.APIKey = m.Decrypt(c.APIKey)
	return c, err
}

// SaveController stores c; an API key equal to the Secret placeholder keeps
// the stored one.
func (m *Meta) SaveController(c *Controller) error {
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
	if c.ID != 0 && c.APIKey == Secret {
		old, err := m.Controller(c.ID)
		if err != nil {
			return err
		}
		c.APIKey = old.APIKey
	}
	key := m.Encrypt(c.APIKey)
	if c.ID == 0 {
		res, err := m.DB.Exec(`INSERT INTO unifi_controllers(name,url,api_key,site,verify_tls,enabled,created_at) VALUES(?,?,?,?,?,?,?)`,
			c.Name, c.URL, key, c.Site, c.VerifyTLS, c.Enabled, time.Now().Unix())
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := m.DB.Exec(`UPDATE unifi_controllers SET name=?,url=?,api_key=?,site=?,verify_tls=?,enabled=? WHERE id=?`,
		c.Name, c.URL, key, c.Site, c.VerifyTLS, c.Enabled, c.ID)
	return err
}

func (m *Meta) DeleteController(id int64) error {
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM unifi_controllers WHERE id=?`, `DELETE FROM client_names WHERE controller_id=? AND source='unifi'`, `DELETE FROM controller_devices WHERE controller_id=?`} {
		if _, err := tx.Exec(q, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (m *Meta) SetControllerSync(id int64, t time.Time, syncErr string, clients, devices int) error {
	if syncErr != "" {
		_, err := m.DB.Exec(`UPDATE unifi_controllers SET last_error=? WHERE id=?`, syncErr, id)
		return err
	}
	_, err := m.DB.Exec(`UPDATE unifi_controllers SET last_sync=?, last_error='', clients=?, devices=? WHERE id=?`, t.Unix(), clients, devices, id)
	return err
}

// MaskController hides the API key for the web interface.
func MaskController(c Controller) Controller {
	if c.APIKey != "" {
		c.APIKey = Secret
	}
	return c
}

// ---------------------------------------------------------------- client names

// Name sources, in order of precedence.
const (
	NameUniFi = "unifi" // from a controller: the alias shown in UniFi Network
	NameDHCP  = "dhcp"  // learnt from the hostname in DHCP logs
)

// ClientName maps a client MAC address to a readable name.
type ClientName struct {
	MAC          string `json:"mac"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	Site         string `json:"site"`
	Kind         string `json:"kind"` // wired, wireless, vpn
	Source       string `json:"source"`
	ControllerID int64  `json:"controller_id"`
	Updated      int64  `json:"updated"`
}

func (m *Meta) ClientNames() ([]ClientName, error) {
	rows, err := m.DB.Query(`SELECT mac,name,hostname,ip,site,kind,source,controller_id,updated FROM client_names`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ClientName{}
	for rows.Next() {
		var c ClientName
		if err := rows.Scan(&c.MAC, &c.Name, &c.Hostname, &c.IP, &c.Site, &c.Kind, &c.Source, &c.ControllerID, &c.Updated); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Meta) SaveClientNames(list []ClientName) error {
	if len(list) == 0 {
		return nil
	}
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, c := range list {
		if _, err := tx.Exec(`INSERT INTO client_names(mac,name,hostname,ip,site,kind,source,controller_id,updated) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(mac) DO UPDATE SET name=excluded.name, hostname=excluded.hostname, ip=excluded.ip, site=excluded.site,
			kind=excluded.kind, source=excluded.source, controller_id=excluded.controller_id, updated=excluded.updated`,
			c.MAC, c.Name, c.Hostname, c.IP, c.Site, c.Kind, c.Source, c.ControllerID, c.Updated); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ControllerDevice is a UniFi device as the controller reports it.
type ControllerDevice struct {
	MAC          string `json:"mac"`
	Name         string `json:"name"`
	Model        string `json:"model"`
	Firmware     string `json:"firmware"`
	Updatable    bool   `json:"updatable"`
	State        string `json:"state"`
	IP           string `json:"ip"`
	Site         string `json:"site"`
	ControllerID int64  `json:"controller_id"`
	Updated      int64  `json:"updated"`
}

func (m *Meta) ControllerDevices() ([]ControllerDevice, error) {
	rows, err := m.DB.Query(`SELECT mac,name,model,firmware,updatable,state,ip,site,controller_id,updated FROM controller_devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ControllerDevice{}
	for rows.Next() {
		var d ControllerDevice
		if err := rows.Scan(&d.MAC, &d.Name, &d.Model, &d.Firmware, &d.Updatable, &d.State, &d.IP, &d.Site, &d.ControllerID, &d.Updated); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReplaceControllerDevices stores the full device list of one controller.
func (m *Meta) ReplaceControllerDevices(controllerID int64, list []ControllerDevice) error {
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM controller_devices WHERE controller_id=?`, controllerID); err != nil {
		tx.Rollback()
		return err
	}
	for _, d := range list {
		if _, err := tx.Exec(`INSERT INTO controller_devices(mac,name,model,firmware,updatable,state,ip,site,controller_id,updated) VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(mac) DO UPDATE SET name=excluded.name, model=excluded.model, firmware=excluded.firmware, updatable=excluded.updatable,
			state=excluded.state, ip=excluded.ip, site=excluded.site, controller_id=excluded.controller_id, updated=excluded.updated`,
			d.MAC, d.Name, d.Model, d.Firmware, d.Updatable, d.State, d.IP, d.Site, controllerID, d.Updated); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- GeoIP settings

type GeoIPSettings struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"` // dbip | maxmind
	AccountID  string `json:"account_id"`
	LicenseKey string `json:"license_key"`
	AutoUpdate bool   `json:"auto_update"`
}

func (m *Meta) GeoIP() GeoIPSettings {
	s := GeoIPSettings{Provider: "dbip", AutoUpdate: true}
	m.GetSetting("geoip", &s)
	s.LicenseKey = m.Decrypt(s.LicenseKey)
	return s
}

func (m *Meta) SaveGeoIP(s GeoIPSettings) error {
	if s.LicenseKey == Secret {
		s.LicenseKey = m.GeoIP().LicenseKey
	}
	s.LicenseKey = m.Encrypt(s.LicenseKey)
	return m.SetSetting("geoip", s)
}

func MaskGeoIP(s GeoIPSettings) GeoIPSettings {
	if s.LicenseKey != "" {
		s.LicenseKey = Secret
	}
	return s
}
