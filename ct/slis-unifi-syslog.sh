#!/usr/bin/env bash
# SLIS small CT provisioner. Run on the Proxmox HOST as root.
set -euo pipefail
[[ $EUID -eq 0 && -d /etc/pve ]] || { echo "Run as root on the Proxmox host." >&2; exit 1; }
command -v pct >/dev/null
VERSION=v0.1.0-beta.2
# Installer repairs can be shipped without replacing tested application binaries.
INSTALL_REF=main
CTID="${SLIS_CTID:-$(pvesh get /cluster/nextid | tr -d '\"')}"
STORAGE="${SLIS_STORAGE:-local-lvm}"
TEMPLATE_STORAGE="${SLIS_TEMPLATE_STORAGE:-local}"
BRIDGE="${SLIS_BRIDGE:-vmbr0}"
[[ "$CTID" =~ ^[1-9][0-9]+$ ]] || { echo "Invalid CTID" >&2; exit 1; }
if pct status "$CTID" >/dev/null 2>&1; then echo "CT $CTID already exists; refusing to overwrite." >&2; exit 1; fi
pveam update
TEMPLATE=$(pveam available --section system | awk '$2 ~ /^debian-13-standard_.*amd64.tar.zst$/ {print $2}' | sort -V | tail -1)
[[ -n "$TEMPLATE" ]] || { echo "Debian 13 amd64 template not found." >&2; exit 1; }
pveam download "$TEMPLATE_STORAGE" "$TEMPLATE"
pct create "$CTID" "$TEMPLATE_STORAGE:vztmpl/$TEMPLATE" \
  --hostname slis-syslog --unprivileged 1 --cores 2 --memory 2048 --swap 1024 \
  --rootfs "$STORAGE:16" --net0 "name=eth0,bridge=$BRIDGE,ip=dhcp" --onboot 1
# On error, keep the CT and all data for diagnosis; never destroy it automatically.
echo "Created CT $CTID. Installation errors can be inspected with: pct enter $CTID"
pct start "$CTID"
for _ in $(seq 1 60); do
  pct exec "$CTID" -- getent hosts deb.debian.org >/dev/null 2>&1 && break
  sleep 2
done
pct exec "$CTID" -- env LANG=C.UTF-8 LC_ALL=C.UTF-8 DEBIAN_FRONTEND=noninteractive bash -c 'apt-get update && apt-get install -y curl ca-certificates'
pct exec "$CTID" -- curl -fsSL "https://raw.githubusercontent.com/slisinternetservices/slis-unifi-syslog/$INSTALL_REF/deploy/install.sh" -o /root/slis-install.sh
pct exec "$CTID" -- env USS_PROFILE=ct-small USS_VERSION="$VERSION" USS_NONINTERACTIVE=1 bash /root/slis-install.sh
echo "SLIS CT $CTID ready: 2 CPU, 2048 MiB RAM, 1024 MiB swap, 16 GiB disk."
pct exec "$CTID" -- hostname -I
echo "Dashboard: http://CT-IP:8080"
echo "Credentials: pct exec $CTID -- cat /root/unify-syslog.creds"
