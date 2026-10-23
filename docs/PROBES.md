# Remote probes

A remote probe is a small program (`unify-syslog-probe`, about 7 MB, no database) that runs at a remote location. UniFi devices at that location send their syslog to the probe on UDP/TCP port 514. The probe stores every message on its own disk first, then forwards the messages over HTTPS to the main server. When the main server or the link to it is down, the probe keeps buffering. Once the connection is back, it delivers everything exactly once and in order.

The main server manages all probes under **Configuration → Remote probes**. It shows each probe's status, message rate, buffer, devices, CPU, memory, disk and version. The overview dashboard shows a summary of all probes.

## How it works

```
UniFi devices ──UDP/TCP 514──▶ probe ──disk buffer──▶ HTTPS 8080 ──▶ main server
  (site A)                      │                     enroll, heartbeat,
                                └── web page :8080     ingest, download
```

- **Site**: every probe belongs to a site. All messages relayed by a probe get that site, and the field `probe` contains the probe's name. Use `field.probe:"Office A"` to search for them.
- **Buffer**: the buffer has a size limit (default 1 GB) and an age limit (default 7 days). When the buffer is full, the oldest messages are dropped first. You can change both limits per probe.
- **Security**: the probe only talks HTTPS to the main server. The enrollment key contains the main server's address, a one-time code and the fingerprint (pin) of the server's public key, so a self-signed certificate is trusted without extra steps. After enrollment the probe authenticates with its own key, which the main server stores as a hash only. To revoke a probe, delete it.
- **Alerts**: the main server can email you when a probe has been offline for too long (default 5 minutes) or when its buffer is filling up (default 80%). Set this under **Settings** on the Remote probes page.

## HTTPS on the main server

The main server serves both HTTP and HTTPS on port 8080. It tells them apart by the first byte of each connection. At first start it creates a self-signed certificate in `/var/lib/unify-syslog/tls/`. To use your own certificate, set these in `/etc/unify-syslog/config.yaml`:

```yaml
tls_cert: /etc/unify-syslog/tls/fullchain.pem
tls_key: /etc/unify-syslog/tls/privkey.pem
```

The probe trusts a certificate signed by a public CA, or a certificate whose public key matches the pin from the enrollment key. If you switch to a certificate with a different key, probes enrolled before the switch keep working only if the new certificate is publicly trusted.

Probes must be able to reach the main server on port 8080. Under **Settings** on the Remote probes page, set the address the probes use, for example `https://syslog.example.com:8080`. This is needed when the browser address is not reachable from the remote sites.

## Adding a probe

1. On the main server, open **Remote probes** and click **Add probe**. Enter a name and a site.
2. Copy the enrollment key. It is valid for 7 days and can be used once. You can create a new key for a probe at any time.
3. Install the probe in one of the three ways below.
4. Point the UniFi devices at the remote location to the probe's address on port 514.

### Proxmox LXC

Run this on the Proxmox host at the remote location:

```bash
USS_PROBE_KEY='uss1_…' bash -c "$(curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/unifi-syslog-server/main/ct/unify-syslog-probe.sh)"
```

The script uses the community-scripts engine. It creates an unprivileged Debian 13 container with 1 vCPU, 512 MB RAM and a 4 GB disk by default. If you leave out `USS_PROBE_KEY`, the script asks for the key.

### Debian 13 (VM or physical server)

Run this as root:

```bash
curl -fsSL https://raw.githubusercontent.com/rhisijmuiden/unifi-syslog-server/main/deploy/install-probe.sh | USS_PROBE_KEY='uss1_…' bash
```

With a key, the installer downloads the probe from the main server itself, so the probe runs the same version as the server. Without a key, it installs the latest GitHub release and you enroll later.

| Variable | Meaning |
|---|---|
| `USS_PROBE_KEY=uss1_…` | The enrollment key. If empty, the installer asks for it, unless it runs non-interactively. |
| `USS_VERSION=v0.8.0` | Without a key: install this release instead of the latest. |
| `USS_LOCAL_TARBALL=…` | Install from a local `unify-syslog-probe_linux_<arch>.tar.gz`. |
| `USS_NONINTERACTIVE=1` | Skips all prompts. |

Supported: amd64, arm64 and 32-bit ARM, on Debian 13 and Raspberry Pi OS.

### Raspberry Pi image

Download `unify-syslog-probe-rpi-arm64.img.xz` from the [release](https://github.com/rhisijmuiden/unifi-syslog-server/releases) that matches your main server. The image is Raspberry Pi OS Lite 64-bit (Debian 13) with the probe installed. It runs on the Raspberry Pi 3, 4, 5 and Zero 2 W.

Write the image with Raspberry Pi Imager and choose *Use custom*. In Imager you can set the hostname, user, Wi-Fi and SSH. Then enroll the probe in one of these ways:

- **Before the first boot**: create the file `uss-probe.txt` on the boot partition and paste the enrollment key into it. The probe enrolls by itself as soon as it has network. The key works only once, so the file is harmless afterwards.
- **In the browser**: open `http://<address of the Pi>:8080` and paste the key.
- **On the Pi**: sign in and run `sudo unify-syslog-probe setup`. At the first sign-in the Pi offers this by itself.

## The probe's own web page

Every probe has a web page on port 8080. Before enrollment, the page asks for the enrollment key. After enrollment, the page shows the probe's status without any settings: connection, messages per second, buffer, devices, version and disk. All settings are managed on the main server.

## Updates

Probes are updated through the main server. They do not need internet access, only access to the main server.

- With **automatic updates** on (the default), every probe moves to the main server's version after the main server is updated.
- Turn automatic updates off under **Settings** to update probes yourself with **Update** per probe or **Update all**.
- The main server downloads the probe release that matches its own version from GitHub, verifies its checksum and keeps it in `/var/lib/unify-syslog/probes/releases/`. If the main server has no internet access, put `unify-syslog-probe_linux_<arch>.tar.gz` and `checksums.txt` in `/var/lib/unify-syslog/probes/releases/<version>/` yourself.
- The probe verifies the checksum, runs the new version once as a test, keeps the old version and restarts. If the new version does not start, or cannot reach the main server within 5 minutes, the probe goes back to the old version by itself. The main server then shows the update as rolled back and does not offer that version to that probe again.

## Command line on the probe

| Command | Meaning |
|---|---|
| `unify-syslog-probe status` | Connection, buffer and version. |
| `unify-syslog-probe enroll '<key>'` | Enroll with a key. |
| `unify-syslog-probe setup` | Ask for the key and enroll. |
| `unify-syslog-probe reset` | Forget the enrollment, for example to move the probe to another main server. The buffer is kept. |
| `unify-syslog-probe version` | The installed version. |

## Files on the probe

| Path | Contents |
|---|---|
| `/opt/unify-syslog-probe/` | The program, and the previous version during an update. |
| `/var/lib/unify-syslog-probe/` | Enrollment (`state.json`) and the buffer. |
| `/var/log/unify-syslog-probe-install.log` | Output of the installer. |

The service is `unify-syslog-probe` and runs as the user `unify-probe`. Its logs are available with `journalctl -u unify-syslog-probe`.
