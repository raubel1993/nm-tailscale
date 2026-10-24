# Troubleshooting

Most problems show up in the NetworkManager journal. The plugin writes to
standard error, which NetworkManager forwards to its own journal, so plugin
lines appear under the `NetworkManager` unit with the prefix
`nm-tailscale-service:`:

```sh
journalctl -u NetworkManager -b | grep -E 'nm-tailscale-service|vpn\['
```

`tailscaled` logs separately:

```sh
journalctl -u tailscaled -b
```

## Quick reference

| Symptom | Likely cause | Section |
|---------|--------------|---------|
| `connect: permission denied` in the journal | SELinux module missing | [SELinux denials](#selinux-denials) |
| "The VPN service failed to start" / "El servicio VPN no pudo iniciar" | Plugin not installed, NetworkManager not restarted, or the binary exited early | [VPN service failed to start](#vpn-service-failed-to-start) |
| Activation fails with "login failed" | Account needs `tailscale login` | [Login required](#login-required) |
| Activation fails after about 30 seconds | `tailscaled` never reached `Running` | [Timeouts](#timeouts) |
| Activation fails with "bad IP config" | No tailnet address on `tailscale0` | [Bad IP configuration](#bad-ip-configuration) |
| `VPN.Error.BadArguments` | `profile` missing, unknown or ambiguous | [Profile not found or ambiguous](#profile-not-found-or-ambiguous) |
| Connection goes down by itself | Profile switched or tunnel stopped outside NetworkManager | [Connection deactivates on its own](#connection-deactivates-on-its-own) |
| All DNS goes through Tailscale | Tailnet overrides DNS | [DNS](#all-dns-queries-go-through-tailscale) |

## SELinux denials

**Symptom.** The first activation on Fedora fails. The NetworkManager journal
shows:

```
<warn>  [...] vpn[...,"Tailscale finquers"]: connect: failed to connect interactively: 'GDBus.Error:org.freedesktop.NetworkManager.VPN.Error.Failed: listing profiles: Get "http://local-tailscaled.sock/localapi/v0/profiles/": dial unix /var/run/tailscale/tailscaled.sock: connect: permission denied'
```

and the audit log contains:

```
avc:  denied  { write } for  pid=... comm="nm-tailscale-se" name="tailscaled.sock" dev="tmpfs" ino=... scontext=system_u:system_r:NetworkManager_t:s0 tcontext=system_u:object_r:var_run_t:s0 tclass=sock_file permissive=0
```

**Cause.** The plugin runs in NetworkManager's SELinux domain
(`NetworkManager_t`), which Fedora's policy does not allow to use the
`tailscaled` socket.

**Fix.** Load the module shipped in `data/selinux`:

```sh
make selinux
sudo make install-selinux
```

No NetworkManager restart is needed; activate the connection again. Then
confirm that no further denials appear:

```sh
sudo ausearch -m avc -ts recent
# or, without the audit tools:
journalctl -b | grep 'avc:  denied'
```

If a different denial appears (another type or permission), do not switch
SELinux to permissive. Open an issue with the full AVC line so the module can
be extended.

## VPN service failed to start

**Symptom.** `nmcli connection up` or GNOME reports that the VPN service
failed to start ("El servicio VPN no pudo iniciar" with a Spanish locale).

**Causes and fixes.**

- **NetworkManager was not restarted after installation.** It only reads
  plugin descriptors at startup: `sudo systemctl restart NetworkManager`.
- **Files missing or in the wrong place.** Check:

  ```sh
  ls -l /usr/libexec/nm-tailscale-service \
        /usr/lib/NetworkManager/VPN/nm-tailscale-service.name \
        /usr/share/dbus-1/system.d/nm-tailscale-service.conf
  ```

  The `.name` file must be under `lib/`, not `lib64/`, and its `program=`
  line must point at the installed binary.
- **The binary cannot claim its bus name.** Without the D-Bus policy file
  the plugin is not allowed to own
  `org.freedesktop.NetworkManager.tailscale.Connection_N` and exits right
  after starting. Look for an `nm-tailscale-service:` line with an
  access-denied error from the bus, or `bus name ... already taken`, in the
  journal, and reinstall with `sudo make install`.

## Login required

**Symptom.** Activation fails with a login failure, or an active connection
stops. The journal shows one of:

```
nm-tailscale-service: connection failed: tailscale login required (NeedsLogin)
nm-tailscale-service: connection failed: tailscale login required (NeedsMachineAuth)
nm-tailscale-service: tailscaled needs login; stopping
```

**Cause.** The account's node key expired or was never authorised, or the
machine is waiting for admin approval in the tailnet. The plugin never logs
in on your behalf.

**Fix.** Switch to the account and log in from a terminal, or approve the
machine in the Tailscale admin console:

```sh
tailscale switch <account>
tailscale login
```

Then activate the connection again.

## Timeouts

**Symptom.** Activation fails after about 30 seconds with:

```
nm-tailscale-service: connection failed: timed out waiting for tailscaled to reach Running
```

**Cause.** `tailscaled` accepted the request but did not reach `Running`
within 30 seconds, usually because the control server is unreachable (no
network, DNS failure, firewall, self-hosted control server down).

**Fix.** Look at `journalctl -u tailscaled -b` for the reason, for example a
line such as `fetch control key: ... failed to resolve ...`, and check
`tailscale status`. Fix connectivity and activate again.

If `tailscaled` itself is not running, activation fails immediately instead,
with `dial unix /var/run/tailscale/tailscaled.sock: connect: no such file or
directory` in the error. Start it with `sudo systemctl start tailscaled`.

## Bad IP configuration

**Symptom.** Activation fails with "bad IP config". The journal shows
`connection failed: no Tailscale address is assigned to tailscale0` or
`connection failed: interface tailscale0: ...`.

**Cause.** `tailscaled` reports `Running`, but the interface named by the
`interface` key (default `tailscale0`) does not exist or does not carry the
node's tailnet address. This happens when `tailscaled` runs in userspace
networking mode, which is not supported, or when `interface` does not match
the tun device name.

**Fix.** Run `tailscaled` in kernel mode and check `ip addr show tailscale0`.
Remove the `interface` key or set it to the actual device name.

## Profile not found or ambiguous

**Symptom.** Activation is refused immediately with
`org.freedesktop.NetworkManager.VPN.Error.BadArguments` and one of:

```
bad arguments: vpn.data profile is required
bad arguments: no Tailscale profile matches "<value>"
bad arguments: profile selector "<value>" is ambiguous
bad arguments: invalid interface name "<value>"
```

**Fix.** Compare the connection's `vpn.data` with `tailscale switch --list`:

```sh
nmcli -g vpn.data connection show "Tailscale sailo"
tailscale switch --list
```

If the value matches several profiles (for example the same login name on
two tailnets), use the tailnet domain or the profile ID instead. See
[selecting the account](configuration.md#selecting-the-account).

A `VPN.Error.WrongState` error means the same plugin instance is already
connecting or connected; deactivate the connection first.

## Connection deactivates on its own

**Symptom.** An active Tailscale connection disappears from the active list,
without a failure notification. The journal shows:

```
nm-tailscale-service: profile switched or stopped outside NetworkManager; deactivating <uuid>
```

**Cause.** This is intended. Something switched `tailscaled` to another
profile or set `WantRunning=false`: `tailscale switch`, `tailscale down`,
or activating another Tailscale connection. The plugin asks NetworkManager to
deactivate its connection so that the state shown in GNOME matches
`tailscaled`.

If the journal shows `ipn bus unreachable, releasing connection` instead,
the plugin lost contact with `tailscaled` for about 30 seconds (for example
because `tailscaled` was stopped) and released the connection.

Restarting NetworkManager also disconnects active Tailscale connections and
brings the tunnel down.

## All DNS queries go through Tailscale

When the tailnet overrides DNS, `tailscaled` makes MagicDNS the default DNS
route (`resolvectl status tailscale0` shows `DNS Domain: ts.<tailnet> ~.`).
This is `tailscaled` behaviour. Use `tailscale set --accept-dns=false` or
change the tailnet's DNS settings; see
[DNS and routes](configuration.md#dns-and-routes).

## Getting more detail

The plugin always logs failures and external deactivations. Verbose
lifecycle lines (bus name owned, signal received, exit) are enabled either
by the `--debug` flag or by the `NM_VPN_LOG_LEVEL` environment variable that
NetworkManager exports to VPN plugins when its own VPN logging is at debug
level. So the normal way to get verbose output from a NetworkManager-started
instance is to raise NetworkManager's VPN log level temporarily:

```sh
sudo nmcli general logging level DEBUG domains VPN
# reproduce the problem, then restore the defaults:
sudo nmcli general logging level INFO domains DEFAULT
```

This also makes NetworkManager itself log every D-Bus exchange with the
plugin. `--debug` additionally keeps the process running when NetworkManager
leaves the bus, which is only useful when launching the binary by hand.

## Collecting information for a bug report

Include:

```sh
cat /etc/os-release | head -n 4
nmcli --version
tailscale version
rpm -q NetworkManager selinux-policy-targeted      # Fedora
sudo semodule -l | grep nm_tailscale
journalctl -u NetworkManager -b | grep -E 'nm-tailscale-service|vpn\[' | tail -n 50
journalctl -u tailscaled -b --since "10 minutes ago"
sudo ausearch -m avc -ts recent
nmcli -g vpn.data,vpn.persistent connection show "<connection name>"
```

Also describe what you did (GNOME menu or `nmcli`, which accounts) and what
you expected.

Review the output before posting: journal lines and `vpn.data` contain login
names, tailnet domains, hostnames and IP addresses. Replace them with
placeholders if you do not want them public. Never post auth keys or the
contents of `/var/lib/tailscale`.
