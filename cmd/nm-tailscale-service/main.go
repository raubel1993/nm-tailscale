// Command nm-tailscale-service is a NetworkManager VPN service plugin that
// drives tailscaled. NetworkManager starts it with --bus-name and talks to it
// over the system bus; it exits after the connection stops unless --persist.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/godbus/dbus/v5"

	"nm-tailscale-service/internal/nmclient"
	"nm-tailscale-service/internal/nmvpn"
	"nm-tailscale-service/internal/service"
	"nm-tailscale-service/internal/tailscale"
)

func main() {
	busName := flag.String("bus-name", nmvpn.ServiceName, "D-Bus name to own (NetworkManager passes it)")
	persist := flag.Bool("persist", false, "do not exit after the connection stops")
	debug := flag.Bool("debug", false, "verbose logging; do not exit when NetworkManager leaves the bus")
	socket := flag.String("socket", tailscale.DefaultSocket, "path to the tailscaled socket")
	flag.Parse()

	log.SetFlags(0)
	log.SetPrefix("nm-tailscale-service: ")
	logf := func(format string, args ...any) {
		if *debug {
			log.Printf(format, args...)
		}
	}

	conn, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("system bus: %v", err)
	}
	defer conn.Close()

	notifier, plugin, err := nmvpn.Export(conn)
	if err != nil {
		log.Fatal(err)
	}
	newService := func() *service.Service {
		return service.New(tailscale.New(*socket), notifier, nmclient.New(conn), service.Options{Logf: log.Printf})
	}
	svc := newService()
	plugin.SetCore(svc)

	if err := nmvpn.RequestName(conn, *busName); err != nil {
		log.Fatal(err)
	}
	logf("owning %s", *busName)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	nmGone := watchNetworkManager(conn)

	for {
		select {
		case <-ctx.Done():
			logf("signal received; disconnecting")
			_ = svc.Disconnect(context.Background())
			return
		case <-nmGone:
			if *debug {
				continue
			}
			log.Print("NetworkManager left the bus; exiting")
			_ = svc.Disconnect(context.Background())
			return
		case <-svc.Stopped:
			if *persist {
				// Allow a new Connect on the same process.
				svc = newService()
				plugin.SetCore(svc)
				continue
			}
			logf("connection stopped; exiting")
			return
		}
	}
}

// watchNetworkManager signals once when org.freedesktop.NetworkManager loses
// its owner, mirroring libnm's watch-peer behaviour.
func watchNetworkManager(conn *dbus.Conn) <-chan struct{} {
	gone := make(chan struct{}, 1)
	if err := conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, "org.freedesktop.NetworkManager"),
	); err != nil {
		fmt.Fprintf(os.Stderr, "cannot watch NetworkManager: %v\n", err)
		return gone
	}
	sigs := make(chan *dbus.Signal, 8)
	conn.Signal(sigs)
	go func() {
		for s := range sigs {
			if s.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(s.Body) < 3 {
				continue
			}
			if newOwner, _ := s.Body[2].(string); newOwner == "" {
				select {
				case gone <- struct{}{}:
				default:
				}
			}
		}
	}()
	return gone
}
