package forward

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

// The output schemas below are mirrored by the bundled content in
// integrations/ (DCR template, workbook, index template); change both.

// sentinelRow is a row of the custom table UnifiSyslog_CL.
func sentinelRow(r *Record) map[string]any {
	row := map[string]any{
		"TimeGenerated": r.TS.Format(time.RFC3339Nano), "ReceivedTime": r.Received.Format(time.RFC3339Nano),
		"EventId": r.ID, "RecordType": r.Kind, "SyslogSourceIp": r.SourceIP, "Site": r.Site,
		"Hostname": r.Hostname, "DeviceName": r.field("device_name"), "DeviceModel": r.DeviceModel, "DeviceMac": r.DeviceMAC, "DeviceVersion": r.DeviceVersion,
		"Facility": r.Facility, "Severity": r.Severity, "SeverityName": severityName(r.Severity), "App": r.App,
		"Category": r.Category, "EventType": r.EventType, "Action": r.Action, "Rule": r.Rule,
		"SrcIp": r.SrcIP, "SrcPort": r.SrcPort, "SrcCountry": r.field("src_country"), "SrcAsn": r.field("src_asn"), "SrcAsOrg": r.field("src_as_org"), "SrcName": r.field("src_name"),
		"DstIp": r.DstIP, "DstPort": r.DstPort, "DstCountry": r.field("dst_country"), "DstAsn": r.field("dst_asn"), "DstAsOrg": r.field("dst_as_org"), "DstName": r.field("dst_name"),
		"Protocol": r.Proto, "InInterface": r.InIf, "OutInterface": r.OutIf,
		"ClientMac": r.ClientMAC, "ClientName": r.ClientName, "ClientIp": r.ClientIP, "Ssid": r.SSID,
		"Message": r.Message, "RawMessage": r.Raw,
	}
	if f := r.extraFields(); f != nil {
		row["Fields"] = f
	}
	return row
}

// cefSeverity maps syslog severity (0 = emergency) to CEF (10 = highest).
func cefSeverity(s uint8) int {
	return [...]int{10, 10, 9, 7, 5, 3, 1, 0}[min(s, 7)]
}

func simplifiedAction(a string) string {
	switch a {
	case "block", "drop", "reject":
		return "Deny"
	case "allow", "accept":
		return "Allow"
	case "alert":
		return "Alert"
	}
	return a
}

func (r *Record) deviceName() string {
	if n := r.field("device_name"); n != "" {
		return n
	}
	return r.Hostname
}

func (r *Record) classID() string {
	if r.Rule != "" {
		return r.Rule
	}
	return r.EventType
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// additionalExtensions lists enrichment that has no CommonSecurityLog column.
func (r *Record) additionalExtensions() string {
	var parts []string
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	for _, k := range []string{"src_country", "dst_country", "src_asn", "dst_asn", "src_as_org", "dst_as_org", "src_name", "dst_name"} {
		add(k, r.field(k))
	}
	add("event_type", r.EventType)
	add("syslog_source", r.SourceIP)
	return strings.Join(parts, ";")
}

// cslRow is a row for the standard CommonSecurityLog table.
func cslRow(r *Record) map[string]any {
	return map[string]any{
		"TimeGenerated": r.TS.Format(time.RFC3339Nano), "DeviceVendor": "Ubiquiti", "DeviceProduct": "UniFi", "DeviceVersion": r.DeviceVersion,
		"DeviceEventClassID": r.classID(), "DeviceEventCategory": r.Category, "Activity": truncate(r.Message, 512),
		"LogSeverity": strconv.Itoa(cefSeverity(r.Severity)), "DeviceAction": r.Action, "SimplifiedDeviceAction": simplifiedAction(r.Action),
		"SourceIP": r.SrcIP, "SourcePort": r.SrcPort, "DestinationIP": r.DstIP, "DestinationPort": r.DstPort, "Protocol": strings.ToUpper(r.Proto),
		"DeviceInboundInterface": r.InIf, "DeviceOutboundInterface": r.OutIf, "DeviceName": r.deviceName(), "DeviceMacAddress": r.DeviceMAC,
		"SourceMACAddress": r.ClientMAC, "Computer": r.Hostname, "DeviceCustomString1": r.Site, "DeviceCustomString1Label": "Site",
		"Message": r.Message, "AdditionalExtensions": r.additionalExtensions(),
	}
}

// toCSL reports whether a record goes to CommonSecurityLog in split mode.
func toCSL(r *Record) bool {
	return r.Kind == KindEvent && (r.Category == unifi.CatFirewall || r.Category == unifi.CatIPS)
}

// ---------------------------------------------------------------- ECS

type obj = map[string]any

// put sets a dotted ECS path, skipping empty values.
func put(o obj, path string, v any) {
	switch x := v.(type) {
	case string:
		if x == "" {
			return
		}
	case uint16:
		if x == 0 {
			return
		}
	case []string:
		if len(x) == 0 {
			return
		}
	case map[string]string:
		if len(x) == 0 {
			return
		}
	case nil:
		return
	}
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		next, ok := o[k].(obj)
		if !ok {
			next = obj{}
			o[k] = next
		}
		o = next
	}
	o[keys[len(keys)-1]] = v
}

