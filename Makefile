PREFIX     ?= /usr
LIBEXECDIR ?= $(PREFIX)/libexec
NMVPNDIR   ?= $(PREFIX)/lib/NetworkManager/VPN
DBUSDIR    ?= $(PREFIX)/share/dbus-1/system.d
DESTDIR    ?=

BIN = bin/nm-tailscale-service

VERSION ?= $(shell git describe --tags --always --dirty | sed 's/^v//')
DISTDIR ?= dist
SPEC    := packaging/nm-tailscale.spec

.PHONY: build test test-dbus lint fmt selinux install install-selinux uninstall clean dist rpm-src

build:
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/nm-tailscale-service

test:
	go test ./...

lint:
	golangci-lint run

# Fails and lists files if any Go file is not gofmt-clean.
fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

# D-Bus adapter tests need a session bus; dbus-run-session provides one.
test-dbus:
	dbus-run-session -- go test ./internal/nmvpn/...

# Installs into the live system. Requires root. Never run automatically.
install: build
	install -D -m 0755 $(BIN) $(DESTDIR)$(LIBEXECDIR)/nm-tailscale-service
	install -D -m 0644 data/nm-tailscale-service.name $(DESTDIR)$(NMVPNDIR)/nm-tailscale-service.name
	install -D -m 0644 data/nm-tailscale-service.conf $(DESTDIR)$(DBUSDIR)/nm-tailscale-service.conf

# Compiles the SELinux module without privileges.
selinux:
	checkmodule -M -m -o bin/nm_tailscale.mod data/selinux/nm-tailscale.te
	semodule_package -o bin/nm_tailscale.pp -m bin/nm_tailscale.mod

# Loads the module into the running policy. Requires root. Never run automatically.
install-selinux: selinux
	semodule -i bin/nm_tailscale.pp

uninstall:
	rm -f $(DESTDIR)$(LIBEXECDIR)/nm-tailscale-service \
	      $(DESTDIR)$(NMVPNDIR)/nm-tailscale-service.name \
	      $(DESTDIR)$(DBUSDIR)/nm-tailscale-service.conf
	-semodule -r nm_tailscale

# Source and vendor tarballs for packaging. The build environment has no
# network, so dependencies ship in the vendor tarball.
# Override VERSION to match the spec, e.g. make dist VERSION=0.1.0
dist:
	mkdir -p $(DISTDIR)
	git archive --format=tar.gz --prefix=nm-tailscale-$(VERSION)/ \
	    -o $(DISTDIR)/nm-tailscale-$(VERSION).tar.gz HEAD
	rm -rf $(DISTDIR)/vendor
	go mod vendor -o $(DISTDIR)/vendor
	tar -C $(DISTDIR) -czf $(DISTDIR)/nm-tailscale-$(VERSION)-vendor.tar.gz vendor
	rm -rf $(DISTDIR)/vendor

# Builds a source RPM in dist/. Uses the Version from the spec file.
rpm-src:
	$(MAKE) dist VERSION=$$(sed -n 's/^Version:[[:space:]]*//p' $(SPEC))
	rpmbuild -bs --define "_sourcedir $(CURDIR)/$(DISTDIR)" \
	    --define "_srcrpmdir $(CURDIR)/$(DISTDIR)" $(SPEC)

clean:
	rm -rf bin $(DISTDIR)
