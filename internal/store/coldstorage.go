package store

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ColdStorage moves logs older than AfterDays from the local disk to S3
// compatible object storage (MinIO, Wasabi, AWS…) with ClickHouse tiered
// storage. They stay searchable; queries over old data are slower.
//
// Mode "s3" lets this server configure the S3 disk of the local ClickHouse
// (through the managed configuration directory); mode "policy" uses a
// storage policy that already exists on an external ClickHouse or cluster.
type ColdStorage struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"` // s3 | policy
	AfterDays int    `json:"after_days"`
	// S3 disk (mode s3).
	Endpoint  string `json:"endpoint"` // https://s3.eu-central-1.wasabisys.com
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	// CacheGB is the local disk cache for cold data that is read.
	CacheGB int `json:"cache_gb"`
	// Existing policy (mode policy) and its volume for cold data.
	Policy string `json:"policy"`
	Volume string `json:"volume"`
}

// Names used for the managed S3 disk and storage policy.
const (
	ColdDisk   = "uss_cold"
	ColdCache  = "uss_cold_cache"
	ColdPolicy = "uss_tiered"
	ColdVolume = "cold"
)

var identRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func (m *Meta) ColdStorage() ColdStorage {
	c := ColdStorage{Mode: "s3", AfterDays: 30, Prefix: "unify-syslog/", CacheGB: 10, Region: "us-east-1"}
	m.GetSetting("cold_storage", &c)
	c.SecretKey = m.Decrypt(c.SecretKey)
	return c
}

func (m *Meta) SaveColdStorage(c ColdStorage) error {
	if c.SecretKey == Secret {
		c.SecretKey = m.ColdStorage().SecretKey
	}
	c.SecretKey = m.Encrypt(c.SecretKey)
	return m.SetSetting("cold_storage", c)
}

func MaskColdStorage(c ColdStorage) ColdStorage {
	if c.SecretKey != "" {
		c.SecretKey = Secret
	}
	return c
}

// Validate checks the settings before they are applied.
func (c *ColdStorage) Validate(retentionDays int) error {
	if !c.Enabled {
		return nil
	}
	if c.AfterDays < 1 || c.AfterDays > 3650 {
		return errors.New("the number of days before logs move must be between 1 and 3650")
	}
	if c.AfterDays >= retentionDays {
		return fmt.Errorf("logs are deleted after %d days, so moving them after %d days does nothing", retentionDays, c.AfterDays)
	}
	switch c.Mode {
	case "s3":
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("the endpoint must be a URL such as https://s3.eu-central-1.wasabisys.com")
		}
		if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/ ") {
			return errors.New("a bucket name is required")
		}
		if c.AccessKey == "" || c.SecretKey == "" {
			return errors.New("access key and secret key are required")
		}
		if c.CacheGB < 1 || c.CacheGB > 10000 {
			return errors.New("the cache size must be between 1 and 10000 GB")
		}
	case "policy":
		if !identRe.MatchString(c.Policy) || !identRe.MatchString(c.Volume) {
			return errors.New("choose a storage policy and its cold volume")
		}
	default:
		return fmt.Errorf("unknown mode %q", c.Mode)
	}
	return nil
}

// DiskEndpoint is the S3 URL of the disk: endpoint, bucket and prefix.
func (c ColdStorage) DiskEndpoint() string {
	p := strings.Trim(c.Prefix, "/")
	u := strings.TrimRight(c.Endpoint, "/") + "/" + c.Bucket + "/"
	if p != "" {
		u += p + "/"
	}
	return u
}

// ColdTTL is the TTL rule that moves parts older than days to a volume.
func ColdTTL(days int, volume string) string {
	if days <= 0 || volume == "" {
		return ""
	}
	return fmt.Sprintf("toDateTime(ts) + INTERVAL %d DAY TO VOLUME '%s'", days, volume)
}

// ManagedXML is the ClickHouse configuration with the S3 disk, a local
// cache in front of it and the tiered policy (default disk, then S3).
//
// ClickHouse adds new disks and policies when it reloads its configuration,
// but changes to an existing disk (endpoint, keys) need a restart of the
// ClickHouse server.
func ManagedXML(c ColdStorage) []byte {
	esc := func(v string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(v))
		return b.String()
	}
	var b strings.Builder
	b.WriteString("<clickhouse>\n  <!-- Managed by UniFi Syslog Server (Administration > Storage); changes here are overwritten. -->\n")
	b.WriteString("  <storage_configuration>\n    <disks>\n")
	fmt.Fprintf(&b, "      <%s>\n        <type>s3</type>\n        <endpoint>%s</endpoint>\n", ColdDisk, esc(c.DiskEndpoint()))
	fmt.Fprintf(&b, "        <access_key_id>%s</access_key_id>\n        <secret_access_key>%s</secret_access_key>\n", esc(c.AccessKey), esc(c.SecretKey))
	if c.Region != "" {
		fmt.Fprintf(&b, "        <region>%s</region>\n", esc(c.Region))
	}
	fmt.Fprintf(&b, "        <use_environment_credentials>false</use_environment_credentials>\n        <metadata_path>/var/lib/clickhouse/disks/%s/</metadata_path>\n      </%s>\n", ColdDisk, ColdDisk)
	// The policy always has the same two volumes and the cache disk:
	// ClickHouse refuses a reloaded policy that drops a volume.
	cacheGB := max(c.CacheGB, 1)
	{
		fmt.Fprintf(&b, "      <%s>\n        <type>cache</type>\n        <disk>%s</disk>\n        <path>/var/lib/clickhouse/disks/%s/</path>\n        <max_size>%dGi</max_size>\n      </%s>\n",
			ColdCache, ColdDisk, ColdCache, cacheGB, ColdCache)
	}
	b.WriteString("    </disks>\n    <policies>\n")
	fmt.Fprintf(&b, "      <%s>\n        <volumes>\n          <default><disk>default</disk></default>\n          <%s><disk>%s</disk></%s>\n        </volumes>\n      </%s>\n",
		ColdPolicy, ColdVolume, ColdCache, ColdVolume, ColdPolicy)
	b.WriteString("    </policies>\n  </storage_configuration>\n</clickhouse>\n")
	return []byte(b.String())
}

