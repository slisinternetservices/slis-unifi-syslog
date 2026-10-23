package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/auth"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/syslogd"
)

func sitesText(sites []string) string {
	if len(sites) == 0 {
		return "all"
	}
	return strings.Join(sites, ", ")
}

// ---------------------------------------------------------------- two-factor sign-in

// loginTicket is handed out after a correct password while the second
// factor is still missing; enroll is set when the user must set one up.
type loginTicket struct {
	userID   int64
	expires  time.Time
	attempts int
	enroll   *auth.TOTPEnrollment
}

type enrollment struct {
	secret  string
	expires time.Time
}

const ticketTTL = 5 * time.Minute

func (s *Server) newTicket(userID int64, enroll *auth.TOTPEnrollment) string {
	tok := store.RandomToken(24)
	s.secMu.Lock()
	defer s.secMu.Unlock()
	if s.tickets == nil {
		s.tickets = map[string]*loginTicket{}
	}
	for k, t := range s.tickets {
		if time.Now().After(t.expires) {
			delete(s.tickets, k)
		}
	}
	s.tickets[tok] = &loginTicket{userID: userID, expires: time.Now().Add(ticketTTL), enroll: enroll}
	return tok
}

func (s *Server) loginTOTP(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.tooManyAttempts(ip) {
		jerr(w, 429, "too many failed attempts, try again in 15 minutes")
		return
	}
	var req struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	s.secMu.Lock()
	t := s.tickets[req.Ticket]
	if t != nil {
		t.attempts++
		if t.attempts > 5 || time.Now().After(t.expires) {
			delete(s.tickets, req.Ticket)
			t = nil
		}
	}
	s.secMu.Unlock()
	if t == nil {
		jerr(w, 401, "sign-in expired, please start again")
		return
	}
	u, err := s.Meta.UserByID(t.userID)
	if err != nil || u.Disabled {
		jerr(w, 401, "sign-in expired, please start again")
		return
	}
	fail := func() {
		s.recordFail(ip)
		s.Meta.AddAudit(store.AuditEntry{User: u.Username, Action: "login_totp_failed", IP: ip})
		jerr(w, 401, "incorrect code")
	}
	out := map[string]any{}
	switch {
	case t.enroll != nil:
		step, ok := auth.CheckTOTP(t.enroll.Secret, req.Code, time.Now())
		if !ok {
			fail()
			return
		}
		codes := store.NewRecoveryCodes(10)
		if err := s.Meta.EnableTOTP(u.ID, t.enroll.Secret, step, codes); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		s.Meta.AddAudit(store.AuditEntry{User: u.Username, Action: "totp_enabled", Detail: "during sign-in", IP: ip})
		out["recovery_codes"] = codes
	default:
		if step, ok := auth.CheckTOTP(u.TOTPSecret(s.Meta), req.Code, time.Now()); ok {
			if s.Meta.UseTOTPStep(u.ID, step) != nil {
				fail()
				return
			}
		} else if s.Meta.UseRecoveryCode(u.ID, req.Code) {
			s.Meta.AddAudit(store.AuditEntry{User: u.Username, Action: "totp_recovery_code_used", IP: ip})
		} else {
			fail()
			return
		}
	}
	s.secMu.Lock()
	delete(s.tickets, req.Ticket)
	s.secMu.Unlock()
	if u, err = s.Meta.UserByID(u.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if err := s.setSession(w, r, u); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.Meta.AddAudit(store.AuditEntry{User: u.Username, Action: "login", Detail: "with two-factor code", IP: ip})
	out["user"] = u
	jsonOut(w, out)
}

// ---------------------------------------------------------------- 2FA self-service

func (s *Server) myTOTP(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	jsonOut(w, map[string]any{"enabled": u.TOTPEnabled, "recovery_left": u.RecoveryLeft, "required": s.Meta.Security().RequireTOTP, "available": u.Provider == "local"})
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	if u.Provider != "local" {
		jerr(w, 400, "two-factor authentication for Entra accounts is managed in Microsoft Entra")
		return
	}
	enr, err := auth.NewTOTP(s.Meta.General().InstanceName, u.Username)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.secMu.Lock()
	if s.enrolls == nil {
		s.enrolls = map[int64]enrollment{}
	}
	s.enrolls[u.ID] = enrollment{secret: enr.Secret, expires: time.Now().Add(15 * time.Minute)}
	s.secMu.Unlock()
	jsonOut(w, enr)
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	u := user(r)
	s.secMu.Lock()
	e, ok := s.enrolls[u.ID]
	s.secMu.Unlock()
	if !ok || time.Now().After(e.expires) {
		jerr(w, 400, "setup expired, please start again")
		return
	}
	step, valid := auth.CheckTOTP(e.secret, req.Code, time.Now())
	if !valid {
		jerr(w, 400, "incorrect code")
		return
	}
	codes := store.NewRecoveryCodes(10)
	if err := s.Meta.EnableTOTP(u.ID, e.secret, step, codes); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.secMu.Lock()
	delete(s.enrolls, u.ID)
	s.secMu.Unlock()
	s.audit_(r, "totp_enabled", "")
	jsonOut(w, map[string]any{"recovery_codes": codes})
}

// checkSecondFactor verifies a current code or a recovery code.
func (s *Server) checkSecondFactor(u *store.User, code string) bool {
	if step, ok := auth.CheckTOTP(u.TOTPSecret(s.Meta), code, time.Now()); ok {
		return s.Meta.UseTOTPStep(u.ID, step) == nil
	}
	return s.Meta.UseRecoveryCode(u.ID, code)
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	u := user(r)
	if s.Meta.Security().RequireTOTP {
		jerr(w, 400, "two-factor authentication is required for local accounts")
		return
	}
	if _, err := s.Meta.CheckPassword(u.Username, req.Password); err != nil {
		jerr(w, 400, "current password is incorrect")
		return
	}
	if !s.checkSecondFactor(u, req.Code) {
		jerr(w, 400, "incorrect code")
		return
	}
	if err := s.Meta.DisableTOTP(u.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "totp_disabled", "")
	jsonOut(w, map[string]bool{"ok": true})
}

func (s *Server) totpRecovery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	u := user(r)
	if !u.TOTPEnabled {
		jerr(w, 400, "two-factor authentication is not enabled")
		return
	}
	if step, ok := auth.CheckTOTP(u.TOTPSecret(s.Meta), req.Code, time.Now()); !ok || s.Meta.UseTOTPStep(u.ID, step) != nil {
		jerr(w, 400, "incorrect code")
		return
	}
	codes := store.NewRecoveryCodes(10)
	if err := s.Meta.SetRecoveryCodes(u.ID, codes); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "totp_recovery_codes_renewed", "")
	jsonOut(w, map[string]any{"recovery_codes": codes})
}

