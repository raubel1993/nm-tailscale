# nm-tailscale-plugin

Objective: NetworkManager VPN service plugin that exposes each Tailscale account as an NM `vpn` connection, so GNOME lists it natively.
Branch: `feat/nm-vpn-plugin` in `nm-tailscale-service/`. Delivery: `single-pr`. Runner: `go test ./...` (Go 1.26, Fedora `golang`).
Engram mirror: pending (server unavailable this session).

## Specs

- S1. Plugin binary `nm-tailscale-service` implementing D-Bus `org.freedesktop.NetworkManager.VPN.Plugin` at `/org/freedesktop/NetworkManager/VPN/Plugin`: `Connect`, `ConnectInteractive`, `NeedSecrets` (returns empty string), `Disconnect`, no-op `SetConfig/SetIp4Config/SetIp6Config/SetFailure/NewSecrets`; property `State`; signals `StateChanged`, `Config`, `Failure`. Flags `--bus-name`, `--persist`, `--debug`, `--socket`.
- S2. `Connect` reads `vpn.data` keys `profile` (required: ID, Name, UserProfile.LoginName or NetworkProfile.DomainName, case-insensitive unique match) and `interface` (default `tailscale0`). Unknown/ambiguous profile returns D-Bus error `org.freedesktop.NetworkManager.VPN.Error.BadArguments`.
- S3. Connect flow against tailscaled LocalAPI over the Unix socket: `GET profiles/`, `GET profiles/current`, `POST profiles/<id>` only when current differs, `PATCH prefs {"WantRunningSet":true,"WantRunning":true}`, then poll `GET status?peers=false` every 500 ms up to 30 s. `Running` → emit `Config` with `tundev`, `gateway` (own tailnet IPv4 as native-endian u32, else IPv6 as bytes), `can-persist=true`, `has-ip4=false`, `has-ip6=false`, then `StateChanged(STARTED)`. `NeedsLogin`/`NeedsMachineAuth` → `Failure(LOGIN_FAILED)` + `StateChanged(STOPPED)`. Timeout → `Failure(CONNECT_FAILED)` + `StateChanged(STOPPED)`.
- S4. External change detection via `GET watch-ipn-bus?mask=0`: when current profile is no longer ours or `WantRunning=false`, call NM `DeactivateConnection` on the active connection whose `Uuid` equals `connection.uuid` (clean disconnect, not failure). `NeedsLogin` while connected → `Failure(LOGIN_FAILED)` + `StateChanged(STOPPED)`.
- S5. `Disconnect`: `PATCH prefs WantRunning=false` only if the current profile is still ours and the connection was not released externally; always emit `StateChanged(STOPPING)` then `STOPPED`; exit unless `--persist`. Exit when NetworkManager leaves the system bus (name owner watch) unless `--debug`.
- S6. No secrets stored or requested; only the LocalAPI endpoints above are used (never `logout`, never `DELETE profiles`). `vpn.data` values are never passed to a shell or used to build file paths.
- S7. Install data: `data/nm-tailscale-service.name` (`[VPN Connection] name=tailscale service=org.freedesktop.NetworkManager.tailscale program=/usr/libexec/nm-tailscale-service supports-multiple-connections=true`) and `data/nm-tailscale-service.conf` (root may own/send; default deny). Makefile `install` targets `/usr/lib/NetworkManager/VPN`, `/usr/share/dbus-1/system.d`, `/usr/libexec`. Installation is never run automatically (user rule: ask before modifying the OS).
- S8. README documents build, install, `nmcli connection add type vpn vpn-type tailscale ... vpn.data profile=<x> vpn.persistent yes`, SELinux check (`ausearch -m avc -ts recent`), and limitations (no GNOME editor plugin, kernel mode only, one profile at a time, exclusivity with other VPNs is the user's responsibility).

## Tasks

- [ ] T1 (S2,S3,S4,S5,S6) tailscale LocalAPI client + tests — inline — written, unverified (no Go toolchain)
- [ ] T2 (S2,S3,S4,S5,S6) service core (connect/disconnect/watch) + tests — inline — written, unverified
- [ ] T3 (S1,S4) D-Bus plugin adapter + NM deactivator + main — inline — written, unverified
- [ ] T4 (S7,S8) data files, Makefile, README — inline — written (structural readback only)
- [ ] T5 `go mod tidy`, `make test`, `make test-dbus`, `make build` — blocked: user must run `sudo dnf install golang`; then commit T1-T4 as work units

## Log

- L1 (2026-10-23) User: "En esta carpeta crea el proyecto e implementalo. Antes de instalar cualquier cosa o modificar el sistema operativo, pregunta."
- L2 (2026-10-23) User chose Go; authorized `sudo dnf install golang`; sudo needs a password the agent cannot supply, so the user runs it.
- L3 Evidence: NM spawns `program --bus-name <service>.Connection_<N>` when `supports-multiple-connections=true` (nm-vpn-connection.c:2634). `_config_process_generic` fails without `gateway` (ext gateway); with `has-ip4=false`/`has-ip6=false` `_check_complete` succeeds once generic config is received. `StateChanged(STOPPED)` from the plugin is always mapped to FAILED by NM, hence S4 deactivates through NM instead. IPv4 gateway decoded as native u32 (`_vardict_to_addr`).
