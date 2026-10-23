# UniFi Syslog Server

A central syslog server built specifically for UniFi. All UniFi sites send their syslog to one server, which recognizes firewall, IPS, Wi-Fi, DHCP, DNS, VPN, switch, Protect, Access and management messages, enriches them with names, GeoIP and site information, and makes them searchable in ready-made dashboards. Selected events can be forwarded to **Microsoft Sentinel**, **Elastic**, **Splunk**, **Graylog** and other tools.

![Overview dashboard](docs/screenshots/dashboard.png)

**Contents**: [Features](#features) · [Notification channels](#notification-channels) · [Forwarding to Sentinel, Elastic and other tools](#forwarding-to-sentinel-elastic-and-other-tools) · [Remote probes](#remote-probes) · [Feature tour](#feature-tour) · [Installation](#installation) · [Configuring UniFi](#configuring-unifi) · [Users, roles and security](#users-roles-and-security) · [Query language](#query-language) · [Branding](#branding) · [Languages](#languages) · [Backup and restore](#backup-and-restore) · [Updates](#updates-release-channels-and-rollback) · [Command line](#command-line) · [Building from source](#building-from-source) · [Architecture](#architecture)

## Features

**Collect and understand**

- **Receiver**: UDP and TCP on port 514 (RFC3164, RFC5424, RFC6587 framing), with an optional [source allowlist](#users-roles-and-security).
- **UniFi parser**: device name `model,mac,version`, iptables rule tags (`WAN_IN-D-3001`, `DESCR="..."`), CEF from UniFi Network SIEM, hostapd/stahtd, dnsmasq/kea DHCP, DNS, Suricata IPS, WireGuard/OpenVPN/IPsec, SSH logins, switch ports and `EVT_*` controller codes. UniFi Protect (cameras, smart detections, doorbells), UniFi Access (doors, granted and denied access) and UniFi OS system events (CEF) get their own categories.
- **UniFi Network API**: connect one or more consoles with an API key. Client and device names are attached to log lines, so you see "Alex's iPhone" and "AP Attic" instead of MAC addresses. Without a controller, names are learnt from DHCP logs. All known names are listed under **Clients**.
- **GeoIP**: country and AS number (provider) of external IP addresses from DB-IP Lite (no account) or MaxMind GeoLite2, updated automatically, with a world map on the dashboards.
- **Storage**: ClickHouse (columnar storage, compression, billions of rows), daily partitions and a configurable retention period, also [per category](#retention-per-category) (for example Wi-Fi 30 days and IPS a year). Metadata in SQLite. ClickHouse can run on the same machine, on a [separate server or a cluster](#scale-and-reliability), with older logs on cheap S3 storage.

**Look and search**

- **Overview dashboard**: messages per category, blocked traffic and threats per country, blocked sources and ports, IPS signatures, Wi-Fi clients, SSIDs, DHCP clients, devices, sites and the status of the remote probes. Click through to the logs.
- **Ready-made dashboards**: Firewall, Wi-Fi (roaming and connection problems), DHCP, VPN (WireGuard, Teleport and site-to-site) and Threats (IPS), each filterable per site and period.
- **Search**: a dedicated [query language](#query-language) with fields, CIDR, ranges and wildcards, a histogram, live tail, and export to CSV and JSON.
- **Saved searches**: save a search with its time range, keep it private or share it with everyone, and pin it as a shortcut in the menu. See [below](#saved-searches).
- **Categories**: your own categories (a saved search with a color) to filter logs, with counters on the Categories page.

**Sites and devices**

- **Sites**: map source IPs to a site, manually or via IP ranges. Sites behind NAT work too. An overview shows, per site, the status, devices and logs, blocks, IPS alerts and errors of the last 24 hours.
- **Devices**: a persistent inventory of all UniFi devices (by MAC address, so also behind NAT), with a custom name and notes, firmware per model and updated firmware. Devices that have not sent logs for too long are marked as *silent* and, optionally, reported by email, as is their return.
- **Firmware warnings** flag devices that run older firmware than other devices of the same model, or for which UniFi Network offers an update, optionally by email.

**Act on events**

- **Forwarding** to Microsoft Sentinel, Elasticsearch / Elastic Cloud / OpenSearch, Logstash, syslog (Graylog, Wazuh, QRadar…), Splunk HEC and JSON webhooks, with per-destination filters, an on-disk queue, retries, volume and cost statistics, and ready-made Sentinel and Kibana content. See [below](#forwarding-to-sentinel-elastic-and-other-tools).
- **Alerts**: notify when a search exceeds a threshold, gets fewer events than expected (a site or device that went quiet), has many different values (a port scan) or spikes compared with normal, for the whole search or per value of a field, on a schedule if you want. By email, in your chat and messaging channels, and optionally as an alert record to Microsoft Sentinel or another forwarding destination. Each rule has a severity (info, warning, critical) and a *Test now* button. See [below](#alert-rules).
- **Notification channels**: Microsoft Teams, Slack, Telegram, SMS via MessageBird / Bird, Pushover and JSON webhooks next to email, with channels per alert rule, a minimum severity per channel, quiet hours, batching against alert storms and a test button. Silent devices, offline probes, outdated firmware and backups can go there too. See [below](#notification-channels).

**Administration and security**

- **Microsoft Entra SSO** (OIDC + PKCE), with Entra groups mapped to roles and sites automatically.
- **Roles and permissions**: administrator, analyst and read-only viewer, each limited to one or more sites if you want, so customers or branch offices only see their own logs.
- **Two-factor authentication** (TOTP) for local accounts, optionally mandatory, with recovery codes.
- **Source allowlist** that only accepts syslog from known IP addresses or networks, and lists rejected senders.
- **Audit log** of who signed in, viewed, changed, exported or restored what.
- **System status**: messages per second, dropped packets, disk space and ClickHouse health on one page, with a notification when something goes wrong. See [below](#system-status).
- **REST API** with personal API tokens (read-only or read and write) for scripts and integrations, see [docs/API.md](docs/API.md).
- **Mail**: your own SMTP server with STARTTLS or TLS, minimum TLS 1.2/1.3, and PLAIN, LOGIN, CRAM-MD5, SCRAM-SHA-256 or OAuth2 (Microsoft 365).
- **Backup**: full and incremental backups to FTP, FTPS, SCP, SFTP, S3 or a local folder, optionally encrypted with a passphrase, with scheduling, retention policy and restore from the remote location.
- **Branding**: your company name and logo (separate light and dark mode variants) and favicon in the menu, on the sign-in page and in e-mails.
- **Languages**: English, Dutch, German and French, per user. Entra SSO users get the language of their Microsoft account. E-mails are sent in the language of the recipient.
- **Updates**: the Info button shows the version, checks for updates and installs them from the interface. Choose the **Production** channel (stable releases) or **Test** (pre-releases), and roll back to a previous version with one click.
- **Remote probes**: a small probe at each remote location receives syslog on port 514, buffers it on disk during outages and forwards it over HTTPS. Install it as a Proxmox LXC, on Debian 13 or from a Raspberry Pi image, and update it from the main server. See [below](#remote-probes).
- **Monitoring**: Prometheus metrics, alert rules and Grafana dashboards, including a read-only Grafana data source for the logs. See [below](#scale-and-reliability).
- **High availability**: two servers behind one virtual IP with keepalived, sharing one ClickHouse and one configuration. See [below](#scale-and-reliability).
- **Deployment**: a Proxmox LXC script (Debian 13) and an installer for Debian 13 VMs and physical servers. The web interface runs on **port 8080**.

See the [roadmap](ROADMAP.md) for what is planned next.

## Alert rules

Alert rules live under **Configuration → Alerts**. Each rule is a search plus a condition within a time window:

| Condition | Fires when | Example |
|---|---|---|
| **At least a number of events** | the search matches at least *N* events | 20 failed SSH logins in 5 minutes |
| **Fewer events than expected** | the search matches fewer than *N* events | a site that sent no logs for 15 minutes |
| **Many different values** | a field has at least *N* different values | 25 destination ports from one source IP: a port scan |
| **Spike compared with normal** | the window has *X*% more events than the average window of the last hours (at least a quarter of that history is needed), and at least *N* | blocked traffic 300% above normal |

- **Per value**: evaluate the rule per source IP, site, device, client, SSID or another field. The alert lists every value that matches, for example each IP address that is scanning. With *fewer events than expected*, every value seen in the last hours that went quiet is reported.
- **Schedule**: only evaluate on some weekdays or between two times (server time), for example sign-ins between 18:00 and 08:00.
- **Test now** evaluates the rule against the current logs without sending anything, and shows the matches or the highest value.
- A **cooldown** keeps a rule from firing again too soon; a severity and channels decide who hears about it.

| | |
|---|---|
| ![Alert rules](docs/screenshots/alerts-rules.png)<br>**Alert rules** with their condition, schedule and channels. | ![Editing an alert rule](docs/screenshots/alerts-edit.png)<br>**A rule** that counts destination ports per source IP, with the result of *Test now*. |

## Saved searches

On the **Logs** page, *Save search* stores the search with its time range and site. **Saved searches** lists your own searches and the ones shared with everyone:

- **Private or shared**: everyone can save searches for themselves; analysts and administrators can share them with all users. Only the owner and administrators can change a shared search.
- **In the menu**: pin a search and it appears as a shortcut under *Saved searches* in the menu on the left. Pins are personal, so every user picks their own.
- **Copy link** and **Create alert from this search** (administrators) on every search.

![Saved searches with shortcuts in the menu](docs/screenshots/saved-searches.png)

## Retention per category

Under **Administration → Settings**, *Retention per category* keeps some categories shorter or longer than the default retention, for example Wi-Fi 30 days, the firewall 90 days and IPS a year. The page shows how many messages each category holds and how old the oldest one is. ClickHouse removes expired logs in the background with its TTL rules, usually within a few hours of saving.

![Retention per category](docs/screenshots/retention.png)

## System status

**Administration → System status** shows the health of the server itself and refreshes every few seconds:

- **Messages per second** received and stored, with a chart of the last hour and the peak.
- **Dropped and rejected**: messages dropped because the ingest queue was full, packets the kernel dropped because the UDP receive buffer was full (`/proc/net/snmp`), senders rejected by the allowlist and failed inserts.
- **Disk space** of every ClickHouse disk and the server's data directory.
- **ClickHouse**: response time, version, uptime, stored rows, size and compression, data parts, merges, mutations and memory.
- **Components**: remote probes, forwarding destinations, notification channels and the last backup.

When a problem starts (less than 15% or 5% disk space free, ClickHouse unreachable, failing inserts, dropped packets, too many data parts) it is shown at the top of the page and sent to the notification channels that subscribe to *System status* and by e-mail to the default recipient; when it is over, a short *resolved* message follows.

![System status](docs/screenshots/status.png)

## Notification channels

Get alerts where your team already is. Add channels under **Configuration → Notification channels**:

| Channel | How it connects |
|---|---|
| **Microsoft Teams** | Adaptive Card in a channel through a Workflows webhook ("Post to a channel when a webhook request is received") |
| **Slack** | Incoming webhook, or a bot token with `chat:write` |
| **Telegram** | Bot that posts to a chat, group or forum topic |
| **SMS** | MessageBird SMS API or the Bird channels API, to one or more phone numbers |
| **Pushover** | Push notifications to phones and desktops, with priority by severity |
| **Webhook** | JSON POST to any HTTPS endpoint, optionally signed with HMAC-SHA256 |

- **Channels per rule**: each alert rule picks its channels and its severity, for example Teams for everything and SMS only for critical alerts. E-mail can be switched off per rule.
- **System events**: silent devices, offline probes, outdated firmware and backups go to every channel that subscribes to them.
- **Minimum severity** per channel, so an SMS channel only gets critical notifications.
- **Quiet hours**: notifications are held at night and sent as one summary in the morning; critical ones can still go out right away.
- **Batching**: notifications within a few minutes become one message, so an alert storm does not flood your phone.
- **Reliable**: notifications wait in the database and are retried with back-off; the page shows the last delivery, errors and what is waiting. Every channel has a test button.
- **Language** per channel: English, Dutch, German or French.

| | |
|---|---|
| ![Notification channels](docs/screenshots/notifications.png)<br>**Notification channels** with their subscriptions, quiet hours, batching, last delivery and errors. | ![Editing a channel](docs/screenshots/notifications-edit.png)<br>**A channel** with its connection, minimum severity, system events, language, quiet hours and batching. |

See [docs/NOTIFICATIONS.md](docs/NOTIFICATIONS.md) for the setup per channel.

## Forwarding to Sentinel, Elastic and other tools

UniFi Syslog Server can send a filtered copy of your UniFi events to the security and log tools you already use, in parallel with storing them locally. Add destinations under **Configuration → Forwarding**.

![Forwarding page with five destinations](docs/screenshots/forwarding.png)

The page shows, per destination, the events and bytes of the last 24 hours, the estimated volume per day (and, with a price per GB, the estimated monthly cost), the queue, errors, the last delivery and a 24-hour sparkline. The panel on the right offers the ready-made content for Sentinel, Elastic and Logstash as downloads.

### Destinations

| Destination | How it is sent | Format |
|---|---|---|
| **Microsoft Sentinel** | Azure Monitor Logs Ingestion API through a data collection endpoint and rule. Entra app registration with a client secret or certificate. | Custom table `UnifiSyslog_CL`; in *split* mode firewall and IPS events go to `CommonSecurityLog` |
| **Elasticsearch / Elastic Cloud / OpenSearch** | Bulk API into a data stream (default `logs-unifi.syslog-default`), API key or basic auth. **Install index template** puts the mappings in place with one click. | Elastic Common Schema (ECS) |
| **Logstash** | JSON lines over TCP or TLS, or a JSON array over HTTP | ECS |
| **Syslog** | UDP, TCP or TLS, octet-counting (RFC 6587) or newline framing. For Graylog, Wazuh, QRadar, ArcSight and others. | RFC 5424 with structured data, or CEF |
| **Splunk** | HTTP Event Collector (HEC) with a HEC token, index and sourcetype | JSON events |
| **JSON webhook** | HTTPS POST of a JSON array, with your own headers (for example `Authorization`) | ECS or the app's native field names |

![Choosing a destination type](docs/screenshots/forwarding-add.png)

### Filters: send only what matters

Each destination has its own filter. An event is forwarded only when it matches **all** parts:

- **Categories** (firewall, IPS / threats, Wi-Fi, DHCP, switch, VPN, sign-in, admin, DNS, clients, devices, system, Protect, Access). None selected is all.
- **Sites** and **devices** (hostname, MAC address or device name). Empty is all.
- **Minimum severity**, for example *warning or worse*.
- **Saved search**: one of your [categories](#features).
- **Extra filter** in the [query language](#query-language), for example `-category:dns` or `country:RU`.

Because only matching events leave the server, you only pay for relevant data in Sentinel or Elastic.

| | |
|---|---|
| ![Microsoft Sentinel destination](docs/screenshots/forwarding-sentinel.png)<br>**Microsoft Sentinel**: tenant and client ID, client secret or certificate, the logs ingestion endpoint and DCR immutable ID, and the table mode. Here: firewall, IPS, VPN and sign-in events, with a price per GB for the cost estimate. | ![Elastic Cloud destination](docs/screenshots/forwarding-elastic.png)<br>**Elastic Cloud**: URL, API key and data stream, *Install index template*, and an extra filter that skips DNS. |
| ![Syslog destination for Graylog](docs/screenshots/forwarding-syslog.png)<br>**Syslog to Graylog**: RFC 5424 over TCP with octet counting, only for two sites and only *warning or worse*. | ![Alert rules with channels and forwarding destinations](docs/screenshots/alerts.png)<br>**Alerts** have a severity and go to e-mail, notification channels, and Sentinel or a webhook. |

### Reliable delivery

- Every destination has its **own queue on disk** (`/var/lib/unify-syslog/forward/<id>`) and its own delivery loop, so a destination that is down never slows down ingestion or the other destinations. Events survive restarts and outages.
- Events go out in **batches** that suit the destination, and failed sends are **retried** with exponential backoff (up to 5 minutes between attempts) until they succeed. A payload that is too large is split.
- The **maximum queue size** is configurable per destination; beyond it the oldest events are dropped and counted.
- **Send test event** checks the settings before you save them.

### Ready-made content

The [integrations](integrations) folder, also downloadable from the Forwarding page, contains:

- **Microsoft Sentinel**: an ARM template for the custom table, data collection endpoint and rule (with the role assignment for the app), KQL hunting queries, a workbook and analytics rules.
- **Elastic**: the index template (also installed with one click from the interface), Kibana saved objects (data view, searches, visualizations and a dashboard) and Elastic Security detection rules.
- **Logstash**: a sample pipeline.

See [docs/FORWARDING.md](docs/FORWARDING.md) for the setup per tool.

## Remote probes

Sites with an unreliable link, or sites you do not want to open port 514 to, can use a **remote probe**. The probe runs at the site, receives the UniFi syslog on UDP/TCP 514, keeps it on its own disk and forwards it to the main server over HTTPS on port 8080. During an outage it keeps buffering (1 GB and 7 days by default), and afterwards it delivers every message exactly once. Messages from a probe get the probe's site and the field `probe`.

Add a probe under **Configuration → Remote probes → Add probe** and copy the enrollment key. Then install the probe in one of three ways:

```bash
# Proxmox LXC (Debian 13), on the Proxmox host
USS_PROBE_KEY='uss1_…' bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/unifi-syslog-server/main/ct/unify-syslog-probe.sh)"

# Debian 13 VM or physical server, as root
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/unifi-syslog-server/main/deploy/install-probe.sh | USS_PROBE_KEY='uss1_…' bash
```

For a **Raspberry Pi** (3, 4, 5 or Zero 2 W), write `unify-syslog-probe-rpi-arm64.img.xz` from the release with Raspberry Pi Imager. Put the key in `uss-probe.txt` on the boot partition, or paste it on the probe's web page at `http://<pi>:8080`.

![Remote probes page](docs/screenshots/probes.png)

The Remote probes page shows the status, rate, buffer, devices and resources of every probe, and the main server can email you when a probe goes offline or its buffer fills up. Probes are updated through the main server, automatically to the server's version or by hand, and roll back by themselves when an update fails. The overview dashboard shows a summary of all probes as well. See [docs/PROBES.md](docs/PROBES.md) for all details, including how to use your own HTTPS certificate.

| | |
|---|---|
| ![Installing a probe](docs/screenshots/probes-install.png)<br>**Add probe** shows the enrollment key with ready-to-paste commands for the Proxmox LXC, Debian 13 and the Raspberry Pi image. | ![The probe's own web page](docs/screenshots/probe-page.png)<br>**The probe's own page** on port 8080 shows its connection, rate, buffer, devices and disk. All settings are managed on the main server. |

## Scale and reliability

For larger environments the logs can move off the syslog server, old logs can move to cheap object storage, and two servers can stand in for each other.

- **External or clustered ClickHouse**: keep the logs on a separate ClickHouse server, installed with `deploy/install-clickhouse.sh`, or on a ClickHouse cluster with replicated tables. Switch an existing installation over under **Administration → Storage**, with the stored logs copied along. See [docs/STORAGE.md](docs/STORAGE.md).
- **Cold storage**: logs older than a number of days move to S3-compatible storage (MinIO, Wasabi, AWS S3) with ClickHouse tiered storage, and stay searchable.
- **More backup targets**: SFTP and S3 next to FTP, FTPS, SCP and a local folder, and backups encrypted with a passphrase ([age](https://age-encryption.org)). Restore works from every target.
- **Monitoring**: `/metrics` for Prometheus, bundled alert rules, and Grafana dashboards for server health and for the logs through a read-only ClickHouse endpoint. See [docs/MONITORING.md](docs/MONITORING.md).
- **High availability**: two servers behind one virtual IP with keepalived. The standby copies all settings from the active node, scheduled jobs run once, and the page generates the keepalived configuration. See [docs/HA.md](docs/HA.md).

![Storage page with cold storage](docs/screenshots/storage.png)

| | |
|---|---|
| ![High availability](docs/screenshots/ha-active.png)<br>**High availability**: both nodes with their role, the settings copy and the keepalived command for this node. | ![Metrics & Grafana](docs/screenshots/metrics-grafana.png)<br>**Metrics & Grafana**: access to `/metrics` and the log store, with the scrape configuration, alert rules and dashboards to download. |
| ![Switching to another ClickHouse](docs/screenshots/storage-connection.png)<br>**Switching ClickHouse**: test the connection, optionally a cluster, and copy the stored logs along. | ![S3 backup target](docs/screenshots/backup-s3.png)<br>**S3 backup target**: MinIO, Wasabi or AWS, next to SFTP, SCP, FTPS and FTP. |

## Feature tour

The screenshots show simulated UniFi traffic from five sites (`unify-syslog simulate`), four of them through remote probes, with the *Northwind IT* branding as an example.

### Dashboards

![Firewall dashboard with GeoIP map](docs/screenshots/dashboard-firewall.png)

| | |
|---|---|
| ![Wi-Fi dashboard](docs/screenshots/dashboard-wifi.png)<br>**Wi-Fi**: connections, roaming and the clients with connection problems. | ![Threats (IPS) dashboard](docs/screenshots/dashboard-ips.png)<br>**Threats (IPS)**: blocked and detected threats, severity and source countries. |
| ![VPN dashboard](docs/screenshots/dashboard-vpn.png)<br>**VPN**: WireGuard, Teleport and site-to-site tunnels. | ![Light mode](docs/screenshots/dashboard-light.png)<br>**Light mode**, next to the default dark mode. |

### Logs, sites and devices

| | |
|---|---|
| ![Logs and search](docs/screenshots/logs.png)<br>**Logs**: search with the query language, histogram, live tail and CSV/JSON export. | ![Sites](docs/screenshots/sites.png)<br>**Sites**: status per site over the last 24 hours, networks per site and sender mapping. |
| ![Devices](docs/screenshots/devices.png)<br>**Devices**: inventory of gateways, switches and access points with firmware per model and silent-device monitoring. | ![Clients](docs/screenshots/clients.png)<br>**Clients**: names from UniFi Network or learnt from DHCP. |
| ![Categories](docs/screenshots/categories.png)<br>**Categories**: saved searches with a color and a counter, used as filters. | ![GeoIP](docs/screenshots/geoip.png)<br>**GeoIP**: DB-IP Lite or MaxMind GeoLite2, updated automatically. |

### Administration and security

| | |
|---|---|
| ![Users](docs/screenshots/users.png)<br>**Users**: roles, sites per user, 2FA status and language. | ![Microsoft Entra SSO](docs/screenshots/sso.png)<br>**Microsoft Entra SSO** with Entra groups mapped to roles and sites. |
| ![Security](docs/screenshots/security.png)<br>**Security**: mandatory two-factor authentication and audit log retention. | ![Audit log](docs/screenshots/audit.png)<br>**Audit log**: sign-ins, views, changes, exports and restores, with filters and CSV export. |
| ![Source allowlist](docs/screenshots/allowlist.png)<br>**Source allowlist**: only accept syslog from known networks; rejected senders can be allowed with one click. | ![My account](docs/screenshots/account.png)<br>**My account**: password, two-factor authentication and API tokens. |
| ![Mail server](docs/screenshots/mail.png)<br>**Mail server**: your own SMTP server with modern TLS and authentication, including OAuth2 for Microsoft 365. | ![Backup and restore](docs/screenshots/backup.png)<br>**Backup & restore**: scheduled full and incremental backups to SFTP, S3, SCP, FTPS, FTP or a local folder, optionally encrypted. |
| ![Branding](docs/screenshots/branding.png)<br>**Branding**: company name, logos for light and dark mode and a favicon. | ![Info and updates](docs/screenshots/info.png)<br>**Info & updates**: current version, release channel, update check, in-app update and rollback. |

![Sign in](docs/screenshots/login.png)

## Installation

### Proxmox LXC

Run this on the Proxmox host:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/unifi-syslog-server/main/ct/unify-syslog-server.sh)"
```

The script uses the community-scripts engine and creates an unprivileged Debian 13 container (by default 4 vCPU, 4 GB RAM and a 40 GB disk). Use *Advanced* to change this. Size the disk to your expected volume: with UniFi logs that is roughly 1 GB per 10 million messages after compression.

You can update the container later by running the same command inside the container, or with the **Info** button in the web interface.

### Debian 13 (VM or physical server)

Run this as root:

```bash
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/unifi-syslog-server/main/deploy/install.sh | bash
```

The install script:

1. installs and configures ClickHouse (reachable on localhost only, with its own user and password);
2. downloads the latest release and verifies the checksum;
3. installs a systemd service `unify-syslog`;
4. creates an admin account and writes the credentials to `/root/unify-syslog.creds`.

You can adjust its behavior with these environment variables:

| Variable | Meaning |
|---|---|
| `USS_VERSION=v0.1.0` | Installs a specific version instead of the latest. |
| `USS_ADMIN_PASSWORD=...` | Sets the initial admin password (generated otherwise). |
| `USS_NONINTERACTIVE=1` | Skips all prompts. |
| `USS_CH_MEMORY_RATIO=0.5` | Sets the share of RAM that ClickHouse may use. |
| `USS_CLICKHOUSE=external` | Uses an external ClickHouse server or cluster instead of installing one; see [docs/STORAGE.md](docs/STORAGE.md) for `USS_CH_ADDR` and the other settings. |

Then open `http://<server>:8080`.

### Remote probe

A probe for a remote location has its own Proxmox LXC script, Debian 13 installer and Raspberry Pi image; all of them need an enrollment key from the main server. See [Remote probes](#remote-probes) and [docs/PROBES.md](docs/PROBES.md).

### Files

| Path | Contents |
|---|---|
| `/etc/unify-syslog/config.yaml` | Ports, ClickHouse connection and ingest settings. |
| `/var/lib/unify-syslog/node.yaml` | Node-local settings made in the interface: the ClickHouse connection and high availability. |
| `/var/lib/unify-syslog-ch/storage.xml` | ClickHouse configuration written by the server for cold storage, linked into `/etc/clickhouse-server/config.d/`. |
| `/var/lib/unify-syslog/` | Metadata (`meta.db`), the encryption key (`secret.key`), local backups, the forwarding queues (`forward/`), the HTTPS certificate (`tls/`) and the cached probe releases (`probes/`). |
| `/opt/unify-syslog/` | The program. |

## Configuring UniFi

**UniFi Network (controller, UDM, UCG, Cloud Key)**

1. Go to *Settings → CyberSecure → Traffic Logging* (in older versions *Settings → System → Advanced → Remote Logging*).
2. Choose **SIEM Server** or **Syslog**, enter the IP address of this server, use port `514` and save.
3. Enable **Syslog Logging** on the firewall rules you want to see.
4. To also receive debug messages from the devices, enable *Debug Logs*.

**Names from UniFi Network (optional)**: in UniFi Network 9.0 or later, go to *Settings → Control Plane → Integrations* and create an API key. Add the console address (for example `https://192.168.1.1`) and the key under **Configuration → UniFi controllers**. Names are refreshed every 5 minutes.

**GeoIP (optional)**: turn it on under **Configuration → GeoIP**. DB-IP Lite needs no account; for MaxMind GeoLite2, enter your account ID and license key. The server needs outbound HTTPS to download the databases.

**Multiple sites**: each site sends to the public IP or the VPN tunnel of this server. Then go to **Sites** in the interface and map each source IP (for example the WAN IP of a branch office) or IP range to a site name.

## Users, roles and security

**Roles** (Administration → Users):

| Role | Can |
|---|---|
| Viewer | View logs, dashboards, devices, clients and sites (read-only) |
| Analyst | Everything a viewer can, plus export logs (CSV/JSON) and manage the shared categories |
| Admin | Everything, on all sites |

Viewers and analysts can be limited to one or more **sites**: they then only see logs, dashboards, devices, clients and senders of those sites. Without sites they see everything.

**Entra groups to roles** (Administration → Microsoft Entra SSO): map Entra security groups (object IDs) to a role and sites. On every sign-in, the highest role of all the user's groups wins and the sites of the groups with that role are combined; users in no mapped group get the default role. The app registration needs the `groups` claim (Token configuration → Add groups claim → Security groups). Without mappings, roles of Entra users are set by hand.

**Two-factor authentication**: every local user can turn on TOTP under **My account** (Microsoft Authenticator, Google Authenticator, 1Password and similar apps) and gets ten one-time recovery codes. Under **Administration → Security** you can make it mandatory for local accounts; users then set it up at their next sign-in. Admins can reset it for a user who lost their phone. Entra users use the MFA of Microsoft Entra.

**API tokens**: under **My account → API tokens**, see [docs/API.md](docs/API.md).

**Source allowlist** (Configuration → Source allowlist): when on, only syslog from the listed IP addresses or networks is accepted on UDP and TCP; everything else is dropped. *Add known senders* fills the list from the devices that already send logs, and rejected senders are listed so you can allow them with one click. For sites behind NAT, list the public WAN address.

**Audit log** (Administration → Audit log): sign-ins (including failed ones and the IP address), changes, log searches, exports, backup downloads, restores and rollbacks, with filters and CSV export. Entries are kept for 365 days by default (Administration → Security).

## Query language

| Example | Meaning |
|---|---|
| `category:firewall action:block` | Blocked firewall traffic. |
| `src:203.0.113.0/24` | Source IP in a subnet. |
| `dport:22 OR dport:3389` | SSH or RDP. |
| `-category:dns` | Everything except DNS. |
| `rule:"WAN_IN-D-*"` | A wildcard. |
| `severity:<=warning` | Severity warning or higher. |
| `dport:1..1024` | A port range. |
| `ssid:"Office" client:aa:bb:*` | An SSID and clients with a MAC prefix. |
| `"authentication failure"` | An exact phrase in the message. |
| `field.signature_id:2210050` | An extra field from the parser. |
| `category:firewall action:block country:CN` | Blocked traffic to or from a country (GeoIP). |
| `src_asn:AS13335` | Traffic from one network (AS number). |
| `category:protect event:person_detected` | People detected by UniFi Protect. |

You can search on these fields:

| Group | Fields |
|---|---|
| General | `category`, `app`, `host`, `site`, `source`, `severity`, `facility`, `msg`, `raw` |
| Device | `model`, `mac`, `firmware` |
| Event | `action`, `rule`, `event` |
| Traffic | `src`, `dst`, `sport`, `dport`, `proto`, `in`, `out`, `ip` (source or destination), `port` |
| Clients | `client`, `client_name`, `client_ip`, `ssid`, `src_name`, `dst_name`, `device_name` |
| GeoIP | `country`, `src_country`, `dst_country`, `asn`, `src_asn`, `dst_asn`, `as`, `src_as`, `dst_as` |
| Protect and Access | `camera`, `door`, `user` |

## Branding

Under **Administration → Branding** you set a company name and upload a logo for light mode, an optional logo for dark mode and a favicon (PNG or SVG, ICO for the favicon, at most 512 KB each). The company name appears below the server name in the menu and on the sign-in page, and in the header and footer of every e-mail. E-mails embed the logo only when it is a PNG, because most mail clients do not show SVG. Branding is stored in the configuration database, so it is part of every backup.

## Languages

The interface, e-mails and notifications are available in English, Dutch (Nederlands), German (Deutsch) and French (Français).

- The server default is set under **Settings → Default language**; it applies to the sign-in page and to users who have not picked a language.
- Every user can pick their own language at the bottom of the menu, and admins can set it per user.
- Entra SSO users get the language of their Microsoft account on every sign-in, until they pick a language themselves. This uses the delegated Graph permission `User.Read` (or the optional `xms_pl` claim) and can be turned off on the SSO page.
- E-mails go out in the language of the user with the recipient's address, otherwise in the server default.
- Each [notification channel](#notification-channels) has its own language, so a German team can get its Teams messages in German.

| | |
|---|---|
| ![Notification channels in German](docs/screenshots/notifications-de.png)<br>**Deutsch**: the notification channels page in German. | ![Alert rule in French](docs/screenshots/alerts-edit-fr.png)<br>**Français**: editing an alert rule in French. |

To add a language, copy `web/src/locales/nl.json` (interface) and `internal/i18n/locales/nl.json` (e-mails) to `<code>.json`, translate the values and set `_language` to the language's own name. Keys are the English texts; a missing key falls back to English. New files are picked up automatically when building. `npm run i18n:check` in `web/` lists interface texts that a translation is missing.

## Backup and restore

Configure backups under **Backup** in the web interface.

- **Full backup**: all logs plus the configuration (users, settings, categories and the encryption key).
- **Incremental backup**: only what has arrived since the previous backup in the chain.
- **Restore**: choose a backup at the remote location. The server fetches the whole chain (the full backup plus the incremental backups), restores the logs and restarts.
- **Targets**: FTP, FTPS (explicit or implicit), SCP and SFTP (password or SSH key, with host key pinning), S3-compatible storage (MinIO, Wasabi, AWS S3; the target's folder becomes a prefix in the bucket) and a local folder.
- **Encryption**: with *Encrypt backups* on, every backup file is encrypted with your passphrase ([age](https://age-encryption.org), scrypt) before it leaves the server and gets the extension `.tar.age`. Restoring uses the passphrase from the settings, or the one you enter in the restore dialog. Without the passphrase an encrypted backup cannot be restored, so keep it somewhere safe. Files can also be decrypted by hand with `age -d`.

A backup contains the encryption key of the settings database; store unencrypted backups in a trusted location.

## Updates, release channels and rollback

*Info & updates* has two release channels:

- **Production**: stable releases only. This is the default, and what the installer installs.
- **Test**: also pre-releases (`v0.4.0-beta.1`, `v0.4.0-rc.1`), to try new features early. Switching back to Production offers the newest stable release, even when its version number is lower than the test version that is running.

Before every update the server keeps the running binary and a snapshot of the settings database in `/var/lib/unify-syslog/versions` (the last three versions). *Previous versions* lists them with a **Roll back** button; a rollback restores the binary and the settings as they were for that version, and keeps the logs.

A new version has to prove itself: when it does not run healthy for 30 seconds within 5 minutes of the update, or keeps crashing (more than 3 starts), the server puts the previous version back by itself and shows that in *Info & updates*. Changes to the log store in ClickHouse are additive only, so an older version can always read the data.

## Command line

```bash
unify-syslog user add jan --admin       # create a user
unify-syslog user passwd admin          # reset a password
unify-syslog update [--check]           # update to the latest release on the configured channel
unify-syslog update rollback [version]  # go back to a kept previous version
unify-syslog simulate --target 127.0.0.1:514 --rate 500 --sites 3   # test traffic
unify-syslog version
```

On a remote probe:

```bash
unify-syslog-probe status               # connection, buffer and version
unify-syslog-probe enroll '<key>'       # enroll with a key from the main server
unify-syslog-probe setup                # ask for the key and enroll
unify-syslog-probe reset                # forget the enrollment (the buffer is kept)
unify-syslog-probe version
```

## Building from source

Requirements: Go 1.26 and Node 22.

```bash
make web      # build the React UI (web/dist, embedded in the binary)
make build    # bin/unify-syslog
make test
make probe    # bin/unify-syslog-probe
make dist     # release tarballs (server: amd64, arm64; probe: amd64, arm64, arm) with checksums.txt
make rpi-image  # after make dist: the Raspberry Pi image (needs root)
```

Releases are made with the **release** workflow in GitHub Actions (*Run workflow*): enter the version and choose the channel. `production` needs a plain version (`v0.4.0`) and becomes the latest release; `test` needs a pre-release suffix (`v0.4.0-beta.1`) and is published as a GitHub pre-release, which only servers on the Test channel are offered. The install script and the updater use these releases.

## Architecture

```
UniFi devices ──UDP/TCP 514──▶ syslogd ─▶ ingest workers (UniFi parser, site)
remote probes ─HTTPS 8080─▶ probe API ──────┘
                                            │ batches of 20k / 1 s
                                            ▼
                     ClickHouse  logs (MergeTree, TTL) + logs_hourly (MV)
                                            ▲         │ filter per destination
     Browser ──HTTP 8080──▶ API + React UI ─┘         ▼
                    SQLite: users, settings,   on-disk queue ─▶ Sentinel, Elastic,
                    sites, destinations                         Logstash, syslog, Splunk, webhook
```
