# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- NetworkManager VPN service plugin for Tailscale: each Tailscale account is an
  ordinary NetworkManager `vpn` connection, controlled through the `tailscaled`
  LocalAPI.
- Profile switching: `vpn.data profile` accepts the profile ID, login name,
  display name or tailnet domain.
- External change detection through the IPN bus: `tailscale down` or
  `tailscale switch` outside NetworkManager deactivates the connection cleanly.
- SELinux module `nm_tailscale` allowing NetworkManager to reach the tailscaled
  socket on Fedora.
- Verified on Fedora.
- RPM spec for COPR builds, GitHub Actions CI and tag-triggered release
  workflow.

[Unreleased]: https://github.com/OWNER/nm-tailscale/compare/v0.1.0...HEAD
