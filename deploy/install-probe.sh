#!/usr/bin/env bash
# UniFi Syslog probe installer for Debian 13 (trixie): VM, bare metal, LXC or
# Raspberry Pi OS. Run as root at the remote site:
#
#   curl -fsSL https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/main/deploy/install-probe.sh | USS_PROBE_KEY='uss1_…' bash
#
# The enrollment key comes from the main server (Probes → Add probe). With a
# key the probe program is downloaded from the main server itself, so the
# probe runs the same version; without one it comes from the GitHub release.
#
# Options (environment variables):
#   USS_PROBE_KEY=uss1_…   enrollment key (asked for when empty and interactive)
#   USS_VERSION=v1.2.3     without a key: release to install (default: latest)
#   USS_LOCAL_TARBALL=…    install this unify-syslog-probe_linux_<arch>.tar.gz
#   USS_NONINTERACTIVE=1   no prompts
set -euo pipefail

REPO="slisinternetservices/slis-unifi-syslog"
APP_DIR="/opt/unify-syslog-probe"
DATA_DIR="/var/lib/unify-syslog-probe"
KEY="${USS_PROBE_KEY:-}"
VERSION="${USS_VERSION:-latest}"

c_ok="\e[32m"; c_info="\e[36m"; c_warn="\e[33m"; c_err="\e[31m"; c_off="\e[0m"
info() { echo -e "${c_info}==>${c_off} $*"; }
ok()   { echo -e "${c_ok}✔${c_off} $*"; }
warn() { echo -e "${c_warn}!${c_off} $*"; }
die()  { echo -e "${c_err}✘ $*${c_off}" >&2; exit 1; }

LOG=/var/log/unify-syslog-probe-install.log
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
case "$OS_ID" in
  debian|raspbian) ;;
  *) die "This script is meant for Debian 13 (found: ${OS_NAME})." ;;
esac
[[ "$OS_VER" == "13" ]] || warn "Tested on Debian 13, found ${OS_NAME}. Continuing..."
command -v systemctl >/dev/null || die "systemd is required."

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv6l|armv7l) ARCH=arm ;;
  *) die "Unsupported architecture $(uname -m)" ;;
esac

export DEBIAN_FRONTEND=noninteractive
: >"$LOG"

if [[ -z "$KEY" && -z "${USS_NONINTERACTIVE:-}" && -r /dev/tty ]] && [[ ! -s "$DATA_DIR/state.json" ]]; then
  echo "Paste the enrollment key from the main server (Probes → Add probe),"
  echo "or press Enter to enroll later on the probe's web page."
  read -r -p "Enrollment key: " KEY </dev/tty || KEY=""
fi
KEY="$(echo -n "$KEY" | tr -d '[:space:]')"

