// Package nmvpn exposes a service.Service on D-Bus as a NetworkManager VPN
// plugin (org.freedesktop.NetworkManager.VPN.Plugin).
package nmvpn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"nm-tailscale-service/internal/service"
)

const (
	// Path and Interface are fixed by NetworkManager (nm-vpn-dbus-interface.h).
	Path      = "/org/freedesktop/NetworkManager/VPN/Plugin"
	Interface = "org.freedesktop.NetworkManager.VPN.Plugin"
	// ServiceName is the base bus name; NM appends ".Connection_<N>" when
	// supports-multiple-connections=true.
	ServiceName = "org.freedesktop.NetworkManager.tailscale"

	errPrefix = "org.freedesktop.NetworkManager.VPN.Error."
)

// Core is what the adapter needs from the service.
type Core interface {
	Connect(ctx context.Context, set service.Settings) error
	Disconnect(ctx context.Context) error
}

// Notifier emits plugin signals on the bus and tracks the State property.
type Notifier struct {
	conn  *dbus.Conn
	props *prop.Properties
}

// StateChanged implements service.Notifier.
func (n *Notifier) StateChanged(state uint32) {
	n.props.SetMust(Interface, "State", state)
	_ = n.conn.Emit(Path, Interface+".StateChanged", state)
}

// Config implements service.Notifier.
func (n *Notifier) Config(cfg map[string]any) {
	out := make(map[string]dbus.Variant, len(cfg))
	for k, v := range cfg {
		out[k] = dbus.MakeVariant(v)
	}
	_ = n.conn.Emit(Path, Interface+".Config", out)
}

// Failure implements service.Notifier.
func (n *Notifier) Failure(reason uint32) {
	_ = n.conn.Emit(Path, Interface+".Failure", reason)
}

// connectTimeout bounds the synchronous part of Connect (profile lookup) so a
// hung tailscaled cannot stall NetworkManager's D-Bus call.
const connectTimeout = 10 * time.Second

// Plugin is the exported D-Bus object.
type Plugin struct {
	mu   sync.RWMutex
	core Core
}

func (p *Plugin) getCore() Core {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.core
}

// Export registers the plugin object and its State property on conn. The
// returned Notifier must be handed to the service before any Connect.
func Export(conn *dbus.Conn) (*Notifier, *Plugin, error) {
	props, err := prop.Export(conn, Path, map[string]map[string]*prop.Prop{
		Interface: {"State": {Value: service.StateInit, Writable: false, Emit: prop.EmitTrue}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("export properties: %w", err)
	}
	p := &Plugin{}
	if err := conn.Export(p, Path, Interface); err != nil {
		return nil, nil, fmt.Errorf("export plugin: %w", err)
	}
	return &Notifier{conn: conn, props: props}, p, nil
}

// SetCore binds the service that handles the calls.
func (p *Plugin) SetCore(c Core) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.core = c
}

// RequestName claims the bus name NM is watching for.
func RequestName(conn *dbus.Conn, name string) error {
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("bus name %s already taken", name)
	}
	return nil
}

// SettingsFromConnection extracts what the service needs from NM's
// a{sa{sv}} connection dictionary.
func SettingsFromConnection(conn map[string]map[string]dbus.Variant) (service.Settings, error) {
	var set service.Settings
	if c, ok := conn["connection"]; ok {
		if v, ok := c["uuid"]; ok {
			set.UUID, _ = v.Value().(string)
		}
	}
	vpn, ok := conn["vpn"]
	if !ok {
		return set, errors.New("connection has no vpn setting")
	}
	data := map[string]string{}
	if v, ok := vpn["data"]; ok {
		switch d := v.Value().(type) {
		case map[string]string:
			data = d
		case map[string]dbus.Variant:
			for k, vv := range d {
				data[k], _ = vv.Value().(string)
			}
		}
	}
	set.Profile = data["profile"]
	set.Interface = data["interface"]
	return set, nil
}

func dbusError(err error) *dbus.Error {
	name := errPrefix + "Failed"
	switch {
	case errors.Is(err, service.ErrBadArguments):
		name = errPrefix + "BadArguments"
	case errors.Is(err, service.ErrWrongState):
		name = errPrefix + "WrongState"
	}
	return dbus.NewError(name, []any{err.Error()})
}

// Connect handles org.freedesktop.NetworkManager.VPN.Plugin.Connect.
func (p *Plugin) Connect(conn map[string]map[string]dbus.Variant) *dbus.Error {
	set, err := SettingsFromConnection(conn)
	if err != nil {
		return dbusError(fmt.Errorf("%w: %v", service.ErrBadArguments, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := p.getCore().Connect(ctx, set); err != nil {
		return dbusError(err)
	}
	return nil
}

// ConnectInteractive behaves like Connect: this plugin never asks for secrets.
func (p *Plugin) ConnectInteractive(conn map[string]map[string]dbus.Variant, _ map[string]dbus.Variant) *dbus.Error {
	return p.Connect(conn)
}

// NeedSecrets reports that no secrets are needed.
func (p *Plugin) NeedSecrets(_ map[string]map[string]dbus.Variant) (string, *dbus.Error) {
	return "", nil
}

// Disconnect handles org.freedesktop.NetworkManager.VPN.Plugin.Disconnect.
func (p *Plugin) Disconnect() *dbus.Error {
	if err := p.getCore().Disconnect(context.Background()); err != nil {
		return dbusError(err)
	}
	return nil
}

// The following methods exist for interface completeness; this plugin emits
// its configuration itself, so they are no-ops.

// SetConfig is a no-op.
func (p *Plugin) SetConfig(_ map[string]dbus.Variant) *dbus.Error { return nil }

// SetIp4Config is a no-op.
func (p *Plugin) SetIp4Config(_ map[string]dbus.Variant) *dbus.Error { return nil }

// SetIp6Config is a no-op.
func (p *Plugin) SetIp6Config(_ map[string]dbus.Variant) *dbus.Error { return nil }

// SetFailure is a no-op.
func (p *Plugin) SetFailure(_ string) *dbus.Error { return nil }

// NewSecrets is a no-op.
func (p *Plugin) NewSecrets(_ map[string]map[string]dbus.Variant) *dbus.Error { return nil }