// ManagedFile is where the managed configuration is written.
func ManagedFile(dir string) string { return filepath.Join(dir, "storage.xml") }

// ManagedAvailable tells whether this server can configure the local
// ClickHouse: the installer created the directory and linked it.
func ManagedAvailable(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".w")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// WriteManaged writes the managed configuration; nil clears it.
func WriteManaged(dir string, xmlDoc []byte) error {
	if xmlDoc == nil {
		xmlDoc = []byte("<clickhouse>\n  <!-- Managed by UniFi Syslog Server (Administration > Storage). -->\n</clickhouse>\n")
	}
	tmp := ManagedFile(dir) + ".tmp"
	if err := os.WriteFile(tmp, xmlDoc, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, ManagedFile(dir))
}

// ---------------------------------------------------------------- log store side

// Policy is a storage policy of the ClickHouse server.
type Policy struct {
	Name    string   `json:"name"`
	Volumes []Volume `json:"volumes"`
}

type Volume struct {
	Name  string   `json:"name"`
	Disks []string `json:"disks"`
}

// Policies lists the storage policies and their volumes.
func (l *Logs) Policies(ctx context.Context) ([]Policy, error) {
	rows, err := l.Conn.Query(ctx, "SELECT policy_name, volume_name, disks FROM system.storage_policies ORDER BY policy_name, volume_priority")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var p, v string
		var disks []string
		if err := rows.Scan(&p, &v, &disks); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != p {
			out = append(out, Policy{Name: p, Volumes: []Volume{}})
		}
		out[len(out)-1].Volumes = append(out[len(out)-1].Volumes, Volume{Name: v, Disks: disks})
	}
	return out, rows.Err()
}

// TableDisk is how much of the logs table is stored on one disk.
type TableDisk struct {
	Disk   string    `json:"disk"`
	Type   string    `json:"type"`
	Rows   uint64    `json:"rows"`
	Bytes  uint64    `json:"bytes"`
	Oldest time.Time `json:"oldest"`
	Newest time.Time `json:"newest"`
}

// TableDisks splits the logs table by disk.
func (l *Logs) TableDisks(ctx context.Context) ([]TableDisk, error) {
	rows, err := l.Conn.Query(ctx, `SELECT p.disk_name, any(d.type), sum(p.rows), sum(p.bytes_on_disk), min(p.min_time), max(p.max_time)
		FROM `+l.system("parts")+` AS p LEFT JOIN system.disks AS d ON d.name = p.disk_name
		WHERE p.active AND p.database = ? AND p.table = ? GROUP BY p.disk_name ORDER BY p.disk_name`, l.db, l.localName("logs"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TableDisk{}
	for rows.Next() {
		var d TableDisk
		if err := rows.Scan(&d.Disk, &d.Type, &d.Rows, &d.Bytes, &d.Oldest, &d.Newest); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// StoragePolicy is the storage policy of the logs table.
func (l *Logs) StoragePolicy(ctx context.Context) (string, error) {
	var p string
	err := l.Conn.QueryRow(ctx, "SELECT storage_policy FROM system.tables WHERE database = ? AND name = ?", l.db, l.localName("logs")).Scan(&p)
	return p, err
}

// HasDisk tells whether the server knows a disk (after a config reload).
func (l *Logs) HasDisk(ctx context.Context, name string) bool {
	var n uint64
	l.Conn.QueryRow(ctx, "SELECT count() FROM system.disks WHERE name = ?", name).Scan(&n)
	return n > 0
}

// ReloadConfig asks ClickHouse to read its configuration files again.
func (l *Logs) ReloadConfig(ctx context.Context) error {
	return l.Conn.Exec(ctx, "SYSTEM RELOAD CONFIG")
}

// SetCold switches the logs table to a storage policy (once; ClickHouse
// cannot switch back) and moves parts older than days to the volume. days
// 0 stops moving; data already moved stays where it is and readable.
func (l *Logs) SetCold(ctx context.Context, policy, volume string, days int) error {
	if policy != "" {
		if !identRe.MatchString(policy) || !identRe.MatchString(volume) {
			return errors.New("invalid storage policy or volume")
		}
		cur, err := l.StoragePolicy(ctx)
		if err != nil {
			return err
		}
		if cur != policy {
			if err := l.Conn.Exec(ctx, fmt.Sprintf("ALTER TABLE %s%s MODIFY SETTING storage_policy = '%s'", l.local("logs"), l.onCluster(), policy)); err != nil {
				return fmt.Errorf("switch storage policy: %w", err)
			}
		}
	}
	l.mu.Lock()
	l.move = ColdTTL(days, volume)
	r := l.retention
	l.mu.Unlock()
	if r.days == 0 {
		return nil
	}
	return l.SetRetention(ctx, r.days, r.perCategory)
}

// InitRetention records the retention and cold storage rule in effect at
// startup, so a later change to either keeps the other.
func (l *Logs) InitRetention(days int, perCategory map[string]int, c ColdStorage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.retention = retention{days: days, perCategory: perCategory}
	if c.Enabled {
		vol := ColdVolume
		if c.Mode == "policy" {
			vol = c.Volume
		}
		l.move = ColdTTL(c.AfterDays, vol)
	}
}
