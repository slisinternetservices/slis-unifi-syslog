// Package mail sends notifications through a user-configured SMTP server.
// Supports implicit TLS (465), STARTTLS (mandatory or opportunistic), TLS
// 1.2/1.3 minimums, PLAIN/LOGIN/CRAM-MD5/SCRAM-SHA-1/SCRAM-SHA-256 and
// XOAUTH2 with Microsoft 365 client-credentials tokens.
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/rhisijmuiden/unify-syslog-server/internal/i18n"
	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

type Mailer struct {
	Meta *store.Meta
}

func (m *Mailer) Configured() bool {
	s := m.Meta.SMTP()
	return s.Enabled && s.Host != "" && s.FromAddress != ""
}

// Brand is the identity shown in the header and footer of every e-mail.
type Brand struct {
	Instance    string
	Company     string
	Logo        []byte // PNG; SVG is not rendered by most mail clients
	LogoOnLight bool   // the logo is the light-mode variant and needs a white backdrop
}

func (m *Mailer) Brand() Brand {
	b := Brand{Instance: m.Meta.General().InstanceName, Company: m.Meta.Branding().CompanyName}
	if a, err := m.Meta.BrandingAsset("logo_light"); err == nil && a.Type == "image/png" {
		b.Logo, b.LogoOnLight = a.Data, true
	} else if a, err := m.Meta.BrandingAsset("logo_dark"); err == nil && a.Type == "image/png" {
		b.Logo = a.Data
	}
	return b
}

// Compose renders a message in the language of its recipients.
type Compose func(t i18n.Translator) (subject, body string)

// Send delivers a message, rendered once per recipient language: recipients
// that belong to a user get that user's language, everyone else the server
// default. body is plain text; an HTML alternative with the app's styling is
// generated from it.
func (m *Mailer) Send(ctx context.Context, to []string, compose Compose) error {
	s := m.Meta.SMTP()
	rcpt := Recipients(to, s.DefaultTo)
	if len(rcpt) == 0 {
		return fmt.Errorf("no recipients specified")
	}
	def := m.Meta.General().Language
	groups := map[string][]string{}
	var order []string
	for _, r := range rcpt {
		lang := m.Meta.LanguageForEmail(r)
		if lang == "" {
			lang = def
		}
		if _, ok := groups[lang]; !ok {
			order = append(order, lang)
		}
		groups[lang] = append(groups[lang], r)
	}
	brand := m.Brand()
	var errs []error
	for _, lang := range order {
		subject, body := compose(i18n.For(lang))
		if err := SendWith(ctx, s, brand, lang, groups[lang], subject, body); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Recipients splits comma, semicolon or space separated address lists,
// falling back to def when to holds none.
func Recipients(to []string, def string) []string {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
	}
	var rcpt []string
	for _, t := range to {
		rcpt = append(rcpt, split(t)...)
	}
	if len(rcpt) == 0 {
		rcpt = split(def)
	}
	return rcpt
}

func SendWith(ctx context.Context, s store.SMTPSettings, brand Brand, lang string, rcpt []string, subject, body string) error {
	if s.Host == "" || s.FromAddress == "" {
		return fmt.Errorf("SMTP is not configured")
	}
	if len(rcpt) == 0 {
		return fmt.Errorf("no recipients specified")
	}

	tlsCfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.SkipVerify}
	if s.MinTLS == "1.3" {
		tlsCfg.MinVersion = tls.VersionTLS13
	}
	opts := []gomail.Option{gomail.WithTimeout(30 * time.Second), gomail.WithTLSConfig(tlsCfg)}
	if s.Port > 0 {
		opts = append(opts, gomail.WithPort(s.Port))
	}
	switch s.Security {
	case "tls":
		opts = append(opts, gomail.WithSSL())
	case "none":
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	case "starttls-optional":
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if s.HELO != "" {
		opts = append(opts, gomail.WithHELO(s.HELO))
	}
	password := s.Password
	switch strings.ToLower(s.Auth) {
	case "", "none":
	case "auto":
		if s.Username != "" {
			opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover))
		}
	case "plain":
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain))
	case "login":
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthLogin))
	case "cram-md5":
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthCramMD5))
	case "scram-sha-1":
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthSCRAMSHA1))
	case "scram-sha-256":
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthSCRAMSHA256))
	case "xoauth2":
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthXOAUTH2))
		if s.OAuthClientID != "" {
			tok, err := oauthToken(ctx, s)
			if err != nil {
				return fmt.Errorf("fetching OAuth2 token: %w", err)
			}
			password = tok
		}
	default:
		return fmt.Errorf("unknown authentication method %q", s.Auth)
	}
	if s.Auth != "none" && s.Username != "" {
		opts = append(opts, gomail.WithUsername(s.Username), gomail.WithPassword(password))
	}
	c, err := gomail.NewClient(s.Host, opts...)
	if err != nil {
		return err
	}

	msg := gomail.NewMsg()
	if err := msg.FromFormat(s.FromName, s.FromAddress); err != nil {
		return err
	}
	if err := msg.To(rcpt...); err != nil {
		return err
	}
	msg.Subject(subject)
	msg.SetDate()
	msg.SetMessageID()
	msg.SetBodyString(gomail.TypeTextPlain, body)
	if len(brand.Logo) > 0 {
		if err := msg.EmbedReader("logo.png", bytes.NewReader(brand.Logo), gomail.WithFileContentType(gomail.ContentType("image/png"))); err != nil {
			return err
		}
	}
	msg.AddAlternativeString(gomail.TypeTextHTML, htmlBody(brand, lang, subject, body))
	return c.DialAndSendWithContext(ctx, msg)
}

