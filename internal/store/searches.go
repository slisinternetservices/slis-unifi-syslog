package store

import (
	"database/sql"
	"errors"
	"time"
)

const searchSchema = `
CREATE TABLE IF NOT EXISTS saved_searches (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  query TEXT NOT NULL DEFAULT '',
  preset TEXT NOT NULL DEFAULT '',
  range_from TEXT NOT NULL DEFAULT '',
  range_to TEXT NOT NULL DEFAULT '',
  site TEXT NOT NULL DEFAULT '',
  shared INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS saved_search_pins (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  search_id INTEGER NOT NULL REFERENCES saved_searches(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, search_id)
);
`

// SavedSearch is a search on the Logs page saved by a user. Shared searches
// are visible to everyone; each user pins the ones they want in the menu.
type SavedSearch struct {
	ID          int64  `json:"id"`
	OwnerID     int64  `json:"owner_id"`
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Query       string `json:"query"`
	// Preset is a time range preset such as "24h", or "custom" with
	// From and To (local "2006-01-02T15:04"); empty keeps the page default.
	Preset    string `json:"preset"`
	From      string `json:"from"`
	To        string `json:"to"`
	Site      string `json:"site"`
	Shared    bool   `json:"shared"`
	Pinned    bool   `json:"pinned"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const searchCols = `s.id, s.owner_id, COALESCE(u.username, ''), s.name, s.description, s.query, s.preset, s.range_from, s.range_to, s.site, s.shared,
	EXISTS(SELECT 1 FROM saved_search_pins p WHERE p.search_id = s.id AND p.user_id = ?), s.created_at, s.updated_at`

func scanSearch(sc interface{ Scan(...any) error }) (SavedSearch, error) {
	var s SavedSearch
	err := sc.Scan(&s.ID, &s.OwnerID, &s.Owner, &s.Name, &s.Description, &s.Query, &s.Preset, &s.From, &s.To, &s.Site, &s.Shared, &s.Pinned, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// SavedSearches lists the searches a user owns plus all shared ones.
func (m *Meta) SavedSearches(userID int64) ([]SavedSearch, error) {
	rows, err := m.DB.Query(`SELECT `+searchCols+` FROM saved_searches s LEFT JOIN users u ON u.id = s.owner_id
		WHERE s.owner_id = ? OR s.shared = 1 ORDER BY s.name COLLATE NOCASE, s.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedSearch{}
	for rows.Next() {
		s, err := scanSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SavedSearch returns one search as seen by userID.
func (m *Meta) SavedSearch(id, userID int64) (SavedSearch, error) {
	s, err := scanSearch(m.DB.QueryRow(`SELECT `+searchCols+` FROM saved_searches s LEFT JOIN users u ON u.id = s.owner_id WHERE s.id = ?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

func (m *Meta) SaveSearch(s *SavedSearch) error {
	now := time.Now().Unix()
	s.UpdatedAt = now
	if s.ID == 0 {
		s.CreatedAt = now
		res, err := m.DB.Exec(`INSERT INTO saved_searches(owner_id,name,description,query,preset,range_from,range_to,site,shared,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			s.OwnerID, s.Name, s.Description, s.Query, s.Preset, s.From, s.To, s.Site, s.Shared, now, now)
		if err != nil {
			return err
		}
		s.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := m.DB.Exec(`UPDATE saved_searches SET name=?,description=?,query=?,preset=?,range_from=?,range_to=?,site=?,shared=?,updated_at=? WHERE id=?`,
		s.Name, s.Description, s.Query, s.Preset, s.From, s.To, s.Site, s.Shared, now, s.ID)
	return err
}

func (m *Meta) DeleteSearch(id int64) error {
	_, err := m.DB.Exec(`DELETE FROM saved_searches WHERE id=?`, id)
	return err
}

// PinSearch adds a search to (or removes it from) a user's menu.
func (m *Meta) PinSearch(userID, searchID int64, pinned bool) error {
	if !pinned {
		_, err := m.DB.Exec(`DELETE FROM saved_search_pins WHERE user_id=? AND search_id=?`, userID, searchID)
		return err
	}
	_, err := m.DB.Exec(`INSERT OR IGNORE INTO saved_search_pins(user_id,search_id,created_at) VALUES(?,?,?)`, userID, searchID, time.Now().Unix())
	return err
}

// UnshareSearch removes other users' pins when a search stops being shared.
func (m *Meta) UnshareSearch(id, ownerID int64) {
	m.DB.Exec(`DELETE FROM saved_search_pins WHERE search_id=? AND user_id<>?`, id, ownerID)
}
