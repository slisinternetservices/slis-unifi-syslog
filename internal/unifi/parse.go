package unifi

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxClockSkew is how far a device timestamp may differ from the receive
// time before it is considered wrong. UniFi devices without NTP (or with a
// wrong timezone) are common, so beyond this we trust the receive time.
var MaxClockSkew = 2 * time.Hour

var (
	// UniFi APs and switches send "<model>,<mac>,<firmware>" as the syslog
	// hostname, e.g. "U7PG2,f09fc2aabbcc,v6.5.28.14491". Generic servers treat
	// this as one opaque host and lose the model/MAC/firmware.
	reDeviceHost = regexp.MustCompile(`^([A-Za-z0-9_\-\.]+),([0-9a-fA-F]{12}),(v?[0-9][^:\s]*):?$`)
	reTag        = regexp.MustCompile(`^([A-Za-z0-9_\-\.\/\(\)]+?)(?:\[(\d+)\])?:(?:\s|$)`)
	reKernTS     = regexp.MustCompile(`^\[\s*\d+\.\d+\]\s*`)
)

// Parse decodes one syslog message. loc is the timezone used for RFC3164
// timestamps, which carry no zone information.
func Parse(raw []byte, sourceIP string, received time.Time, loc *time.Location) *Event {
	raw = bytes.TrimRight(raw, "\r\n\x00 ")
	e := &Event{Received: received.UTC(), SourceIP: sourceIP, Raw: string(raw), Facility: 1, Severity: 5}
	s := e.Raw

	// <PRI>
	if len(s) > 2 && s[0] == '<' {
		if end := strings.IndexByte(s[:min(len(s), 5)], '>'); end > 1 {
			if pri, err := strconv.Atoi(s[1:end]); err == nil && pri >= 0 && pri < 192 {
				e.Facility = uint8(pri / 8)
				e.Severity = uint8(pri % 8)
				s = s[end+1:]
			}
		}
	}

	var ts time.Time
	if strings.HasPrefix(s, "1 ") {
		ts, s = parse5424(e, s[2:])
	} else {
		ts, s = parse3164Header(e, s, received, loc)
	}
	if ts.IsZero() {
		ts = received
	} else if d := ts.Sub(received); d > MaxClockSkew || d < -MaxClockSkew {
		e.set("device_time", ts.Format(time.RFC3339))
		ts = received
	}
	e.TS = ts.UTC()
	e.Message = strings.TrimSpace(s)
	classify(e)
	if e.Category == "" {
		e.Category = CatSystem
	}
	return e
}

func parse5424(e *Event, s string) (time.Time, string) {
	// TIMESTAMP HOSTNAME APP-NAME PROCID MSGID [SD] MSG
	parts := strings.SplitN(s, " ", 6)
	if len(parts) < 5 {
		return time.Time{}, s
	}
	var ts time.Time
	if parts[0] != "-" {
		ts, _ = time.Parse(time.RFC3339Nano, parts[0])
	}
	setHost(e, nil2empty(parts[1]))
	e.App = nil2empty(parts[2])
	e.PID = nil2empty(parts[3])
	if m := nil2empty(parts[4]); m != "" {
		e.set("msgid", m)
	}
	rest := ""
	if len(parts) == 6 {
		rest = parts[5]
	}
	// Structured data: "-" or one or more [..] blocks.
	if strings.HasPrefix(rest, "-") {
		rest = strings.TrimPrefix(rest, "-")
	} else {
		for strings.HasPrefix(rest, "[") {
			end := sdEnd(rest)
			if end < 0 {
				break
			}
			e.set("sd", strings.TrimSpace(e.Fields["sd"]+rest[:end+1]))
			rest = rest[end+1:]
		}
	}
	rest = strings.TrimPrefix(strings.TrimSpace(rest), "\ufeff")
	return ts, rest
}

func sdEnd(s string) int {
	esc := false
	for i := 1; i < len(s); i++ {
		switch {
		case esc:
			esc = false
		case s[i] == '\\':
			esc = true
		case s[i] == ']':
			return i
		}
	}
	return -1
}

