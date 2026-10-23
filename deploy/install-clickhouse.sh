#!/usr/bin/env bash
# Standalone ClickHouse server for UniFi Syslog Server, on Debian 13 (VM,
# bare metal or LXC). Use it to keep logs on a separate machine, or as the
# shared log store of a high availability pair.
#
#   curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/deploy/install-clickhouse.sh | bash
#
# Options (environment variables):
#   USS_CH_ALLOW="10.0.0.0/8 192.168.0.0/16"  networks allowed to connect (default: private ranges)
#   USS_CH_PASSWORD=...        password of the "unify" user (default: generated, kept on re-runs)
#   USS_CH_TLS=1               also serve TLS (native 9440, HTTPS 8443) with a self-signed certificate
#   USS_CH_MEMORY_RATIO=0.8    ClickHouse share of RAM (default 0.8, it has the machine to itself)
#   USS_CH_VERSION='26.3.*'    pin a ClickHouse version (done automatically on CPUs without AVX2)
#
# Cold storage on S3 (logs older than a number of days move to object storage;
# choose the storage policy "uss_tiered", volume "cold" under Administration > Storage):
#   USS_S3_ENDPOINT=https://s3.eu-central-1.wasabisys.com  USS_S3_BUCKET=my-bucket
#   USS_S3_PREFIX=unify-syslog/  USS_S3_REGION=eu-central-1
#   USS_S3_ACCESS_KEY=...  USS_S3_SECRET_KEY=...  USS_S3_CACHE_GB=10
set -euo pipefail

c_ok="\e[32m"; c_info="\e[36m"; c_warn="\e[33m"; c_err="\e[31m"; c_off="\e[0m"
info() { echo -e "${c_info}==>${c_off} $*"; }
ok()   { echo -e "${c_ok}✔${c_off} $*"; }
warn() { echo -e "${c_warn}!${c_off} $*"; }
die()  { echo -e "${c_err}✘ $*${c_off}" >&2; exit 1; }

LOG=/var/log/unify-syslog-clickhouse-install.log
run() {
  echo "+ $*" >>"$LOG"
  if ! "$@" >>"$LOG" 2>&1; then
    echo -e "${c_err}✘ Failed: $*${c_off}" >&2
    tail -n 30 "$LOG" >&2
    return 1
  fi
}

[[ $EUID -eq 0 ]] || die "Run this script as root."
read -r OS_ID OS_VER OS_NAME < <(. /etc/os-release; echo "${ID:-} ${VERSION_ID:-} ${PRETTY_NAME:-unknown}")
[[ "$OS_ID" == debian ]] || die "This script is meant for Debian 13 (found: ${OS_NAME})."
[[ "$OS_VER" == 13 ]] || warn "Tested on Debian 13, found ${OS_NAME}. Continuing..."
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "Unsupported architecture $(uname -m)" ;;
esac
export DEBIAN_FRONTEND=noninteractive
: >"$LOG"

CONF_DIR=/etc/unify-syslog-clickhouse
ALLOW="${USS_CH_ALLOW:-10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7}"
MEM_RATIO="${USS_CH_MEMORY_RATIO:-0.8}"

FREE_MB=$(df -Pm / | awk 'NR==2 {print $4}')
(( FREE_MB >= 3072 )) || die "Not enough disk space: ${FREE_MB} MB free on /. At least 3 GB is required, much more for logs."
if [[ "$ARCH" == amd64 ]] && ! grep -qw sse4_2 /proc/cpuinfo; then
  die "The CPU does not support SSE4.2, which ClickHouse requires. In Proxmox, set the VM CPU type to 'host' or 'x86-64-v2-AES'."
fi
CH_PIN="${USS_CH_VERSION:-}"
if [[ -z "$CH_PIN" && "$ARCH" == amd64 ]] && ! grep -qw avx2 /proc/cpuinfo; then
  CH_PIN="26.3.*"
  warn "CPU without AVX2 detected: pinning ClickHouse to 26.3 LTS."
fi
install -d /etc/apt/preferences.d
if [[ -n "$CH_PIN" ]]; then
  printf 'Package: clickhouse-server clickhouse-client clickhouse-common-static clickhouse-keeper\nPin: version %s\nPin-Priority: 1001\n' "$CH_PIN" > /etc/apt/preferences.d/clickhouse
else
  rm -f /etc/apt/preferences.d/clickhouse
fi
dpkg --configure -a >>"$LOG" 2>&1 || true

