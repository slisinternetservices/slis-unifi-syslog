#!/usr/bin/env bash
# Run inside a NEW Debian 13 amd64 CT, never on the Proxmox host.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"
[[ $EUID -eq 0 ]] || { echo "Run: sudo bash install-slis.sh" >&2; exit 1; }
[[ ! -e /etc/pve ]] || { echo "Refusing to install on a Proxmox host." >&2; exit 1; }
[[ ! -e /etc/unify-syslog/config.yaml && ! -e /var/lib/unify-syslog/meta.db ]] || { echo "Use a fresh CT; an existing installation was found." >&2; exit 1; }
[[ $(uname -m) == x86_64 ]] || { echo "This test bundle requires amd64." >&2; exit 1; }
sha256sum --check checksums.txt
export USS_LOCAL_TARBALL="$PWD/unify-syslog-server_linux_amd64.tar.gz"
export USS_NONINTERACTIVE=1
export USS_PROFILE=ct-small
bash deploy/install.sh
