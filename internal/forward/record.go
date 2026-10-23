package forward

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

// Record kinds.
const (
	KindEvent = "event"
	KindAlert = "alert"
	KindTest  = "test"
)

// Record is what the queue stores: a UniFi event in a neutral form. Each
// destination formats it when sending, so a changed table choice or data
// stream also applies to what is still queued.
type Record struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	TS       time.Time `json:"ts"`
	Received time.Time `json:"received"`
	SourceIP string    `json:"source_ip,omitempty"`
	Site     string    `json:"site,omitempty"`

	Hostname      string `json:"hostname,omitempty"`
	DeviceModel   string `json:"device_model,omitempty"`
	DeviceMAC     string `json:"device_mac,omitempty"`
	DeviceVersion string `json:"device_version,omitempty"`

	Facility uint8  `json:"facility"`
	Severity uint8  `json:"severity"`
	App      string `json:"app,omitempty"`
	PID      string `json:"pid,omitempty"`

	Category  string `json:"category,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Action    string `json:"action,omitempty"`
	Rule      string `json:"rule,omitempty"`

	SrcIP   string `json:"src_ip,omitempty"`
	DstIP   string `json:"dst_ip,omitempty"`
	SrcPort uint16 `json:"src_port,omitempty"`
	DstPort uint16 `json:"dst_port,omitempty"`
	Proto   string `json:"proto,omitempty"`
	InIf    string `json:"in_if,omitempty"`
	OutIf   string `json:"out_if,omitempty"`

	ClientMAC  string `json:"client_mac,omitempty"`
	ClientName string `json:"client_name,omitempty"`
	ClientIP   string `json:"client_ip,omitempty"`
	SSID       string `json:"ssid,omitempty"`

	Message string            `json:"message,omitempty"`
	Raw     string            `json:"raw,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// FromEvent converts a parsed event.
func FromEvent(e *unifi.Event) Record {
	return Record{
		ID: newID(), Kind: KindEvent, TS: e.TS.UTC(), Received: e.Received.UTC(), SourceIP: e.SourceIP, Site: e.Site,
		Hostname: e.Hostname, DeviceModel: e.DeviceModel, DeviceMAC: e.DeviceMAC, DeviceVersion: e.DeviceVersion,
		Facility: e.Facility, Severity: e.Severity, App: e.App, PID: e.PID,
		Category: e.Category, EventType: e.EventType, Action: e.Action, Rule: e.Rule,
		SrcIP: e.SrcIP, DstIP: e.DstIP, SrcPort: e.SrcPort, DstPort: e.DstPort, Proto: e.Proto, InIf: e.InIf, OutIf: e.OutIf,
		ClientMAC: e.ClientMAC, ClientName: e.ClientName, ClientIP: e.ClientIP, SSID: e.SSID,
		Message: e.Message, Raw: e.Raw, Fields: e.Fields,
	}
}

// TestRecord is sent by the Test button.
func TestRecord(instance string) Record {
	now := time.Now().UTC()
	return Record{ID: newID(), Kind: KindTest, TS: now, Received: now, Hostname: instance, Facility: 1, Severity: 6, App: "unify-syslog",
		Category: unifi.CatSystem, EventType: "test", Message: "Test event from " + instance + ": forwarding works"}
}

func (r *Record) field(k string) string { return r.Fields[k] }

func severityName(s uint8) string {
	if int(s) < len(unifi.SeverityNames) {
		return unifi.SeverityNames[s]
	}
	return ""
}

// knownFields are fields that get their own column or ECS field; the
// rest is sent as a map.
var knownFields = map[string]bool{
	"device_name": true, "src_country": true, "dst_country": true, "src_asn": true, "dst_asn": true,
	"src_as_org": true, "dst_as_org": true, "src_name": true, "dst_name": true,
}

func (r *Record) extraFields() map[string]string {
	var out map[string]string
	for k, v := range r.Fields {
		if knownFields[k] || v == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}