info "Installing system packages"
run apt-get update || die "apt-get update failed"
run apt-get install -y curl ca-certificates gnupg apt-transport-https openssl libcap2-bin || die "Installing base packages failed"

ch_works() { dpkg -s clickhouse-server 2>/dev/null | grep -q 'Status: install ok installed' && clickhouse local -q 'SELECT 1' >/dev/null 2>&1; }
if ! ch_works; then
  info "Installing ClickHouse (official repository${CH_PIN:+, version $CH_PIN})"
  install -d -m 0755 /usr/share/keyrings
  curl -fsSL 'https://packages.clickhouse.com/rpm/lts/repodata/repomd.xml.key' | gpg --dearmor --yes -o /usr/share/keyrings/clickhouse-keyring.gpg
  echo "deb [signed-by=/usr/share/keyrings/clickhouse-keyring.gpg arch=${ARCH}] https://packages.clickhouse.com/deb lts main" > /etc/apt/sources.list.d/clickhouse.list
  run apt-get update || die "apt-get update failed"
  run apt-get install -y --allow-downgrades clickhouse-common-static clickhouse-server clickhouse-client || run apt-get install -y -f || true
  ch_works || die "Installing ClickHouse failed. The full log is in $LOG"
fi
ok "ClickHouse installed"

install -d -m 0750 "$CONF_DIR"
if [[ -n "${USS_CH_PASSWORD:-}" ]]; then
  CH_PASS="$USS_CH_PASSWORD"
  echo -n "$CH_PASS" > "$CONF_DIR/password"; chmod 0600 "$CONF_DIR/password"
elif [[ -s "$CONF_DIR/password" ]]; then
  CH_PASS="$(cat "$CONF_DIR/password")"
else
  CH_PASS="$(openssl rand -hex 24)"
  echo -n "$CH_PASS" > "$CONF_DIR/password"; chmod 0600 "$CONF_DIR/password"
fi
CH_HASH="$(echo -n "$CH_PASS" | sha256sum | cut -d' ' -f1)"

TLS_XML=""
if [[ -n "${USS_CH_TLS:-}" ]]; then
  CERT_DIR=/etc/clickhouse-server/certs
  install -d -m 0750 -o clickhouse -g clickhouse "$CERT_DIR"
  if [[ ! -s "$CERT_DIR/server.crt" ]]; then
    IPS="$(hostname -I | tr ' ' '\n' | grep -v '^$' | sed 's/^/IP:/' | paste -sd, -)"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 7300 \
      -subj "/CN=$(hostname -f 2>/dev/null || hostname)" -addext "subjectAltName=DNS:$(hostname),${IPS}" \
      -keyout "$CERT_DIR/server.key" -out "$CERT_DIR/server.crt" >>"$LOG" 2>&1
    chown clickhouse:clickhouse "$CERT_DIR"/server.*
    chmod 0640 "$CERT_DIR/server.key"
  fi
  TLS_XML="  <tcp_port_secure>9440</tcp_port_secure>
  <https_port>8443</https_port>
  <openSSL><server>
    <certificateFile>${CERT_DIR}/server.crt</certificateFile>
    <privateKeyFile>${CERT_DIR}/server.key</privateKeyFile>
    <verificationMode>none</verificationMode>
    <disableProtocols>sslv2,sslv3,tlsv1,tlsv1_1</disableProtocols>
    <preferServerCiphers>true</preferServerCiphers>
  </server></openSSL>"
fi

install -d -m 0755 /etc/clickhouse-server/config.d /etc/clickhouse-server/users.d
cat > /etc/clickhouse-server/config.d/unify-syslog.xml <<EOF
<clickhouse>
  <!-- Managed by the UniFi Syslog Server ClickHouse installer -->
  <listen_host>::</listen_host>
  <listen_host>0.0.0.0</listen_host>
  <listen_try>1</listen_try>
  <max_server_memory_usage_to_ram_ratio>${MEM_RATIO}</max_server_memory_usage_to_ram_ratio>
  <logger><level>warning</level><size>100M</size><count>3</count></logger>
${TLS_XML}
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

NETS=""
for n in $ALLOW 127.0.0.1 ::1; do NETS+="<ip>${n}</ip>"; done
cat > /etc/clickhouse-server/users.d/unify-syslog.xml <<EOF
<clickhouse>
  <users>
    <default remove="1"/>
    <unify>
      <password_sha256_hex>${CH_HASH}</password_sha256_hex>
      <networks>${NETS}</networks>
      <profile>default</profile>
      <quota>default</quota>
      <access_management>0</access_management>
    </unify>
  </users>
