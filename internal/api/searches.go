package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/query"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// Time range presets of the Logs page.
var searchPresets = []string{"", "15m", "1h", "6h", "24h", "7d", "30d", "90d", "custom"}

type searchView struct {
	store.SavedSearch
	Mine    bool `json:"mine"`
	CanEdit bool `json:"can_edit"`
}

func viewSearch(u *store.User, s store.SavedSearch) searchView {
	return searchView{SavedSearch: s, Mine: s.OwnerID == u.ID, CanEdit: s.OwnerID == u.ID || u.IsAdmin()}
}

func (s *Server) listSearches(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	list, err := s.Meta.SavedSearches(u.ID)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	out := make([]searchView, 0, len(list))
	for _, x := range list {
		out = append(out, viewSearch(u, x))
	}
	jsonOut(w, out)
}

// editableSearch loads the search in the URL when the user may change it.
func (s *Server) editableSearch(w http.ResponseWriter, r *http.Request) (store.SavedSearch, bool) {
	u := user(r)
	x, err := s.Meta.SavedSearch(idParam(r), u.ID)
	if err != nil || (!x.Shared && x.OwnerID != u.ID) {
		jerr(w, 404, "not found")
		return x, false
	}
	if x.OwnerID != u.ID && !u.IsAdmin() {
		jerr(w, 403, "only the owner or an administrator can change this search")
		return x, false
	}
	return x, true
}

func (s *Server) saveSearch(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	// Pinned is the caller's own menu shortcut; left out, it stays as is.
	var in struct {
		store.SavedSearch
		Pinned *bool `json:"pinned"`
	}
	if err := decode(r, &in); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	b := in.SavedSearch
	b.Name, b.Query = strings.TrimSpace(b.Name), strings.TrimSpace(b.Query)
	if b.Name == "" || len(b.Name) > 100 {
		jerr(w, 400, "name is required (at most 100 characters)")
		return
	}
	if _, err := query.Compile(b.Query); err != nil {
		jerr(w, 400, "filter: "+err.Error())
		return
	}
	if !slices.Contains(searchPresets, b.Preset) {
		jerr(w, 400, "unknown time range")
		return
	}
	if b.Preset == "custom" {
		for _, t := range []string{b.From, b.To} {
			if _, err := time.Parse("2006-01-02T15:04", t); err != nil {
				jerr(w, 400, "custom time range needs a start and end")
				return
			}
		}
	} else {
		b.From, b.To = "", ""
	}
	wasShared := false
	if id := idParam(r); id > 0 {
		old, ok := s.editableSearch(w, r)
		if !ok {
			return
		}
		b.ID, b.OwnerID, wasShared = old.ID, old.OwnerID, old.Shared
	} else {
		b.OwnerID = u.ID
	}
	// Sharing (and unsharing) needs the analyst role.
	if b.Shared != wasShared && !u.CanManageCategories() {
		jerr(w, 403, "sharing searches needs the analyst or administrator role")
		return
	}
	if err := s.Meta.SaveSearch(&b); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if wasShared && !b.Shared {
		s.Meta.UnshareSearch(b.ID, b.OwnerID)
	}
	if in.Pinned != nil {
		s.Meta.PinSearch(u.ID, b.ID, *in.Pinned)
	}
	s.audit_(r, "search_saved", b.Name)
	x, err := s.Meta.SavedSearch(b.ID, u.ID)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	jsonOut(w, viewSearch(u, x))
}

func (s *Server) deleteSearch(w http.ResponseWriter, r *http.Request) {
	x, ok := s.editableSearch(w, r)
	if !ok {
		return
	}
	if err := s.Meta.DeleteSearch(x.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "search_deleted", x.Name)
	jsonOut(w, map[string]bool{"ok": true})
}

// pinSearch adds a search the user can see to their own menu, or removes it.
func (s *Server) pinSearch(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	var b struct {
		Pinned bool `json:"pinned"`
	}
	if err := decode(r, &b); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	x, err := s.Meta.SavedSearch(idParam(r), u.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !x.Shared && x.OwnerID != u.ID) {
		jerr(w, 404, "not found")
		return
	}
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if err := s.Meta.PinSearch(u.ID, x.ID, b.Pinned); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	x.Pinned = b.Pinned
	jsonOut(w, viewSearch(u, x))
}
