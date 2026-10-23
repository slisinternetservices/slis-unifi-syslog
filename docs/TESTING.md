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

## GitHub Linux validation

The Publish SLIS test release workflow has now passed the full `go test ./...`
suite and installer shell syntax checks on Ubuntu Linux for commit
93ad183e059f98bd99b532591459494cb584b078.
The production web build and npm audit also passed (zero known vulnerabilities).
This supersedes the earlier Windows-only backend validation limitation above.
Actual Proxmox guest installation and acceptance testing remain outstanding.

Workflow: https://github.com/slisinternetservices/slis-unifi-syslog/actions/runs/39990936443

## Beta.2 small container validation

Passed in an unprivileged Debian 13 container managed by Incus/LXC on a GitHub
Ubuntu runner, with 2 CPU and a hard 2 GiB RAM limit, swap disabled:

- Fresh installation of the server and its local ClickHouse database.
- Application/database health, admin login and authenticated session.
- 149 simulated syslog messages persisted at 10 messages/second over 15 seconds.
- Container restart followed by a successful health check.
- The small queue/batch configuration was verified in the installed config.

Cgroup memory after ingestion: 1,606,684,672 bytes. Recorded peak:
1,662,599,168 bytes (about 1586 MiB), including filesystem/page cache and installation activity.
This short smoke test does not measure production capacity, sustained load, or the lowest
possible resident memory. The supported test profile therefore stays at 2048 MiB.
The provisioner assigns 1024 MiB swap on Proxmox as an emergency buffer.

Workflow: https://github.com/slisinternetservices/slis-unifi-syslog/actions/runs/40006877500

Actual pct provisioning on the user's Proxmox 9.2.21 host remains untested.
The Incus test exercises Linux container confinement, service installation and ingestion;
it is not an execution of the Proxmox host provisioning script.
