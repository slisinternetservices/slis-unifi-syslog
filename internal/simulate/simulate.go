// Package simulate generates realistic UniFi syslog traffic for testing an
// installation (`unify-syslog simulate`).
package simulate

import (
	"fmt"
	"math/rand"
	"net"
	"time"
)

type device struct {
	host string
	ip   string
}

var aps = []string{"U7PG2,f09fc2a1b2c3,v6.6.77.15402", "U6-Pro,245a4c112233,v6.7.10.15511", "U6-LR,e063da445566,v6.7.10.15511", "UAP-AC-Lite,788a20778899,v6.6.77.15402"}
var switches = []string{"USW-24-PoE,74acb9aabbcc,v7.1.26.15869", "USW-Lite-8-PoE,d021f9ddeeff,v7.1.26.15869"}
var ssids = []string{"Home", "Guest", "IoT", "Office"}
var clients = []struct{ mac, name string }{
	{"a4:83:e7:11:22:33", "iPhone-Alex"}, {"3c:22:fb:44:55:66", "MacBook-Pro"}, {"b8:27:eb:77:88:99", "raspberrypi"},
	{"dc:a6:32:aa:bb:cc", "Hue-Bridge"}, {"f0:18:98:dd:ee:ff", "iPad"}, {"00:17:88:12:34:56", "Sonos-LivingRoom"},
	{"7c:d5:66:ab:cd:ef", "Galaxy-S24"}, {"18:b4:30:fe:dc:ba", "Nest-Thermostat"},
}
var attackers = []string{"185.220.101.4", "45.9.20.11", "193.32.162.77", "89.248.165.52", "141.98.10.63", "222.186.30.112", "92.63.197.153"}
var ports = []int{22, 23, 445, 3389, 80, 8080, 5060, 1433, 3306, 8443, 6379}

func pick[T any](r *rand.Rand, l []T) T { return l[r.Intn(len(l))] }

// Run sends rate messages per second to target for duration. site selects a
// simulated site (its index varies the source addresses).
func Run(target string, rate int, duration time.Duration, sites int, out func(string)) error {
	conn, err := net.Dial("udp", target)
	if err != nil {
		return err
	}
	defer conn.Close()
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	if rate < 1 {
		rate = 100
	}
	if sites < 1 {
		sites = 1
	}
	tick := time.NewTicker(time.Second / time.Duration(min(rate, 1000)))
	defer tick.Stop()
	perTick := max(1, rate/1000)
	end := time.Now().Add(duration)
	sent := 0
	for time.Now().Before(end) {
		<-tick.C
		for i := 0; i < perTick; i++ {
			conn.Write([]byte(message(r, r.Intn(sites))))
			sent++
		}
	}
	out(fmt.Sprintf("%d messages sent to %s", sent, target))
	return nil
}

