package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
)

// Service endpoints; variables so tests can point them at a local server.
var (
	slackAPI       = "https://slack.com/api"
	telegramAPI    = "https://api.telegram.org"
	messagebirdAPI = "https://rest.messagebird.com"
	birdAPI        = "https://api.bird.com"
	pushoverAPI    = "https://api.pushover.net/1"
)

var client = &http.Client{}

// Validate checks that a channel has the settings its kind needs.
func Validate(c store.NotifyChannel) error {
	s := c.Settings
	need := func(v, what string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", what)
		}
		return nil
	}
	httpsURL := func(v string) error {
		if err := need(v, "the webhook URL"); err != nil {
			return err
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("the webhook URL must start with https://")
		}
		return nil
	}
	switch c.Kind {
	case store.NotifyTeams:
		return httpsURL(s.URL)
	case store.NotifySlack:
		if s.Mode == "bot" {
			return errors.Join(need(s.Token, "the bot token"), need(s.Channel, "the channel"))
		}
		return httpsURL(s.URL)
	case store.NotifyTelegram:
		return errors.Join(need(s.Token, "the bot token"), need(s.ChatID, "the chat ID"))
	case store.NotifySMS:
		err := errors.Join(need(s.Token, "the access key"), need(s.Recipients, "at least one phone number"))
		if s.Provider == "bird" {
			return errors.Join(err, need(s.Workspace, "the workspace ID"), need(s.ChannelID, "the channel ID"))
		}
		return errors.Join(err, need(s.Originator, "the sender"))
	case store.NotifyPushover:
		return errors.Join(need(s.Token, "the application token"), need(s.User, "the user key"))
	case store.NotifyWebhook:
		return httpsURL(s.URL)
	}
	return fmt.Errorf("unknown channel type %q", c.Kind)
}

// Send delivers m to channel c.
func Send(ctx context.Context, c store.NotifyChannel, m Message) error {
	if err := Validate(c); err != nil {
		return err
	}
	switch c.Kind {
	case store.NotifyTeams:
		return sendTeams(ctx, c.Settings, m)
	case store.NotifySlack:
		return sendSlack(ctx, c.Settings, m)
	case store.NotifyTelegram:
		return sendTelegram(ctx, c.Settings, m)
	case store.NotifySMS:
		return sendSMS(ctx, c.Settings, m)
	case store.NotifyPushover:
		return sendPushover(ctx, c.Settings, m)
	default:
		return sendWebhook(ctx, c.Settings, m)
	}
}

var sevLabel = map[string]string{store.SevInfo: "INFO", store.SevWarning: "WARNING", store.SevCritical: "CRITICAL"}
var sevEmoji = map[string]string{store.SevInfo: "ℹ️", store.SevWarning: "⚠️", store.SevCritical: "🚨"}

// footer names the server and the time of the (last) notification.
func footer(m Message) string {
	return m.Instance + " · " + m.Time.Local().Format("2006-01-02 15:04")
}

// cut shortens s to at most n runes, marking the cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:max(n-1, 0)]) + "…"
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out = append(out, strings.TrimRight(l, " "))
	}
	return out
}

// post sends a request and returns the response body, or an error for
// non-2xx statuses that includes the start of the body.
func post(ctx context.Context, u, contentType string, body []byte, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "unifi-syslog-server")
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		// Do not leak secret URLs (webhook signatures, bot tokens) in errors.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(b))
		return b, fmt.Errorf("HTTP %d: %s", resp.StatusCode, cut(msg, 300))
	}
	return b, nil
}

func postJSON(ctx context.Context, u string, v any, header http.Header) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return post(ctx, u, "application/json", b, header)
}

// ---------------------------------------------------------------- Teams

