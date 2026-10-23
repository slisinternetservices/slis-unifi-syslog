#!/usr/bin/env bash
# UniFi Syslog Server installer for Debian 13 (trixie): VM, bare metal or LXC.
#
#   curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/deploy/install.sh | bash
#
# Options (environment variables):
#   USS_VERSION=v1.2.3     install a specific release (default: latest)
#   USS_ADMIN_PASSWORD=..  initial admin password (default: generated)
#   USS_NONINTERACTIVE=1   no prompts
#   USS_CH_MEMORY_RATIO    ClickHouse share of RAM (default 0.5)
#
# Logs on an external ClickHouse server or cluster instead of a local one
# (for example one set up with deploy/install-clickhouse.sh):
#   USS_CLICKHOUSE=external
#   USS_CH_ADDR=10.0.0.20:9000         native protocol (9440 with TLS); more servers comma separated
#   USS_CH_HTTP_URL=http://10.0.0.20:8123
#   USS_CH_USER=unify  USS_CH_PASSWORD=...  USS_CH_DATABASE=unify_syslog
#   USS_CH_SECURE=1                    use TLS (USS_CH_SKIP_VERIFY=1 for a self-signed certificate)
#   USS_CH_CLUSTER=name                store logs replicated on this cluster
# The connection can also be changed later under Administration > Storage.
set -euo pipefail

REPO="slisinternetservices/slis-unifi-syslog"
APP_DIR="/opt/unify-syslog"
CONF_DIR="/etc/unify-syslog"
DATA_DIR="/var/lib/unify-syslog"
SVC_USER="unify-syslog"
VERSION="${USS_VERSION:-latest}"
CH_MODE="${USS_CLICKHOUSE:-local}"
MANAGED_DIR="/var/lib/unify-syslog-ch"

c_ok="\e[32m"; c_info="\e[36m"; c_warn="\e[33m"; c_err="\e[31m"; c_off="\e[0m"
info() { echo -e "${c_info}==>${c_off} $*"; }
ok()   { echo -e "${c_ok}✔${c_off} $*"; }
warn() { echo -e "${c_warn}!${c_off} $*"; }
die()  { echo -e "${c_err}✘ $*${c_off}" >&2; exit 1; }

LOG=/var/log/unify-syslog-install.log
# Run a command with its output in $LOG; on failure show the tail so the cause is visible.
run() {
  echo "+ $*" >>"$LOG"
  if ! "$@" >>"$LOG" 2>&1; then
    echo -e "${c_err}✘ Failed: $*${c_off}" >&2
    echo "----- last lines of $LOG -----" >&2
    tail -n 30 "$LOG" >&2
    echo "-----------------------------------" >&2
    return 1
  fi
}

[[ $EUID -eq 0 ]] || die "Run this script as root."
read -r OS_ID OS_VER OS_NAME < <(. /etc/os-release; echo "${ID:-} ${VERSION_ID:-} ${PRETTY_NAME:-unknown}")
if [[ "${OS_ID}" != "debian" ]]; then
  die "This script is meant for Debian 13 (found: ${OS_NAME})."
fi
if [[ "${OS_VER}" != "13" ]]; then
  warn "Tested on Debian 13, found ${OS_NAME}. Continuing..."
fi

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Unsupported architecture $(uname -m)" ;;
esac

export DEBIAN_FRONTEND=noninteractive
: >"$LOG"

# ClickHouse needs about 2 GB on disk to install (the default Debian LXC disk is only 2 GB).
FREE_MB=$(df -Pm / | awk 'NR==2 {print $4}')
if [[ "$CH_MODE" == external ]]; then
  (( FREE_MB >= 1024 )) || die "Not enough disk space: ${FREE_MB} MB free on /. At least 1 GB is required."
elif (( FREE_MB < 3072 )); then
  die "Not enough disk space: ${FREE_MB} MB free on /. At least 3 GB is required, 20 GB or more is recommended. Grow the disk (Proxmox: Resources → Root Disk → Resize) and run again."
