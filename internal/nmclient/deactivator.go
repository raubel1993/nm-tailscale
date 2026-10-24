// SPDX-License-Identifier: GPL-2.0-or-later

// Package nmclient contains the small NetworkManager client the plugin needs
// to deactivate its own connection when tailscaled changes underneath it.
package nmclient

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	nmName         = "org.freedesktop.NetworkManager"
	nmPath         = "/org/freedesktop/NetworkManager"
	nmIface        = "org.freedesktop.NetworkManager"
	activeIface    = "org.freedesktop.NetworkManager.Connection.Active"
	activeConnProp = nmIface + ".ActiveConnections"
	uuidProp       = activeIface + ".Uuid"
)

// Deactivator deactivates NM active connections by UUID.
type Deactivator struct {
	conn *dbus.Conn
}

// New wraps a system bus connection.
func New(conn *dbus.Conn) *Deactivator { return &Deactivator{conn: conn} }

// DeactivateByUUID finds the active connection whose Uuid matches and calls
// DeactivateConnection on it. It is a no-op when none is active.
func (d *Deactivator) DeactivateByUUID(ctx context.Context, uuid string) error {
	if uuid == "" {
		return fmt.Errorf("empty connection uuid")
	}
	mgr := d.conn.Object(nmName, nmPath)
	var active dbus.Variant
	if err := mgr.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, nmIface, "ActiveConnections").Store(&active); err != nil {
		return fmt.Errorf("list active connections: %w", err)
	}
	paths, _ := active.Value().([]dbus.ObjectPath)
	for _, p := range paths {
		var v dbus.Variant
		if err := d.conn.Object(nmName, p).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, activeIface, "Uuid").Store(&v); err != nil {
			continue
		}
		if got, _ := v.Value().(string); got != uuid {
			continue
		}
		if call := mgr.CallWithContext(ctx, nmIface+".DeactivateConnection", 0, p); call.Err != nil {
			return fmt.Errorf("deactivate %s: %w", p, call.Err)
		}
		return nil
	}
	return nil
}
