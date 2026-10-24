// SPDX-License-Identifier: GPL-2.0-or-later

package nmvpn

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/raubel1993/nm-tailscale/internal/service"
)

type fakeCore struct {
	mu   sync.Mutex
	set  service.Settings
	err  error
	disc int
}

func (f *fakeCore) Connect(_ context.Context, s service.Settings) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = s
	return f.err
}

func (f *fakeCore) Disconnect(context.Context) error {
	f.mu.Lock()
	f.disc++
	f.mu.Unlock()
	return nil
}

// bus returns a private session bus connection or skips. Run the tests under
// `dbus-run-session` when no session bus is available.
func bus(t *testing.T) (*dbus.Conn, *dbus.Conn) {
	t.Helper()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Skip("no session bus; run under dbus-run-session")
	}
	server, err := dbus.SessionBusPrivate()
	if err != nil {
		t.Skip(err)
	}
	if err := server.Auth(nil); err != nil {
		t.Fatal(err)
	}
	if err := server.Hello(); err != nil {
		t.Fatal(err)
	}
	client, err := dbus.SessionBusPrivate()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Auth(nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Hello(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close(); client.Close() })
	return server, client
}

func connDict(profile, iface, uuid string) map[string]map[string]dbus.Variant {
	data := map[string]string{"profile": profile}
	if iface != "" {
		data["interface"] = iface
	}
	return map[string]map[string]dbus.Variant{
		"connection": {"id": dbus.MakeVariant("Tailscale"), "uuid": dbus.MakeVariant(uuid)},
		"vpn": {
			"service-type": dbus.MakeVariant(ServiceName),
			"data":         dbus.MakeVariant(data),
		},
	}
}

func TestSettingsFromConnection(t *testing.T) {
	set, err := SettingsFromConnection(connDict("sailo", "ts1", "u-1"))
	if err != nil {
		t.Fatal(err)
	}
	if set.Profile != "sailo" || set.Interface != "ts1" || set.UUID != "u-1" {
		t.Fatalf("%+v", set)
	}
	if _, err := SettingsFromConnection(map[string]map[string]dbus.Variant{}); err == nil {
		t.Fatal("expected error without vpn setting")
	}
}

func TestConnectOverBusReachesCoreAndEmitsSignals(t *testing.T) {
	server, client := bus(t)
	name := ServiceName + ".Connection_test"
	notifier, plugin, err := Export(server)
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeCore{}
	plugin.SetCore(core)
	if err := RequestName(server, name); err != nil {
		t.Fatal(err)
	}

	sigs := make(chan *dbus.Signal, 8)
	client.Signal(sigs)
	if err := client.AddMatchSignal(dbus.WithMatchObjectPath(Path), dbus.WithMatchInterface(Interface)); err != nil {
		t.Fatal(err)
	}

	obj := client.Object(name, Path)
	if call := obj.Call(Interface+".Connect", 0, connDict("sailo", "", "u-9")); call.Err != nil {
		t.Fatal(call.Err)
	}
	core.mu.Lock()
	if core.set.Profile != "sailo" || core.set.UUID != "u-9" || core.set.Interface != "" {
		t.Fatalf("core got %+v", core.set)
	}
	core.mu.Unlock()

	var secret string
	if err := obj.Call(Interface+".NeedSecrets", 0, connDict("sailo", "", "u-9")).Store(&secret); err != nil || secret != "" {
		t.Fatalf("NeedSecrets: %q %v", secret, err)
	}

	notifier.StateChanged(service.StateStarting)
	notifier.Config(map[string]any{"tundev": "tailscale0", "has-ip4": false, "gateway": uint32(7)})
	notifier.Failure(service.FailureLoginFailed)

	got := map[string]*dbus.Signal{}
	deadline := time.After(3 * time.Second)
	for len(got) < 3 {
		select {
		case s := <-sigs:
			if s.Path == Path {
				got[s.Name] = s
			}
		case <-deadline:
			t.Fatalf("signals received: %v", got)
		}
	}
	if st, ok := got[Interface+".StateChanged"].Body[0].(uint32); !ok || st != service.StateStarting {
		t.Fatalf("StateChanged body: %v", got[Interface+".StateChanged"].Body)
	}
	cfg, ok := got[Interface+".Config"].Body[0].(map[string]dbus.Variant)
	if !ok || cfg["tundev"].Value() != "tailscale0" || cfg["gateway"].Value() != uint32(7) {
		t.Fatalf("Config body: %v", got[Interface+".Config"].Body)
	}
	if r, ok := got[Interface+".Failure"].Body[0].(uint32); !ok || r != service.FailureLoginFailed {
		t.Fatalf("Failure body: %v", got[Interface+".Failure"].Body)
	}

	var state dbus.Variant
	if err := obj.Call("org.freedesktop.DBus.Properties.Get", 0, Interface, "State").Store(&state); err != nil {
		t.Fatal(err)
	}
	if state.Value() != service.StateStarting {
		t.Fatalf("State property: %v", state.Value())
	}

	if call := obj.Call(Interface+".Disconnect", 0); call.Err != nil {
		t.Fatal(call.Err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.disc != 1 {
		t.Fatalf("Disconnect calls: %d", core.disc)
	}
}

func TestConnectErrorsMapToNMErrorNames(t *testing.T) {
	server, client := bus(t)
	name := ServiceName + ".Connection_err"
	_, plugin, err := Export(server)
	if err != nil {
		t.Fatal(err)
	}
	core := &fakeCore{err: service.ErrBadArguments}
	plugin.SetCore(core)
	if err := RequestName(server, name); err != nil {
		t.Fatal(err)
	}
	call := client.Object(name, Path).Call(Interface+".Connect", 0, connDict("x", "", "u"))
	var dbErr dbus.Error
	if !errors.As(call.Err, &dbErr) || dbErr.Name != errPrefix+"BadArguments" {
		t.Fatalf("got %v", call.Err)
	}
}
