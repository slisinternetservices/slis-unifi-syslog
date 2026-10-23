package store

import (
	"context"
	"strings"
	"time"
)

// InvDevice is a UniFi device in the inventory. Devices are keyed by MAC
// because remote sites usually arrive NATed from one WAN address, so many
// devices share a source IP. Devices that never send their MAC are keyed by
// hostname and source IP.
type InvDevice struct {
	Key            string `json:"key"`
	MAC            string `json:"mac"`
	Hostname       string `json:"hostname"`
	Model          string `json:"model"`
	Version        string `json:"version"`
	PrevVersion    string `json:"prev_version"`
	VersionChanged int64  `json:"version_changed"`
	Name           string `json:"name"`
	Notes          string `json:"notes"`
	Site           string `json:"site"`
	SourceIP       string `json:"source_ip"`
	Monitored      bool   `json:"monitored"`
	Silent         bool   `json:"silent"`
	FirstSeen      int64  `json:"first_seen"`
	LastSeen       int64  `json:"last_seen"`
	// FirmwareAlerted is the version last reported as outdated by mail.
	FirmwareAlerted string `json:"-"`
	dirty           bool
}

// InventorySettings controls when a device counts as silent and who is
// mailed about it.
type InventorySettings struct {
	SilentMinutes  int    `json:"silent_minutes"`
	Notify         bool   `json:"notify"`
	FirmwareNotify bool   `json:"firmware_notify"`
	Recipients     string `json:"recipients"`
}

func (m *Meta) Inventory() InventorySettings {
	s := InventorySettings{SilentMinutes: 60}
	m.GetSetting("inventory", &s)
	if s.SilentMinutes < 5 {
		s.SilentMinutes = 5
	}
	return s
}

func DeviceKey(mac, hostname, sourceIP string) string {
	if mac != "" {
		return strings.ToLower(mac)
	}
	return "h:" + strings.ToLower(hostname) + "@" + sourceIP
}

const deviceCols = `key,mac,hostname,model,version,prev_version,version_changed,name,notes,site,source_ip,monitored,silent,first_seen,last_seen,firmware_alerted`

func (m *Meta) reloadDevices() error {
	rows, err := m.DB.Query(`SELECT ` + deviceCols + ` FROM devices`)
	if err != nil {
		return err
	}
	defer rows.Close()
	dev := map[string]InvDevice{}
	for rows.Next() {
		var d InvDevice
		if err := rows.Scan(&d.Key, &d.MAC, &d.Hostname, &d.Model, &d.Version, &d.PrevVersion, &d.VersionChanged, &d.Name, &d.Notes,
			&d.Site, &d.SourceIP, &d.Monitored, &d.Silent, &d.FirstSeen, &d.LastSeen, &d.FirmwareAlerted); err != nil {
			return err
		}
		dev[d.Key] = d
	}
	m.mu.Lock()
	m.devices = dev
	m.mu.Unlock()
	return rows.Err()
}

