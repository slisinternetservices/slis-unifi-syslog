#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# Load the actual verification function without executing the root installer.
source <(sed -n '/^verify_checksum() {$/,/^}$/p' deploy/install.sh)
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
asset=unify-syslog-server_linux_amd64.tar.gz
printf 'known payload\n' > "$dir/$asset"
hash=$(sha256sum "$dir/$asset" | cut -d' ' -f1)
for ending in '\n' '\r\n'; do
  printf "%s  %s$ending" "$hash" "$asset" > "$dir/checksums.txt"
  verify_checksum "$dir" "$asset"
done
printf '%s *%s\r\n' "$hash" "$asset" > "$dir/checksums.txt"
verify_checksum "$dir" "$asset"
for bad in missing malformed duplicate mismatch; do
  case "$bad" in
    missing) printf '%s  other.tar.gz\n' "$hash" > "$dir/checksums.txt" ;;
    malformed) printf 'invalid  %s\n' "$asset" > "$dir/checksums.txt" ;;
    duplicate) printf '%s  %s\n%s  %s\n' "$hash" "$asset" "$hash" "$asset" > "$dir/checksums.txt" ;;
    mismatch) printf '%064d  %s\n' 0 "$asset" > "$dir/checksums.txt" ;;
  esac
  if verify_checksum "$dir" "$asset"; then echo "Unexpected acceptance: $bad" >&2; exit 1; fi
done
echo 'Checksum tests passed: LF/CRLF accepted; missing/malformed/duplicate/mismatch rejected.'
