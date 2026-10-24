# Packaging

`nm-tailscale.spec` builds two packages:

- `nm-tailscale`: the plugin binary, NetworkManager descriptor and D-Bus policy.
- `nm-tailscale-selinux`: the `nm_tailscale` SELinux module (noarch).

The spec targets COPR first. Fedora proper requires vendored dependencies to
be unbundled into distro packages; this spec instead builds from a vendor
tarball, which COPR allows.

NetworkManager reads `.name` files at start, so no scriptlet is needed for it.
Restart NetworkManager after installing or removing the package.

## Build the source RPM locally

Requires `rpm-build`, `golang >= 1.24` and git. The tarballs are made from
committed content (`git archive`), so commit or tag first.

```
make dist VERSION=0.1.0   # dist/nm-tailscale-0.1.0.tar.gz and ...-vendor.tar.gz
make rpm-src              # dist/nm-tailscale-0.1.0-1.<dist>.src.rpm
```

`make rpm-src` reads the version from the spec, so bump `Version:` there
(and add a `%changelog` entry) before releasing.

The vendor tarball must be generated with `go mod vendor`, because RPM builds
have no network access. `make dist` does this into a temporary directory, then
archives it as a top-level `vendor/` directory.

## Test the build

```
mock -r fedora-rawhide-x86_64 dist/nm-tailscale-0.1.0-1.*.src.rpm
```

## Submit to COPR

Create the project once in the COPR web UI, then:

```
copr-cli build <owner>/<project> dist/nm-tailscale-0.1.0-1.*.src.rpm
```

Enable the project's Fedora chroots to match the releases you support.