func nil2empty(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

var months = map[string]time.Month{"Jan": 1, "Feb": 2, "Mar": 3, "Apr": 4, "May": 5, "Jun": 6, "Jul": 7, "Aug": 8, "Sep": 9, "Oct": 10, "Nov": 11, "Dec": 12}

func parse3164Header(e *Event, s string, received time.Time, loc *time.Location) (time.Time, string) {
	var ts time.Time
	s = strings.TrimLeft(s, " ")
	// ISO8601 timestamp (rsyslog-style forwarding / UniFi OS)
	if len(s) > 19 && s[4] == '-' && s[10] == 'T' {
		if sp := strings.IndexByte(s, ' '); sp > 0 {
			if t, err := time.Parse(time.RFC3339Nano, s[:sp]); err == nil {
				ts, s = t, s[sp+1:]
			} else if t, err := time.ParseInLocation("2006-01-02T15:04:05", s[:sp], loc); err == nil {
				ts, s = t, s[sp+1:]
			}
		}
	} else if len(s) >= 15 && s[3] == ' ' && s[6] == ' ' && s[9] == ':' {
		if mon, ok := months[s[:3]]; ok {
			day, _ := strconv.Atoi(strings.TrimSpace(s[4:6]))
			hh, _ := strconv.Atoi(s[7:9])
			mm, _ := strconv.Atoi(s[10:12])
			ss, _ := strconv.Atoi(s[13:15])
			rl := received.In(loc)
			year := rl.Year()
			rest := s[15:]
			// Optional ".mmm" fraction and optional year ("Oct 17 2026 19:55:06" is not handled; "Oct 17 19:55:06 2026" is)
			nsec := 0
			if len(rest) > 1 && rest[0] == '.' {
				j := 1
				for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
					j++
				}
				frac := rest[1:j]
				if len(frac) > 9 {
					frac = frac[:9]
				}
				n, _ := strconv.Atoi(frac + strings.Repeat("0", 9-len(frac)))
				nsec = n
				rest = rest[j:]
			}
			if len(rest) > 5 && rest[0] == ' ' && rest[5] == ' ' {
				if y, err := strconv.Atoi(rest[1:5]); err == nil && y > 2000 {
					year = y
					rest = rest[5:]
				}
			}
			ts = time.Date(year, mon, day, hh, mm, ss, nsec, loc)
			// Messages from late December arriving in early January.
			if ts.Sub(rl) > 24*time.Hour*30 {
				ts = ts.AddDate(-1, 0, 0)
			}
			s = strings.TrimLeft(rest, " ")
		}
	}

	// Hostname or tag?
	if strings.HasPrefix(s, "CEF:") {
		return ts, s
	}
	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		return ts, s
	}
	tok := s[:sp]
	if reDeviceHost.MatchString(tok) {
		setHost(e, tok)
		s = s[sp+1:]
	} else if !strings.HasSuffix(tok, ":") && !strings.Contains(tok, "[") && !strings.HasPrefix(tok, "CEF") {
		setHost(e, tok)
		s = s[sp+1:]
	}
	// Tag
	if strings.HasPrefix(s, "CEF:") {
		return ts, s
	}
	if m := reTag.FindStringSubmatchIndex(s); m != nil {
		e.App = s[m[2]:m[3]]
		if m[4] >= 0 {
			e.PID = s[m[4]:m[5]]
		}
		s = s[m[1]:]
	}
	return ts, s
}

func setHost(e *Event, h string) {
	if m := reDeviceHost.FindStringSubmatch(h); m != nil {
		e.DeviceModel = m[1]
		e.DeviceMAC = formatMAC(m[2])
		e.DeviceVersion = m[3]
		e.Hostname = m[1]
		return
	}
	e.Hostname = h
}

func formatMAC(hex12 string) string {
	h := strings.ToLower(hex12)
	if len(h) != 12 {
		return h
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(h[i : i+2])
	}
	return b.String()
}

func classify(e *Event) {
	msg := e.Message
	app := strings.ToLower(e.App)
	// The kernel ring buffer prefixes a monotonic timestamp: "[ 1234.567] ..."
	if strings.HasPrefix(e.App, "EVT_") {
		msg = e.App + ": " + msg
		e.Message = msg
		e.App = "unifi"
		app = "unifi"
	}
	if app == "kernel" {
		msg = reKernTS.ReplaceAllString(msg, "")
		e.Message = msg
	}
	switch {
	case strings.HasPrefix(msg, "CEF:"):
		parseCEF(e, msg)
	case strings.Contains(msg, "IN=") && strings.Contains(msg, "SRC="):
		parseFirewall(e, msg)
	case app == "hostapd" || strings.Contains(msg, ": STA "):
		parseHostapd(e, msg)
	case app == "stahtd" || app == "stamgr" || strings.Contains(msg, "STA-TRACKER"):
		parseStahtd(e, msg)
	case app == "wevent" || strings.Contains(msg, "EVENT_STA_"):
		parseWevent(e, msg)
	case app == "dnsmasq-dhcp" || strings.HasPrefix(app, "kea-dhcp") || app == "dhcpd" || app == "udhcpd" || strings.HasPrefix(msg, "DHCP"):
		parseDHCP(e, msg)
	case app == "dnsmasq" || app == "coredns" || app == "unbound":
		parseDNS(e, msg)
	case app == "suricata" || strings.Contains(msg, "[Classification:"):
		parseSuricata(e, msg)
	case isVPNApp(app):
		parseVPN(e, msg)
	case app == "sshd" || app == "dropbear" || app == "login" || app == "sudo" || app == "su":
		parseAuth(e, msg)
	case strings.Contains(app, "protect"):
		parseProtect(e, msg, nil)
	case app == "unifi-access" || strings.HasPrefix(app, "unifi-access-") || app == "access-controller":
		parseAccess(e, msg, nil)
	case strings.Contains(msg, "EVT_"):
		parseControllerEvent(e, msg)
	case isSwitchApp(app) || reLink.MatchString(msg):
		parseSwitch(e, msg)
	case isDeviceApp(app):
		e.Category = CatDevice
	default:
		if strings.HasPrefix(e.DeviceModel, "US") {
			e.Category = CatSwitch
		} else if e.DeviceModel != "" {
			e.Category = CatDevice
		}
	}
}