// TouchDevice records that a device sent a message. Like Resolve it runs on
// the hot ingest path and only touches memory.
func (m *Meta) TouchDevice(hostname, model, mac, version, site, sourceIP string, ts time.Time) {
	if hostname == "" && mac == "" {
		return
	}
	key := DeviceKey(mac, hostname, sourceIP)
	now := ts.Unix()
	m.mu.RLock()
	d, ok := m.devices[key]
	m.mu.RUnlock()
	if ok && now-d.LastSeen < 30 && (version == "" || version == d.Version) && d.Site == site {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok = m.devices[key]
	if !ok {
		d = InvDevice{Key: key, MAC: strings.ToLower(mac), FirstSeen: now, Monitored: true}
	}
	if hostname != "" {
		d.Hostname = hostname
	}
	if model != "" {
		d.Model = model
	}
	if version != "" && version != d.Version {
		if d.Version != "" {
			d.PrevVersion, d.VersionChanged = d.Version, now
		}
		d.Version = version
	}
	d.Site, d.SourceIP = site, sourceIP
	if now > d.LastSeen {
		d.LastSeen = now
	}
	d.dirty = true
	m.devices[key] = d
}

func (m *Meta) FlushDevices() error {
	m.mu.Lock()
	var dirty []InvDevice
	for k, d := range m.devices {
		if d.dirty {
			dirty = append(dirty, d)
			d.dirty = false
			m.devices[k] = d
		}
	}
	m.mu.Unlock()
	if len(dirty) == 0 {
		return nil
	}
	tx, err := m.DB.Begin()
	if err != nil {
		return err
	}
	for _, d := range dirty {
		// name, notes, monitored and silent are owned by the UI and the
		// monitor, so ingest never overwrites them.
		_, err := tx.Exec(`INSERT INTO devices(`+deviceCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(key) DO UPDATE SET hostname=excluded.hostname, model=excluded.model, version=excluded.version,
			prev_version=excluded.prev_version, version_changed=excluded.version_changed, site=excluded.site,
			source_ip=excluded.source_ip, last_seen=max(last_seen, excluded.last_seen)`,
			d.Key, d.MAC, d.Hostname, d.Model, d.Version, d.PrevVersion, d.VersionChanged, d.Name, d.Notes,
			d.Site, d.SourceIP, d.Monitored, d.Silent, d.FirstSeen, d.LastSeen, d.FirmwareAlerted)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (m *Meta) Devices() []InvDevice {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]InvDevice, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d)
	}
	return out
}

func (m *Meta) UpdateDevice(key, name, notes string, monitored bool) error {
	m.FlushDevices()
	if _, err := m.DB.Exec(`UPDATE devices SET name=?, notes=?, monitored=? WHERE key=?`, name, notes, monitored, key); err != nil {
		return err
	}
	return m.reloadDevices()
}

func (m *Meta) DeleteDevice(key string) error {
	m.FlushDevices()
	if _, err := m.DB.Exec(`DELETE FROM devices WHERE key=?`, key); err != nil {
		return err
	}
	return m.reloadDevices()
}

// SetDeviceSilent stores the monitor's verdict so a restart does not mail
// the same silent device again.
func (m *Meta) SetDeviceSilent(key string, silent bool) error {
	m.mu.Lock()
	if d, ok := m.devices[key]; ok {
		d.Silent = silent
		m.devices[key] = d
	}
	m.mu.Unlock()
	_, err := m.DB.Exec(`UPDATE devices SET silent=? WHERE key=?`, silent, key)
	return err
}

// SetFirmwareAlerted records which version was last mailed as outdated
// ("" once the device is current again).
func (m *Meta) SetFirmwareAlerted(key, version string) error {
	m.mu.Lock()
	if d, ok := m.devices[key]; ok {
		d.FirmwareAlerted = version
		m.devices[key] = d
	}
	m.mu.Unlock()
	_, err := m.DB.Exec(`UPDATE devices SET firmware_alerted=? WHERE key=?`, version, key)
	return err
}

// SeedDevices fills an empty inventory from the devices already in the log
// store, so upgrading installs do not start with an empty list.
func (m *Meta) SeedDevices(ctx context.Context, logs *Logs) error {
	var n int
	if err := m.DB.QueryRow(`SELECT count(*) FROM devices`).Scan(&n); err != nil || n > 0 {
		return err
	}
	devs, err := logs.Devices(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil {
		return err
	}
	for _, d := range devs {
		m.TouchDevice(d.Hostname, d.Model, d.MAC, d.Version, d.Site, d.SourceIP, d.LastSeen)
	}
	// Devices that were already quiet start out silent, so the first check
	// does not mail about long-gone hardware.
	cutoff := time.Now().Add(-time.Duration(m.Inventory().SilentMinutes) * time.Minute).Unix()
	m.mu.Lock()
	for k, d := range m.devices {
		if d.LastSeen < cutoff {
			d.Silent = true
			m.devices[k] = d
		}
	}
	m.mu.Unlock()
	return m.FlushDevices()
}