func oauthToken(ctx context.Context, s store.SMTPSettings) (string, error) {
	scope := s.OAuthScope
	if scope == "" {
		scope = "https://outlook.office365.com/.default"
	}
	cc := clientcredentials.Config{
		ClientID:     s.OAuthClientID,
		ClientSecret: s.OAuthSecret,
		TokenURL:     "https://login.microsoftonline.com/" + s.OAuthTenant + "/oauth2/v2.0/token",
		Scopes:       []string{scope},
	}
	t, err := cc.Token(ctx)
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

func htmlBody(b Brand, lang, subject, body string) string {
	name := b.Company
	if name == "" {
		name = b.Instance
	}
	logo := ""
	if len(b.Logo) > 0 {
		img := `<img src="cid:logo.png" alt="" height="32" style="display:block;height:32px;max-width:200px;border:0">`
		if b.LogoOnLight {
			img = `<span style="display:inline-block;background:#fff;border-radius:8px;padding:6px 10px">` + img + `</span>`
		}
		logo = `<td style="padding-right:14px;vertical-align:middle">` + img + `</td>`
	}
	header := `<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>` + logo +
		`<td style="vertical-align:middle;color:#fff;font-weight:600;font-size:16px">` + html.EscapeString(name)
	if b.Company != "" && b.Instance != "" {
		header += `<div style="font-weight:400;font-size:12px;opacity:.85">` + html.EscapeString(b.Instance) + `</div>`
	}
	header += `</td></tr></table>`
	footer := i18n.T(lang, "Sent automatically by %s", b.Instance)
	if b.Company != "" {
		footer += " · " + b.Company
	}
	htmlLang := i18n.Normalize(lang)
	if htmlLang == "" {
		htmlLang = "en"
	}
	return `<!doctype html><html lang="` + htmlLang + `"><body style="margin:0;background:#0b1020;font-family:Inter,Segoe UI,Arial,sans-serif">
<div style="max-width:640px;margin:32px auto;background:#111827;border-radius:16px;overflow:hidden;border:1px solid #1f2937">
<div style="padding:20px 28px;background:#4f46e5;background:linear-gradient(135deg,#4f46e5,#06b6d4)">` + header + `</div>
<div style="padding:28px;color:#e5e7eb"><h2 style="margin:0 0 16px;font-size:20px;color:#fff">` + html.EscapeString(subject) + `</h2>
<pre style="white-space:pre-wrap;font-family:ui-monospace,Consolas,monospace;font-size:13px;line-height:1.6;color:#cbd5e1;margin:0">` + html.EscapeString(body) + `</pre></div>
<div style="padding:16px 28px;color:#6b7280;font-size:12px;border-top:1px solid #1f2937">` + html.EscapeString(footer) + `</div>
</div></body></html>`
}
