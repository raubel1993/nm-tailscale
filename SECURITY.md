# Security policy

## Supported versions

Only the latest released version receives security fixes.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub Security Advisories:
<https://github.com/OWNER/nm-tailscale/security/advisories/new>

Do not open a public issue or pull request for a suspected vulnerability.
Include the affected version, a description of the impact and, if possible,
steps to reproduce. Redact tailnet names, addresses and tokens.

## Disclosure

We follow a 90-day coordinated disclosure policy. We aim to acknowledge a
report within 7 days. The fix and advisory are published together once a
fix is available, or after 90 days from the report, whichever comes first.
Reporters are credited unless they ask otherwise.

## Scope

`nm-tailscale-service` runs as root, spawned by NetworkManager, and talks to
the `tailscaled` LocalAPI socket. Vulnerabilities in how it validates
`vpn.data`, handles D-Bus calls, or uses that socket are in scope.

Reports about `tailscaled`, Tailscale itself, or NetworkManager itself belong
to those projects upstream.
