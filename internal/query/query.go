// Package query compiles the search language used in the UI into a
// parameterised ClickHouse WHERE clause.
//
//	category:firewall action:block dport:22
//	src:192.168.1.0/24 -proto:udp "connection refused"
//	severity:<=3 OR category:ips
//	host:U6* field.UNIFIwifiName:Home
//
// Terms are ANDed, OR splits the query into alternatives, a leading "-"
// negates a term and "*" is a wildcard.
package query

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type Filter struct {
	Where string
	Args  []any
}

type col struct {
	expr    string
	numeric bool
	ip      bool
	upper   bool // stored upper-case (country codes)
	asn     bool // "AS13335" and "13335" both match
}

var fields = map[string]col{
	"category":    {expr: "category"},
	"cat":         {expr: "category"},
	"app":         {expr: "app"},
	"host":        {expr: "hostname"},
	"hostname":    {expr: "hostname"},
	"site":        {expr: "site"},
	"source":      {expr: "source_ip", ip: true},
	"model":       {expr: "device_model"},
	"device":      {expr: "device_mac"},
	"mac":         {expr: "device_mac"},
	"firmware":    {expr: "device_version"},
	"action":      {expr: "action"},
	"rule":        {expr: "rule"},
	"event":       {expr: "event_type"},
	"src":         {expr: "src_ip", ip: true},
	"src_ip":      {expr: "src_ip", ip: true},
	"dst":         {expr: "dst_ip", ip: true},
	"dst_ip":      {expr: "dst_ip", ip: true},
	"sport":       {expr: "src_port", numeric: true},
	"src_port":    {expr: "src_port", numeric: true},
	"dport":       {expr: "dst_port", numeric: true},
	"dst_port":    {expr: "dst_port", numeric: true},
	"proto":       {expr: "proto"},
	"in":          {expr: "in_if"},
	"out":         {expr: "out_if"},
	"client":      {expr: "client_mac"},
	"client_mac":  {expr: "client_mac"},
	"client_name": {expr: "client_name"},
	"client_ip":   {expr: "client_ip", ip: true},
	"ssid":        {expr: "ssid"},
	"severity":    {expr: "severity", numeric: true},
	"sev":         {expr: "severity", numeric: true},
	"facility":    {expr: "facility", numeric: true},
	"msg":         {expr: "message"},
	"message":     {expr: "message"},
	"raw":         {expr: "raw"},
	// Enrichment stored in the fields map (GeoIP, UniFi API names).
	"src_country": {expr: "fields['src_country']", upper: true},
	"dst_country": {expr: "fields['dst_country']", upper: true},
	"src_asn":     {expr: "fields['src_asn']", asn: true},
	"dst_asn":     {expr: "fields['dst_asn']", asn: true},
	"src_as":      {expr: "fields['src_as_org']"},
	"dst_as":      {expr: "fields['dst_as_org']"},
	"src_name":    {expr: "fields['src_name']"},
	"dst_name":    {expr: "fields['dst_name']"},
	"device_name": {expr: "fields['device_name']"},
	"camera":      {expr: "fields['camera']"},
	"door":        {expr: "fields['door']"},
	"user":        {expr: "fields['user']"},
}

// Fields returns the searchable field names for UI autocompletion.
func Fields() []string {
	out := make([]string, 0, len(fields)+2)
	for k := range fields {
		out = append(out, k)
	}
	return append(out, "ip", "port", "country", "asn", "as")
}

var sevNames = map[string]int{"emerg": 0, "emergency": 0, "alert": 1, "crit": 2, "critical": 2, "err": 3, "error": 3, "warning": 4, "warn": 4, "notice": 5, "info": 6, "debug": 7}

var reFieldKey = regexp.MustCompile(`^field\.([A-Za-z0-9_\.\-]+)$`)

