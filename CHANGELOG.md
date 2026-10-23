# Changelog

## [0.1.0-beta.2] - 2026-10-23

### Changed
- Primary target is now an unprivileged Debian 13 Proxmox CT: 2 CPU, 2048 MiB RAM,
  1024 MiB swap and 16 GiB disk. This replaces the beta.1 VM recommendation.
- Added a native pct provisioner and a ct-small installer profile.
- Reduced ingestion buffers, ClickHouse caches/background pools and query parallelism.
- Added an isolated 2 CPU / 2 GiB container installation and ingestion smoke test.
- Retention stays configurable; 7 days is recommended for the small test disk.

## [Unreleased]

### Branding
- Replaced the default square badge with the original blue SLIS letterhead wordmark,
  including INTERNET SERVICES, rendered directly from the supplied vector artwork.
- Added transparent light/dark PNGs and intrinsic dimensions to preserve proportions.
- Dark backgrounds use a light subtitle; no white rectangle, rounded crop or image shadow.


### Fixed
- Installer checksum verification accepts LF and CRLF manifests and fails closed on missing,
  duplicate, malformed or mismatched entries. Fixes the beta.2 Windows checksum packaging error.
- Bundled installer normalizes checksum manifest line endings before checking.
- Bootstrap installation uses the Debian C.UTF-8 locale to avoid missing en_US locale warnings.
- Updated host provisioner retrieves the repaired installer while retaining beta.2 binaries.


- Published the source to the SLIS GitHub repository and added a Linux-tested beta release workflow.
- Full backend tests passed on the GitHub Linux runner; actual Proxmox guest acceptance remains outstanding.

## [0.1.0-beta.1] - 2026-10-23

### Added
- First SLIS Internet Services test distribution from upstream commit 698b733df37d7daeaf7799f20647c28a4d4ab8aa.
- Supplied SLIS logo, blue/cyan theme, browser identity and company defaults.
- Debian 13 / Proxmox VM deployment guide, bundled installer and checksum verification.
- English README, source attribution and validation report.

### Changed
- Installer and updater distribution destination to slisinternetservices/slis-unifi-syslog.
- Web dependencies updated to address reported advisories; see lockfile.

### Limitations
- Beta software; exact Proxmox 9.2.21 installation and end-to-end ingestion require guest acceptance testing.
- No GitHub publication; upstream redistribution license still needs to be agreed.