func ecsCategory(r *Record) (kind string, cat, typ []string) {
	kind = "event"
	switch {
	case r.Kind == KindAlert:
		return "alert", []string{"threat"}, []string{"indicator"}
	case r.Category == unifi.CatFirewall:
		t := "allowed"
		if r.Action == "block" || r.Action == "drop" || r.Action == "reject" {
			t = "denied"
		}
		return kind, []string{"network"}, []string{"connection", t}
	case r.Category == unifi.CatIPS:
		t := "info"
		if r.Action == "block" {
			t = "denied"
		}
		return "alert", []string{"intrusion_detection", "network"}, []string{t}
	case r.Category == unifi.CatAuth:
		return kind, []string{"authentication"}, []string{"start"}
	case r.Category == unifi.CatVPN:
		return kind, []string{"network"}, []string{"connection"}
	case r.Category == unifi.CatWiFi, r.Category == unifi.CatDHCP, r.Category == unifi.CatDNS, r.Category == unifi.CatSwitch, r.Category == unifi.CatClient:
		return kind, []string{"network"}, []string{"info"}
	case r.Category == unifi.CatAdmin:
		return kind, []string{"configuration"}, []string{"change"}
	case r.Category == unifi.CatAccess:
		return kind, []string{"authentication"}, []string{"access"}
	}
	return kind, []string{"host"}, []string{"info"}
}