elif (( FREE_MB < 10240 )); then
  warn "Only ${FREE_MB} MB free on /. 20 GB or more is recommended for log storage."
fi
if [[ "$CH_MODE" == local && "$ARCH" == amd64 ]] && ! grep -qw sse4_2 /proc/cpuinfo; then
  die "The CPU does not support SSE4.2, which ClickHouse requires. In Proxmox, set the VM CPU type to 'host' or 'x86-64-v2-AES'."
fi
TOTAL_MB=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
(( TOTAL_MB >= 1800 )) || warn "Only ${TOTAL_MB} MB RAM. 4 GB or more is recommended."

# ClickHouse 26.8 and later need AVX2 (Intel Haswell / AMD Zen or newer) and die with
# "Illegal instruction" on older CPUs such as the Xeon E5 v1/v2. 26.3 LTS still runs there.
CH_PIN="${USS_CH_VERSION:-}"
if [[ "$CH_MODE" == local && -z "$CH_PIN" && "$ARCH" == amd64 ]] && ! grep -qw avx2 /proc/cpuinfo; then
  CH_PIN="26.3.*"
  warn "CPU without AVX2 detected: pinning ClickHouse to 26.3 LTS."
fi
install -d /etc/apt/preferences.d
if [[ -n "$CH_PIN" ]]; then
  printf 'Package: clickhouse-server clickhouse-client clickhouse-common-static clickhouse-keeper\nPin: version %s\nPin-Priority: 1001\n' "$CH_PIN" > /etc/apt/preferences.d/clickhouse
else
  rm -f /etc/apt/preferences.d/clickhouse
fi

# A ClickHouse package left half-configured by an earlier failed run blocks apt; remove it
# (data and config stay) so it is reinstalled below.
if dpkg -s clickhouse-server >/dev/null 2>&1 && ! dpkg -s clickhouse-server | grep -q 'Status: install ok installed'; then
  warn "Found a half-installed ClickHouse from an earlier attempt; reinstalling it."
  dpkg --remove --force-remove-reinstreq clickhouse-server >>"$LOG" 2>&1 || true
fi
dpkg --configure -a >>"$LOG" 2>&1 || true

info "Installing system packages"
run apt-get update || die "apt-get update failed"
run apt-get install -y curl ca-certificates gnupg apt-transport-https tar openssl tzdata libcap2-bin || die "Installing base packages failed"
ok "Base packages installed"

if ! id "$SVC_USER" >/dev/null 2>&1; then
  useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin "$SVC_USER"
fi

# ---------------------------------------------------------------- ClickHouse
if [[ "$CH_MODE" == local ]]; then
ch_works() { dpkg -s clickhouse-server 2>/dev/null | grep -q 'Status: install ok installed' && clickhouse local -q 'SELECT 1' >/dev/null 2>&1; }
if ! ch_works; then
  info "Installing ClickHouse (official repository${CH_PIN:+, version $CH_PIN})"
  install -d -m 0755 /usr/share/keyrings
  curl -fsSL 'https://packages.clickhouse.com/rpm/lts/repodata/repomd.xml.key' | gpg --dearmor --yes -o /usr/share/keyrings/clickhouse-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/clickhouse-keyring.gpg arch=${ARCH}] https://packages.clickhouse.com/deb lts main" > /etc/apt/sources.list.d/clickhouse.list
  run apt-get update || die "apt-get update failed"
  if ! run apt-get install -y --allow-downgrades clickhouse-common-static clickhouse-server clickhouse-client; then
    run apt-get install -y -f || true
  fi
  if ! ch_works; then
    rc=0; clickhouse local -q 'SELECT 1' >/dev/null 2>&1 || rc=$?
    if (( rc == 132 )); then  # SIGILL
      die "ClickHouse does not run on this CPU. Try again with USS_CH_VERSION='26.3.*', or for a VM set the CPU type to 'host'."
    fi
    die "Installing ClickHouse failed. The full log is in $LOG"
  fi
  ok "ClickHouse installed"
