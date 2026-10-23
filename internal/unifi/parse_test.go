package unifi

import (
	"testing"
	"time"
)

var recv = time.Date(2026, 10, 17, 19, 55, 10, 0, time.UTC)

func p(t *testing.T, raw string) *Event {
	t.Helper()
	return Parse([]byte(raw), "10.0.0.1", recv, time.UTC)
}

func TestFirewallUDM(t *testing.T) {
	e := p(t, `<4>Oct 17 19:55:06 UDM-Pro kernel: [123456.789012] [WAN_LOCAL-D-2147483647] IN=eth8 OUT= MAC=74:ac:b9:11:22:33:00:31:46:aa:bb:cc:08:00 SRC=185.220.101.4 DST=84.1.2.3 LEN=44 TOS=0x00 PREC=0x00 TTL=243 ID=54321 PROTO=TCP SPT=48211 DPT=23 WINDOW=1024 RES=0x00 SYN URGP=0 MARK=1a0000`)
	if e.Hostname != "UDM-Pro" || e.App != "kernel" {
		t.Fatalf("host/app: %q %q", e.Hostname, e.App)
	}
	if e.Category != CatFirewall || e.Action != "block" || e.Fields["chain"] != "WAN_LOCAL" || e.Fields["rule_id"] != "2147483647" {
		t.Fatalf("fw: %+v", e)
	}
	if e.SrcIP != "185.220.101.4" || e.DstPort != 23 || e.Proto != "tcp" || e.InIf != "eth8" {
		t.Fatalf("fw fields: %+v", e)
	}
	if e.ClientMAC != "00:31:46:aa:bb:cc" || e.Fields["flags"] != "SYN" {
		t.Fatalf("mac/flags: %q %q", e.ClientMAC, e.Fields["flags"])
	}
	if !e.TS.Equal(time.Date(2026, 10, 17, 19, 55, 6, 0, time.UTC)) {
		t.Fatalf("ts %v", e.TS)
	}
}

func TestFirewallDescr(t *testing.T) {
	e := p(t, `<12>Oct 17 19:55:06 Gateway kernel: [CUSTOM1_LAN-B-30000] DESCR="Block IoT to LAN" IN=br20 OUT=br0 MAC=aa SRC=192.168.20.5 DST=192.168.1.10 LEN=60 PROTO=UDP SPT=5353 DPT=5353`)
	if e.Rule != "Block IoT to LAN" || e.Action != "block" || e.OutIf != "br0" {
		t.Fatalf("%+v", e)
	}
}

func TestAPHostnameAndHostapd(t *testing.T) {
	e := p(t, `<30>Oct 17 19:55:06 U7PG2,f09fc2aabbcc,v6.5.28.14491: hostapd: ath0: STA 11:22:33:44:55:66 IEEE 802.11: associated`)
	if e.DeviceModel != "U7PG2" || e.DeviceMAC != "f0:9f:c2:aa:bb:cc" || e.DeviceVersion != "v6.5.28.14491" {
		t.Fatalf("device: %+v", e)
	}
	if e.App != "hostapd" || e.Category != CatWiFi || e.EventType != "associated" || e.ClientMAC != "11:22:33:44:55:66" || e.InIf != "ath0" {
		t.Fatalf("wifi: %+v", e)
	}
}

func TestStahtd(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 U6-Pro,245a4c112233,v6.6.77: stahtd[2873]: [STA-TRACKER].stahtd_dump_event(): {"message_type":"STA_ASSOC_TRACKER","mac":"AA:BB:CC:DD:EE:FF","vap":"ra0","event_type":"soft failure","assoc_status":"0","auth_failures":"3","dns_resp_seen":"no"}`)
	if e.App != "stahtd" || e.PID != "2873" || e.EventType != "soft_failure" || e.ClientMAC != "aa:bb:cc:dd:ee:ff" || e.Fields["auth_failures"] != "3" {
		t.Fatalf("%+v", e)
	}
}

func TestCEF(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 UDM-Pro CEF:0|Ubiquiti|UniFi Network|9.0.114|400|WiFi Client Connected|1|UNIFIcategory=Monitoring UNIFIsubCategory=WiFi UNIFIhost=UDM Pro UNIFIclientAlias=Alex iPhone UNIFIclientMac=AA:BB:CC:00:11:22 UNIFIclientIp=192.168.1.50 UNIFIwifiName=Home UNIFIdeviceName=AP Living Room msg=Alex iPhone connected to Home`)
	if e.Category != CatWiFi || e.EventType != "WiFi Client Connected" {
		t.Fatalf("cef cat: %q %q", e.Category, e.EventType)
	}
	if e.ClientName != "Alex iPhone" || e.SSID != "Home" || e.ClientMAC != "aa:bb:cc:00:11:22" || e.ClientIP != "192.168.1.50" {
		t.Fatalf("cef fields: %+v", e)
	}
	if e.Fields["UNIFIdeviceName"] != "AP Living Room" || e.Message != "WiFi Client Connected: Alex iPhone connected to Home" {
		t.Fatalf("cef: %+v", e.Fields)
	}
}

