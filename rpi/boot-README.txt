UniFi Syslog probe for Raspberry Pi
====================================

This SD card runs the UniFi Syslog probe: UniFi devices send their syslog
to this Pi (UDP/TCP port 514), it keeps everything on the SD card while the
main server is unreachable and forwards it encrypted (HTTPS) to the main
server.

Connect the probe to your main server in one of these ways:

1. Before the first boot: create a file named uss-probe.txt next to this
   file (on this boot partition) and paste the enrollment key into it. The
   key comes from the main server: Probes > Add probe. The probe connects by
   itself as soon as it has network.

2. After booting: open http://<address of the Pi>:8080 in a browser and
   paste the enrollment key.

3. Sign in on the Pi (console or SSH) and run: sudo unify-syslog-probe setup

Then point the UniFi devices at the Pi: in UniFi Network go to Settings >
CyberSecure > Traffic Logging, choose SIEM Server or Syslog and enter the
address of the Pi with port 514.

Updates of the probe come from the main server.
https://github.com/slisinternetservices/slis-unifi-syslog