// Compile translates q. An empty query yields "1".
func Compile(q string) (Filter, error) {
	toks, err := tokenize(q)
	if err != nil {
		return Filter{}, err
	}
	var groups [][]string
	cur := []string{}
	var args []any
	for _, t := range toks {
		if t.raw == "OR" && !t.quoted {
			if len(cur) > 0 {
				groups = append(groups, cur)
			}
			cur = []string{}
			continue
		}
		if t.raw == "AND" && !t.quoted {
			continue
		}
		expr, a, err := compileTerm(t)
		if err != nil {
			return Filter{}, err
		}
		cur = append(cur, expr)
		args = append(args, a...)
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	if len(groups) == 0 {
		return Filter{Where: "1"}, nil
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = "(" + strings.Join(g, " AND ") + ")"
	}
	return Filter{Where: "(" + strings.Join(parts, " OR ") + ")", Args: args}, nil
}

type token struct {
	raw    string
	quoted bool
	phrase bool // started with a quote: never a field:value term
}

func tokenize(q string) ([]token, error) {
	var out []token
	var b strings.Builder
	inQ, quoted, phrase := false, false, false
	flush := func() {
		if b.Len() > 0 || quoted {
			out = append(out, token{raw: b.String(), quoted: quoted, phrase: phrase})
		}
		b.Reset()
		quoted, phrase = false, false
	}
	for _, r := range q {
		switch {
		case r == '"':
			if !inQ && b.Len() == 0 || !inQ && b.String() == "-" {
				phrase = true
			}
			inQ = !inQ
			quoted = true
		case (r == ' ' || r == '\t' || r == '\n') && !inQ:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	if inQ {
		return nil, fmt.Errorf("missing closing quote")
	}
	flush()
	return out, nil
}

func compileTerm(t token) (string, []any, error) {
	s := t.raw
	neg := false
	if strings.HasPrefix(s, "-") && len(s) > 1 {
		neg = true
		s = s[1:]
	}
	wrap := func(e string) string {
		if neg {
			return "NOT (" + e + ")"
		}
		return e
	}
	key, val, hasKey := strings.Cut(s, ":")
	if !hasKey || key == "" || strings.ContainsAny(key, " ") || t.phrase {
		return wrap("(positionCaseInsensitiveUTF8(message, ?) > 0 OR positionCaseInsensitiveUTF8(raw, ?) > 0)"), []any{s, s}, nil
	}
	key = strings.ToLower(key)
	switch key {
	case "ip":
		e, a, err := ipExpr([]string{"src_ip", "dst_ip", "client_ip", "source_ip"}, val)
		return wrap(e), a, err
	case "country", "asn", "as":
		k := map[string]string{"country": "country", "asn": "asn", "as": "as"}[key]
		e1, a1, _ := compileTerm(token{raw: "src_" + k + ":" + val})
		e2, a2, _ := compileTerm(token{raw: "dst_" + k + ":" + val})
		return wrap("(" + e1 + " OR " + e2 + ")"), append(a1, a2...), nil
	case "port":
		e1, a1, err := numExpr("src_port", val)
		if err != nil {
			return "", nil, err
		}
		e2, a2, _ := numExpr("dst_port", val)
		return wrap("(" + e1 + " OR " + e2 + ")"), append(a1, a2...), nil
	}
	if m := reFieldKey.FindStringSubmatch(key); m != nil {
		// Map keys are case-sensitive; take the original spelling.
		orig := strings.SplitN(s, ":", 2)[0][len("field."):]
		e, a := strExpr("fields[?]", val)
		return wrap(e), append([]any{orig}, a...), nil
	}
	c, ok := fields[key]
	if !ok {
		return "", nil, fmt.Errorf("unknown field %q", key)
	}
	switch {
	case c.numeric:
		if c.expr == "severity" {
			rest := strings.TrimLeft(val, "<>=")
			if n, ok := sevNames[strings.ToLower(rest)]; ok {
				val = val[:len(val)-len(rest)] + strconv.Itoa(n)
			}
		}
		e, a, err := numExpr(c.expr, val)
		return wrap(e), a, err
	case c.ip:
		e, a, err := ipExpr([]string{c.expr}, val)
		return wrap(e), a, err
	default:
		if c.expr == "category" || c.expr == "action" || c.expr == "proto" {
			val = strings.ToLower(val)
		}
		if c.expr == "client_mac" || c.expr == "device_mac" {
			val = strings.ToLower(val)
		}
		if c.upper {
			val = strings.ToUpper(val)
		}
		if c.asn {
			val = strings.TrimPrefix(strings.ToUpper(val), "AS")
		}
		e, a := strExpr(c.expr, val)
		return wrap(e), a, nil
	}
}

func strExpr(colExpr, val string) (string, []any) {
	if val == "" {
		return colExpr + " = ''", nil
	}
	if strings.Contains(val, "*") {
		like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`, `*`, `%`).Replace(val)
		return colExpr + " ILIKE ?", []any{like}
	}
	if colExpr == "message" || colExpr == "raw" || colExpr == "rule" || colExpr == "client_name" || strings.HasSuffix(colExpr, "_name']") || strings.HasSuffix(colExpr, "_org']") {
		return "positionCaseInsensitiveUTF8(" + colExpr + ", ?) > 0", []any{val}
	}
	return colExpr + " = ?", []any{val}
}

func numExpr(colExpr, val string) (string, []any, error) {
	ops := []string{">=", "<=", ">", "<", "="}
	for _, op := range ops {
		if strings.HasPrefix(val, op) {
			n, err := strconv.ParseInt(val[len(op):], 10, 64)
			if err != nil {
				return "", nil, fmt.Errorf("invalid number %q", val)
			}
			return colExpr + " " + op + " ?", []any{n}, nil
		}
	}
	if a, b, ok := strings.Cut(val, ".."); ok {
		lo, err1 := strconv.ParseInt(a, 10, 64)
		hi, err2 := strconv.ParseInt(b, 10, 64)
		if err1 != nil || err2 != nil {
			return "", nil, fmt.Errorf("invalid range %q", val)
		}
		return colExpr + " BETWEEN ? AND ?", []any{lo, hi}, nil
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("invalid number %q", val)
	}
	return colExpr + " = ?", []any{n}, nil
}

func ipExpr(cols []string, val string) (string, []any, error) {
	var parts []string
	var args []any
	if pfx, err := netip.ParsePrefix(val); err == nil && pfx.Addr().Is4() {
		pfx = pfx.Masked()
		a := pfx.Addr().As4()
		lo := binary.BigEndian.Uint32(a[:])
		hi := lo | (^uint32(0) >> pfx.Bits())
		if pfx.Bits() == 0 {
			hi = ^uint32(0)
		}
		for _, c := range cols {
			parts = append(parts, fmt.Sprintf("(%s != '' AND toUInt32(toIPv4OrZero(%s)) BETWEEN ? AND ?)", c, c))
			args = append(args, lo, hi)
		}
	} else {
		for _, c := range cols {
			e, a := strExpr(c, val)
			parts = append(parts, e)
			args = append(args, a...)
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")", args, nil
}
