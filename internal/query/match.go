package query

import (
	"fmt"
	"net/netip"
	"path"
	"strconv"
	"strings"

	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

// Matcher reports whether an event matches a query. It evaluates the same
// language as Compile, in memory, so events can be filtered before they are
// stored (forwarding).
type Matcher func(e *unifi.Event) bool

// MatchAll matches every event.
func MatchAll(*unifi.Event) bool { return true }

// CompileMatcher builds an in-memory Matcher for q. An empty query matches
// everything.
func CompileMatcher(q string) (Matcher, error) {
	toks, err := tokenize(q)
	if err != nil {
		return nil, err
	}
	var groups [][]Matcher
	cur := []Matcher{}
	for _, t := range toks {
		if t.raw == "OR" && !t.quoted {
			if len(cur) > 0 {
				groups = append(groups, cur)
			}
			cur = []Matcher{}
			continue
		}
		if t.raw == "AND" && !t.quoted {
			continue
		}
		m, err := matchTerm(t)
		if err != nil {
			return nil, err
		}
		cur = append(cur, m)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	if len(groups) == 0 {
		return MatchAll, nil
	}
	return func(e *unifi.Event) bool {
		for _, g := range groups {
			ok := true
			for _, m := range g {
				if !m(e) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}, nil
}

// getters read the column a search field refers to; keys match col.expr.
var getters = map[string]func(e *unifi.Event) string{
	"category":       func(e *unifi.Event) string { return e.Category },
	"app":            func(e *unifi.Event) string { return e.App },
	"hostname":       func(e *unifi.Event) string { return e.Hostname },
	"site":           func(e *unifi.Event) string { return e.Site },
	"source_ip":      func(e *unifi.Event) string { return e.SourceIP },
	"device_model":   func(e *unifi.Event) string { return e.DeviceModel },
	"device_mac":     func(e *unifi.Event) string { return e.DeviceMAC },
	"device_version": func(e *unifi.Event) string { return e.DeviceVersion },
	"action":         func(e *unifi.Event) string { return e.Action },
	"rule":           func(e *unifi.Event) string { return e.Rule },
	"event_type":     func(e *unifi.Event) string { return e.EventType },
	"src_ip":         func(e *unifi.Event) string { return e.SrcIP },
	"dst_ip":         func(e *unifi.Event) string { return e.DstIP },
	"proto":          func(e *unifi.Event) string { return e.Proto },
	"in_if":          func(e *unifi.Event) string { return e.InIf },
	"out_if":         func(e *unifi.Event) string { return e.OutIf },
	"client_mac":     func(e *unifi.Event) string { return e.ClientMAC },
	"client_name":    func(e *unifi.Event) string { return e.ClientName },
	"client_ip":      func(e *unifi.Event) string { return e.ClientIP },
	"ssid":           func(e *unifi.Event) string { return e.SSID },
	"message":        func(e *unifi.Event) string { return e.Message },
	"raw":            func(e *unifi.Event) string { return e.Raw },
}

var numGetters = map[string]func(e *unifi.Event) int64{
	"src_port": func(e *unifi.Event) int64 { return int64(e.SrcPort) },
	"dst_port": func(e *unifi.Event) int64 { return int64(e.DstPort) },
	"severity": func(e *unifi.Event) int64 { return int64(e.Severity) },
	"facility": func(e *unifi.Event) int64 { return int64(e.Facility) },
}

func getter(expr string) func(e *unifi.Event) string {
	if g, ok := getters[expr]; ok {
		return g
	}
	if k, ok := strings.CutPrefix(expr, "fields['"); ok {
		k = strings.TrimSuffix(k, "']")
		return func(e *unifi.Event) string { return e.Fields[k] }
	}
	return func(*unifi.Event) string { return "" }
}

func not(m Matcher, neg bool) Matcher {
	if !neg {
		return m
	}
	return func(e *unifi.Event) bool { return !m(e) }
}

func matchTerm(t token) (Matcher, error) {
	s := t.raw
	neg := false
	if strings.HasPrefix(s, "-") && len(s) > 1 {
		neg = true
		s = s[1:]
	}
	key, val, hasKey := strings.Cut(s, ":")
	if !hasKey || key == "" || strings.ContainsAny(key, " ") || t.phrase {
		needle := strings.ToLower(s)
		return not(func(e *unifi.Event) bool {
			return strings.Contains(strings.ToLower(e.Message), needle) || strings.Contains(strings.ToLower(e.Raw), needle)
		}, neg), nil
	}
	key = strings.ToLower(key)
	switch key {
	case "ip":
		m, err := ipMatch([]func(*unifi.Event) string{getters["src_ip"], getters["dst_ip"], getters["client_ip"], getters["source_ip"]}, val)
		return not(m, neg), err
	case "country", "asn", "as":
		m1, _ := matchTerm(token{raw: "src_" + key + ":" + val})
		m2, _ := matchTerm(token{raw: "dst_" + key + ":" + val})
		return not(func(e *unifi.Event) bool { return m1(e) || m2(e) }, neg), nil
	case "port":
		m1, err := numMatch(numGetters["src_port"], val)
		if err != nil {
			return nil, err
		}
		m2, _ := numMatch(numGetters["dst_port"], val)
		return not(func(e *unifi.Event) bool { return m1(e) || m2(e) }, neg), nil
	}
	if m := reFieldKey.FindStringSubmatch(key); m != nil {
		orig := strings.SplitN(s, ":", 2)[0][len("field."):]
		return not(strMatch(func(e *unifi.Event) string { return e.Fields[orig] }, "fields[?]", val), neg), nil
	}
	c, ok := fields[key]
	if !ok {
		return nil, errUnknownField(key)
	}
	switch {
	case c.numeric:
		if c.expr == "severity" {
			rest := strings.TrimLeft(val, "<>=")
			if n, ok := sevNames[strings.ToLower(rest)]; ok {
				val = val[:len(val)-len(rest)] + strconv.Itoa(n)
			}
		}
		m, err := numMatch(numGetters[c.expr], val)
		return not(m, neg), err
	case c.ip:
		m, err := ipMatch([]func(*unifi.Event) string{getter(c.expr)}, val)
		return not(m, neg), err
	default:
		if c.expr == "category" || c.expr == "action" || c.expr == "proto" || c.expr == "client_mac" || c.expr == "device_mac" {
			val = strings.ToLower(val)
		}
		if c.upper {
			val = strings.ToUpper(val)
		}
		if c.asn {
			val = strings.TrimPrefix(strings.ToUpper(val), "AS")
		}
		return not(strMatch(getter(c.expr), c.expr, val), neg), nil
	}
}

// strMatch mirrors strExpr: "*" is a case-insensitive wildcard, free-text
// columns match a case-insensitive substring and the rest compare exactly.
func strMatch(get func(*unifi.Event) string, colExpr, val string) Matcher {
	if val == "" {
		return func(e *unifi.Event) bool { return get(e) == "" }
	}
	if strings.Contains(val, "*") {
		pat := strings.ToLower(globEscape(val))
		return func(e *unifi.Event) bool {
			ok, _ := path.Match(pat, strings.ToLower(get(e)))
			return ok
		}
	}
	if colExpr == "message" || colExpr == "raw" || colExpr == "rule" || colExpr == "client_name" || strings.HasSuffix(colExpr, "_name']") || strings.HasSuffix(colExpr, "_org']") {
		needle := strings.ToLower(val)
		return func(e *unifi.Event) bool { return strings.Contains(strings.ToLower(get(e)), needle) }
	}
	return func(e *unifi.Event) bool { return get(e) == val }
}

// globEscape keeps "*" as the only wildcard; path.Match would also treat
// ?, [ and \ specially. "/" never separates anything here, so it is
// replaced by a character path.Match treats like any other.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '?', '[', ']', '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case '/':
			b.WriteRune('\x00')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func numMatch(get func(*unifi.Event) int64, val string) (Matcher, error) {
	for _, op := range []string{">=", "<=", ">", "<", "="} {
		if rest, ok := strings.CutPrefix(val, op); ok {
			n, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return nil, errNumber(val)
			}
			switch op {
			case ">=":
				return func(e *unifi.Event) bool { return get(e) >= n }, nil
			case "<=":
				return func(e *unifi.Event) bool { return get(e) <= n }, nil
			case ">":
				return func(e *unifi.Event) bool { return get(e) > n }, nil
			case "<":
				return func(e *unifi.Event) bool { return get(e) < n }, nil
			default:
				return func(e *unifi.Event) bool { return get(e) == n }, nil
			}
		}
	}
	if a, b, ok := strings.Cut(val, ".."); ok {
		lo, err1 := strconv.ParseInt(a, 10, 64)
		hi, err2 := strconv.ParseInt(b, 10, 64)
		if err1 != nil || err2 != nil {
			return nil, errRange(val)
		}
		return func(e *unifi.Event) bool { v := get(e); return v >= lo && v <= hi }, nil
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return nil, errNumber(val)
	}
	return func(e *unifi.Event) bool { return get(e) == n }, nil
}

func ipMatch(gets []func(*unifi.Event) string, val string) (Matcher, error) {
	if pfx, err := netip.ParsePrefix(val); err == nil && pfx.Addr().Is4() {
		pfx = pfx.Masked()
		return func(e *unifi.Event) bool {
			for _, g := range gets {
				if a, err := netip.ParseAddr(g(e)); err == nil && pfx.Contains(a) {
					return true
				}
			}
			return false
		}, nil
	}
	ms := make([]Matcher, len(gets))
	for i, g := range gets {
		ms[i] = strMatch(g, "", val)
	}
	return func(e *unifi.Event) bool {
		for _, m := range ms {
			if m(e) {
				return true
			}
		}
		return false
	}, nil
}

func errUnknownField(k string) error { return fmt.Errorf("unknown field %q", k) }
func errNumber(v string) error       { return fmt.Errorf("invalid number %q", v) }
func errRange(v string) error        { return fmt.Errorf("invalid range %q", v) }