func isVPNApp(app string) bool {
	switch app {
	case "charon", "ipsec", "strongswan", "openvpn", "wireguard", "wg", "xl2tpd", "teleport", "wgclient", "wg-quick":
		return true
	}
	return strings.HasPrefix(app, "openvpn") || strings.HasPrefix(app, "wireguard")
}

func isSwitchApp(app string) bool {
	switch app {
	case "switch", "swctrl", "trapmgr", "lldpd", "poemgr", "stp", "rstp", "switchd", "port":
		return true
	}
	return false
}

func isDeviceApp(app string) bool {
	switch app {
	case "mcad", "syswrapper", "upgrade", "ubios-udapi-server", "unifi-core", "ulcmd", "utermd", "inform", "mca-ctrl", "mcadump", "ubnt-systool", "uplink-monitor", "ubnt-dpi", "dpi-flow-stats":
		return true
	}
	return strings.HasPrefix(app, "mca-") || strings.HasPrefix(app, "ubnt") || strings.HasPrefix(app, "unifi")
}

// ---------------------------------------------------------------- firewall

var reFWTag = regexp.MustCompile(`\[([^\[\]\s]+)\]\s*(?:DESCR=|IN=)`)

func parseFirewall(e *Event, msg string) {
	e.Category = CatFirewall
	e.EventType = "packet"
	kvStart := strings.Index(msg, "IN=")
	if m := reFWTag.FindStringSubmatchIndex(msg); m != nil {
		tag := msg[m[2]:m[3]]
		kvStart = m[3] + 1
		e.set("rule_tag", tag)
		e.Rule = tag
		chain, action, id := splitRuleTag(tag)
		e.set("chain", chain)
		e.set("rule_id", id)
		e.Action = action
	}
	kv := parseKV(msg[kvStart:])
	e.InIf = kv["IN"]
	e.OutIf = kv["OUT"]
	e.SrcIP = kv["SRC"]
	e.DstIP = kv["DST"]
	e.Proto = strings.ToLower(kv["PROTO"])
	e.SrcPort = atou16(kv["SPT"])
	e.DstPort = atou16(kv["DPT"])
	if d := kv["DESCR"]; d != "" {
		e.Rule = d
		e.set("rule_name", d)
	}
	if mac := kv["MAC"]; len(mac) >= 35 {
		// dst(6):src(6):ethertype(2)
		e.set("dst_mac", mac[0:17])
		e.ClientMAC = mac[18:35]
	}
	for _, k := range []string{"LEN", "TTL", "ID", "TOS", "WINDOW", "TYPE", "CODE", "MARK"} {
		e.set(strings.ToLower(k), kv[k])
	}
	if f := kv["_flags"]; f != "" {
		e.set("flags", f)
	}
	switch e.InIf {
	case "":
		e.set("direction", "out")
	}
	if e.Action == "" {
		e.Action = "log"
	}
	e.Message = describeFirewall(e)
}

