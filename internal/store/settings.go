package store

import (
	"strings"
	"time"
)

// Secret is the placeholder the API returns instead of stored secrets.
const Secret = "••••••••"

type GeneralSettings struct {
	Timezone      string `json:"timezone"`
	RetentionDays int    `json:"retention_days"`
	BaseURL       string `json:"base_url"`
	InstanceName  string `json:"instance_name"`
	Language      string `json:"language"` // default interface and e-mail language
	// CategoryRetention keeps logs of some categories shorter or longer
	// than RetentionDays, e.g. {"wifi": 30, "ips": 365}.
	CategoryRetention map[string]int `json:"category_retention"`
}

type SSOSettings struct {
	Enabled      bool   `json:"enabled"`
	SyncLanguage bool   `json:"sync_language"` // take the language of the Microsoft account
	TenantID     string `json:"tenant_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// AdminGroupID is the single admin group of earlier versions. It is
	// still written (the first admin mapping) so a rollback keeps working.
	AdminGroupID  string `json:"admin_group_id"`
	AllowedGroup  string `json:"allowed_group_id"`
	AutoProvision bool   `json:"auto_provision"`
	DefaultRole   string `json:"default_role"`
	ButtonLabel   string `json:"button_label"`
	// GroupRoles assigns roles and sites from Entra group membership. When
	// set, it decides role and sites of Entra users on every sign-in.
	GroupRoles []GroupRole `json:"group_roles"`
}

// GroupRole maps one Entra group (object ID) to a role and sites.
type GroupRole struct {
	GroupID string   `json:"group_id"`
	Name    string   `json:"name"` // label for the interface only
	Role    string   `json:"role"`
	Sites   []string `json:"sites"` // empty = all sites
}

type SMTPSettings struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"` // none | starttls | starttls-optional | tls
	Auth        string `json:"auth"`     // none | auto | plain | login | cram-md5 | scram-sha-256 | scram-sha-1 | xoauth2
	Username    string `json:"username"`
	Password    string `json:"password"`
	FromAddress string `json:"from_address"`
	FromName    string `json:"from_name"`
	HELO        string `json:"helo"`
	SkipVerify  bool   `json:"skip_verify"`
	MinTLS      string `json:"min_tls"` // 1.2 | 1.3
	// Microsoft 365 / Entra OAuth2 client credentials for XOAUTH2.
	OAuthTenant   string `json:"oauth_tenant"`
	OAuthClientID string `json:"oauth_client_id"`
	OAuthSecret   string `json:"oauth_secret"`
	OAuthScope    string `json:"oauth_scope"`
	DefaultTo     string `json:"default_to"`
}

type BackupTarget struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // ftp | ftps | ftps-implicit | scp | sftp | s3 | local
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	HostKey    string `json:"host_key"` // optional SHA256 fingerprint for scp and sftp
	Path       string `json:"path"`     // directory, or the key prefix for s3
	SkipVerify bool   `json:"skip_verify"`
	Enabled    bool   `json:"enabled"`
	// S3 compatible storage (AWS, MinIO, Wasabi…): Username is the access
	// key and Password the secret key.
	Endpoint string `json:"endpoint"` // https://s3.eu-central-1.wasabisys.com
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
}

type BackupSettings struct {
	Enabled          bool           `json:"enabled"`
	Time             string         `json:"time"`              // HH:MM local time for the daily run
	FullEveryDays    int            `json:"full_every_days"`   // full backup cadence; incrementals in between
	IncrementalHours int            `json:"incremental_hours"` // extra incrementals during the day (0 = only daily)
	KeepChains       int            `json:"keep_chains"`       // number of full backups (with their incrementals) to keep
	IncludeLogs      bool           `json:"include_logs"`      // include ClickHouse log data, not only configuration
	NotifyTo         string         `json:"notify_to"`
	NotifyOn         string         `json:"notify_on"` // never | failure | always
	Targets          []BackupTarget `json:"targets"`
	// Encrypt protects new backups with Passphrase (age, scrypt + ChaCha20-
	// Poly1305); restoring them needs the same passphrase.
	Encrypt    bool   `json:"encrypt"`
	Passphrase string `json:"passphrase"`
}

type UpdateSettings struct {
	AutoCheck bool `json:"auto_check"`
	// Channel is "stable" (production releases) or "test" (pre-releases).
	Channel string `json:"channel"`
	// Prerelease is the setting Channel replaced; still read for old databases.
	Prerelease  bool      `json:"prerelease,omitempty"`
	LastCheck   time.Time `json:"last_check"`
	LastVersion string    `json:"last_version"`
}

func (m *Meta) General() GeneralSettings {
	g := GeneralSettings{Timezone: "Europe/Amsterdam", RetentionDays: 90, InstanceName: "SLIS UniFi Syslog", Language: "en"}
	m.GetSetting("general", &g)
	if g.CategoryRetention == nil {
		g.CategoryRetention = map[string]int{}
	}
	return g
}

func (m *Meta) SSO() SSOSettings {
	s := SSOSettings{DefaultRole: "viewer", AutoProvision: true, ButtonLabel: "Sign in with Microsoft", SyncLanguage: true}
	m.GetSetting("sso", &s)
	s.ClientSecret = m.Decrypt(s.ClientSecret)
	if len(s.GroupRoles) == 0 && s.AdminGroupID != "" {
		s.GroupRoles = []GroupRole{{GroupID: s.AdminGroupID, Name: "Administrators", Role: RoleAdmin, Sites: []string{}}}
	}
	if s.GroupRoles == nil {
		s.GroupRoles = []GroupRole{}
	}
	return s
}

