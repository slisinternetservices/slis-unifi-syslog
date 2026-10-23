package store

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const securitySchema = `
CREATE TABLE IF NOT EXISTS api_tokens (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  read_only INTEGER NOT NULL DEFAULT 1,
  expires_at INTEGER NOT NULL DEFAULT 0,
  last_used INTEGER NOT NULL DEFAULT 0,
  last_ip TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);
`

// ---------------------------------------------------------------- settings

// SecuritySettings holds the sign-in policy and audit retention.
type SecuritySettings struct {
	// RequireTOTP makes two-factor authentication mandatory for local
	// accounts; users without it set it up at their next sign-in.
	RequireTOTP        bool `json:"require_totp"`
	AuditRetentionDays int  `json:"audit_retention_days"`
}

func (m *Meta) Security() SecuritySettings {
	s := SecuritySettings{AuditRetentionDays: 365}
	m.GetSetting("security", &s)
	return s
}

// AllowEntry is one allowed syslog sender: an IP address or a network.
type AllowEntry struct {
	CIDR string `json:"cidr"`
	Note string `json:"note"`
}

type AllowlistSettings struct {
	Enabled bool         `json:"enabled"`
	Entries []AllowEntry `json:"entries"`
}

func (m *Meta) Allowlist() AllowlistSettings {
	a := AllowlistSettings{}
	m.GetSetting("allowlist", &a)
	if a.Entries == nil {
		a.Entries = []AllowEntry{}
	}
	return a
}

