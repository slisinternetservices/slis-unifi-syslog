// Package unifi turns raw syslog datagrams from UniFi gateways, access points
// and switches into structured events.
package unifi

import "time"

// Categories produced by the parser.
const (
	CatFirewall = "firewall"
	CatIPS      = "ips"
	CatWiFi     = "wifi"
	CatDHCP     = "dhcp"
	CatDNS      = "dns"
	CatVPN      = "vpn"
	CatSwitch   = "switch"
	CatAuth     = "auth"
	CatAdmin    = "admin"
	CatClient   = "client"
	CatDevice   = "device"
	CatSystem   = "system"
	CatProtect  = "protect" // UniFi Protect cameras and doorbells
	CatAccess   = "access"  // UniFi Access doors and readers
)

var AllCategories = []string{CatFirewall, CatIPS, CatWiFi, CatDHCP, CatDNS, CatVPN, CatSwitch, CatAuth, CatAdmin, CatClient, CatDevice, CatSystem, CatProtect, CatAccess}

type Event struct {
	TS       time.Time
	Received time.Time
	SourceIP string
	Site     string

	Hostname      string
	DeviceModel   string
	DeviceMAC     string
	DeviceVersion string

	Facility uint8
	Severity uint8
	App      string
	PID      string

	Category  string
	EventType string
	Action    string // allow, block, alert, ...
	Rule      string

	SrcIP   string
	DstIP   string
	SrcPort uint16
	DstPort uint16
	Proto   string
	InIf    string
	OutIf   string

	ClientMAC  string
	ClientName string
	ClientIP   string
	SSID       string

	Message string
	Raw     string
	Fields  map[string]string
}

func (e *Event) set(k, v string) {
	if v == "" {
		return
	}
	if e.Fields == nil {
		e.Fields = make(map[string]string)
	}
	e.Fields[k] = v
}

var SeverityNames = []string{"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"}

// SetField stores an extra key/value on the event.
func (e *Event) SetField(k, v string) { e.set(k, v) }