func (m *Meta) SaveSSO(s SSOSettings) error {
	if s.ClientSecret == Secret {
		s.ClientSecret = m.SSO().ClientSecret
	}
	s.ClientSecret = m.Encrypt(s.ClientSecret)
	s.AdminGroupID = ""
	groups := []GroupRole{}
	for _, g := range s.GroupRoles {
		g.GroupID = strings.TrimSpace(g.GroupID)
		if g.GroupID == "" {
			continue
		}
		g.Sites = CleanSites(g.Sites)
		if g.Role == RoleAdmin {
			g.Sites = []string{}
			if s.AdminGroupID == "" {
				s.AdminGroupID = g.GroupID
			}
		}
		groups = append(groups, g)
	}
	s.GroupRoles = groups
	return m.SetSetting("sso", s)
}

func (m *Meta) SMTP() SMTPSettings {
	s := SMTPSettings{Port: 587, Security: "starttls", Auth: "auto", MinTLS: "1.2", FromName: "SLIS Internet Services"}
	m.GetSetting("smtp", &s)
	s.Password = m.Decrypt(s.Password)
	s.OAuthSecret = m.Decrypt(s.OAuthSecret)
	return s
}

func (m *Meta) SaveSMTP(s SMTPSettings) error {
	old := m.SMTP()
	if s.Password == Secret {
		s.Password = old.Password
	}
	if s.OAuthSecret == Secret {
		s.OAuthSecret = old.OAuthSecret
	}
	s.Password = m.Encrypt(s.Password)
	s.OAuthSecret = m.Encrypt(s.OAuthSecret)
	return m.SetSetting("smtp", s)
}

func (m *Meta) Backup() BackupSettings {
	b := BackupSettings{Time: "02:30", FullEveryDays: 7, KeepChains: 4, IncludeLogs: true, NotifyOn: "failure"}
	m.GetSetting("backup", &b)
	for i := range b.Targets {
		b.Targets[i].Password = m.Decrypt(b.Targets[i].Password)
		b.Targets[i].PrivateKey = m.Decrypt(b.Targets[i].PrivateKey)
	}
	b.Passphrase = m.Decrypt(b.Passphrase)
	if b.Targets == nil {
		b.Targets = []BackupTarget{}
	}
	return b
}

func (m *Meta) SaveBackup(b BackupSettings) error {
	cur := m.Backup()
	old := map[string]BackupTarget{}
	for _, t := range cur.Targets {
		old[t.ID] = t
	}
	if b.Passphrase == Secret {
		b.Passphrase = cur.Passphrase
	}
	b.Passphrase = m.Encrypt(b.Passphrase)
	for i := range b.Targets {
		t := &b.Targets[i]
		if t.ID == "" {
			t.ID = RandomToken(6)
		}
		if t.Password == Secret {
			t.Password = old[t.ID].Password
		}
		if t.PrivateKey == Secret {
			t.PrivateKey = old[t.ID].PrivateKey
		}
		t.Password = m.Encrypt(t.Password)
		t.PrivateKey = m.Encrypt(t.PrivateKey)
	}
	return m.SetSetting("backup", b)
}

func (m *Meta) Update() UpdateSettings {
	u := UpdateSettings{AutoCheck: true}
	m.GetSetting("update", &u)
	if u.Channel == "" {
		u.Channel = "stable"
		if u.Prerelease {
			u.Channel = "test"
		}
	}
	u.Prerelease = false
	return u
}

// Masked copies for API responses.
func MaskSSO(s SSOSettings) SSOSettings {
	if s.ClientSecret != "" {
		s.ClientSecret = Secret
	}
	return s
}

func MaskSMTP(s SMTPSettings) SMTPSettings {
	if s.Password != "" {
		s.Password = Secret
	}
	if s.OAuthSecret != "" {
		s.OAuthSecret = Secret
	}
	return s
}

func MaskBackup(b BackupSettings) BackupSettings {
	t := make([]BackupTarget, len(b.Targets))
	copy(t, b.Targets)
	for i := range t {
		if t[i].Password != "" {
			t[i].Password = Secret
		}
		if t[i].PrivateKey != "" {
			t[i].PrivateKey = Secret
		}
	}
	b.Targets = t
	if b.Passphrase != "" {
		b.Passphrase = Secret
	}
	return b
}

// ---------------------------------------------------------------- backup records

type BackupRecord struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // full | incremental
	BaseID    string `json:"base_id"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	Rows      int64  `json:"rows"`
	Watermark int64  `json:"watermark"` // unix ms of max(received) included
	Status    string `json:"status"`
	Message   string `json:"message"`
	Targets   string `json:"targets"`
	CreatedAt int64  `json:"created_at"`
}

func (m *Meta) SaveBackupRecord(r *BackupRecord) error {
	_, err := m.DB.Exec(`INSERT INTO backups(id,kind,base_id,file,size,rows,watermark,status,message,targets,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET file=excluded.file, size=excluded.size, rows=excluded.rows, watermark=excluded.watermark, status=excluded.status, message=excluded.message, targets=excluded.targets`,
		r.ID, r.Kind, r.BaseID, r.File, r.Size, r.Rows, r.Watermark, r.Status, r.Message, r.Targets, r.CreatedAt)
	return err
}

func (m *Meta) BackupRecords(limit int) ([]BackupRecord, error) {
	rows, err := m.DB.Query(`SELECT id,kind,base_id,file,size,rows,watermark,status,message,targets,created_at FROM backups ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupRecord{}
	for rows.Next() {
		var r BackupRecord
		if err := rows.Scan(&r.ID, &r.Kind, &r.BaseID, &r.File, &r.Size, &r.Rows, &r.Watermark, &r.Status, &r.Message, &r.Targets, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (m *Meta) DeleteBackupRecord(id string) {
	m.DB.Exec(`DELETE FROM backups WHERE id=?`, id)
}
