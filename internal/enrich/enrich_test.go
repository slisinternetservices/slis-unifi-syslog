package enrich

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/rhisijmuiden/unify-syslog-server/internal/store"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifi"
	"github.com/rhisijmuiden/unify-syslog-server/internal/unifiapi"
)

func newEnricher(t *testing.T) *Enricher {
	t.Helper()
	dir := t.TempDir()
	m, err := store.OpenMeta(filepath.Join(dir, "meta.db"), filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}
	return New(m, nil)
}

func parse(raw, site string) *unifi.Event {
	e := unifi.Parse([]byte(raw), "10.0.0.1", time.Now(), time.UTC)
	e.Site = site
	return e
}

func TestControllerNames(t *testing.T) {
	x := newEnricher(t)
	ctrl := store.Controller{ID: 1, Name: "HQ", Site: "Office"}
	snap := &unifiapi.Snapshot{
		Sites:   []unifiapi.Site{{ID: "s1"}},
		Clients: map[string][]unifiapi.ClientInfo{"s1": {{Name: "Jan's iPhone", Type: "WIRELESS", MACAddress: "A4:83:E7:11:22:33", IPAddress: "192.168.1.50"}}},
		Devices: map[string][]unifiapi.Device{"s1": {{Name: "AP Hall", Model: "U6-Pro", MACAddress: "F4:E2:C6:00:00:01", FirmwareVersion: "6.6.55"}}},
	}
	if _, _, err := x.ApplySnapshot(ctrl, snap, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Wi-Fi log from the AP, keyed by MAC.
	e := parse(`<30>Oct 17 19:55:06 U6-Pro,f4e2c6000001,v6.6.55.15189 hostapd: ath1: STA a4:83:e7:11:22:33 IEEE 802.11: associated`, "Office")
	x.Apply(e)
	if e.ClientName != "Jan's iPhone" || e.Fields["device_name"] != "AP Hall" {
		t.Fatalf("client %q device %q", e.ClientName, e.Fields["device_name"])
	}

	// The controller name wins over the DHCP hostname, which is kept.
	e = parse(`<30>Oct 17 19:55:06 UDM dnsmasq-dhcp[123]: DHCPACK(br0) 192.168.1.50 a4:83:e7:11:22:33 iPhone`, "Office")
	x.Apply(e)
	if e.ClientName != "Jan's iPhone" || e.Fields["client_hostname"] != "iPhone" {
		t.Fatalf("dhcp: %q %v", e.ClientName, e.Fields)
	}

	// Firewall logs get names for LAN addresses at the same site only.
	e = parse(`<4>Oct 17 19:55:06 UDM kernel: [LAN_IN-B-3001] IN=br0 OUT=eth8 SRC=192.168.1.50 DST=8.8.8.8 PROTO=UDP SPT=5353 DPT=53`, "Office")
	x.Apply(e)
	if e.Fields["src_name"] != "Jan's iPhone" {
		t.Fatalf("fw src_name: %v", e.Fields)
	}
	e = parse(`<4>Oct 17 19:55:06 UDM kernel: [LAN_IN-B-3001] IN=br0 OUT=eth8 SRC=192.168.1.50 DST=8.8.8.8 PROTO=UDP SPT=5353 DPT=53`, "Branch")
	x.Apply(e)
	if e.Fields["src_name"] != "" {
		t.Fatalf("name leaked to another site: %v", e.Fields)
	}
}

func TestLearnFromDHCP(t *testing.T) {
	x := newEnricher(t)
	x.Apply(parse(`<30>Oct 17 19:55:06 UDM dnsmasq-dhcp[1]: DHCPACK(br0) 192.168.1.77 3c:22:fb:00:00:01 Printer-Office`, "Office"))
	if err := x.Flush(); err != nil {
		t.Fatal(err)
	}
	// After a restart the learnt name is still known.
	y := New(x.Meta, nil)
	if err := y.Load(); err != nil {
		t.Fatal(err)
	}
	e := parse(`<30>Oct 17 19:55:06 U6-Pro,f4e2c6000001,v6.6.55 hostapd: ath0: STA 3c:22:fb:00:00:01 IEEE 802.11: disassociated`, "Office")
	y.Apply(e)
	if e.ClientName != "Printer-Office" {
		t.Fatalf("got %q", e.ClientName)
	}
	if c := y.Clients(); len(c) != 1 || c[0].Source != store.NameDHCP || c[0].IP != "192.168.1.77" {
		t.Fatalf("clients %+v", c)
	}
}