func describeFirewall(e *Event) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(e.Action))
	if e.Rule != "" {
		b.WriteString(" [" + e.Rule + "]")
	}
	b.WriteString(" " + e.Proto + " " + e.SrcIP)
	if e.SrcPort > 0 {
		b.WriteString(":" + strconv.Itoa(int(e.SrcPort)))
	}
	b.WriteString(" -> " + e.DstIP)
	if e.DstPort > 0 {
		b.WriteString(":" + strconv.Itoa(int(e.DstPort)))
	}
	if e.InIf != "" || e.OutIf != "" {
		b.WriteString(" (" + e.InIf + " -> " + e.OutIf + ")")
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// splitRuleTag decodes UniFi iptables log prefixes such as
// "WAN_LOCAL-D-2147483647", "LAN_IN-A-3001", "WAN_IN-RET-3" or
// "CUSTOM1_LAN-B-30000".
func splitRuleTag(tag string) (chain, action, id string) {
	parts := strings.Split(tag, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if a := fwAction(parts[i]); a != "" {
			chain = strings.Join(parts[:i], "-")
			action = a
			id = strings.Join(parts[i+1:], "-")
			return
		}
	}
	return tag, "", ""
}

func fwAction(s string) string {
	switch strings.ToUpper(s) {
	case "A", "ACCEPT", "RET", "RETURN", "ALLOW":
		return "allow"
	case "D", "DROP", "B", "BLOCK":
		return "block"
	case "R", "REJECT":
		return "reject"
	case "DNAT", "SNAT", "MASQ":
		return "nat"
	}
	return ""
}

// parseKV parses iptables-style KEY=VALUE pairs; values may be quoted.
// Bare words (TCP flags) are collected under "_flags".
func parseKV(s string) map[string]string {
	out := make(map[string]string, 24)
	var flags []string
	i := 0
	for i < len(s) {
		for i < len(s) && s[i] == ' ' {
			i++
		}
		if i >= len(s) {
			break
		}
		j := i
		for j < len(s) && s[j] != '=' && s[j] != ' ' {
			j++
		}
		key := s[i:j]
		if j >= len(s) || s[j] == ' ' {
			if key != "" {
				flags = append(flags, key)
			}
			i = j
			continue
		}
		j++ // skip '='
		var val string
		if j < len(s) && s[j] == '"' {
			end := strings.IndexByte(s[j+1:], '"')
			if end < 0 {
				val = s[j+1:]
				j = len(s)
			} else {
				val = s[j+1 : j+1+end]
				j = j + 2 + end
			}
		} else {
			k := j
			for k < len(s) && s[k] != ' ' {
				k++
			}
			val = s[j:k]
			j = k
		}
		if _, dup := out[key]; !dup {
			out[key] = val
		}
		i = j
	}
	if len(flags) > 0 {
		out["_flags"] = strings.Join(flags, " ")
	}
	return out
}

func atou16(s string) uint16 {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return uint16(n)
}

// ---------------------------------------------------------------- CEF

var reCEFKey = regexp.MustCompile(`(?:^|\s)([A-Za-z0-9_\.]+)=`)

// parseCEF handles UniFi Network's "SIEM integration" output (Network 9+).
func parseCEF(e *Event, msg string) {
	hdr := splitCEF(msg, 8)
	if len(hdr) < 7 {
		e.Category = CatSystem
		return
	}
	e.set("cef_vendor", hdr[1])
	e.set("cef_product", hdr[2])
	e.set("cef_version", hdr[3])
	e.set("cef_signature", hdr[4])
	e.EventType = hdr[5]
	if sev, err := strconv.Atoi(hdr[6]); err == nil {
		e.Severity = cefSeverity(sev)
		e.set("cef_severity", hdr[6])
	}
	ext := map[string]string{}
	if len(hdr) == 8 {
		ext = parseCEFExt(hdr[7])
	}
	for k, v := range ext {
		e.set(k, v)
	}
	if e.App == "" || e.App == "CEF" {
		e.App = "unifi-network"
	}
	e.SrcIP = first(ext["src"], ext["UNIFIclientIp"])
	e.DstIP = ext["dst"]
	e.SrcPort = atou16(ext["spt"])
	e.DstPort = atou16(ext["dpt"])
	e.Proto = strings.ToLower(ext["proto"])
	e.ClientMAC = strings.ToLower(first(ext["UNIFIclientMac"], ext["smac"]))
	e.ClientName = first(ext["UNIFIclientAlias"], ext["UNIFIclientHostname"], ext["shost"])
	e.ClientIP = ext["UNIFIclientIp"]
	e.SSID = ext["UNIFIwifiName"]
	e.Rule = first(ext["UNIFIpolicyName"], ext["UNIFIipsSignature"], ext["UNIFIrulesetName"], ext["cs1"])
	if h := first(ext["UNIFIdeviceName"], ext["UNIFIhost"]); h != "" && e.Hostname == "" {
		e.Hostname = h
	}
	if m := ext["UNIFIdeviceMac"]; m != "" {
		e.DeviceMAC = strings.ToLower(m)
	}
	e.DeviceModel = first(e.DeviceModel, ext["UNIFIdeviceModel"])
	e.DeviceVersion = first(e.DeviceVersion, ext["UNIFIdeviceVersion"])
	e.Category = cefCategory(ext["UNIFIcategory"], ext["UNIFIsubCategory"], hdr[5])
	if m := ext["msg"]; m != "" {
		e.Message = hdr[5] + ": " + m
	} else {
		e.Message = hdr[5]
	}
	// UniFi OS consoles forward Protect, Access and system events through
	// the same SIEM integration; the CEF product tells them apart.
	switch product := strings.ToLower(hdr[2]); {
	case strings.Contains(product, "protect"):
		e.App = "unifi-protect"
		parseProtect(e, hdr[5], ext)
		return
	case strings.Contains(product, "access"):
		e.App = "unifi-access"
		parseAccess(e, hdr[5], ext)
		return
	case strings.Contains(product, "unifi os") || strings.Contains(product, "unifi-os") || product == "ucore":
		e.App = "unifi-os"
		if e.Category == CatSystem || e.Category == CatDevice {
			e.EventType = slug(hdr[5])
		}
	}
	name := strings.ToLower(hdr[5])
	act := strings.ToLower(ext["act"])
	switch {
	case strings.Contains(act, "block") || strings.Contains(act, "drop") || strings.Contains(name, "blocked"):
		e.Action = "block"
	case strings.Contains(act, "allow") || strings.Contains(act, "accept"):
		e.Action = "allow"
	case e.Category == CatIPS:
		e.Action = "alert"
	}
}

func splitCEF(s string, n int) []string {
	var out []string
	var cur strings.Builder
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if len(out) == n-1 {
			out = append(out, s[i:])
			return out
		}
		switch {
		case esc:
			cur.WriteByte(c)
			esc = false
		case c == '\\':
			esc = true
		case c == '|':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

func parseCEFExt(s string) map[string]string {
	out := map[string]string{}
	idx := reCEFKey.FindAllStringSubmatchIndex(s, -1)
	// Ignore matches preceded by a backslash (escaped '=' inside a value).
	var keys [][]int
	for _, m := range idx {
		if m[3] < len(s) && m[3] > 0 && s[m[3]-1] == '\\' {
			continue
		}
		keys = append(keys, m)
	}
	for i, m := range keys {
		key := s[m[2]:m[3]]
		start := m[3] + 1
		end := len(s)
		if i+1 < len(keys) {
			end = keys[i+1][0]
		}
		if start > end {
			continue
		}
		v := strings.TrimSpace(s[start:end])
		v = strings.NewReplacer(`\=`, "=", `\\`, `\`, `\n`, "\n", `\r`, "").Replace(v)
		out[key] = v
	}
	return out
}

func cefSeverity(n int) uint8 {
	switch {
	case n >= 9:
		return 2
	case n >= 7:
		return 3
	case n >= 5:
		return 4
	case n >= 3:
		return 5
	default:
		return 6
	}
}

func cefCategory(cat, sub, name string) string {
	s := strings.ToLower(sub + " " + name)
	switch {
	case strings.Contains(s, "intrusion") || strings.Contains(s, "threat") || strings.Contains(s, "honeypot") || strings.Contains(s, "ips") || strings.Contains(s, "ids"):
		return CatIPS
	case strings.Contains(s, "firewall") || strings.Contains(s, "traffic rule") || strings.Contains(s, "policy"):
		return CatFirewall
	case strings.Contains(s, "vpn") || strings.Contains(s, "teleport") || strings.Contains(s, "wireguard") || strings.Contains(s, "site-to-site"):
		return CatVPN
	case strings.Contains(s, "admin") || strings.Contains(s, "login") || strings.Contains(s, "accessed"):
		return CatAdmin
	case strings.Contains(s, "wifi") || strings.Contains(s, "wireless") || strings.Contains(s, "wlan"):
		return CatWiFi
	case strings.Contains(s, "client"):
		return CatClient
	case strings.Contains(s, "dhcp"):
		return CatDHCP
	case strings.Contains(s, "dns") || strings.Contains(s, "content filter"):
		return CatDNS
	case strings.Contains(s, "switch") || strings.Contains(s, "port"):
		return CatSwitch
	case strings.Contains(s, "device") || strings.Contains(s, "adopt") || strings.Contains(s, "firmware") || strings.Contains(s, "update"):
		return CatDevice
	}
	switch strings.ToLower(cat) {
	case "security":
		return CatIPS
	case "monitoring":
		return CatDevice
	}
	return CatSystem
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------- Wi-Fi

var (
	reHostapd = regexp.MustCompile(`^(\S+): STA ([0-9a-fA-F:]{17}) (.+?): (.+)$`)
	reMAC     = regexp.MustCompile(`(?i)\b([0-9a-f]{2}(?::[0-9a-f]{2}){5})\b`)
	reIPv4    = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3})\b`)
)

func parseHostapd(e *Event, msg string) {
	e.Category = CatWiFi
	if m := reHostapd.FindStringSubmatch(msg); m != nil {
		e.InIf = m[1]
		e.set("vap", m[1])
		e.ClientMAC = strings.ToLower(m[2])
		e.set("module", m[3])
		e.EventType = wifiEvent(m[4])
		return
	}
	if m := reMAC.FindStringSubmatch(msg); m != nil {
		e.ClientMAC = strings.ToLower(m[1])
	}
	e.EventType = wifiEvent(msg)
}

func wifiEvent(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "disassociated"):
		return "disassociated"
	case strings.Contains(l, "deauthenticated"):
		return "deauthenticated"
	case strings.Contains(l, "reassociated"):
		return "reassociated"
	case strings.Contains(l, "associated"):
		return "associated"
	case strings.Contains(l, "handshake completed"):
		return "wpa_handshake"
	case strings.Contains(l, "authenticated"):
		return "authenticated"
	case strings.Contains(l, "failed") || strings.Contains(l, "timeout"):
		return "auth_failed"
	case strings.Contains(l, "radius"):
		return "radius"
	}
	return "wifi"
}

func parseStahtd(e *Event, msg string) {
	e.Category = CatWiFi
	e.EventType = "sta_tracker"
	i := strings.IndexByte(msg, '{')
	if i < 0 {
		return
	}
	j := strings.LastIndexByte(msg, '}')
	if j <= i {
		return
	}
	kv := flatJSON(msg[i : j+1])
	for k, v := range kv {
		e.set(k, v)
	}
	e.ClientMAC = strings.ToLower(kv["mac"])
	if v := kv["vap"]; v != "" {
		e.InIf = v
	}
	e.SSID = first(kv["essid"], kv["ssid"])
	if et := kv["event_type"]; et != "" {
		e.EventType = strings.ReplaceAll(strings.ToLower(et), " ", "_")
	}
	if strings.Contains(e.EventType, "failure") {
		e.Action = "fail"
	}
	e.ClientIP = kv["ip"]
	e.Message = strings.TrimSpace(msg[:i]) + " " + describeSTA(kv)
}

func describeSTA(kv map[string]string) string {
	var p []string
	for _, k := range []string{"message_type", "event_type", "mac", "vap", "assoc_status", "auth_failures", "dns_resp_seen", "ip"} {
		if v := kv[k]; v != "" {
			p = append(p, k+"="+v)
		}
	}
	return strings.Join(p, " ")
}

// flatJSON extracts top-level string/number/bool members from a JSON object
// without allocating a full decoder tree; nested objects are skipped.
var reFlatJSON = regexp.MustCompile(`"([A-Za-z0-9_]+)"\s*:\s*("((?:[^"\\]|\\.)*)"|-?[0-9.eE+]+|true|false|null)`)

func flatJSON(s string) map[string]string {
	out := map[string]string{}
	for _, m := range reFlatJSON.FindAllStringSubmatch(s, -1) {
		v := m[2]
		if strings.HasPrefix(v, `"`) {
			v = m[3]
		}
		if v == "null" {
			continue
		}
		if _, ok := out[m[1]]; !ok {
			out[m[1]] = v
		}
	}
	return out
}

var reWevent = regexp.MustCompile(`EVENT_STA_(JOIN|LEAVE|IP)\s+(\S+):?\s+([0-9a-fA-F:]{17})`)

func parseWevent(e *Event, msg string) {
	e.Category = CatWiFi
	if m := reWevent.FindStringSubmatch(msg); m != nil {
		e.EventType = "sta_" + strings.ToLower(m[1])
		e.InIf = strings.TrimSuffix(m[2], ":")
		e.ClientMAC = strings.ToLower(m[3])
		if ip := reIPv4.FindString(msg[strings.Index(msg, m[3])+17:]); ip != "" {
			e.ClientIP = ip
		}
		return
	}
	e.EventType = wifiEvent(msg)
	if m := reMAC.FindStringSubmatch(msg); m != nil {
		e.ClientMAC = strings.ToLower(m[1])
	}
}

// ---------------------------------------------------------------- DHCP / DNS

var (
	reDnsmasqDHCP = regexp.MustCompile(`^(?:\d+\s+)?(DHCP[A-Z]+)\(([^)]+)\)\s*(\d{1,3}(?:\.\d{1,3}){3})?\s*([0-9a-fA-F:]{17})?\s*(.*)$`)
	reDNSQuery    = regexp.MustCompile(`^(?:\d+\s+[\d\.:a-f]+/\d+\s+)?query\[(\w+)\]\s+(\S+)\s+from\s+(\S+)`)
	reDNSReply    = regexp.MustCompile(`^(?:\d+\s+[\d\.:a-f]+/\d+\s+)?(reply|cached|forwarded|config|/etc/hosts)\s+(\S+)\s+(?:is|to)\s+(\S+)`)
)

func parseDHCP(e *Event, msg string) {
	e.Category = CatDHCP
	if m := reDnsmasqDHCP.FindStringSubmatch(msg); m != nil {
		e.EventType = m[1]
		e.InIf = m[2]
		e.ClientIP = m[3]
		e.ClientMAC = strings.ToLower(m[4])
		if n := strings.TrimSpace(m[5]); n != "" && !strings.Contains(n, " ") {
			e.ClientName = n
		} else if n != "" {
			e.set("detail", n)
		}
		return
	}
	// kea / generic
	for _, t := range []string{"LEASE_ALLOC", "LEASE_RENEW", "LEASE_RELEASE", "LEASE_EXPIRE", "DHCPACK", "DHCPNAK", "DHCPOFFER", "DHCPREQUEST", "DHCPDISCOVER", "DHCPRELEASE"} {
		if strings.Contains(msg, t) {
			e.EventType = t
			break
		}
	}
	if m := reMAC.FindStringSubmatch(msg); m != nil {
		e.ClientMAC = strings.ToLower(m[1])
	}
	if ip := reIPv4.FindString(msg); ip != "" {
		e.ClientIP = ip
	}
}

func parseDNS(e *Event, msg string) {
	e.Category = CatDNS
	if m := reDNSQuery.FindStringSubmatch(msg); m != nil {
		e.EventType = "query"
		e.set("qtype", m[1])
		e.set("query", m[2])
		e.ClientIP = m[3]
		return
	}
	if m := reDNSReply.FindStringSubmatch(msg); m != nil {
		e.EventType = m[1]
		e.set("query", m[2])
		e.set("answer", m[3])
		return
	}
	e.EventType = "dns"
}

// ---------------------------------------------------------------- IPS

var (
	reSurSig   = regexp.MustCompile(`\[(\d+):(\d+):(\d+)\]\s+(.+?)\s+\[\*\*\]`)
	reSurClass = regexp.MustCompile(`\[Classification:\s*([^\]]+)\]`)
	reSurPrio  = regexp.MustCompile(`\[Priority:\s*(\d+)\]`)
	reSurFlow  = regexp.MustCompile(`\{(\w+)\}\s+(\S+)\s+->\s+(\S+)`)
)

func parseSuricata(e *Event, msg string) {
	e.Category = CatIPS
	e.EventType = "alert"
	e.Action = "alert"
	if strings.Contains(msg, "[Drop]") || strings.Contains(msg, "[wDrop]") {
		e.Action = "block"
	}
	if m := reSurSig.FindStringSubmatch(msg); m != nil {
		e.set("gid", m[1])
		e.set("sid", m[2])
		e.set("rev", m[3])
		e.Rule = m[4]
	}
	if m := reSurClass.FindStringSubmatch(msg); m != nil {
		e.set("classification", m[1])
	}
	if m := reSurPrio.FindStringSubmatch(msg); m != nil {
		e.set("priority", m[1])
		switch m[1] {
		case "1":
			e.Severity = 2
		case "2":
			e.Severity = 3
		case "3":
			e.Severity = 4
		}
	}
	if m := reSurFlow.FindStringSubmatch(msg); m != nil {
		e.Proto = strings.ToLower(m[1])
		e.SrcIP, e.SrcPort = splitHostPort(m[2])
		e.DstIP, e.DstPort = splitHostPort(m[3])
	}
	if e.Rule != "" {
		e.Message = fmt.Sprintf("%s · %s %s:%d -> %s:%d", e.Rule, e.Proto, e.SrcIP, e.SrcPort, e.DstIP, e.DstPort)
		if c := e.Fields["classification"]; c != "" {
			e.Message += " (" + c + ")"
		}
	}
}

func splitHostPort(s string) (string, uint16) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, 0
	}
	// Suricata always appends ":port", also after IPv6 addresses.
	if p := atou16(s[i+1:]); p > 0 || s[i+1:] == "0" {
		return strings.Trim(s[:i], "[]"), p
	}
	return s, 0
}

