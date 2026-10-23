package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       string `json:"role"` // admin | analyst | viewer
	Provider   string `json:"provider"`
	ExternalID string `json:"-"`
	Disabled   bool   `json:"disabled"`
	CreatedAt  int64  `json:"created_at"`
	LastLogin  int64  `json:"last_login"`
	// Language is the interface and e-mail language; empty means the server
	// default. LanguageManual is set once the user picks one themselves, so
	// the Entra account language no longer overrides it.
	Language       string `json:"language"`
	LanguageManual bool   `json:"language_manual"`
	// Sites limits what a non-admin sees; empty means every site.
	Sites []string `json:"sites"`
	// RoleManaged is set when Entra group mappings decide role and sites.
	RoleManaged bool `json:"role_managed"`
	TOTPEnabled bool `json:"totp_enabled"`
	// RecoveryLeft is the number of unused 2FA recovery codes.
	RecoveryLeft int `json:"recovery_left"`
	hash         string
	totpSecret   string
	totpLast     int64
	recovery     string
}

var ErrNotFound = errors.New("not found")

var (
	dummyOnce sync.Once
	dummyHash []byte
)

var userColList = []string{"id", "username", "email", "name", "password_hash", "role", "provider", "external_id", "disabled", "created_at", "last_login",
	"language", "language_manual", "sites", "role_managed", "totp_secret", "totp_enabled", "totp_last", "recovery_codes"}

var userCols = strings.Join(userColList, ",")

// userColsAs prefixes every user column with a table alias for joins.
func userColsAs(alias string) string {
	out := make([]string, len(userColList))
	for i, c := range userColList {
		out[i] = alias + "." + c
	}
	return strings.Join(out, ",")
}

type scanner interface{ Scan(...any) error }

func scanUser(r scanner) (*User, error) { return scanUserWith(r) }

func decodeList(s string) []string {
	var l []string
	if s != "" {
		json.Unmarshal([]byte(s), &l)
	}
	return l
}

func decodeSites(s string) []string {
	l := decodeList(s)
	if l == nil {
		l = []string{}
	}
	return l
}

func encodeSites(l []string) string {
	l = CleanSites(l)
	if len(l) == 0 {
		return ""
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// CleanSites trims, de-duplicates and sorts a site list.
func CleanSites(l []string) []string {
	out := []string{}
	for _, s := range l {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

func (m *Meta) UserCount() int {
	var n int
	m.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n
}

func (m *Meta) Users() ([]User, error) {
	rows, err := m.DB.Query(`SELECT ` + userCols + ` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func (m *Meta) UserByID(id int64) (*User, error) {
	return scanUser(m.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id=?`, id))
}

func (m *Meta) UserByName(name string) (*User, error) {
	return scanUser(m.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE username=?`, name))
}

func (m *Meta) UserByExternal(provider, ext string) (*User, error) {
	return scanUser(m.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE provider=? AND external_id=?`, provider, ext))
}

func (m *Meta) CheckPassword(username, password string) (*User, error) {
	u, err := m.UserByName(username)
	if err != nil {
		// Spend comparable time to avoid user enumeration.
		dummyOnce.Do(func() { dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy"), 12) })
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrNotFound
	}
	if u.Disabled || u.hash == "" || bcrypt.CompareHashAndPassword([]byte(u.hash), []byte(password)) != nil {
		return nil, ErrNotFound
	}
	return u, nil
}

func (m *Meta) CreateUser(u *User, password string) error {
	hash := ""
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
		if err != nil {
			return err
		}
		hash = string(h)
	}
	if u.Provider == "" {
		u.Provider = "local"
	}
	if u.Role == "" {
		u.Role = RoleViewer
	}
	u.CreatedAt = time.Now().Unix()
	u.Sites = CleanSites(u.Sites)
	res, err := m.DB.Exec(`INSERT INTO users(username,email,name,password_hash,role,provider,external_id,disabled,created_at,language,language_manual,sites,role_managed) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.Username, u.Email, u.Name, hash, u.Role, u.Provider, u.ExternalID, u.Disabled, u.CreatedAt, u.Language, u.LanguageManual, encodeSites(u.Sites), u.RoleManaged)
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

func (m *Meta) UpdateUser(u *User) error {
	u.Sites = CleanSites(u.Sites)
	_, err := m.DB.Exec(`UPDATE users SET email=?,name=?,role=?,disabled=?,language=?,language_manual=?,sites=?,role_managed=? WHERE id=?`,
		u.Email, u.Name, u.Role, u.Disabled, u.Language, u.LanguageManual, encodeSites(u.Sites), u.RoleManaged, u.ID)
	return err
}

// SetUserLanguage stores a user's language; manual marks a choice the user or
// an admin made, as opposed to one taken over from the Entra account.
func (m *Meta) SetUserLanguage(id int64, lang string, manual bool) error {
	_, err := m.DB.Exec(`UPDATE users SET language=?,language_manual=? WHERE id=?`, lang, manual, id)
	return err
}

// LanguageForEmail returns the language of the enabled user with this e-mail
// address, or "" when there is none or the user follows the server default.
func (m *Meta) LanguageForEmail(email string) string {
	var lang string
	m.DB.QueryRow(`SELECT language FROM users WHERE disabled=0 AND language<>'' AND (email=? COLLATE NOCASE OR username=? COLLATE NOCASE) ORDER BY last_login DESC LIMIT 1`, email, email).Scan(&lang)
	return lang
}

func (m *Meta) SetPassword(id int64, password string) error {
	h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	_, err = m.DB.Exec(`UPDATE users SET password_hash=? WHERE id=?`, string(h), id)
	return err
}

func (m *Meta) DeleteUser(id int64) error {
	_, err := m.DB.Exec(`DELETE FROM users WHERE id=?`, id)
	return err
}

func (m *Meta) TouchLogin(id int64) {
	m.DB.Exec(`UPDATE users SET last_login=? WHERE id=?`, time.Now().Unix(), id)
}

// ---------------------------------------------------------------- sessions

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func RandomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Meta) CreateSession(userID int64, ttl time.Duration) (string, error) {
	tok := RandomToken(32)
	now := time.Now()
	_, err := m.DB.Exec(`INSERT INTO sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, hashToken(tok), userID, now.Add(ttl).Unix(), now.Unix())
	return tok, err
}

func (m *Meta) SessionUser(tok string) (*User, error) {
	u, err := scanUser(m.DB.QueryRow(`SELECT `+userColsAs("u")+`
		FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.expires_at>?`, hashToken(tok), time.Now().Unix()))
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrNotFound
	}
	return u, nil
}

func (m *Meta) DeleteSession(tok string) {
	m.DB.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(tok))
}

func (m *Meta) PurgeSessions() {
	m.DB.Exec(`DELETE FROM sessions WHERE expires_at<?`, time.Now().Unix())
}
