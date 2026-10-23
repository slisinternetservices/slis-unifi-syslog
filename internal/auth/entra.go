// Package auth implements Microsoft Entra ID (Azure AD) single sign-on via
// OpenID Connect with PKCE.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

type Entra struct {
	Meta *store.Meta

	mu       sync.Mutex
	key      string
	provider *oidc.Provider
	pending  map[string]pending
}

type pending struct {
	verifier string
	nonce    string
	expires  time.Time
}

type Identity struct {
	Subject  string
	Username string
	Email    string
	Name     string
	Groups   []string
	Language string // preferred language of the Microsoft account, e.g. "nl-NL"
}

func (e *Entra) issuer(s store.SSOSettings) string {
	return "https://login.microsoftonline.com/" + s.TenantID + "/v2.0"
}

func (e *Entra) RedirectURL(base string) string {
	return strings.TrimRight(base, "/") + "/api/auth/entra/callback"
}

func (e *Entra) config(ctx context.Context, base string) (*oauth2.Config, *oidc.Provider, store.SSOSettings, error) {
	s := e.Meta.SSO()
	if !s.Enabled || s.TenantID == "" || s.ClientID == "" {
		return nil, nil, s, errors.New("Entra SSO is not enabled")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.provider == nil || e.key != s.TenantID {
		p, err := oidc.NewProvider(ctx, e.issuer(s))
		if err != nil {
			return nil, nil, s, fmt.Errorf("OIDC discovery: %w", err)
		}
		e.provider, e.key = p, s.TenantID
	}
	return &oauth2.Config{
		ClientID:     s.ClientID,
		ClientSecret: s.ClientSecret,
		Endpoint:     e.provider.Endpoint(),
		RedirectURL:  e.RedirectURL(base),
		Scopes:       scopes(s),
	}, e.provider, s, nil
}

func scopes(s store.SSOSettings) []string {
	sc := []string{oidc.ScopeOpenID, "profile", "email"}
	if s.SyncLanguage {
		// Lets us read preferredLanguage from Microsoft Graph.
		sc = append(sc, "User.Read")
	}
	return sc
}

// Begin returns the authorization URL and the state value to store in a cookie.
func (e *Entra) Begin(ctx context.Context, base string) (string, string, error) {
	cfg, _, _, err := e.config(ctx, base)
	if err != nil {
		return "", "", err
	}
	state := store.RandomToken(16)
	verifier := oauth2.GenerateVerifier()
	nonce := store.RandomToken(16)
	e.mu.Lock()
	if e.pending == nil {
		e.pending = map[string]pending{}
	}
	for k, v := range e.pending {
		if time.Now().After(v.expires) {
			delete(e.pending, k)
		}
	}
	e.pending[state] = pending{verifier: verifier, nonce: nonce, expires: time.Now().Add(10 * time.Minute)}
	e.mu.Unlock()
	url := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce), oauth2.SetAuthURLParam("response_mode", "query"))
	return url, state, nil
}

func (e *Entra) Finish(ctx context.Context, base, state, code string) (*Identity, error) {
	e.mu.Lock()
	p, ok := e.pending[state]
	delete(e.pending, state)
	e.mu.Unlock()
	if !ok || time.Now().After(p.expires) {
		return nil, errors.New("invalid or expired sign-in session")
	}
	cfg, prov, s, err := e.config(ctx, base)
	if err != nil {
		return nil, err
	}
	tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(p.verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("no id_token received")
	}
	idt, err := prov.Verifier(&oidc.Config{ClientID: s.ClientID}).Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("id_token: %w", err)
	}
	if idt.Nonce != p.nonce {
		return nil, errors.New("nonce mismatch")
	}
	var c struct {
		OID               string   `json:"oid"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		Groups            []string `json:"groups"`
		TID               string   `json:"tid"`
		PreferredLang     string   `json:"xms_pl"` // optional claim
	}
	if err := idt.Claims(&c); err != nil {
		return nil, err
	}
	if !strings.EqualFold(c.TID, s.TenantID) && !strings.Contains(s.TenantID, ".") {
		return nil, errors.New("wrong tenant")
	}
	id := &Identity{Subject: first(c.OID, idt.Subject), Username: first(c.PreferredUsername, c.Email), Email: first(c.Email, c.PreferredUsername), Name: c.Name, Groups: c.Groups}
	if s.SyncLanguage {
		id.Language = c.PreferredLang
		if id.Language == "" {
			id.Language = graphLanguage(ctx, cfg, tok)
		}
	}
	if s.AllowedGroup != "" && !contains(id.Groups, s.AllowedGroup) && !inMappedGroup(s, id.Groups) {
		return nil, errors.New("your account is not a member of the allowed group")
	}
	return id, nil
}

func inMappedGroup(s store.SSOSettings, groups []string) bool {
	for _, g := range s.GroupRoles {
		if contains(groups, g.GroupID) {
			return true
		}
	}
	return false
}

// Access maps group membership to a role and sites. The highest role of
// all matching groups wins; the sites of the groups with that role are
// combined, and a group without sites gives every site. Users in no mapped
// group get the default role for every site. managed is false when no
// mappings are configured: roles are then set by hand in the interface.
func (e *Entra) Access(id *Identity) (role string, sites []string, managed bool) {
	return Access(e.Meta.SSO(), id.Groups)
}

func Access(s store.SSOSettings, groups []string) (role string, sites []string, managed bool) {
	role = s.DefaultRole
	if !store.ValidRole(role) {
		role = store.RoleViewer
	}
	if len(s.GroupRoles) == 0 {
		return role, []string{}, false
	}
	var best []store.GroupRole
	for _, g := range s.GroupRoles {
		if !contains(groups, g.GroupID) || !store.ValidRole(g.Role) {
			continue
		}
		if len(best) == 0 || store.RoleRank(g.Role) > store.RoleRank(best[0].Role) {
			best = []store.GroupRole{g}
		} else if g.Role == best[0].Role {
			best = append(best, g)
		}
	}
	if len(best) == 0 {
		return role, []string{}, true
	}
	var all []string
	for _, g := range best {
		if len(g.Sites) == 0 {
			return best[0].Role, []string{}, true
		}
		all = append(all, g.Sites...)
	}
	return best[0].Role, store.CleanSites(all), true
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// graphLanguage reads the account's preferred language from Microsoft Graph.
// Failure is not fatal: the user then keeps the server default.
func graphLanguage(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := cfg.Client(ctx, tok).Get("https://graph.microsoft.com/v1.0/me?$select=preferredLanguage")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var v struct {
		PreferredLanguage string `json:"preferredLanguage"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v)
	return v.PreferredLanguage
}