// ---------------------------------------------------------------- VPN / auth / switch

func parseVPN(e *Event, msg string) {
	e.Category = CatVPN
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "authentication failed") || strings.Contains(l, "auth_failed") || strings.Contains(l, "tls error"):
		e.EventType = "auth_failed"
		e.Action = "fail"
	case strings.Contains(l, "established") || strings.Contains(l, "peer connection initiated") || strings.Contains(l, "handshake"):
		e.EventType = "connected"
	case strings.Contains(l, "deleting") || strings.Contains(l, "closing") || strings.Contains(l, "disconnect") || strings.Contains(l, "sigterm"):
		e.EventType = "disconnected"
	default:
		e.EventType = "vpn"
	}
	if ip := reIPv4.FindString(msg); ip != "" {
		e.SrcIP = ip
	}
}

var (
	reSSHOK   = regexp.MustCompile(`(?:Accepted \S+ for (\S+) from (\S+)|[Pp]assword auth succeeded for '([^']+)' from ([^:\s]+))`)
	reSSHFail = regexp.MustCompile(`(?:Failed \S+ for (?:invalid user )?(\S+) from (\S+)|[Bb]ad password attempt for '([^']+)' from ([^:\s]+)|Invalid user (\S+) from (\S+))`)
)

func parseAuth(e *Event, msg string) {
	e.Category = CatAuth
	e.EventType = "auth"
	if m := reSSHOK.FindStringSubmatch(msg); m != nil {
		e.EventType = "login_success"
		e.Action = "allow"
		e.set("user", first(m[1], m[3]))
		e.SrcIP = first(m[2], m[4])
		return
	}
	if m := reSSHFail.FindStringSubmatch(msg); m != nil {
		e.EventType = "login_failed"
		e.Action = "fail"
		e.set("user", first(m[1], m[3], m[5]))
		e.SrcIP = first(m[2], m[4], m[6])
	}
}