else
  ok "ClickHouse is already installed"
fi

CH_PASS_FILE="${CONF_DIR}/.clickhouse-password"
install -d -m 0750 "$CONF_DIR"
if [[ -s "$CH_PASS_FILE" ]]; then
  CH_PASS="$(cat "$CH_PASS_FILE")"
else
  CH_PASS="$(openssl rand -hex 24)"
  echo -n "$CH_PASS" > "$CH_PASS_FILE"
  chmod 0600 "$CH_PASS_FILE"
fi
CH_HASH="$(echo -n "$CH_PASS" | sha256sum | cut -d' ' -f1)"

MEM_RATIO="${USS_CH_MEMORY_RATIO:-0.5}"
install -d -m 0755 /etc/clickhouse-server/config.d /etc/clickhouse-server/users.d
cat > /etc/clickhouse-server/config.d/unify-syslog.xml <<EOF
<clickhouse>
  <!-- Managed by unify-syslog installer -->
  <listen_host>127.0.0.1</listen_host>
  <listen_host>::1</listen_host>
  <max_server_memory_usage_to_ram_ratio>${MEM_RATIO}</max_server_memory_usage_to_ram_ratio>
  <mark_cache_size>268435456</mark_cache_size>
  <logger><level>warning</level><size>100M</size><count>3</count></logger>
  <!-- Keep system log tables small; they otherwise grow without bound. -->
  <query_log replace="1"><database>system</database><table>query_log</table><ttl>event_date + INTERVAL 3 DAY DELETE</ttl><flush_interval_milliseconds>7500</flush_interval_milliseconds></query_log>
  <trace_log remove="1"/>
  <metric_log remove="1"/>
  <asynchronous_metric_log remove="1"/>
  <text_log remove="1"/>
  <part_log remove="1"/>
  <processors_profile_log remove="1"/>
  <latency_log remove="1"/>
  <error_log remove="1"/>
  <query_metric_log remove="1"/>
</clickhouse>
EOF
cat > /etc/clickhouse-server/users.d/unify-syslog.xml <<EOF
<clickhouse>
  <users>
    <default remove="1"/>
    <unify>
      <password_sha256_hex>${CH_HASH}</password_sha256_hex>
      <networks><ip>127.0.0.1</ip><ip>::1</ip></networks>
      <profile>default</profile>
      <quota>default</quota>
      <access_management>0</access_management>
    </unify>
  </users>
</clickhouse>
EOF
rm -f /etc/clickhouse-server/users.d/default-password.xml
chown -R clickhouse:clickhouse /etc/clickhouse-server/config.d /etc/clickhouse-server/users.d 2>/dev/null || true
# Configuration the server writes itself (cold storage on S3, Administration > Storage):
# a directory it owns, readable by ClickHouse, linked into config.d.
install -d -m 2750 -o "$SVC_USER" -g clickhouse "$MANAGED_DIR"
[[ -f "$MANAGED_DIR/storage.xml" ]] || echo '<clickhouse/>' > "$MANAGED_DIR/storage.xml"
chown "$SVC_USER":clickhouse "$MANAGED_DIR/storage.xml"
chmod 0640 "$MANAGED_DIR/storage.xml"
ln -sfn "$MANAGED_DIR/storage.xml" /etc/clickhouse-server/config.d/zz-unify-syslog-managed.xml
if [[ "$(systemd-detect-virt -c 2>/dev/null || true)" == "lxc" ]]; then
  # Unprivileged containers cannot grant ClickHouse's extra capabilities; it runs fine without.
  # The package puts file capabilities on the binary (again on every upgrade), and a binary
  # with file capabilities outside the bounding set cannot be executed, so strip them first.
  install -d /etc/systemd/system/clickhouse-server.service.d
  cat > /etc/systemd/system/clickhouse-server.service.d/lxc.conf <<'EOF2'