</clickhouse>
EOF
rm -f /etc/clickhouse-server/users.d/default-password.xml

if [[ -n "${USS_S3_ENDPOINT:-}" ]]; then
  [[ -n "${USS_S3_BUCKET:-}" && -n "${USS_S3_ACCESS_KEY:-}" && -n "${USS_S3_SECRET_KEY:-}" ]] || die "USS_S3_BUCKET, USS_S3_ACCESS_KEY and USS_S3_SECRET_KEY are required with USS_S3_ENDPOINT"
  PREFIX="${USS_S3_PREFIX:-unify-syslog/}"; PREFIX="${PREFIX#/}"; PREFIX="${PREFIX%/}"
  EP="${USS_S3_ENDPOINT%/}/${USS_S3_BUCKET}/${PREFIX:+$PREFIX/}"
  cat > /etc/clickhouse-server/config.d/zz-unify-syslog-storage.xml <<EOF
<clickhouse>
  <!-- Cold storage for UniFi Syslog Server: Administration > Storage > existing policy uss_tiered, volume cold -->
  <storage_configuration>
    <disks>
      <uss_cold>
        <type>s3</type>
        <endpoint>${EP}</endpoint>
        <access_key_id>${USS_S3_ACCESS_KEY}</access_key_id>
        <secret_access_key>${USS_S3_SECRET_KEY}</secret_access_key>
        <region>${USS_S3_REGION:-us-east-1}</region>
        <use_environment_credentials>false</use_environment_credentials>
        <metadata_path>/var/lib/clickhouse/disks/uss_cold/</metadata_path>
      </uss_cold>
      <uss_cold_cache>
        <type>cache</type>
        <disk>uss_cold</disk>
        <path>/var/lib/clickhouse/disks/uss_cold_cache/</path>
        <max_size>${USS_S3_CACHE_GB:-10}Gi</max_size>
      </uss_cold_cache>
    </disks>
    <policies>
      <uss_tiered>
        <volumes>
          <default><disk>default</disk></default>
          <cold><disk>uss_cold_cache</disk></cold>
        </volumes>
      </uss_tiered>
    </policies>
  </storage_configuration>
</clickhouse>
EOF
  chmod 0640 /etc/clickhouse-server/config.d/zz-unify-syslog-storage.xml
fi
chown -R clickhouse:clickhouse /etc/clickhouse-server/config.d /etc/clickhouse-server/users.d 2>/dev/null || true

if [[ "$(systemd-detect-virt -c 2>/dev/null || true)" == "lxc" ]]; then
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

IP="$(hostname -I | awk '{print $1}')"
echo
ok "ClickHouse is ready for UniFi Syslog Server"
echo "   Enter these under Administration > Storage, or pass them to the installer of the syslog server:"
if [[ -n "${USS_CH_TLS:-}" ]]; then
  echo -e "   Native address:  ${c_info}${IP}:9440${c_off} (TLS, self-signed: turn off certificate verification)"
  echo -e "   HTTP URL:        ${c_info}https://${IP}:8443${c_off}"
else
  echo -e "   Native address:  ${c_info}${IP}:9000${c_off}"
  echo -e "   HTTP URL:        ${c_info}http://${IP}:8123${c_off}"
fi
echo -e "   User:            ${c_info}unify${c_off}"
echo -e "   Password:        ${c_ok}${CH_PASS}${c_off}   (also in ${CONF_DIR}/password)"
echo -e "   Allowed from:    ${ALLOW}"
[[ -n "${USS_S3_ENDPOINT:-}" ]] && echo -e "   Cold storage:    policy ${c_info}uss_tiered${c_off}, volume ${c_info}cold${c_off}"
echo
if [[ -n "${USS_CH_TLS:-}" ]]; then
  echo "   Install the syslog server against it:"
  echo "   USS_CLICKHOUSE=external USS_CH_ADDR=${IP}:9440 USS_CH_SECURE=1 USS_CH_SKIP_VERIFY=1 USS_CH_PASSWORD='${CH_PASS}' bash install.sh"
else
  echo "   Install the syslog server against it:"
  echo "   USS_CLICKHOUSE=external USS_CH_ADDR=${IP}:9000 USS_CH_PASSWORD='${CH_PASS}' bash install.sh"
fi
