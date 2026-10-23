PREFIX     ?= /usr
LIBEXECDIR ?= $(PREFIX)/libexec
NMVPNDIR   ?= $(PREFIX)/lib/NetworkManager/VPN
DBUSDIR    ?= $(PREFIX)/share/dbus-1/system.d
DESTDIR    ?=

BIN = bin/nm-tailscale-service

.PHONY: build test test-dbus install uninstall clean

build:
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/nm-tailscale-service

test:
	go test ./...

# D-Bus adapter tests need a session bus; dbus-run-session provides one.
test-dbus:
	dbus-run-session -- go test ./internal/nmvpn/...

# Installs into the live system. Requires root. Never run automatically.
install: build
	install -D -m 0755 $(BIN) $(DESTDIR)$(LIBEXECDIR)/nm-tailscale-service
	install -D -m 0644 data/nm-tailscale-service.name $(DESTDIR)$(NMVPNDIR)/nm-tailscale-service.name
	install -D -m 0644 data/nm-tailscale-service.conf $(DESTDIR)$(DBUSDIR)/nm-tailscale-service.conf

uninstall:
	rm -f $(DESTDIR)$(LIBEXECDIR)/nm-tailscale-service \
	      $(DESTDIR)$(NMVPNDIR)/nm-tailscale-service.name \
	      $(DESTDIR)$(DBUSDIR)/nm-tailscale-service.conf

clean:
	rm -rf bin
