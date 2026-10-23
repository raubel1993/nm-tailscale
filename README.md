# nm-tailscale-service

NetworkManager VPN service plugin for Tailscale. Each Tailscale account
becomes an ordinary NetworkManager `vpn` connection, so it shows up in the
GNOME system menu and in Settings → Network → VPN, next to WireGuard and
OpenVPN profiles, with no desktop extension.

The plugin only tells `tailscaled` which profile to run and whether the
tunnel should be up. `tailscaled` keeps owning the interface, routes and DNS.
NetworkManager's behaviour is extended, not replaced.

## How it works

NetworkManager starts `/usr/libexec/nm-tailscale-service --bus-name
org.freedesktop.NetworkManager.tailscale.Connection_N` on the system bus and
calls `Connect` with the connection settings. The plugin then uses the
`tailscaled` LocalAPI over `/var/run/tailscale/tailscaled.sock`:

| Step | LocalAPI call |
|------|---------------|
| Resolve `vpn.data profile` | `GET profiles/` |
| Switch account if needed | `GET profiles/current`, `POST profiles/<id>` |
| Bring the tunnel up | `PATCH prefs {"WantRunningSet":true,"WantRunning":true}` |
| Wait for `Running` (30 s) | `GET status?peers=false` |
| Follow external changes | `GET watch-ipn-bus?mask=0` |
| Disconnect | `PATCH prefs {"WantRunningSet":true,"WantRunning":false}` |

It never calls `logout` and never deletes profiles. No secrets are stored in
NetworkManager; authentication stays with `tailscale login`.

If you run `tailscale down` or `tailscale switch` from a terminal, the plugin
asks NetworkManager to deactivate its connection, so the UI shows a clean
disconnect rather than a failure.

## Build and test

Requires Go (Fedora: `sudo dnf install golang`).

```
make build        # bin/nm-tailscale-service
make test         # unit tests (no bus needed)
make test-dbus    # D-Bus adapter tests under dbus-run-session
```

## Install

Installation changes the system: it adds a NetworkManager plugin
descriptor, a D-Bus policy and a root-run binary.

```
sudo make install
sudo systemctl restart NetworkManager
```

Files installed:

- `/usr/libexec/nm-tailscale-service`
- `/usr/lib/NetworkManager/VPN/nm-tailscale-service.name`
- `/usr/share/dbus-1/system.d/nm-tailscale-service.conf`

NetworkManager only reads `.name` files from `lib/`, never `lib64/`.

## Create connections

Find your accounts first:

```
tailscale switch --list
```

Then add one connection per account. `profile` accepts the profile ID, the
login name, the user-visible name or the tailnet domain:

```
nmcli connection add type vpn vpn-type tailscale con-name "Tailscale sailo" \
  vpn.data profile=vpn.sailo.ag vpn.persistent yes
nmcli connection add type vpn vpn-type tailscale con-name "Tailscale finquers" \
  vpn.data profile=vpn.finquers.com vpn.persistent yes
```

Optional: `vpn.data "profile=...,interface=tailscale0"` when the tunnel device
has another name. To restrict a profile to your user:

```
nmcli connection modify "Tailscale sailo" connection.permissions user:$USER
```

Activate from the GNOME menu or with `nmcli connection up "Tailscale sailo"`.

## SELinux (Fedora)

NetworkManager runs confined. After the first activation, check for denials:

```
sudo ausearch -m avc -ts recent
```

If the plugin is denied access to the tailscaled socket, build a small local
policy module from the denials instead of disabling enforcement.

## Limitations

- No GNOME connection editor plugin: create and edit profiles with `nmcli`.
  GNOME can still activate and deactivate them.
- `tailscaled` runs one profile at a time. Activating a second Tailscale
  connection switches profiles and the first connection is deactivated.
- Kernel mode only (`tailscale0` must exist). Userspace networking is not
  supported.
- Exclusivity with WireGuard or OpenVPN connections is not enforced; you can
  have several VPNs active, with the DNS and routing consequences that implies.
- The plugin runs as root and is reachable by any user who may activate
  connections (`org.freedesktop.NetworkManager.network-control`). Values from
  `vpn.data` are validated and never passed to a shell.

## License

GPL-2.0-or-later, like the NetworkManager VPN plugins it follows.
