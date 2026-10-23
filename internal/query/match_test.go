package query

import (
	"testing"

	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
)

func TestMatcher(t *testing.T) {
	e := &unifi.Event{Category: "firewall", Action: "block", Site: "HQ", Hostname: "UDM-Pro", SrcIP: "192.168.1.20", DstIP: "8.8.8.8",
		DstPort: 22, Proto: "tcp", Severity: 4, Message: "Connection refused by rule", Rule: "WAN_IN-4000",
		Fields: map[string]string{"dst_country": "US", "UNIFIwifiName": "Home"}}
	cases := map[string]bool{
		"":                               true,
		"category:firewall action:block": true,
		"category:FIREWALL":              true,
		"category:ips":                   false,
		"category:ips OR dport:22":       true,
		"-proto:udp dport:20..25":        true,
		"dport:>22":                      false,
		"severity:<=warning":             true,
		"severity:<=err":                 false,
		"host:udm*":                      true,
		"host:U6*":                       false,
		"src:192.168.1.0/24":             true,
		"src:10.0.0.0/8":                 false,
		"ip:8.8.8.8":                     true,
		`"connection refused"`:           true,
		`msg:"by rule"`:                  true,
		"refused -site:HQ":               false,
		"field.UNIFIwifiName:Home":       true,
		"country:us":                     true,
		"dst_country:NL":                 false,
		"rule:wan_in":                    true,
		"port:22":                        true,
		`site:HQ "not there"`:            false,
	}
	for q, want := range cases {
		m, err := CompileMatcher(q)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if got := m(e); got != want {
			t.Errorf("%q: got %v, want %v", q, got, want)
		}
	}
	if _, err := CompileMatcher("bogus:x"); err == nil {
		t.Fatal("expected error")
	}
}