var reLink = regexp.MustCompile(`(?i)\blink (up|down)\b[^0-9]*([0-9][0-9/\.]*)?`)

func parseSwitch(e *Event, msg string) {
	e.Category = CatSwitch
	e.EventType = "switch"
	if m := reLink.FindStringSubmatch(msg); m != nil {
		e.EventType = "link_" + strings.ToLower(m[1])
		if m[2] != "" {
			e.InIf = m[2]
			e.set("port", m[2])
		}
		return
	}
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "topology change") || strings.Contains(l, "stp"):
		e.EventType = "stp"
	case strings.Contains(l, "poe"):
		e.EventType = "poe"
	case strings.Contains(l, "loop"):
		e.EventType = "loop_detected"
		e.Severity = min(e.Severity, 3)
	}
}

// ---------------------------------------------------------------- controller EVT_*

var (
	reEVT     = regexp.MustCompile(`\b(EVT_[A-Za-z0-9_]+)`)
	reEVTUser = regexp.MustCompile(`(?:User|Guest)\[([0-9a-fA-F:]{17})\]`)
	reEVTSSID = regexp.MustCompile(`SSID "([^"]*)"`)
)

func parseControllerEvent(e *Event, msg string) {
	m := reEVT.FindStringSubmatch(msg)
	if m == nil {
		e.Category = CatSystem
		return
	}
	code := m[1]
	e.EventType = code
	switch {
	case strings.HasPrefix(code, "EVT_WU_") || strings.HasPrefix(code, "EVT_WG_") || strings.HasPrefix(code, "EVT_LU_") || strings.HasPrefix(code, "EVT_LG_"):
		e.Category = CatClient
	case strings.HasPrefix(code, "EVT_AD_"):
		e.Category = CatAdmin
	case strings.HasPrefix(code, "EVT_IPS_"):
		e.Category = CatIPS
	case strings.HasPrefix(code, "EVT_SW_"):
		e.Category = CatSwitch
	case strings.HasPrefix(code, "EVT_VPN_"):
		e.Category = CatVPN
	default:
		e.Category = CatDevice
	}
	if u := reEVTUser.FindStringSubmatch(msg); u != nil {
		e.ClientMAC = strings.ToLower(u[1])
	}
	if s := reEVTSSID.FindStringSubmatch(msg); s != nil {
		e.SSID = s[1]
	}
}