// dataStreamParts splits logs-<dataset>-<namespace>.
func dataStreamParts(name string) (typ, dataset, ns string, ok bool) {
	parts := strings.Split(name, "-")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// ecsDoc formats a record in the Elastic Common Schema. dataStream sets the
// data_stream fields when it follows the type-dataset-namespace scheme.
func ecsDoc(r *Record, dataStream string) obj {
	d := obj{"@timestamp": r.TS.Format(time.RFC3339Nano)}
	kind, cat, typ := ecsCategory(r)
	put(d, "event.created", r.Received.Format(time.RFC3339Nano))
	put(d, "event.id", r.ID)
	put(d, "event.kind", kind)
	put(d, "event.module", "unifi")
	put(d, "event.dataset", "unifi.syslog")
	put(d, "event.category", cat)
	put(d, "event.type", typ)
	put(d, "event.action", r.EventType)
	put(d, "event.severity", int(r.Severity))
	put(d, "event.original", r.Raw)
	if r.Category == unifi.CatAuth {
		switch r.Action {
		case "fail":
			put(d, "event.outcome", "failure")
		case "allow":
			put(d, "event.outcome", "success")
		}
	}
	if t, ds, ns, ok := dataStreamParts(dataStream); ok {
		put(d, "data_stream.type", t)
		put(d, "data_stream.dataset", ds)
		put(d, "data_stream.namespace", ns)
	}
	put(d, "message", r.Message)
	put(d, "log.level", severityName(r.Severity))
	put(d, "log.syslog.facility.code", int(r.Facility))
	put(d, "log.syslog.severity.code", int(r.Severity))
	put(d, "log.syslog.severity.name", severityName(r.Severity))
	put(d, "log.syslog.appname", r.App)
	put(d, "log.syslog.procid", r.PID)
	put(d, "log.syslog.hostname", r.Hostname)

	put(d, "observer.vendor", "Ubiquiti")
	put(d, "observer.product", "UniFi")
	put(d, "observer.hostname", r.Hostname)
	put(d, "observer.name", r.deviceName())
	put(d, "observer.ip", r.SourceIP)
	put(d, "observer.mac", r.DeviceMAC)
	put(d, "observer.version", r.DeviceVersion)
	put(d, "observer.ingress.interface.name", r.InIf)
	put(d, "observer.egress.interface.name", r.OutIf)

	for _, side := range []struct {
		ecs, f, ip string
		port       uint16
	}{{"source", "src", r.SrcIP, r.SrcPort}, {"destination", "dst", r.DstIP, r.DstPort}} {
		put(d, side.ecs+".ip", side.ip)
		put(d, side.ecs+".port", side.port)
		put(d, side.ecs+".domain", r.field(side.f+"_name"))
		put(d, side.ecs+".geo.country_iso_code", r.field(side.f+"_country"))
		if n, err := strconv.ParseInt(strings.TrimPrefix(r.field(side.f+"_asn"), "AS"), 10, 64); err == nil {
			put(d, side.ecs+".as.number", n)
		}
		put(d, side.ecs+".as.organization.name", r.field(side.f+"_as_org"))
	}
	put(d, "network.transport", strings.ToLower(r.Proto))
	put(d, "rule.name", r.Rule)
	put(d, "process.name", r.App)
	if n, err := strconv.ParseInt(r.PID, 10, 64); err == nil {
		put(d, "process.pid", n)
	}
	tags := []string{"unifi"}
	if r.Category != "" {
		tags = append(tags, r.Category)
	}
	put(d, "tags", tags)

	put(d, "unifi.site", r.Site)
	put(d, "unifi.category", r.Category)
	put(d, "unifi.event_type", r.EventType)
	put(d, "unifi.action", r.Action)
	put(d, "unifi.ssid", r.SSID)
	put(d, "unifi.record_type", r.Kind)
	put(d, "unifi.device.name", r.field("device_name"))
	put(d, "unifi.device.model", r.DeviceModel)
	put(d, "unifi.device.mac", r.DeviceMAC)
	put(d, "unifi.client.mac", r.ClientMAC)
	put(d, "unifi.client.name", r.ClientName)
	put(d, "unifi.client.ip", r.ClientIP)
	put(d, "unifi.fields", r.extraFields())
	return d
}

// ---------------------------------------------------------------- syslog

func sdEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `]`, `\]`).Replace(s)
}

func syslogToken(s string, max int) string {
	if s == "" {
		return "-"
	}
	var b strings.Builder
	for _, c := range s {
		if c > 32 && c < 127 {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= max {
			break
		}
	}
	return b.String()
}

func (r *Record) pairs() [][2]string {
	p := [][2]string{
		{"site", r.Site}, {"category", r.Category}, {"event", r.EventType}, {"action", r.Action}, {"rule", r.Rule},
		{"src", r.SrcIP}, {"dst", r.DstIP}, {"proto", r.Proto}, {"in", r.InIf}, {"out", r.OutIf},
		{"client_mac", r.ClientMAC}, {"client_name", r.ClientName}, {"client_ip", r.ClientIP}, {"ssid", r.SSID},
		{"device_model", r.DeviceModel}, {"device_mac", r.DeviceMAC}, {"firmware", r.DeviceVersion}, {"source_ip", r.SourceIP},
	}
	if r.SrcPort != 0 {
		p = append(p, [2]string{"sport", strconv.Itoa(int(r.SrcPort))})
	}
	if r.DstPort != 0 {
		p = append(p, [2]string{"dport", strconv.Itoa(int(r.DstPort))})
	}
	keys := make([]string, 0, len(r.Fields))
	for k := range r.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p = append(p, [2]string{k, r.Fields[k]})
	}
	return p
}