// ParseAllowEntry accepts "10.0.0.1", "10.0.0.0/24" or IPv6 equivalents and
// returns the network it covers.
func ParseAllowEntry(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// ---------------------------------------------------------------- audit

type AuditEntry struct {
	ID     int64  `json:"id"`
	TS     int64  `json:"ts"`
	User   string `json:"user"`
	Action string `json:"action"`
	Detail string `json:"detail"`
	IP     string `json:"ip"`
	// Kind groups actions: auth, view, change, export or restore.
	Kind string `json:"kind"`
	// Via is "api: <token name>" for requests made with an API token.
	Via string `json:"via"`
}

// AuditKind derives the kind of an action from its name.
func AuditKind(action string) string {
	switch {
	case strings.HasPrefix(action, "login"), strings.HasPrefix(action, "logout"), strings.HasPrefix(action, "totp_"),
		strings.HasPrefix(action, "token_"), action == "password_changed":
		return "auth"
	case strings.HasSuffix(action, "_viewed"):
		return "view"
	case strings.HasPrefix(action, "export"), action == "backup_downloaded":
		return "export"
	case strings.HasPrefix(action, "restore"), action == "update_rolled_back":
		return "restore"
	}
	return "change"
}

func (m *Meta) Audit(user, action, detail string) {
	m.AddAudit(AuditEntry{User: user, Action: action, Detail: detail})
}

func (m *Meta) AddAudit(e AuditEntry) {
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	if e.Kind == "" {
		e.Kind = AuditKind(e.Action)
	}
	m.DB.Exec(`INSERT INTO audit(ts,username,action,detail,ip,kind,via) VALUES(?,?,?,?,?,?,?)`, e.TS, e.User, e.Action, e.Detail, e.IP, e.Kind, e.Via)
}

type AuditQuery struct {
	User     string
	Kind     string
	Text     string
	From, To int64 // unix seconds, 0 = open
	BeforeID int64 // keyset pagination
	Limit    int
}

func (m *Meta) AuditEntries(q AuditQuery) ([]AuditEntry, error) {
	if q.Limit <= 0 || q.Limit > 5000 {
		q.Limit = 200
	}
	w := []string{"1=1"}
	var args []any
	if q.User != "" {
		w, args = append(w, "username=? COLLATE NOCASE"), append(args, q.User)
	}
	if q.Kind != "" {
		w, args = append(w, "kind=?"), append(args, q.Kind)
	}
	if q.Text != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q.Text) + "%"
		w = append(w, `(action LIKE ? ESCAPE '\' OR detail LIKE ? ESCAPE '\' OR ip LIKE ? ESCAPE '\' OR username LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	if q.From > 0 {
		w, args = append(w, "ts>=?"), append(args, q.From)
	}
	if q.To > 0 {
		w, args = append(w, "ts<=?"), append(args, q.To)
	}
	if q.BeforeID > 0 {
		w, args = append(w, "id<?"), append(args, q.BeforeID)
	}
	rows, err := m.DB.Query(fmt.Sprintf(`SELECT id,ts,username,action,detail,ip,kind,via FROM audit WHERE %s ORDER BY id DESC LIMIT %d`, strings.Join(w, " AND "), q.Limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.User, &e.Action, &e.Detail, &e.IP, &e.Kind, &e.Via); err != nil {
			return nil, err
		}
		if e.Kind == "" {
			e.Kind = AuditKind(e.Action) // rows from before kinds were stored
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AuditUsers lists every user name that appears in the audit log.
func (m *Meta) AuditUsers() []string {
	out := []string{}
	rows, err := m.DB.Query(`SELECT DISTINCT username FROM audit ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (m *Meta) PurgeAudit(days int) {
	if days > 0 {
		m.DB.Exec(`DELETE FROM audit WHERE ts<?`, time.Now().AddDate(0, 0, -days).Unix())
	}
}

// ---------------------------------------------------------------- two-factor authentication

func (u *User) TOTPSecret(m *Meta) string { return m.Decrypt(u.totpSecret) }
func (u *User) TOTPLastStep() int64       { return u.totpLast }

// EnableTOTP stores a confirmed secret and fresh recovery codes (hashed).
func (m *Meta) EnableTOTP(id int64, secret string, step int64, recovery []string) error {
	hashes := make([]string, len(recovery))
	for i, c := range recovery {
		hashes[i] = hashToken(normalizeRecovery(c))
	}
	b, _ := json.Marshal(hashes)
	_, err := m.DB.Exec(`UPDATE users SET totp_secret=?,totp_enabled=1,totp_last=?,recovery_codes=? WHERE id=?`, m.Encrypt(secret), step, string(b), id)
	return err
}

func (m *Meta) DisableTOTP(id int64) error {
	_, err := m.DB.Exec(`UPDATE users SET totp_secret='',totp_enabled=0,totp_last=0,recovery_codes='' WHERE id=?`, id)
	return err
}

// UseTOTPStep records the last accepted time step so a code cannot be
// replayed. It fails when the step was already used.
func (m *Meta) UseTOTPStep(id, step int64) error {
	res, err := m.DB.Exec(`UPDATE users SET totp_last=? WHERE id=? AND totp_last<?`, step, id, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("code already used")
	}
	return nil
}

func normalizeRecovery(c string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// UseRecoveryCode consumes one recovery code.
func (m *Meta) UseRecoveryCode(id int64, code string) bool {
	u, err := m.UserByID(id)
	if err != nil {
		return false
	}
	h := hashToken(normalizeRecovery(code))
	l := decodeList(u.recovery)
	for i, x := range l {
		if subtle.ConstantTimeCompare([]byte(x), []byte(h)) == 1 {
			l = append(l[:i], l[i+1:]...)
			b, _ := json.Marshal(l)
			m.DB.Exec(`UPDATE users SET recovery_codes=? WHERE id=?`, string(b), id)
			return true
		}
	}
	return false
}

func (m *Meta) SetRecoveryCodes(id int64, recovery []string) error {
	hashes := make([]string, len(recovery))
	for i, c := range recovery {
		hashes[i] = hashToken(normalizeRecovery(c))
	}
	b, _ := json.Marshal(hashes)
	_, err := m.DB.Exec(`UPDATE users SET recovery_codes=? WHERE id=?`, string(b), id)
	return err
}

// NewRecoveryCodes returns n codes like "3f9a-c21b".
func NewRecoveryCodes(n int) []string {
	out := make([]string, n)
	for i := range out {
		t := RandomToken(4)
		out[i] = t[:4] + "-" + t[4:]
	}
	return out
}

// ---------------------------------------------------------------- API tokens

const TokenPrefix = "uss_"

type APIToken struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	ReadOnly  bool   `json:"read_only"`
	ExpiresAt int64  `json:"expires_at"`
	LastUsed  int64  `json:"last_used"`
	LastIP    string `json:"last_ip"`
	CreatedAt int64  `json:"created_at"`
}

// CreateToken stores a new token and returns its secret value, which is
// shown once and only kept as a hash.
func (m *Meta) CreateToken(t *APIToken) (string, error) {
	secret := TokenPrefix + RandomToken(24)
	t.Prefix = secret[:len(TokenPrefix)+6]
	t.CreatedAt = time.Now().Unix()
	res, err := m.DB.Exec(`INSERT INTO api_tokens(user_id,name,prefix,token_hash,read_only,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`,
		t.UserID, t.Name, t.Prefix, hashToken(secret), t.ReadOnly, t.ExpiresAt, t.CreatedAt)
	if err != nil {
		return "", err
	}
	t.ID, _ = res.LastInsertId()
	return secret, nil
}

// Tokens lists the tokens of one user, or of everyone when userID is 0.
func (m *Meta) Tokens(userID int64) ([]APIToken, error) {
	q := `SELECT t.id,t.user_id,u.username,t.name,t.prefix,t.read_only,t.expires_at,t.last_used,t.last_ip,t.created_at FROM api_tokens t JOIN users u ON u.id=t.user_id`
	var args []any
	if userID > 0 {
		q += ` WHERE t.user_id=?`
		args = append(args, userID)
	}
	rows, err := m.DB.Query(q+` ORDER BY t.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &t.Name, &t.Prefix, &t.ReadOnly, &t.ExpiresAt, &t.LastUsed, &t.LastIP, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (m *Meta) Token(id int64) (*APIToken, error) {
	var t APIToken
	err := m.DB.QueryRow(`SELECT t.id,t.user_id,u.username,t.name,t.prefix,t.read_only,t.expires_at,t.last_used,t.last_ip,t.created_at FROM api_tokens t JOIN users u ON u.id=t.user_id WHERE t.id=?`, id).
		Scan(&t.ID, &t.UserID, &t.Username, &t.Name, &t.Prefix, &t.ReadOnly, &t.ExpiresAt, &t.LastUsed, &t.LastIP, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

func (m *Meta) DeleteToken(id int64) error {
	_, err := m.DB.Exec(`DELETE FROM api_tokens WHERE id=?`, id)
	return err
}

// TokenUser resolves a secret token to its token and enabled, unexpired user.
func (m *Meta) TokenUser(secret string) (*APIToken, *User, error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return nil, nil, ErrNotFound
	}
	var t APIToken
	u, err := scanUserWith(m.DB.QueryRow(`SELECT t.id,t.name,t.prefix,t.read_only,t.expires_at,t.last_used,`+userColsAs("u")+`
		FROM api_tokens t JOIN users u ON u.id=t.user_id WHERE t.token_hash=?`, hashToken(secret)), &t.ID, &t.Name, &t.Prefix, &t.ReadOnly, &t.ExpiresAt, &t.LastUsed)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled || (t.ExpiresAt > 0 && t.ExpiresAt < time.Now().Unix()) {
		return nil, nil, ErrNotFound
	}
	t.UserID, t.Username = u.ID, u.Username
	return &t, u, nil
}

// TouchToken records use, at most once a minute per token.
func (m *Meta) TouchToken(t *APIToken, ip string) {
	now := time.Now().Unix()
	if now-t.LastUsed >= 60 {
		m.DB.Exec(`UPDATE api_tokens SET last_used=?,last_ip=? WHERE id=?`, now, ip, t.ID)
	}
}

// scanUserWith scans extra leading columns followed by the user columns.
func scanUserWith(r scanner, lead ...any) (*User, error) {
	var u User
	var sites string
	dst := append(lead, &u.ID, &u.Username, &u.Email, &u.Name, &u.hash, &u.Role, &u.Provider, &u.ExternalID, &u.Disabled, &u.CreatedAt, &u.LastLogin,
		&u.Language, &u.LanguageManual, &sites, &u.RoleManaged, &u.totpSecret, &u.TOTPEnabled, &u.totpLast, &u.recovery)
	if err := r.Scan(dst...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Sites = decodeSites(sites)
	u.RecoveryLeft = len(decodeList(u.recovery))
	return &u, nil
}