[Service]
AmbientCapabilities=
CapabilityBoundingSet=
ExecStartPre=-+/usr/sbin/setcap -r /usr/bin/clickhouse
EOF2
  setcap -r /usr/bin/clickhouse 2>/dev/null || true
  systemctl daemon-reload
fi
systemctl enable -q clickhouse-server
systemctl restart clickhouse-server
for _ in $(seq 1 60); do
  clickhouse-client --user unify --password "$CH_PASS" -q "SELECT 1" >/dev/null 2>&1 && break
  sleep 1
done
clickhouse-client --user unify --password "$CH_PASS" -q "SELECT 1" >/dev/null 2>&1 || die "ClickHouse does not start; see journalctl -u clickhouse-server"
ok "ClickHouse is running (local access only)"
else
  CH_ADDR="${USS_CH_ADDR:?USS_CH_ADDR is required with USS_CLICKHOUSE=external}"
  CH_PASS="${USS_CH_PASSWORD:-}"
  ok "Using the external ClickHouse at ${CH_ADDR}"
fi

# ---------------------------------------------------------------- application
install -d -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$APP_DIR"
install -d -m 0750 -o "$SVC_USER" -g "$SVC_USER" "$DATA_DIR" "$DATA_DIR/backups"

if [[ -n "${USS_LOCAL_TARBALL:-}" ]]; then
  TARBALL="$USS_LOCAL_TARBALL"