// sendTeams posts an Adaptive Card to a Teams Workflows ("Post to a channel
// when a webhook request is received") or legacy incoming webhook URL.
func sendTeams(ctx context.Context, s store.NotifySettings, m Message) error {
	color := map[string]string{store.SevInfo: "Accent", store.SevWarning: "Warning", store.SevCritical: "Attention"}[m.Severity]
	body := []map[string]any{
		{"type": "TextBlock", "text": sevEmoji[m.Severity] + " " + m.Title, "weight": "Bolder", "size": "Medium", "color": color, "wrap": true},
		{"type": "TextBlock", "text": footer(m), "isSubtle": true, "size": "Small", "spacing": "None", "wrap": true},
	}
	ls := lines(cut(m.Body, 20000))
	for i, l := range ls {
		if i >= 60 {
			body = append(body, map[string]any{"type": "TextBlock", "text": "…", "spacing": "None"})
			break
		}
		if l == "" {
			l = " "
		}
		block := map[string]any{"type": "TextBlock", "text": l, "wrap": true, "spacing": "None"}
		if i == 0 {
			block["spacing"] = "Medium"
		}
		body = append(body, block)
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	if m.Link != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open " + m.Instance, "url": m.Link}}
	}
	payload := map[string]any{
		"type":        "message",
		"summary":     m.Title,
		"attachments": []map[string]any{{"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil, "content": card}},
	}
	_, err := postJSON(ctx, s.URL, payload, nil)
	return err
}

// ---------------------------------------------------------------- Slack

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func slackPayload(m Message) map[string]any {
	color := map[string]string{store.SevInfo: "#3b82f6", store.SevWarning: "#f59e0b", store.SevCritical: "#dc2626"}[m.Severity]
	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*" + slackEscape(sevEmoji[m.Severity]+" "+m.Title) + "*"}},
	}
	if b := strings.TrimSpace(m.Body); b != "" {
		// A section text holds at most 3000 characters.
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "```" + cut(slackEscape(b), 2900) + "```"}})
	}
	ctxText := slackEscape(footer(m))
	if m.Link != "" {
		ctxText += " · <" + m.Link + "|" + slackEscape(m.Instance) + ">"
	}
	blocks = append(blocks, map[string]any{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": ctxText}}})
	return map[string]any{
		"text":        slackEscape(m.Title),
		"attachments": []map[string]any{{"color": color, "blocks": blocks}},
	}
}

func sendSlack(ctx context.Context, s store.NotifySettings, m Message) error {
	p := slackPayload(m)
	if s.Mode != "bot" {
		_, err := postJSON(ctx, s.URL, p, nil)
		return err
	}
	p["channel"] = s.Channel
	b, err := postJSON(ctx, slackAPI+"/chat.postMessage", p, http.Header{"Authorization": {"Bearer " + s.Token}})
	if err != nil {
		return err
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &r) == nil && !r.OK {
		return fmt.Errorf("slack: %s", r.Error)
	}
	return nil
}

// ---------------------------------------------------------------- Telegram

func sendTelegram(ctx context.Context, s store.NotifySettings, m Message) error {
	text := "<b>" + html.EscapeString(sevEmoji[m.Severity]+" "+m.Title) + "</b>\n"
	if b := strings.TrimSpace(m.Body); b != "" {
		// Messages hold at most 4096 characters after entity parsing.
		text += "<pre>" + html.EscapeString(cut(b, 3500)) + "</pre>\n"
	}
	text += "<i>" + html.EscapeString(footer(m)) + "</i>"
	p := map[string]any{"chat_id": s.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if s.ThreadID != "" {
		p["message_thread_id"] = s.ThreadID
	}
	b, err := postJSON(ctx, telegramAPI+"/bot"+s.Token+"/sendMessage", p, nil)
	if err != nil {
		var r struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(b, &r) == nil && r.Description != "" {
			return fmt.Errorf("telegram: %s", r.Description)
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------- SMS

// smsText is the title plus as much of the body as fits.
func smsText(s store.NotifySettings, m Message) string {
	limit := s.MaxLength
	if limit <= 0 {
		limit = 306 // two concatenated GSM-7 segments
	}
	text := "[" + sevLabel[m.Severity] + "] " + m.Title
	if b := strings.Join(strings.Fields(m.Body), " "); b != "" {
		text += ": " + b
	}
	return cut(text, limit)
}

// phoneNumbers splits a list of numbers and keeps digits and a leading +.
func phoneNumbers(list string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		var b strings.Builder
		for i, r := range strings.TrimSpace(p) {
			if r >= '0' && r <= '9' || r == '+' && i == 0 {
				b.WriteRune(r)
			}
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

func sendSMS(ctx context.Context, s store.NotifySettings, m Message) error {
	text := smsText(s, m)
	nums := phoneNumbers(s.Recipients)
	if len(nums) == 0 {
		return errors.New("no valid phone numbers")
	}
	auth := http.Header{"Authorization": {"AccessKey " + s.Token}}
	if s.Provider == "bird" {
		// Bird channels API: one message per recipient.
		u := fmt.Sprintf("%s/workspaces/%s/channels/%s/messages", birdAPI, url.PathEscape(s.Workspace), url.PathEscape(s.ChannelID))
		var errs []error
		for _, n := range nums {
			p := map[string]any{
				"receiver": map[string]any{"contacts": []map[string]any{{"identifierKey": "phonenumber", "identifierValue": n}}},
				"body":     map[string]any{"type": "text", "text": map[string]any{"text": text}},
			}
			if _, err := postJSON(ctx, u, p, auth); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", n, err))
			}
		}
		return errors.Join(errs...)
	}
	// MessageBird SMS API wants numbers without the leading +.
	rcpt := make([]string, len(nums))
	for i, n := range nums {
		rcpt[i] = strings.TrimPrefix(n, "+")
	}
	b, err := postJSON(ctx, messagebirdAPI+"/messages", map[string]any{"originator": s.Originator, "recipients": rcpt, "body": text}, auth)
	if err != nil {
		var r struct {
			Errors []struct {
				Description string `json:"description"`
			} `json:"errors"`
		}
		if json.Unmarshal(b, &r) == nil && len(r.Errors) > 0 {
			return fmt.Errorf("messagebird: %s", r.Errors[0].Description)
		}
	}
	return err
}

// ---------------------------------------------------------------- Pushover

func sendPushover(ctx context.Context, s store.NotifySettings, m Message) error {
	prio := map[string]string{store.SevInfo: "-1", store.SevWarning: "0", store.SevCritical: "1"}[m.Severity]
	form := url.Values{
		"token":    {s.Token},
		"user":     {s.User},
		"title":    {cut(m.Title, 250)},
		"message":  {cut(firstNonEmpty(strings.TrimSpace(m.Body), m.Title), 1024)},
		"priority": {prio},
	}
	if m.Link != "" {
		form.Set("url", m.Link)
		form.Set("url_title", m.Instance)
	}
	b, err := post(ctx, pushoverAPI+"/messages.json", "application/x-www-form-urlencoded", []byte(form.Encode()), nil)
	if err != nil {
		var r struct {
			Errors []string `json:"errors"`
		}
		if json.Unmarshal(b, &r) == nil && len(r.Errors) > 0 {
			return fmt.Errorf("pushover: %s", strings.Join(r.Errors, "; "))
		}
	}
	return err
}

// ---------------------------------------------------------------- webhook

// WebhookPayload is the JSON body of the generic webhook.
type WebhookPayload struct {
	Instance string `json:"instance"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Count    int    `json:"count"`
	Time     string `json:"time"`
	Link     string `json:"link,omitempty"`
}

func sendWebhook(ctx context.Context, s store.NotifySettings, m Message) error {
	b, err := json.Marshal(WebhookPayload{Instance: m.Instance, Severity: m.Severity, Title: m.Title, Body: m.Body, Count: m.Count, Time: m.Time.UTC().Format("2006-01-02T15:04:05Z"), Link: m.Link})
	if err != nil {
		return err
	}
	h := http.Header{}
	for _, l := range strings.Split(s.Headers, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) != "" {
			h.Add(http.CanonicalHeaderKey(strings.TrimSpace(k)), strings.TrimSpace(v))
		}
	}
	if s.Secret != "" {
		mac := hmac.New(sha256.New, []byte(s.Secret))
		mac.Write(b)
		h.Set("X-Uss-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	_, err = post(ctx, s.URL, "application/json", b, h)
	return err
}