func TestCEFThreat(t *testing.T) {
	e := p(t, `<10>Oct 17 19:55:06 UDM CEF:0|Ubiquiti|UniFi Network|9.1.0|201|Threat Detected and Blocked|9|UNIFIcategory=Security UNIFIsubCategory=Intrusion Prevention src=45.9.1.2 spt=4444 dst=192.168.1.5 dpt=445 proto=TCP UNIFIipsSignature=ET EXPLOIT SMB`)
	if e.Category != CatIPS || e.Action != "block" || e.Severity != 2 || e.Rule != "ET EXPLOIT SMB" || e.DstPort != 445 {
		t.Fatalf("%+v", e)
	}
}

func TestDnsmasqDHCP(t *testing.T) {
	e := p(t, `<30>Oct 17 19:55:06 UDM dnsmasq-dhcp[1234]: DHCPACK(br0) 192.168.1.20 aa:bb:cc:dd:ee:01 Alexs-MBP`)
	if e.Category != CatDHCP || e.EventType != "DHCPACK" || e.ClientIP != "192.168.1.20" || e.ClientName != "Alexs-MBP" || e.InIf != "br0" {
		t.Fatalf("%+v", e)
	}
}

func TestDNSQuery(t *testing.T) {
	e := p(t, `<30>Oct 17 19:55:06 UDM dnsmasq[1234]: query[AAAA] example.com from 192.168.1.20`)
	if e.Category != CatDNS || e.Fields["query"] != "example.com" || e.ClientIP != "192.168.1.20" {
		t.Fatalf("%+v", e)
	}
}

func TestSuricata(t *testing.T) {
	e := p(t, `<9>Oct 17 19:55:06 USG suricata[999]: [Drop] [**] [1:2027865:4] ET INFO Observed DNS Query to .cloud TLD [**] [Classification: Potentially Bad Traffic] [Priority: 2] {UDP} 192.168.1.10:53682 -> 1.1.1.1:53`)
	if e.Category != CatIPS || e.Action != "block" || e.Rule != "ET INFO Observed DNS Query to .cloud TLD" || e.SrcPort != 53682 || e.DstIP != "1.1.1.1" || e.Severity != 3 {
		t.Fatalf("%+v", e)
	}
}

func TestSwitchLink(t *testing.T) {
	e := p(t, `<13>Oct 17 19:55:06 USW24P,74acb9aabbcc,v7.1.26.15869: switch: TRAPMGR: Link Down: 0/12`)
	if e.Category != CatSwitch || e.EventType != "link_down" || e.InIf != "0/12" || e.DeviceModel != "USW24P" {
		t.Fatalf("%+v", e)
	}
}

func TestSSH(t *testing.T) {
	e := p(t, `<38>Oct 17 19:55:06 UDM sshd[555]: Failed password for invalid user admin from 203.0.113.9 port 51234 ssh2`)
	if e.Category != CatAuth || e.EventType != "login_failed" || e.SrcIP != "203.0.113.9" || e.Fields["user"] != "admin" {
		t.Fatalf("%+v", e)
	}
}

func TestNoHostname(t *testing.T) {
	e := p(t, `<13>Oct 17 19:55:06 mcad: mcad[1222]: ace_reporter.reporter_fail(): initial contact failed`)
	if e.Hostname != "" || e.App != "mcad" || e.Category != CatDevice {
		t.Fatalf("%+v", e)
	}
}

func TestRFC5424(t *testing.T) {
	e := p(t, `<165>1 2026-10-17T19:55:05.123Z udm-se ubios-udapi-server 1234 - - Internet connection restored`)
	if e.Hostname != "udm-se" || e.App != "ubios-udapi-server" || e.Message != "Internet connection restored" || e.Severity != 5 || e.Facility != 20 {
		t.Fatalf("%+v", e)
	}
}

func TestClockSkew(t *testing.T) {
	e := p(t, `<13>Jan  1 00:00:01 U7PG2,f09fc2aabbcc,v4.0.1: syswrapper: boot`)
	if !e.TS.Equal(recv) || e.Fields["device_time"] == "" {
		t.Fatalf("skew: %v %v", e.TS, e.Fields)
	}
}

func TestControllerEvent(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 unifi EVT_WU_Connected: User[aa:bb:cc:dd:ee:ff] has connected to AP[f0:9f:c2:00:00:01] with SSID "Home" on "channel 36(na)"`)
	if e.Category != CatClient || e.EventType != "EVT_WU_Connected" || e.SSID != "Home" {
		t.Fatalf("%+v", e)
	}
}

func BenchmarkParseFirewall(b *testing.B) {
	raw := []byte(`<4>Oct 17 19:55:06 UDM-Pro kernel: [WAN_LOCAL-D-2147483647] IN=eth8 OUT= MAC=74:ac:b9:11:22:33:00:31:46:aa:bb:cc:08:00 SRC=185.220.101.4 DST=84.1.2.3 LEN=44 TOS=0x00 PREC=0x00 TTL=243 ID=54321 PROTO=TCP SPT=48211 DPT=23 WINDOW=1024 RES=0x00 SYN URGP=0`)
	for i := 0; i < b.N; i++ {
		Parse(raw, "10.0.0.1", recv, time.UTC)
	}
}