else
  if [[ "$VERSION" == "latest" ]]; then
    # Follow the /releases/latest redirect (no API rate limit, no grep closing the pipe early).
    LATEST_URL="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" || true)"
    VERSION="${LATEST_URL##*/tag/}"
    [[ "$LATEST_URL" == */tag/* ]] || VERSION=""
    [[ -n "$VERSION" ]] || die "Could not determine the latest release (are there releases on github.com/${REPO}?)"
  fi
  info "Downloading UniFi Syslog Server ${VERSION}"
  TMP="$(mktemp -d)"
  trap 'rm -rf "$TMP"' EXIT
  ASSET="unify-syslog-server_linux_${ARCH}.tar.gz"
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"
  curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET"
  if curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"; then
    (cd "$TMP" && grep " ${ASSET}\$" checksums.txt | sha256sum -c --quiet -) || die "Checksum mismatch"
  fi
  TARBALL="$TMP/$ASSET"
fi
tar -xzf "$TARBALL" -C "$APP_DIR" unify-syslog
chmod 0755 "$APP_DIR/unify-syslog"
chown "$SVC_USER:$SVC_USER" "$APP_DIR/unify-syslog"
ln -sf "$APP_DIR/unify-syslog" /usr/local/bin/unify-syslog
ok "Program installed in $APP_DIR ($("$APP_DIR/unify-syslog" version))"

install -d -m 0750 "$CONF_DIR"
if [[ ! -f "$CONF_DIR/config.yaml" ]]; then
  if [[ "$CH_MODE" == local ]]; then
    CH_YAML="  addr: \"127.0.0.1:9000\"
  http_url: \"http://127.0.0.1:8123\"
  database: unify_syslog
  username: unify
  password: \"${CH_PASS}\""
  else
    IFS=',' read -r -a CH_ADDRS <<<"$CH_ADDR"
    CH_HOST="${CH_ADDRS[0]%:*}"
    CH_HTTP="${USS_CH_HTTP_URL:-}"
    if [[ -z "$CH_HTTP" ]]; then
      if [[ -n "${USS_CH_SECURE:-}" ]]; then CH_HTTP="https://${CH_HOST}:8443"; else CH_HTTP="http://${CH_HOST}:8123"; fi
    fi
    CH_YAML="  addr: \"${CH_ADDRS[0]}\""
    if (( ${#CH_ADDRS[@]} > 1 )); then
      CH_YAML+=$'\n  addrs:'
      for a in "${CH_ADDRS[@]:1}"; do CH_YAML+=$'\n    - "'"$a"'"'; done
    fi
    CH_YAML+="
  http_url: \"${CH_HTTP}\"
  database: ${USS_CH_DATABASE:-unify_syslog}
  username: \"${USS_CH_USER:-unify}\"
  password: \"${CH_PASS}\""
    if [[ -n "${USS_CH_SECURE:-}" ]]; then CH_YAML+=$'\n  secure: true'; fi
    if [[ -n "${USS_CH_SKIP_VERIFY:-}" ]]; then CH_YAML+=$'\n  skip_verify: true'; fi
    if [[ -n "${USS_CH_CLUSTER:-}" ]]; then CH_YAML+=$'\n  cluster: "'"${USS_CH_CLUSTER}"'"'; fi
  fi
  cat > "$CONF_DIR/config.yaml" <<EOF
# UniFi Syslog Server - bootstrap configuration.
# All other settings are managed in the web interface.
http_listen: ":8080"
data_dir: ${DATA_DIR}
syslog:
  udp: ":514"
  tcp: ":514"
  udp_read_buffer: 8388608
clickhouse:
${CH_YAML}
ingest:
  queue_size: 200000
  workers: $(nproc)
  batch_size: 20000
  flush_interval_ms: 1000
EOF
fi
chown root:"$SVC_USER" "$CONF_DIR" "$CONF_DIR/config.yaml"
chmod 0640 "$CONF_DIR/config.yaml"

# Larger socket buffers so bursts from many sites are not dropped by the kernel.
cat > /etc/sysctl.d/90-unify-syslog.conf <<EOF
net.core.rmem_max = 33554432
net.core.rmem_default = 8388608
EOF
sysctl -q -p /etc/sysctl.d/90-unify-syslog.conf 2>/dev/null || warn "sysctl not applied (normal in an unprivileged LXC)"

UNIT_CH=""
[[ "$CH_MODE" == local ]] && UNIT_CH=" clickhouse-server.service"
cat > /etc/systemd/system/unify-syslog.service <<EOF
[Unit]
Description=UniFi Syslog Server
Documentation=https://github.com/${REPO}
After=network-online.target${UNIT_CH}
Wants=network-online.target${UNIT_CH}

[Service]
User=${SVC_USER}
Group=${SVC_USER}
ExecStart=${APP_DIR}/unify-syslog -c ${CONF_DIR}/config.yaml serve
Restart=always
RestartSec=2
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${DATA_DIR} ${APP_DIR}
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload

FIRST_RUN=0
if [[ ! -f "$DATA_DIR/meta.db" ]]; then
  FIRST_RUN=1
  ADMIN_PW="${USS_ADMIN_PASSWORD:-$(openssl rand -base64 18 | tr -d '/+=' | cut -c1-16)}"
  sudo_u() { runuser -u "$SVC_USER" -- "$@"; }
  echo "$ADMIN_PW" | sudo_u "$APP_DIR/unify-syslog" -c "$CONF_DIR/config.yaml" user add admin --admin
  cat > /root/unify-syslog.creds <<EOF
UniFi Syslog Server
Web:        http://$(hostname -I | awk '{print $1}'):8080
User:       admin
Password:   ${ADMIN_PW}
EOF
  chmod 0600 /root/unify-syslog.creds
fi

systemctl enable -q unify-syslog
systemctl restart unify-syslog
sleep 2
systemctl is-active -q unify-syslog || die "The service does not start; see journalctl -u unify-syslog"

IP="$(hostname -I | awk '{print $1}')"
echo
ok "Installation complete!"
echo -e "   Web interface: ${c_info}http://${IP}:8080${c_off}"
echo -e "   Syslog:        UDP/TCP ${IP}:514"
if [[ $FIRST_RUN -eq 1 ]]; then
  echo -e "   Login:         admin / ${c_ok}${ADMIN_PW}${c_off}   (also saved in /root/unify-syslog.creds)"
fi
echo -e "   Update:        via Info & updates in the web interface, or: unify-syslog update && systemctl restart unify-syslog"