// rfc5424 formats a record as an RFC 5424 message with the UniFi fields as
// structured data (SD-ID unifi@32473; 32473 is the example enterprise
// number reserved by RFC 5612).
func rfc5424(r *Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%d>1 %s %s %s %s %s ", int(r.Facility)*8+int(r.Severity), r.TS.Format("2006-01-02T15:04:05.000Z07:00"),
		syslogToken(r.Hostname, 255), syslogToken(r.App, 48), syslogToken(r.PID, 128), syslogToken(r.EventType, 32))
	b.WriteString("[unifi@32473")
	for _, kv := range r.pairs() {
		if kv[1] == "" {
			continue
		}
		name := syslogToken(strings.NewReplacer("=", "_", `"`, "_", "]", "_").Replace(kv[0]), 32)
		fmt.Fprintf(&b, ` %s="%s"`, name, sdEscape(kv[1]))
	}
	b.WriteString("] ")
	b.WriteString(r.Message)
	return b.String()
}

func cefHeaderEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `|`, `\|`, "\n", " ", "\r", " ").Replace(s)
}

func cefExtEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `=`, `\=`, "\n", `\n`, "\r", `\r`).Replace(s)
}

// cef formats a record as a CEF message inside an RFC 5424 envelope, for
// tools such as QRadar and ArcSight.
func cef(r *Record) string {
	name := r.Message
	if r.Rule != "" && r.Category == unifi.CatFirewall {
		name = r.Rule + " " + r.Action
	}
	var ext []string
	add := func(k, v string) {
		if v != "" && v != "0" {
			ext = append(ext, k+"="+cefExtEscape(v))
		}
	}
	add("rt", strconv.FormatInt(r.TS.UnixMilli(), 10))
	add("src", r.SrcIP)
	add("spt", strconv.Itoa(int(r.SrcPort)))
	add("dst", r.DstIP)
	add("dpt", strconv.Itoa(int(r.DstPort)))
	add("proto", strings.ToUpper(r.Proto))
	add("act", r.Action)
	add("deviceInboundInterface", r.InIf)
	add("deviceOutboundInterface", r.OutIf)
	add("smac", r.ClientMAC)
	add("shost", r.ClientName)
	add("dvchost", r.Hostname)
	add("dvcmac", r.DeviceMAC)
	add("cat", r.Category)
	if r.Site != "" {
		add("cs1Label", "Site")
		add("cs1", r.Site)
	}
	if r.SSID != "" {
		add("cs2Label", "SSID")
		add("cs2", r.SSID)
	}
	add("msg", r.Message)
	body := fmt.Sprintf("CEF:0|Ubiquiti|UniFi|%s|%s|%s|%d|%s", cefHeaderEscape(r.DeviceVersion), cefHeaderEscape(r.classID()),
		cefHeaderEscape(truncate(name, 512)), cefSeverity(r.Severity), strings.Join(ext, " "))
	return fmt.Sprintf("<%d>1 %s %s %s %s %s - %s", int(r.Facility)*8+int(r.Severity), r.TS.Format("2006-01-02T15:04:05.000Z07:00"),
		syslogToken(r.Hostname, 255), syslogToken(r.App, 48), syslogToken(r.PID, 128), syslogToken(r.EventType, 32), body)
}
