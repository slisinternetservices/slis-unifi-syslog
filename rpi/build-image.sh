#!/usr/bin/env bash
# Builds the Raspberry Pi image of the UniFi Syslog probe: Raspberry Pi OS
# Lite (64-bit, based on Debian 13) with the probe installed and enabled.
#
#   sudo rpi/build-image.sh <probe arm64 tarball> <probe binary runnable here> <output.img.xz>
#
# The second binary (the probe for the build machine's architecture) writes
# the systemd unit and friends into the image; nothing inside the image has
# to run, so no emulation is needed. On first boot systemd creates the
# service user, the probe starts buffering on port 514 and asks for its
# enrollment key: on its web page (port 8080), at the first sign-in, or from
# uss-probe.txt on the boot partition.
#
# Options (environment variables):
#   RPI_BASE_URL   base image (default: latest Raspberry Pi OS Lite arm64)
#   RPI_CACHE      directory to cache the base image download
#   KEEP_MOUNTED=1 leave the image mounted at $WORK/root for tests (prints WORK=…)
set -euo pipefail

TARBALL="$(realpath "$1")"
TOOL="$(realpath "$2")"
OUT="$(realpath -m "$3")"
BASE_URL="${RPI_BASE_URL:-https://downloads.raspberrypi.com/raspios_lite_arm64_latest}"
CACHE="${RPI_CACHE:-/var/tmp/rpi-cache}"
HERE="$(cd "$(dirname "$0")" && pwd)"

[[ $EUID -eq 0 ]] || { echo "run as root (loop devices and mounts)" >&2; exit 1; }
for c in losetup xz curl sha256sum; do command -v "$c" >/dev/null || { echo "missing $c" >&2; exit 1; }; done

WORK="$(mktemp -d /var/tmp/uss-rpi.XXXXXX)"
LOOP=""
cleanup() {
  local rc=$?
  if [[ -n "${KEEP_MOUNTED:-}" && $rc -eq 0 ]]; then return; fi
  umount -R "$WORK/root" 2>/dev/null || true
  [[ -n "$LOOP" ]] && losetup -d "$LOOP" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# ---------------------------------------------------------------- base image
mkdir -p "$CACHE"
final="$(curl -fsSL -o /dev/null -w '%{url_effective}' "$BASE_URL")"
name="$(basename "$final")"
echo "==> Base image: $name"
if [[ ! -s "$CACHE/$name" ]]; then
  curl -fsSL --retry 3 -o "$CACHE/$name.part" "$final"
  mv "$CACHE/$name.part" "$CACHE/$name"
fi
if curl -fsSL -o "$WORK/base.sha256" "$final.sha256"; then
  (cd "$CACHE" && awk -v n="$name" '{print $1"  "n}' "$WORK/base.sha256" | sha256sum -c --quiet -) || { rm -f "$CACHE/$name"; echo "checksum mismatch of the base image" >&2; exit 1; }
fi
IMG="$WORK/probe.img"
xz -dc "$CACHE/$name" >"$IMG"

# ---------------------------------------------------------------- mount
LOOP="$(losetup -f --show -P "$IMG")"
for _ in $(seq 1 10); do [[ -b "${LOOP}p2" ]] && break; sleep 0.5; done
mkdir -p "$WORK/root"
mount "${LOOP}p2" "$WORK/root"
mkdir -p "$WORK/root/boot/firmware"
mount "${LOOP}p1" "$WORK/root/boot/firmware"
R="$WORK/root"
grep -q 'VERSION_ID="13"' "$R/etc/os-release" || echo "warning: base image is not Debian 13: $(grep PRETTY_NAME "$R/etc/os-release")"

# ---------------------------------------------------------------- probe
install -d -m 0755 "$R/opt/unify-syslog-probe"
tar --no-same-owner -xzf "$TARBALL" -C "$R/opt/unify-syslog-probe" unify-syslog-probe
chmod 0755 "$R/opt/unify-syslog-probe/unify-syslog-probe"
file "$R/opt/unify-syslog-probe/unify-syslog-probe" 2>/dev/null | grep -q aarch64 || echo "warning: the probe in the image may not be arm64"
ln -sf /opt/unify-syslog-probe/unify-syslog-probe "$R/usr/local/bin/unify-syslog-probe"
"$TOOL" install-files --root "$R"
# Create the service user and directories now, so the first boot does not depend on it.
if command -v systemd-sysusers >/dev/null; then
  systemd-sysusers --root "$R" unify-syslog-probe.conf
  systemd-tmpfiles --root "$R" --create unify-syslog-probe.conf
else
  echo "warning: systemd-sysusers not found; the probe creates its user at first boot"
fi
cat >"$R/etc/sysctl.d/90-unify-syslog-probe.conf" <<EOF
net.core.rmem_max = 8388608
net.core.rmem_default = 4194304
EOF

# A recognisable name on the network; Raspberry Pi Imager can override it.
echo uss-probe >"$R/etc/hostname"
sed -i 's/^127\.0\.1\.1.*/127.0.1.1\tuss-probe/' "$R/etc/hosts"

install -m 0644 "$HERE/boot-README.txt" "$R/boot/firmware/uss-probe-README.txt"

VERSION="$("$TOOL" version | awk '{print $2}')"
echo "unify-syslog-probe $VERSION, built $(date -u +%Y-%m-%d) from $name" >"$R/etc/unify-syslog-probe-image"
echo "==> Probe $VERSION installed into the image"

if [[ -n "${KEEP_MOUNTED:-}" ]]; then
  echo "WORK=$WORK"
  echo "LOOP=$LOOP"
  exit 0
fi

sync
umount -R "$R"
losetup -d "$LOOP"
LOOP=""
echo "==> Compressing"
xz -T0 -6 -c "$IMG" >"$OUT.part"
mv "$OUT.part" "$OUT"
(cd "$(dirname "$OUT")" && sha256sum "$(basename "$OUT")")
