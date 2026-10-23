package unifi

import "testing"

func TestProtectCEF(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 UNVR CEF:0|Ubiquiti|UniFi Protect|4.1.53|100|Smart Detection: Person|3|UNIFIcategory=Protect UNIFIcameraName=Front Door UNIFIcameraId=abc123 UNIFIzoneName=Driveway msg=Person detected`)
	if e.Category != CatProtect || e.EventType != "person_detected" || e.App != "unifi-protect" {
		t.Fatalf("%s %s %s", e.Category, e.EventType, e.App)
	}
	if e.Fields["camera"] != "Front Door" || e.Fields["zone"] != "Driveway" {
		t.Fatalf("fields %v", e.Fields)
	}
	e = p(t, `<12>Oct 17 19:55:06 UNVR CEF:0|Ubiquiti|UniFi Protect|4.1.53|200|Camera Disconnected|5|UNIFIcameraName=Garage`)
	if e.EventType != "camera_offline" || e.Severity > 4 {
		t.Fatalf("%s %d", e.EventType, e.Severity)
	}
}

func TestAccessCEF(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 UDM CEF:0|Ubiquiti|UniFi Access|3.2.1|300|Access Denied|5|UNIFIdoorName=Main Entrance UNIFIactorName=Piet Jansen UNIFIauthMethod=NFC`)
	if e.Category != CatAccess || e.EventType != "access_denied" || e.Action != "fail" {
		t.Fatalf("%s %s %s", e.Category, e.EventType, e.Action)
	}
	if e.Fields["door"] != "Main Entrance" || e.Fields["user"] != "Piet Jansen" || e.Fields["method"] != "NFC" {
		t.Fatalf("fields %v", e.Fields)
	}
	e = p(t, `<14>Oct 17 19:55:06 UDM CEF:0|Ubiquiti|UniFi Access|3.2.1|301|Door Unlocked|2|UNIFIdoorName=Back Door`)
	if e.EventType != "access_granted" || e.Action != "allow" {
		t.Fatalf("%s %s", e.EventType, e.Action)
	}
}

func TestUniFiOSCEF(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 UDM CEF:0|Ubiquiti|UniFi OS|4.3.6|400|Admin Accessed UniFi OS|2|suser=admin src=192.168.1.10`)
	if e.App != "unifi-os" || e.Category != CatAdmin {
		t.Fatalf("%s %s", e.App, e.Category)
	}
	e = p(t, `<14>Oct 17 19:55:06 UDM CEF:0|Ubiquiti|UniFi OS|4.3.6|401|Storage Disk Warning|5|msg=Disk 2 SMART warning`)
	if e.App != "unifi-os" || e.EventType != "storage_disk_warning" {
		t.Fatalf("%s %s", e.App, e.EventType)
	}
}

func TestProtectSyslog(t *testing.T) {
	e := p(t, `<14>Oct 17 19:55:06 UNVR unifi-protect[812]: Motion detected on camera G4 Bullet`)
	if e.Category != CatProtect || e.EventType != "motion" {
		t.Fatalf("%s %s", e.Category, e.EventType)
	}
}