// ---------------------------------------------------------------- Protect / Access

// extValue returns the first CEF extension whose key contains all parts
// (case-insensitive), skipping keys with any of the excluded words.
func extValue(ext map[string]string, parts ...string) string {
	keys := make([]string, 0, len(ext))
	for k := range ext {
		keys = append(keys, k)
	}
	sort.Strings(keys)
next:
	for _, k := range keys {
		l := strings.ToLower(k)
		for _, p := range parts {
			if !strings.Contains(l, p) {
				continue next
			}
		}
		if strings.HasSuffix(l, "id") || strings.HasSuffix(l, "mac") {
			continue
		}
		if v := strings.TrimSpace(ext[k]); v != "" {
			return v
		}
	}
	return ""
}

var reSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(reSlug.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func parseProtect(e *Event, name string, ext map[string]string) {
	e.Category = CatProtect
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "person"):
		e.EventType = "person_detected"
	case strings.Contains(l, "vehicle"):
		e.EventType = "vehicle_detected"
	case strings.Contains(l, "animal"):
		e.EventType = "animal_detected"
	case strings.Contains(l, "package"):
		e.EventType = "package_detected"
	case strings.Contains(l, "license plate") || strings.Contains(l, "lpr"):
		e.EventType = "plate_detected"
	case strings.Contains(l, "face"):
		e.EventType = "face_detected"
	case strings.Contains(l, "ring") || strings.Contains(l, "doorbell"):
		e.EventType = "doorbell_ring"
	case strings.Contains(l, "tamper"):
		e.EventType = "tamper"
		e.Severity = min(e.Severity, 3)
	case strings.Contains(l, "offline") || strings.Contains(l, "disconnected"):
		e.EventType = "camera_offline"
		e.Severity = min(e.Severity, 4)
	case strings.Contains(l, "online") || strings.Contains(l, "connected") || strings.Contains(l, "adopted"):
		e.EventType = "camera_online"
	case strings.Contains(l, "motion"):
		e.EventType = "motion"
	case strings.Contains(l, "record"):
		e.EventType = "recording"
	default:
		e.EventType = first(slug(name), "protect")
	}
	if ext == nil {
		return
	}
	cam := first(extValue(ext, "camera", "name"), extValue(ext, "camera"), ext["UNIFIdeviceName"], ext["dvchost"])
	e.set("camera", cam)
	if cam != "" && e.Hostname == "" {
		e.Hostname = cam
	}
	e.set("zone", first(extValue(ext, "zone"), extValue(ext, "area")))
}

