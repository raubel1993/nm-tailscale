# nm-tailscale

[![CI](https://github.com/OWNER/nm-tailscale/actions/workflows/ci.yml/badge.svg)](https://github.com/OWNER/nm-tailscale/actions/workflows/ci.yml)
[![License: GPL-2.0-or-later](https://img.shields.io/badge/license-GPL--2.0--or--later-blue.svg)](LICENSE)

A NetworkManager VPN service plugin for Tailscale. Each Tailscale account
(login profile) becomes an ordinary NetworkManager `vpn` connection, so it
appears in GNOME's VPN quick-settings menu and in Settings → Network → VPN,
next to your WireGuard and OpenVPN connections, with no desktop extension.
The plugin only tells `tailscaled` which profile to run and whether the tunnel
should be up; `tailscaled` keeps owning the `tailscale0` interface, routes and
DNS. NetworkManager's behaviour is extended, not replaced.

> **Status:** 0.1.0, pre-release. Verified on Fedora 44 with GNOME 50,
> NetworkManager 1.56 and Tailscale 1.98. Other distributions are untested.

## Why

Tailscale on Linux is driven from the `tailscale` CLI or a third-party tray
or shell extension. Desktop users already have one place to turn VPNs on and
off: NetworkManager. This plugin puts Tailscale there, so that:

- each account is a named connection you can toggle from the system menu or
  with `nmcli connection up|down`;
- switching between several tailnets is a matter of activating another
  connection;
- no GNOME Shell extension is required, and nothing changes in how
  `tailscaled` manages the network.

## How it works

NetworkManager starts `/usr/libexec/nm-tailscale-service` on the system bus
(as `org.freedesktop.NetworkManager.tailscale.Connection_N`) and calls
`Connect` with the connection settings. The plugin then talks to the
`tailscaled` LocalAPI over `/var/run/tailscale/tailscaled.sock`:

| Step | LocalAPI call |
|------|---------------|
| Resolve `vpn.data profile` | `GET profiles/` |
| Switch account if needed | `GET profiles/current`, `POST profiles/<id>` |
| Bring the tunnel up | `PATCH prefs {"WantRunningSet":true,"WantRunning":true}` |
| Wait for `Running` (up to 30 s) | `GET status?peers=false` |
| Follow external changes | `GET watch-ipn-bus?mask=0` |
| Disconnect | `PATCH prefs {"WantRunningSet":true,"WantRunning":false}` |

If you run `tailscale down` or `tailscale switch` from a terminal while the
connection is active, the plugin asks NetworkManager to deactivate its
connection, so the UI shows a clean disconnect rather than a failure.

See [docs/architecture.md](docs/architecture.md) for the D-Bus contract and
state machine.

## Quick start

Full, step-by-step instructions are in
[docs/installation.md](docs/installation.md). On Fedora, building from source
looks like this:

```sh
sudo dnf install golang make checkpolicy policycoreutils
git clone https://github.com/OWNER/nm-tailscale.git
cd nm-tailscale
make build test
sudo make install
make selinux && sudo make install-selinux   # Fedora / SELinux enforcing
sudo systemctl restart NetworkManager       # brief network interruption
```

Fedora package: see `packaging/` (COPR repository to be announced).

`tailscaled` must already be installed and each account logged in once with
`tailscale login`.

## Usage

List your Tailscale accounts:

```sh
tailscale switch --list
```

Create one NetworkManager connection per account:

```sh
nmcli connection add type vpn vpn-type tailscale con-name "Tailscale sailo" \
  vpn.data profile=vpn.sailo.ag vpn.persistent yes
```

`profile` accepts the profile ID, the user-visible name, the login name or the
tailnet domain. Then activate it from the GNOME VPN menu, or:

```sh
nmcli connection up "Tailscale sailo"
nmcli connection down "Tailscale sailo"
```

There is no GNOME connection editor for this plugin: GNOME Settings shows the
on/off toggle, but profiles are created and edited with `nmcli`. All keys,
permissions and DNS behaviour are described in
[docs/configuration.md](docs/configuration.md).

## Security

Read this before installing; the plugin runs as root.

- NetworkManager spawns the plugin as root. Any user that polkit allows to
  use `org.freedesktop.NetworkManager.network-control` (by default, users in
  an active local session; the same rule that applies to the OpenVPN plugin)
  can activate or deactivate Tailscale connections. Non-root users therefore
  do not need `tailscale set --operator`. Use `connection.permissions` to
  restrict a connection to specific users.
- No secrets are stored in or requested by NetworkManager (`NeedSecrets`
  always reports none). Authentication stays with `tailscale login`.
- Only the LocalAPI endpoints listed above are used. The plugin never logs
  out and never deletes profiles.
- `vpn.data` values are validated and are never passed to a shell or used to
  build file paths.
- The D-Bus policy only lets root own the plugin's bus names.
- The optional SELinux module grants exactly two accesses to
  `NetworkManager_t`: write to the `tailscaled` socket file and connect to the
  `tailscaled` process.

## Limitations

- **Kernel mode only.** The `tailscale0` tun device must exist; `tailscaled`
  in userspace-networking mode is not supported.
- **One account at a time.** `tailscaled` runs one profile at a time.
  Activating a second Tailscale connection switches accounts, and the plugin
  deactivates the first connection.
- **No VPN exclusivity.** Neither NetworkManager nor the plugin prevents a
  Tailscale connection from being active together with WireGuard or OpenVPN
  connections. DNS and routing conflicts between them are yours to manage.
- **DNS is decided by tailscaled.** If your tailnet overrides DNS, MagicDNS
  becomes the default DNS route while connected. See
  [docs/configuration.md](docs/configuration.md#dns-and-routes).
- **No GNOME editor plugin.** Create and edit connections with `nmcli`.
- **Fedora-first.** Paths and the SELinux module target Fedora; other
  distributions may need adjustments.

## Documentation

- [Installation](docs/installation.md): prerequisites, build, install,
  SELinux, upgrade and uninstall.
- [Configuration](docs/configuration.md): connection keys, account
  selection, permissions, DNS and routes.
- [Troubleshooting](docs/troubleshooting.md): symptoms, causes, fixes and
  how to collect logs.
- [Architecture](docs/architecture.md): components, D-Bus contract, state
  machine and test strategy.

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md)
before opening an issue or pull request. Run `make test` and `make test-dbus`
before submitting changes.

## License

GPL-2.0-or-later, like the NetworkManager VPN plugins it follows. See
[LICENSE](LICENSE).
