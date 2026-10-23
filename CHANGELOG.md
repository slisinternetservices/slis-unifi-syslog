# Changelog

## [Unreleased]

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