func parseAccess(e *Event, name string, ext map[string]string) {
	e.Category = CatAccess
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "denied") || strings.Contains(l, "failed") || strings.Contains(l, "invalid") || strings.Contains(l, "rejected"):
		e.EventType = "access_denied"
		e.Action = "fail"
		e.Severity = min(e.Severity, 4)
	case strings.Contains(l, "forced"):
		e.EventType = "door_forced_open"
		e.Severity = min(e.Severity, 3)
	case strings.Contains(l, "held open") || strings.Contains(l, "left open"):
		e.EventType = "door_held_open"
		e.Severity = min(e.Severity, 4)
	case strings.Contains(l, "doorbell") || strings.Contains(l, "ring"):
		e.EventType = "doorbell"
	case strings.Contains(l, "granted") || strings.Contains(l, "unlock") || strings.Contains(l, "opened"):
		e.EventType = "access_granted"
		e.Action = "allow"
	case strings.Contains(l, "locked"):
		e.EventType = "door_locked"
	default:
		e.EventType = first(slug(name), "access")
	}
	if ext == nil {
		return
	}
	e.set("door", first(extValue(ext, "door", "name"), extValue(ext, "door"), extValue(ext, "location")))
	e.set("user", first(extValue(ext, "actor", "name"), extValue(ext, "actor"), ext["suser"], ext["duser"], extValue(ext, "user", "name")))
	e.set("method", first(extValue(ext, "method"), extValue(ext, "credential")))
	if e.Hostname == "" {
		e.Hostname = first(ext["UNIFIdeviceName"], ext["dvchost"])
	}
}
