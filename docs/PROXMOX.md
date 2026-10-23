# Proxmox test deployment

Target: Debian 13 amd64 guest on Proxmox VE 9.x. The requested host version is 9.2.21;
this package has not been boot-tested on that exact host. No access to your Proxmox server was supplied.
Reference: [official Proxmox documentation](https://www.proxmox.com/en/downloads/proxmox-virtual-environment/documentation).

## Create the guest

1. Upload a Debian 13 amd64 installer ISO to Proxmox storage and create a new VM.
2. Allocate 4 vCPU, 8 GB RAM and a 64 GB VirtIO SCSI disk for this initial test.
3. Set CPU type to `host`, enable the QEMU guest agent option and attach a VirtIO NIC to the appropriate bridge/VLAN.
4. Install Debian 13 with SSH server, a stable IP address and correct DNS/NTP.
5. In the guest, optionally install `qemu-guest-agent` and enable its service.
6. Copy the release bundle to the guest using SCP, extract it and enter its directory.

Example from a machine with the bundle:

```bash
scp slis-unifi-syslog-0.1.0-beta.1-linux-amd64.tar.gz user@VM-IP:/tmp/
```

Inside the guest:

```bash
mkdir -p ~/slis-test
cd ~/slis-test
tar -xzf /tmp/slis-unifi-syslog-0.1.0-beta.1-linux-amd64.tar.gz
sudo bash install-slis.sh
```

The wrapper verifies the bundled binary archive and installer checksums before installing.
Run on a fresh VM: it deliberately refuses an existing SLIS/upstream configuration.
The upstream installer sets up ClickHouse, a restricted service account and systemd services.
A CPU with SSE4.2 is required by the bundled installation path; the installer checks CPU flags.
Internet access is required for OS/database packages. No Go/Node compiler is needed in the VM.

## Acceptance checklist

- `systemctl is-active unify-syslog clickhouse-server` reports both active.
- `curl -f http://127.0.0.1:8080/api/health` returns an OK response.
- Open port 8080 from your management machine and sign in with generated credentials.
- Confirm SLIS logo, blue/cyan interface, English labels and version 0.1.0-beta.1.
- Start the simulator, confirm searchable logs and stop it after a minute.
- Configure one real UniFi device and verify its hostname, events and timestamps.
- Restart the VM and verify both services return and previously received logs remain searchable.
- Restrict the syslog allowlist and test that an unlisted source is rejected.
- Create a backup and verify restore on a separate disposable VM before using production data.

## Troubleshooting

Installer log: `/var/log/unify-syslog-install.log`.
Services: `journalctl -u unify-syslog -u clickhouse-server -n 100`.
No logs: check UniFi destination, source allowlist, VM bridge/VLAN, guest and Proxmox firewall rules.
CPU errors: confirm the guest sees the required physical CPU flags.
For HTTPS use the application security settings/reverse proxy; do not expose plain HTTP to the internet.

LXC scripts are inherited in the source tree, but this first SLIS test guide and bundle target a VM.
