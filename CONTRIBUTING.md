# Contributing to nm-tailscale

Thanks for helping. This project is a NetworkManager VPN service plugin for
Tailscale, written in Go and targeting Fedora first.

## Before you start

- Open an issue before starting a large change so the approach can be agreed.
- Small fixes and documentation improvements can go straight to a pull request.
- Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).
- Report vulnerabilities privately, as described in [SECURITY.md](SECURITY.md).

## Development setup

You need Go 1.24 or newer, `golangci-lint` (v2), and `dbus-run-session` for the
D-Bus tests. On Fedora: `sudo dnf install golang dbus-daemon`.

```
make build        # bin/nm-tailscale-service
make fmt          # fails if any file is not gofmt-clean
make lint         # golangci-lint run
make test         # unit tests
make test-dbus    # D-Bus adapter tests under dbus-run-session
```

Run `make fmt lint test test-dbus` before opening a pull request.

## Testing safely

The plugin runs as root and controls your network. Prefer tests over a live
system:

- Run the unit tests, which use fakes and need no privileges.
- To exercise the binary by hand, point `--socket` at a fake tailscaled
  listening on a Unix socket you own, and use a session bus.
- `make install` and `make install-selinux` modify the live system (files under
  `/usr`, the SELinux policy). Run them only on a machine where that is
  acceptable, and never from scripts or CI.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) such as
  `feat(service): ...`, `fix(tailscale): ...`, `docs: ...`.
- Releases use SemVer with `vX.Y.Z` tags.
- Behaviour changes need tests. Fix a bug with a test that fails first.
- Keep `docs/` and `README.md` up to date when flags, options or behaviour
  change, and add user-visible changes to `CHANGELOG.md` under `[Unreleased]`.
- Keep pull requests focused. One logical change per pull request.
- There is no DCO or CLA. By contributing you agree your work is licensed under
  GPL-2.0-or-later, the project license.

## Never commit

Do not commit secrets, auth keys, tokens, tailnet names, account names or
internal IP addresses, including in tests, fixtures and logs. Use placeholders
such as `example.ts.net`.
