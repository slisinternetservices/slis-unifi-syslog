# SLIS UniFi Syslog Server

**SLIS Internet Services â€” 0.1.0-beta.1 (test release)**

A central log server for UniFi networks, based on [rhisijmuiden/unifi-syslog-server](https://github.com/rhisijmuiden/unifi-syslog-server).
It receives UDP/TCP syslog on port 514, parses UniFi events, stores logs in ClickHouse,
and provides a searchable web interface on port 8080. Configuration and accounts use SQLite.

## What it does

- Collect firewall, IPS, Wi-Fi, DHCP, DNS, VPN, switch and other supported UniFi events.
- Search logs, inspect dashboards, live traffic, devices and multiple sites.
- Configure retention, alerts, optional controller enrichment, GeoIP and forwarding.
- Manage users, roles, optional MFA and backups using the existing upstream features.
- Show the supplied SLIS logo, blue/cyan branding, English interface and Amsterdam timezone.

These capabilities come from upstream. Their individual integrations have not all been tested in this SLIS release.

## First installation on Proxmox

Use a **dedicated Debian 13 amd64 VM** on Proxmox VE 9.x, including the requested 9.2.21 environment.
Start with **4 vCPU, 8 GB RAM, 64 GB disk**, VirtIO network and CPU type `host`.
This is a suggested test allocation; storage requirements depend on log volume and retention.
The installer must run **inside the guest VM**, never in the Proxmox host shell.

Download/extract the test bundle, then follow [the installation guide](docs/PROXMOX.md).
The bundle contains the Linux executable and installer; no GitHub release is needed.
It needs internet access for Debian and ClickHouse packages during installation.

```bash
sudo bash install-slis.sh
```

Open `http://VM-IP:8080`. The installer creates an admin password and saves it in
`/root/unify-syslog.creds` inside the VM. Change it after signing in.
Allow TCP 8080 from administrators and UDP/TCP 514 only from your UniFi devices or VPN.
Keep ClickHouse ports private. Use the application's HTTPS settings or a TLS reverse proxy
before accessing the dashboard over untrusted networks.

## Send UniFi logs

In your UniFi Network remote logging / SIEM settings, set the VM IP and port 514.
Enable logging on the firewall rules you want to inspect. Menu names vary by UniFi version.
Optional controller API keys can enrich client and device names.

For test traffic on the VM:

```bash
unify-syslog simulate --target 127.0.0.1:514 --rate 10 --sites 1
```

Stop with Ctrl+C, then search the dashboard. See [the acceptance checklist](docs/PROXMOX.md).

## Files and maintenance

- `/etc/unify-syslog/config.yaml`: application configuration.
- `/var/lib/unify-syslog/`: metadata, secrets and application state.
- `/var/lib/clickhouse/`: log storage.
- `systemctl status unify-syslog clickhouse-server`: service status.
- `journalctl -u unify-syslog -n 100`: application diagnostics.

Back up both configuration and log storage through the Backup feature. A VM snapshot is useful
before testing upgrades but does not replace a verified backup and restore procedure.
The original binary, service and directory names are retained for upstream compatibility.

## Development and releases

Requirements: Go 1.26.8+, Node 22.12+ (or a compatible current Node version), npm and Linux for the full test suite.

```bash
cd web
npm ci
npm run build
cd ..
go test ./...
make dist VERSION=0.1.0-beta.1
```

See [CHANGELOG.md](CHANGELOG.md), [TESTING.md](docs/TESTING.md) and [UPSTREAM.md](UPSTREAM.md).
Update CHANGELOG.md with every released change. Version numbers use semantic versioning;
test releases carry a `-beta.N` suffix.

Repository: [slisinternetservices/slis-unifi-syslog](https://github.com/slisinternetservices/slis-unifi-syslog).
Download test packages from [Releases](https://github.com/slisinternetservices/slis-unifi-syslog/releases).
Installer/update URLs point to this SLIS repository. The test release workflow runs Linux tests
before creating the prerelease and installation bundle.
The upstream source snapshot contains no LICENSE file; see UPSTREAM.md for provenance and licensing status.

Advanced upstream documentation: [original README](docs/UPSTREAM-README.md).
Paths and upstream installation URLs in that archived document are historical references;
use the SLIS installation guide for this release.