# The key is "uss1_" + base64url("<server URL>|<code>|<public key pin>").
SERVER=""; PIN=""
if [[ -n "$KEY" ]]; then
  [[ "$KEY" == uss1_* ]] || die "That is not an enrollment key (it starts with uss1_)."
  b64="$(echo -n "${KEY#uss1_}" | tr '_-' '/+')"
  while (( ${#b64} % 4 )); do b64+="="; done
  decoded="$(echo -n "$b64" | base64 -d 2>/dev/null)" || die "The enrollment key is damaged."
  IFS='|' read -r SERVER _ PIN <<<"$decoded"
  [[ "$SERVER" == https://* ]] || die "The enrollment key is damaged."
fi

info "Installing system packages"
run apt-get update || die "apt-get update failed"
run apt-get install -y curl ca-certificates tar || die "Installing base packages failed"
ok "Base packages installed"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
ASSET="unify-syslog-probe_linux_${ARCH}.tar.gz"
if [[ -n "${USS_LOCAL_TARBALL:-}" ]]; then
  TARBALL="$USS_LOCAL_TARBALL"
elif [[ -n "$SERVER" ]]; then
  info "Downloading the probe from the main server ${SERVER}"
  url="${SERVER}/api/probe/v1/download?arch=${ARCH}"
  # A public certificate is verified as usual; the self-signed one by its pinned key.
  if ! curl -fsS --max-time 600 -D "$TMP/headers" -o "$TMP/$ASSET" "$url" 2>>"$LOG"; then
    curl -fsS --max-time 600 -k --pinnedpubkey "sha256//${PIN}" -D "$TMP/headers" -o "$TMP/$ASSET" "$url" 2>>"$LOG" \
      || die "Cannot download the probe from ${SERVER}. Is the main server reachable from here (port and firewall)? Details: $LOG"
  fi
  want="$(awk -F': *' 'tolower($1)=="x-checksum-sha256" {print $2}' "$TMP/headers" | tr -d '\r')"
  got="$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1)"
  [[ -n "$want" && "$want" == "$got" ]] || die "Checksum mismatch for the downloaded probe."
  TARBALL="$TMP/$ASSET"
else
  if [[ "$VERSION" == "latest" ]]; then
    LATEST_URL="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest" || true)"
    VERSION="${LATEST_URL##*/tag/}"
    [[ "$LATEST_URL" == */tag/* ]] || die "Could not determine the latest release of github.com/${REPO}"
  fi
  info "Downloading UniFi Syslog probe ${VERSION} from GitHub"
  BASE="https://github.com/${REPO}/releases/download/${VERSION}"
  curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET" || die "Release ${VERSION} has no probe for ${ARCH}. Install with an enrollment key, so the probe comes from the main server."
  if curl -fsSL -o "$TMP/checksums.txt" "$BASE/checksums.txt"; then
    (cd "$TMP" && grep " ${ASSET}\$" checksums.txt | sha256sum -c --quiet -) || die "Checksum mismatch"
  fi
  TARBALL="$TMP/$ASSET"
fi

install -d -m 0755 "$APP_DIR"
tar --no-same-owner -xzf "$TARBALL" -C "$APP_DIR" unify-syslog-probe
chmod 0755 "$APP_DIR/unify-syslog-probe"
ln -sf "$APP_DIR/unify-syslog-probe" /usr/local/bin/unify-syslog-probe
"$APP_DIR/unify-syslog-probe" install-files >>"$LOG"
run systemd-sysusers
run systemd-tmpfiles --create
ok "Program installed in $APP_DIR ($("$APP_DIR/unify-syslog-probe" version))"

# Larger socket buffers so bursts are not dropped by the kernel.
cat > /etc/sysctl.d/90-unify-syslog-probe.conf <<EOF
net.core.rmem_max = 8388608
net.core.rmem_default = 4194304
EOF
sysctl -q -p /etc/sysctl.d/90-unify-syslog-probe.conf 2>/dev/null || warn "sysctl not applied (normal in an unprivileged LXC)"

systemctl daemon-reload
systemctl enable -q unify-syslog-probe
systemctl restart unify-syslog-probe
for _ in $(seq 1 30); do
  unify-syslog-probe status >/dev/null 2>&1 && break
  sleep 1
done
systemctl is-active -q unify-syslog-probe || die "The service does not start; see journalctl -u unify-syslog-probe"
ok "Probe service running"

if [[ -n "$KEY" ]]; then
  info "Enrolling with the main server"
  if unify-syslog-probe enroll "$KEY"; then
    ok "Enrolled"
  else
    warn "Enrollment failed. Messages are buffered meanwhile; enroll later with: unify-syslog-probe enroll '<key>'"
  fi
fi

IP="$(hostname -I | awk '{print $1}')"
echo
ok "Installation complete!"
echo -e "   Syslog:      UDP/TCP ${c_info}${IP}:514${c_off}  (point the UniFi devices here)"
echo -e "   Probe page:  ${c_info}http://${IP}:8080${c_off}"
if ! unify-syslog-probe status 2>/dev/null | grep -q '^State: *connected'; then
  echo -e "   Enrollment:  open the probe page and paste the key, or run: unify-syslog-probe enroll '<key>'"
fi
echo -e "   Status:      unify-syslog-probe status"
echo -e "   Updates:     come from the main server (Probes page)"
