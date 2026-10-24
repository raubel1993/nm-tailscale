# Configuration

Each Tailscale account is represented by one NetworkManager connection of type
`vpn` with `vpn-type tailscale`. The plugin stores nothing of its own: all
settings live in the NetworkManager connection, and all Tailscale state
(keys, login, prefs) stays in `tailscaled`.

## Creating a connection

1. List the accounts `tailscaled` knows about:

   ```sh
   tailscale switch --list
   ```

   An account that is not listed must be added first with `tailscale login`.

2. Create one connection per account:

   ```sh
   nmcli connection add type vpn vpn-type tailscale con-name "Tailscale sailo" \
     vpn.data profile=vpn.sailo.ag vpn.persistent yes
   nmcli connection add type vpn vpn-type tailscale con-name "Tailscale finquers" \
     vpn.data profile=vpn.finquers.com vpn.persistent yes
   ```

3. Activate it:

   ```sh
   nmcli connection up "Tailscale sailo"
   ```

The connection name (`con-name`) is free text; it is what GNOME displays.

## `vpn.data` keys

| Key | Required | Default | Meaning |
|-----|----------|---------|---------|
| `profile` | yes | none | Which Tailscale login profile to run. See [selecting the account](#selecting-the-account). |
| `interface` | no | `tailscale0` | Name of the tun device `tailscaled` uses. Must be a valid interface name (no `/` or whitespace, at most 15 characters). |

Unknown keys are ignored. Several keys are given comma-separated:

```sh
nmcli connection modify "Tailscale sailo" vpn.data "profile=vpn.sailo.ag,interface=tailscale0"
```

`interface` only tells the plugin where to look for the node's tailnet
address after `tailscaled` reports `Running`; it does not rename or create
anything. Set it only if you run `tailscaled` with a different `--tun` name.

There are no secrets to configure; `nmcli connection up` never prompts for a
password.

## Selecting the account

`profile` is matched against the profiles returned by `tailscaled`, in this
order:

1. **Exact profile ID.** If the value equals a profile ID, that profile is
   used.
2. **Unique case-insensitive match** on any of:
   - the profile's user-visible name,
   - the login name (for example `user@example.com`),
   - the tailnet domain (for example `vpn.sailo.ag`).

If nothing matches, or more than one profile matches (for example two
accounts with the same login name on different tailnets), activation is
refused with `org.freedesktop.NetworkManager.VPN.Error.BadArguments`. Use a
more specific value, such as the tailnet domain or the profile ID, in that
case.

The value is only used for this lookup. It is never passed to a shell, used
in a file path, or sent to `tailscaled` verbatim; only the ID of the matched
profile is.

## Using GNOME

Once a connection exists, GNOME lists it:

- in the VPN section of the quick-settings menu (top-right system menu), and
- in Settings → Network → VPN.

Both places can turn the connection on and off. There is no GNOME editor
plugin for the `tailscale` VPN type, so the edit dialog cannot change
`vpn.data`; use `nmcli connection modify` for that.

## Restricting who can use a connection

By default a connection is available to every user that polkit allows to
control networking (`org.freedesktop.NetworkManager.network-control`,
normally every user in an active local session). To restrict a connection to
one user:

```sh
nmcli connection modify "Tailscale sailo" connection.permissions user:$USER
```

Because the plugin runs as root under NetworkManager, users do not need to be
made Tailscale operators (`tailscale set --operator`) to activate a
connection.

## Switching accounts

`tailscaled` runs one profile at a time. Activating a Tailscale connection
whose profile is not the current one switches `tailscaled` to it. The
connection that was using the previous profile notices the switch and asks
NetworkManager to deactivate it, so exactly one Tailscale connection stays
active.

The same happens if you switch accounts or stop the tunnel outside
NetworkManager, for example with `tailscale switch` or `tailscale down`:
the active connection is deactivated cleanly, not reported as failed.

## DNS and routes

The plugin does not configure addresses, routes or DNS. `tailscaled` applies
them to `tailscale0` exactly as it does without NetworkManager, according to
the node's prefs and the tailnet's settings.

One visible consequence: when the tailnet's DNS settings override local DNS,
activating the connection makes MagicDNS the default DNS route. You can see it
with:

```sh
resolvectl status tailscale0
```

which then shows a line such as `DNS Domain: ts.<tailnet> ~.`; the `~.`
routes all DNS queries through Tailscale. This is `tailscaled` behaviour, not
the plugin's. To change it, either:

- stop accepting the tailnet's DNS on this machine:

  ```sh
  tailscale set --accept-dns=false
  ```

  (this is a per-profile pref; run it while the relevant profile is active), or
- change the DNS settings of the tailnet in the Tailscale admin console.

Similarly, accepting subnet routes or using an exit node is controlled with
`tailscale set` (`--accept-routes`, `--exit-node`), not through NetworkManager.

## Several VPNs at once

NetworkManager does not make VPN connections mutually exclusive, and neither
does the plugin. A Tailscale connection can be active at the same time as a
WireGuard or OpenVPN connection; this was observed in practice. Whether that
works for you depends on how their routes and DNS settings overlap, which is
your responsibility to check.

## Persistent connections

`vpn.persistent yes` asks NetworkManager to keep the VPN connection across
link changes and outages of the underlying network, until it is explicitly
disconnected. NetworkManager only honours it for plugins that declare support,
which this plugin does (`can-persist`); `tailscaled` re-establishes the tunnel
itself when the network returns. The examples in this
guide set it; it is optional.

## Removing a connection

```sh
nmcli connection delete "Tailscale sailo"
```

This only removes the NetworkManager connection. The Tailscale account stays
logged in inside `tailscaled`; remove it with the `tailscale` CLI if you want
to.