func message(r *rand.Rand, site int) string {
	ts := time.Now().Format("Jan _2 15:04:05")
	gw := fmt.Sprintf("UDM-Pro-Site%d", site+1)
	lan := fmt.Sprintf("192.168.%d.%d", 10+site, 20+r.Intn(200))
	c := pick(r, clients)
	switch n := r.Intn(100); {
	case n < 35:
		chain := pick(r, []string{"WAN_LOCAL-D-2147483647", "WAN_IN-D-2147483647", "LAN_IN-A-3001", "CUSTOM1_LAN-B-30000"})
		src, dst := pick(r, attackers), fmt.Sprintf("84.%d.%d.%d", site+1, r.Intn(255), r.Intn(255))
		in, out := "eth8", ""
		if chain == "LAN_IN-A-3001" || chain == "CUSTOM1_LAN-B-30000" {
			src, dst, in, out = lan, "8.8.8.8", "br0", "eth8"
		}
		descr := ""
		if chain == "CUSTOM1_LAN-B-30000" {
			descr = `DESCR="Block IoT to LAN" `
		}
		return fmt.Sprintf(`<4>%s %s kernel: [%d.%06d] [%s] %sIN=%s OUT=%s MAC=74:ac:b9:11:22:33:%s:08:00 SRC=%s DST=%s LEN=44 TOS=0x00 PREC=0x00 TTL=%d ID=%d PROTO=%s SPT=%d DPT=%d WINDOW=1024 RES=0x00 SYN URGP=0`,
			ts, gw, r.Intn(999999), r.Intn(999999), chain, descr, in, out, c.mac, src, dst, 40+r.Intn(200), r.Intn(65535), pick(r, []string{"TCP", "TCP", "UDP"}), 1024+r.Intn(60000), pick(r, ports))
	case n < 55:
		ap := pick(r, aps)
		ev := pick(r, []string{"IEEE 802.11: associated", "IEEE 802.11: disassociated", "WPA: pairwise key handshake completed (RSN)", "IEEE 802.11: deauthenticated due to local deauth request"})
		return fmt.Sprintf(`<30>%s %s: hostapd: ath%d: STA %s %s`, ts, ap, r.Intn(3), c.mac, ev)
	case n < 62:
		ap := pick(r, aps)
		return fmt.Sprintf(`<14>%s %s: stahtd[%d]: [STA-TRACKER].stahtd_dump_event(): {"message_type":"STA_ASSOC_TRACKER","mac":"%s","vap":"ath%d","event_type":"%s","assoc_status":"0","auth_failures":"%d","dns_resp_seen":"yes"}`,
			ts, ap, 2000+r.Intn(999), c.mac, r.Intn(3), pick(r, []string{"fixup", "soft failure", "success"}), r.Intn(3))
	case n < 72:
		return fmt.Sprintf(`<30>%s %s dnsmasq-dhcp[%d]: DHCPACK(br0) %s %s %s`, ts, gw, 1000+r.Intn(999), lan, c.mac, c.name)
	case n < 78:
		return fmt.Sprintf(`<14>%s %s CEF:0|Ubiquiti|UniFi Network|9.1.120|400|WiFi Client Connected|1|UNIFIcategory=Monitoring UNIFIsubCategory=WiFi UNIFIhost=%s UNIFIclientAlias=%s UNIFIclientMac=%s UNIFIclientIp=%s UNIFIwifiName=%s UNIFIdeviceName=AP-%d msg=%s connected to %s`,
			ts, gw, gw, c.name, c.mac, lan, pick(r, ssids), r.Intn(6), c.name, pick(r, ssids))
	case n < 84:
		sig := pick(r, []string{"ET SCAN Suspicious inbound to MSSQL port 1433", "ET DROP Dshield Block Listed Source", "ET EXPLOIT Possible SMB exploit attempt", "ET POLICY DNS Query to .onion proxy Domain"})
		act := pick(r, []string{"[Drop] ", ""})
		return fmt.Sprintf(`<9>%s %s suricata[%d]: %s[**] [1:%d:%d] %s [**] [Classification: Attempted Information Leak] [Priority: %d] {TCP} %s:%d -> %s:%d`,
			ts, gw, 3000+r.Intn(99), act, 2000000+r.Intn(99999), 1+r.Intn(5), sig, 1+r.Intn(3), pick(r, attackers), 1024+r.Intn(60000), lan, pick(r, ports))
	case n < 89:
		sw := pick(r, switches)
		return fmt.Sprintf(`<13>%s %s: switch: TRAPMGR: Link %s: 0/%d`, ts, sw, pick(r, []string{"Up", "Down"}), 1+r.Intn(24))
	case n < 93:
		if r.Intn(2) == 0 {
			return fmt.Sprintf(`<38>%s %s sshd[%d]: Failed password for invalid user %s from %s port %d ssh2`, ts, gw, 4000+r.Intn(999), pick(r, []string{"admin", "root", "ubnt", "test"}), pick(r, attackers), 30000+r.Intn(30000))
		}
		return fmt.Sprintf(`<38>%s %s sshd[%d]: Accepted publickey for root from 192.168.%d.5 port %d ssh2`, ts, gw, 4000+r.Intn(999), 10+site, 30000+r.Intn(30000))
	case n < 96:
		return fmt.Sprintf(`<30>%s %s charon: 10[IKE] IKE_SA site-to-site[%d] established between 84.%d.0.1[84.%d.0.1]...84.9.0.1[84.9.0.1]`, ts, gw, r.Intn(99), site+1, site+1)
	case n < 97:
		return fmt.Sprintf(`<30>%s %s wireguard: wgsrv1: Handshake for peer %d (%d.%d.%d.%d:%d) completed`, ts, gw, r.Intn(20), pick(r, []int{77, 31, 62, 145}), r.Intn(255), r.Intn(255), 1+r.Intn(254), 30000+r.Intn(30000))
	case n < 98:
		return fmt.Sprintf(`<14>%s UNVR CEF:0|Ubiquiti|UniFi Protect|5.1.42|100|%s|3|UNIFIcameraName=%s UNIFIzoneName=%s`, ts,
			pick(r, []string{"Smart Detection: Person", "Smart Detection: Vehicle", "Motion Detected", "Doorbell Ring"}), pick(r, []string{"Front Door", "Driveway", "Garden", "Garage"}), pick(r, []string{"Entrance", "Street", "Yard"}))
	case n < 99:
		return fmt.Sprintf(`<14>%s %s CEF:0|Ubiquiti|UniFi Access|3.3.18|300|%s|%d|UNIFIdoorName=%s UNIFIactorName=%s UNIFIauthMethod=%s`, ts, gw,
			pick(r, []string{"Access Granted", "Access Granted", "Access Denied", "Door Held Open"}), 2+r.Intn(4), pick(r, []string{"Main Entrance", "Server Room", "Back Door"}), pick(r, []string{"Alex de Vries", "Sam Jansen", "Visitor"}), pick(r, []string{"NFC", "PIN", "Mobile"}))
	default:
		return fmt.Sprintf(`<13>%s %s: mcad: mcad[%d]: ace_reporter.reporter_handle_response(): inform ok`, ts, pick(r, aps), 1000+r.Intn(999))
	}
}
