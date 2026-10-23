# Proxmox CT installation (beta.2)

Target: an unprivileged Debian 13 amd64 CT (LXC) on Proxmox VE 9.x.
The exact requested host version 9.2.21 still requires testing on your host.

## Small profile

| Resource | Setting |
| --- | --- |
| CPU | 2 cores (maximum in the SLIS provisioner) |
| RAM | 2048 MiB |
| Swap | 1024 MiB |
| Root disk | 16 GiB |
| Container | Unprivileged, no nesting required |
| Network | DHCP on vmbr0 by default |

This is a low-volume test profile for the complete server including local ClickHouse.
It is not a measured production minimum. RAM below 2 GiB is not currently supported.
The automated installation smoke test applies a 2 GiB memory limit and 2 CPU limit.
Heavy queries, GeoIP, backups or more traffic can require more memory or disk.
Swap is an emergency buffer, not a substitute for RAM.

## Create a CT from the Proxmox host

Run in the Proxmox HOST shell:

```bash
curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/ct/slis-unifi-syslog.sh -o /root/slis-ct.sh
bash /root/slis-ct.sh
```

The script allocates a free CT ID, downloads the official Debian 13 template, creates
an unprivileged CT and installs the server INSIDE it. It never installs ClickHouse on
the Proxmox host. Existing CT IDs are refused and failed CTs are retained for diagnosis.

For other storage/bridge settings:

```bash
SLIS_CTID=120 SLIS_STORAGE=local-lvm SLIS_TEMPLATE_STORAGE=local SLIS_BRIDGE=vmbr0 bash /root/slis-ct.sh
```

Choose storage and bridge names available on YOUR host. Template storage must accept
container templates; root disk storage must accept container root disks.
A DHCP server, DNS and outbound HTTPS are required. No SSH password is set by the
provisioner: administer the CT using the Proxmox console or `pct enter CTID`.

## Existing empty CT

Alternatively create a fresh Debian 13 unprivileged CT in Proxmox with the resources
above, copy/extract the beta.2 installation bundle INSIDE the CT, and run:

```bash
sudo bash install-slis.sh
```

Do not run this inner installer on the Proxmox host. Do not use it to retune an existing
beta.1 installation; install a fresh CT for this beta and preserve existing logs/backups.

## What the small profile changes

- Ingestion queue: 4096 messages, batches of 1000 and one ingestion worker.
- Go runtime: GOMEMLIMIT=192MiB (soft GC target), GOMAXPROCS=2.
- ClickHouse: 32 MiB mark cache, uncompressed cache disabled, smaller background pools.
- Query parallelism: 2 threads; tracked query memory limit: 256 MiB.
- ClickHouse tracked server memory budget: 65% of available RAM, capped around 1.3 GiB.

These are application budgets, not hard limits for total resident memory. Proxmox enforces
the CT memory limit. During bursts a small queue can drop messages; watch System status.
Start with 7 days retention under Administration > Settings for the 16 GiB disk.
The upstream 90-day default remains until you change it. Monitor storage usage.

## Acceptance checklist

1. Both `systemctl is-active unify-syslog clickhouse-server` services are active in the CT.
2. `curl -f http://127.0.0.1:8080/api/health` returns OK.
3. Sign in at `http://CT-IP:8080`, check SLIS logo and beta.2 version.
4. Send logs from one real UniFi device to UDP/TCP 514; check timestamps and device name.
5. Set retention to 7 days and restrict the syslog allowlist to your devices/VPN.
6. Observe RAM, swap, dropped packets and disk usage during a typical day.
7. Restart the CT and verify old logs remain searchable and new messages arrive.

Credentials: `/root/unify-syslog.creds` inside the CT. Change the admin password.
Logs: `/var/log/unify-syslog-install.log` and `journalctl -u unify-syslog -u clickhouse-server`.
Allow web access only from administrators; keep database ports private.
Use HTTPS before access over untrusted networks. Back up and verify restore separately.

Reference: https://pve.proxmox.com/pve-docs/
ClickHouse memory settings: https://clickhouse.com/docs/reference/settings/server-settings/settings

## Repair beta.2 checksum installation failure

The first beta.2 checksum manifests used Windows CRLF line endings. The original
installer failed to select the matching checksum line even though the downloaded
binary was intact. The repaired installer accepts both line ending formats and
still verifies the actual SHA-256 digest. Application binaries remain beta.2.

For the reported partial installation in CT 103, run on the Proxmox HOST:

```bash
pct exec 103 -- curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/deploy/install.sh -o /root/slis-install-fixed.sh
pct exec 103 -- env USS_PROFILE=ct-small USS_VERSION=v0.1.0-beta.2 USS_NONINTERACTIVE=1 bash /root/slis-install-fixed.sh
```

This resumes installation inside the existing CT. It does not destroy the CT or
reset its database/configuration. Once complete:

```bash
pct exec 103 -- systemctl is-active clickhouse-server unify-syslog
pct exec 103 -- cat /root/unify-syslog.creds
pct exec 103 -- hostname -I
```

Open http://CT-IP:8080 using the printed address and credentials.
The locale and systemd nesting warnings in the reported output did not cause
this checksum failure; ClickHouse had already started. No nesting change is
required to repair this specific failure.

For an extracted original beta.2 bundle inside a fresh CT, normalize the inner
manifest before using its original wrapper: `sed -i 's/\r$//' checksums.txt`.
Then run `sudo bash install-slis.sh`; checksum verification remains enabled.
