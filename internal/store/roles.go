package store

import "slices"

// Roles, from most to least privileged.
const (
	RoleAdmin   = "admin"   // everything
	RoleAnalyst = "analyst" // viewer plus exports and shared categories
	RoleViewer  = "viewer"  // read-only
)

func ValidRole(r string) bool { return r == RoleAdmin || r == RoleAnalyst || r == RoleViewer }

// RoleRank orders roles so the highest of several group roles can win.
func RoleRank(r string) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleAnalyst:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// CanExport covers log exports (CSV/NDJSON) through the UI and the API.
func (u *User) CanExport() bool { return u.Role == RoleAdmin || u.Role == RoleAnalyst }

// CanManageCategories covers the shared categories (saved searches).
func (u *User) CanManageCategories() bool { return u.Role == RoleAdmin || u.Role == RoleAnalyst }

// AllSites reports whether the user may see every site. Administrators
// always can; other users when no sites are assigned.
func (u *User) AllSites() bool { return u.IsAdmin() || len(u.Sites) == 0 }

func (u *User) SiteAllowed(site string) bool {
	if u.AllSites() {
		return true
	}
	if site == "" {
		site = DefaultSite
	}
	return slices.Contains(u.Sites, site)
}

// NoSite matches no row; used when a restricted user asks only for sites
// they may not see.
const NoSite = "\x00"

// ScopeSites narrows a requested site filter to what the user may see. An
// empty request means "all sites I may see".
func (u *User) ScopeSites(req []string) []string {
	if u.AllSites() {
		return req
	}
	if len(req) == 0 {
		return slices.Clone(u.Sites)
	}
	var out []string
	for _, s := range req {
		if slices.Contains(u.Sites, s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return []string{NoSite}
	}
	return out
}
