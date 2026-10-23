# Validation — 2026-10-23

## Passed locally

- Web TypeScript check and production build with Vite 8.3.5.
- npm audit: zero known vulnerabilities at packaging time (all dependency scopes).
- Linux amd64 server cross-build using Go 1.26.8, CGO disabled.
- SLIS default identity, English language and persisted branding override test.
- Existing authentication, UniFi parser, UniFi API client and query package tests on Windows.
- Archive contents, executable architecture and SHA-256 checksums verified during packaging.

## Not passed / not executed

The complete `go test ./...` was attempted on Windows. It does not pass there:
upstream uses Linux syscall types, Unix executables and file lifecycle behavior
that Windows cannot provide. WSL is not installed. This is not a full backend test pass.
Linux CI workflows are included for the full suite, vet, shell syntax checks and
upstream installation tests. They have not been run on GitHub for this SLIS snapshot.

No Proxmox guest was available. Actual Debian package installation, ClickHouse startup,
web login and end-to-end syslog ingestion still need the acceptance checklist in PROXMOX.md.
Optional integrations, backups/restore, alerts, SSO and update/rollback need their own acceptance tests.

This is a compiled test candidate, not a production-validated appliance or bootable VM image.
