# Installation

This guide installs nm-tailscale from source. It is written for Fedora, where
the plugin has been verified (Fedora 44, GNOME 50, NetworkManager 1.56,
Tailscale 1.98). Notes for other distributions are at the end.

> **Installation runs code as root.** NetworkManager starts the installed
> binary as root whenever a Tailscale connection is activated. Review the
> source and the [security notes](../README.md#security) before installing.

Fedora package: see `packaging/` (COPR repository to be announced). Building
from source, described below, is the primary installation path.

## 1. Prerequisites

### Tailscale

`tailscaled` must be installed, enabled, and running in kernel mode (the
default on Fedora), so that the `tailscale0` interface exists. Follow
Tailscale's own installation instructions, then log in every account you want
to expose once:

```sh
sudo systemctl enable --now tailscaled
tailscale login            # repeat for each account
tailscale switch --list    # the accounts the plugin will be able to use
```

The plugin never logs in for you; it only switches between profiles that
`tailscaled` already knows.

### Build tools

```sh
sudo dnf install golang make
```

Go 1.24 or newer is required (see `go.mod`). Check with `go version`.

Optional, for the D-Bus adapter tests (`dbus-run-session` is part of
`dbus-daemon`, normally already installed):

```sh
sudo dnf install dbus-daemon
```

Required on systems with SELinux enforcing (the Fedora default), to compile
the policy module (`checkmodule` comes from `checkpolicy`, `semodule_package`
and `semodule` from `policycoreutils`):

```sh
sudo dnf install checkpolicy policycoreutils
```

To inspect SELinux denials you also want `audit` (provides `ausearch`), which
Fedora installs by default.

## 2. Get the source and build

```sh
git clone https://github.com/OWNER/nm-tailscale.git
cd nm-tailscale
make build
```

`make build` produces a static binary at `bin/nm-tailscale-service`
(`CGO_ENABLED=0`, `-trimpath`). No privileges are needed.

## 3. Run the tests

```sh
make test         # unit tests: go test ./...  (no bus needed)
make test-dbus    # D-Bus adapter tests under dbus-run-session
```

Both targets run without root and without touching NetworkManager or
`tailscaled`; the tests use fakes and a private session bus.

## 4. Install the plugin

```sh
sudo make install
```

This installs three files:

| File | Purpose |
|------|---------|
| `/usr/libexec/nm-tailscale-service` | The plugin binary, started by NetworkManager |
| `/usr/lib/NetworkManager/VPN/nm-tailscale-service.name` | Tells NetworkManager about the `tailscale` VPN type |
| `/usr/share/dbus-1/system.d/nm-tailscale-service.conf` | System bus policy: only root may own or call the plugin |

NetworkManager reads `.name` files only from `/usr/lib/NetworkManager/VPN/`,
never from `lib64/`, even on 64-bit systems. Do not change `NMVPNDIR` to a
`lib64` path.

The Makefile honours `PREFIX`, `LIBEXECDIR`, `NMVPNDIR`, `DBUSDIR` and
`DESTDIR`. If you change `LIBEXECDIR`, also update the `program=` line in
`data/nm-tailscale-service.name`, which is installed as-is.

## 5. Install the SELinux module (Fedora)

NetworkManager runs confined in the `NetworkManager_t` domain and the plugin
inherits it. Fedora's policy does not let that domain reach the `tailscaled`
socket, so without this module the first activation fails with
`connect: permission denied`.

```sh
make selinux               # compiles bin/nm_tailscale.pp, no privileges
sudo make install-selinux  # loads it with semodule -i
```

The module (`data/selinux/nm-tailscale.te`) grants exactly two accesses:

```
allow NetworkManager_t var_run_t:sock_file write;
allow NetworkManager_t unconfined_service_t:unix_stream_socket connectto;
```

Check that it is loaded:

```sh
sudo semodule -l | grep nm_tailscale
```

Expected output: `nm_tailscale`.

Do not switch SELinux to permissive to work around denials; extend the module
instead and report the denial (see
[troubleshooting](troubleshooting.md#selinux-denials)).

## 6. Restart NetworkManager

NetworkManager reads VPN plugin descriptors at startup:

```sh
sudo systemctl restart NetworkManager
```

This briefly interrupts networking. When NetworkManager stops, any active
Tailscale connection managed by the plugin is disconnected and the tunnel is
brought down (`WantRunning=false`); reactivate it afterwards.

## 7. Verify

Create a connection for one of your accounts (see
[configuration](configuration.md) for details) and activate it:

```sh
nmcli connection add type vpn vpn-type tailscale con-name "Tailscale example" \
  vpn.data profile=<account from tailscale switch --list> vpn.persistent yes
nmcli connection up "Tailscale example"
```

Expected: `Connection successfully activated (D-Bus active path: ...)`
(localized according to your locale).

```sh
tailscale status                       # lists this node; not "Tailscale is stopped."
nmcli connection show --active         # lists "Tailscale example" with type vpn
sudo ausearch -m avc -ts recent        # expected: <no matches>
```

Then test an external change:

```sh
tailscale down
journalctl -u NetworkManager -b | grep nm-tailscale-service | tail -n 3
```

Expected journal line:

```
nm-tailscale-service: profile switched or stopped outside NetworkManager; deactivating <uuid>
```

and the connection disappears from `nmcli connection show --active` without
being reported as failed.

## Upgrading

```sh
cd nm-tailscale
git pull
make build test
sudo make install
make selinux && sudo make install-selinux   # only if the module changed
sudo systemctl restart NetworkManager
```

Existing NetworkManager connections are kept; they are stored by
NetworkManager, not by the plugin.

## Uninstalling

Delete the connections you created first, so NetworkManager does not keep
connections for a VPN type it can no longer handle:

```sh
nmcli connection show | grep -i tailscale
nmcli connection delete "Tailscale example"
```

Then remove the plugin files and the SELinux module, and restart
NetworkManager:

```sh
sudo make uninstall
sudo systemctl restart NetworkManager
```

`make uninstall` removes the three installed files and runs
`semodule -r nm_tailscale` (an error from that step is ignored when the module
was never loaded). `tailscaled` and its profiles are not touched.

## Other distributions

Other distributions are untested. What to adapt:

- **Paths.** Confirm where your NetworkManager build looks for VPN plugin
  descriptors (usually `/usr/lib/NetworkManager/VPN/`) and where helper
  binaries belong (`/usr/libexec` or `/usr/lib/NetworkManager`). Override
  `LIBEXECDIR`, `NMVPNDIR` or `DBUSDIR` on the `make install` command line and
  keep `program=` in `data/nm-tailscale-service.name` consistent.
- **tailscaled socket.** The default is `/var/run/tailscale/tailscaled.sock`.
  The binary accepts `--socket`, but NetworkManager starts it without extra
  flags, so a different socket path currently needs a code change to
  `tailscale.DefaultSocket`.
- **SELinux policies other than Fedora's.** Type names (`NetworkManager_t`,
  `var_run_t`, `unconfined_service_t`) may differ. Check
  `ls -Z /var/run/tailscale/tailscaled.sock` and the denials in the audit log,
  and adjust the module accordingly.
- **AppArmor.** No profile is shipped. If NetworkManager is confined by
  AppArmor on your system, it must be allowed to execute
  `/usr/libexec/nm-tailscale-service` and to connect to the `tailscaled`
  socket.
- **Userspace networking.** Distributions or setups that run `tailscaled`
  with `--tun=userspace-networking` are not supported.