func (s *Server) resetUserTOTP(w http.ResponseWriter, r *http.Request) {
	u, err := s.Meta.UserByID(idParam(r))
	if err != nil {
		jerr(w, 404, "not found")
		return
	}
	if err := s.Meta.DisableTOTP(u.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "totp_reset", u.Username)
	jsonOut(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- API tokens

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	id := u.ID
	if r.URL.Query().Get("all") == "1" && u.IsAdmin() {
		id = 0
	}
	t, err := s.Meta.Tokens(id)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	jsonOut(w, t)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"read_only"`
		Days     int    `json:"days"` // 0 = never expires
	}
	if err := decode(r, &req); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 80 {
		jerr(w, 400, "a name is required")
		return
	}
	if req.Days < 0 || req.Days > 3650 {
		jerr(w, 400, "expiry must be between 0 and 3650 days")
		return
	}
	t := store.APIToken{UserID: user(r).ID, Name: req.Name, ReadOnly: req.ReadOnly}
	if req.Days > 0 {
		t.ExpiresAt = time.Now().AddDate(0, 0, req.Days).Unix()
	}
	secret, err := s.Meta.CreateToken(&t)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	mode := "read and write"
	if t.ReadOnly {
		mode = "read-only"
	}
	s.audit_(r, "token_created", fmt.Sprintf("%s (%s, %s)", t.Name, t.Prefix, mode))
	jsonOut(w, map[string]any{"token": t, "secret": secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	t, err := s.Meta.Token(idParam(r))
	u := user(r)
	if err != nil || (t.UserID != u.ID && !u.IsAdmin()) {
		jerr(w, 404, "not found")
		return
	}
	if err := s.Meta.DeleteToken(t.ID); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.audit_(r, "token_revoked", fmt.Sprintf("%s (%s) of %s", t.Name, t.Prefix, t.Username))
	jsonOut(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- security settings

func (s *Server) getSecurity(w http.ResponseWriter, r *http.Request) {
	users, _ := s.Meta.Users()
	without := 0
	for _, u := range users {
		if u.Provider == "local" && !u.Disabled && !u.TOTPEnabled {
			without++
		}
	}
	jsonOut(w, map[string]any{"settings": s.Meta.Security(), "local_without_totp": without})
}

func (s *Server) putSecurity(w http.ResponseWriter, r *http.Request) {
	var v store.SecuritySettings
	if err := decode(r, &v); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	if v.AuditRetentionDays < 30 || v.AuditRetentionDays > 3650 {
		jerr(w, 400, "audit retention must be between 30 and 3650 days")
		return
	}
	old := s.Meta.Security()
	if err := s.Meta.SetSetting("security", v); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	detail := fmt.Sprintf("2FA required: %t, audit retention: %d days", v.RequireTOTP, v.AuditRetentionDays)
	if old != v {
		s.audit_(r, "settings_security", detail)
	}
	jsonOut(w, v)
}

func (s *Server) getAllowlist(w http.ResponseWriter, r *http.Request) {
	rej := []syslogd.RejectedSender{}
	var total uint64
	if s.Filter != nil {
		rej = s.Filter.RecentRejected()
		total = s.Filter.Rejected.Load()
	}
	jsonOut(w, map[string]any{"settings": s.Meta.Allowlist(), "rejected": rej, "rejected_total": total, "client_ip": clientIP(r)})
}

func (s *Server) putAllowlist(w http.ResponseWriter, r *http.Request) {
	var v store.AllowlistSettings
	if err := decode(r, &v); err != nil {
		jerr(w, 400, "invalid request")
		return
	}
	entries := []store.AllowEntry{}
	for _, e := range v.Entries {
		if strings.TrimSpace(e.CIDR) == "" {
			continue
		}
		p, err := store.ParseAllowEntry(e.CIDR)
		if err != nil {
			jerr(w, 400, "invalid address or network: "+e.CIDR)
			return
		}
		e.CIDR, e.Note = p.String(), strings.TrimSpace(e.Note)
		if p.IsSingleIP() {
			e.CIDR = p.Addr().String()
		}
		if !slices.ContainsFunc(entries, func(x store.AllowEntry) bool { return x.CIDR == e.CIDR }) {
			entries = append(entries, e)
		}
	}
	v.Entries = entries
	if v.Enabled && len(entries) == 0 {
		jerr(w, 400, "add at least one address or network before enabling the allowlist")
		return
	}
	if err := s.Meta.SetSetting("allowlist", v); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	if err := ApplyAllowlist(s.Filter, v); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	s.Filter.Forget()
	s.audit_(r, "settings_allowlist", fmt.Sprintf("enabled: %t, %d entries", v.Enabled, len(entries)))
	jsonOut(w, v)
}

// ApplyAllowlist loads allowlist settings into the syslog filter.
func ApplyAllowlist(f *syslogd.Filter, v store.AllowlistSettings) error {
	if f == nil {
		return nil
	}
	nets, err := allowNets(v)
	if err != nil {
		return err
	}
	f.Set(v.Enabled && len(nets) > 0, nets)
	return nil
}

func allowNets(v store.AllowlistSettings) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(v.Entries))
	for _, e := range v.Entries {
		p, err := store.ParseAllowEntry(e.CIDR)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ---------------------------------------------------------------- audit log

func auditQuery(r *http.Request) store.AuditQuery {
	q := r.URL.Query()
	a := store.AuditQuery{User: q.Get("user"), Kind: q.Get("kind"), Text: strings.TrimSpace(q.Get("q"))}
	a.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	a.Limit, _ = strconv.Atoi(q.Get("limit"))
	if t, err := time.Parse(time.RFC3339, q.Get("from")); err == nil {
		a.From = t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, q.Get("to")); err == nil {
		a.To = t.Unix()
	}
	return a
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	q := auditQuery(r)
	rows, err := s.Meta.AuditEntries(q)
	if err != nil {
		jerr(w, 500, err.Error())
		return
	}
	out := map[string]any{"rows": rows, "users": s.Meta.AuditUsers()}
	if q.Limit <= 0 {
		q.Limit = 200
	}
	if len(rows) == q.Limit {
		out["next"] = rows[len(rows)-1].ID
	}
	jsonOut(w, out)
}

func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	q := auditQuery(r)
	q.Limit = 5000
	s.audit_(r, "export_audit", "")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="unify-syslog-audit-`+time.Now().Format("20060102-150405")+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"time", "user", "kind", "action", "detail", "ip", "via"})
	loc := time.Local
	for n := 0; n < 100000; {
		rows, err := s.Meta.AuditEntries(q)
		if err != nil || len(rows) == 0 {
			break
		}
		for _, e := range rows {
			cw.Write([]string{time.Unix(e.TS, 0).In(loc).Format(time.RFC3339), e.User, e.Kind, e.Action, e.Detail, e.IP, e.Via})
		}
		n += len(rows)
		if len(rows) < q.Limit {
			break
		}
		q.BeforeID = rows[len(rows)-1].ID
	}
	cw.Flush()
}
