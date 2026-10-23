// Package service holds the plugin's core logic, independent of D-Bus and HTTP.
// It drives tailscaled through the Backend port and reports to NetworkManager
// through the Notifier and Deactivator ports.
package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"nm-tailscale-service/internal/tailscale"
)

// NetworkManager VPN service states (NMVpnServiceState).
const (
	StateInit     uint32 = 1
	StateStarting uint32 = 3
	StateStarted  uint32 = 4
	StateStopping uint32 = 5
	StateStopped  uint32 = 6
)

// NetworkManager VPN plugin failure reasons (NMVpnPluginFailure).
const (
	FailureLoginFailed   uint32 = 0
	FailureConnectFailed uint32 = 1
	FailureBadIPConfig   uint32 = 2
)

// ipn.State values carried by IPN bus notifications.
const (
	ipnNeedsLogin       = 2
	ipnNeedsMachineAuth = 3
)

// Backend is the tailscaled port.
type Backend interface {
	Profiles(ctx context.Context) ([]tailscale.Profile, error)
	CurrentProfile(ctx context.Context) (tailscale.Profile, error)
	SwitchProfile(ctx context.Context, id string) error
	SetWantRunning(ctx context.Context, on bool) error
	Prefs(ctx context.Context) (tailscale.Prefs, error)
	Status(ctx context.Context) (tailscale.Status, error)
	Watch(ctx context.Context) (<-chan tailscale.Event, error)
}

// Notifier is the NetworkManager-facing port (D-Bus signals).
type Notifier interface {
	StateChanged(state uint32)
	Config(cfg map[string]any)
	Failure(reason uint32)
}

// Deactivator asks NetworkManager to deactivate the active connection with
// the given UUID. It is used for clean external disconnects.
type Deactivator interface {
	DeactivateByUUID(ctx context.Context, uuid string) error
}

// Settings is what the plugin needs from the NM connection dictionary.
type Settings struct {
	UUID      string
	Profile   string
	Interface string
}

// DefaultInterface is the tailscaled tunnel device name.
const DefaultInterface = "tailscale0"

// ErrBadArguments marks an invalid connection setting.
var ErrBadArguments = errors.New("bad arguments")

// ErrWrongState marks a Connect while already connecting/connected.
var ErrWrongState = errors.New("wrong state")

// Options tune timing and host lookups; zero values use defaults.
type Options struct {
	ConnectTimeout time.Duration
	PollInterval   time.Duration
	// InterfaceAddrs returns the addresses assigned to a network interface.
	InterfaceAddrs func(name string) ([]netip.Addr, error)
	Logf           func(format string, args ...any)
}

// Service is one VPN connection instance.
type Service struct {
	be   Backend
	nm   Notifier
	deac Deactivator
	opt  Options

	mu        sync.Mutex
	state     uint32
	profileID string
	uuid      string
	released  bool
	cancel    context.CancelFunc
	done      chan struct{}
	// Stopped is closed once the connection reached STOPPED.
	Stopped chan struct{}
}

// New builds a service with its ports.
func New(be Backend, nm Notifier, deac Deactivator, opt Options) *Service {
	if opt.ConnectTimeout == 0 {
		opt.ConnectTimeout = 30 * time.Second
	}
	if opt.PollInterval == 0 {
		opt.PollInterval = 500 * time.Millisecond
	}
	if opt.InterfaceAddrs == nil {
		opt.InterfaceAddrs = systemInterfaceAddrs
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	return &Service{be: be, nm: nm, deac: deac, opt: opt, state: StateInit, Stopped: make(chan struct{})}
}

// State returns the current NMVpnServiceState.
func (s *Service) State() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Service) setState(st uint32) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	s.nm.StateChanged(st)
	if st == StateStopped {
		select {
		case <-s.Stopped:
		default:
			close(s.Stopped)
		}
	}
}

// ResolveProfile maps the user-provided selector to a profile ID.
// Exact ID wins; otherwise a unique case-insensitive match on Name,
// UserProfile.LoginName or NetworkProfile.DomainName is required.
func ResolveProfile(profiles []tailscale.Profile, selector string) (tailscale.Profile, error) {
	sel := strings.TrimSpace(selector)
	if sel == "" {
		return tailscale.Profile{}, fmt.Errorf("%w: vpn.data profile is required", ErrBadArguments)
	}
	for _, p := range profiles {
		if p.ID == sel {
			return p, nil
		}
	}
	var matches []tailscale.Profile
	for _, p := range profiles {
		if strings.EqualFold(p.Name, sel) || strings.EqualFold(p.UserProfile.LoginName, sel) || strings.EqualFold(p.NetworkProfile.DomainName, sel) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return tailscale.Profile{}, fmt.Errorf("%w: no Tailscale profile matches %q", ErrBadArguments, sel)
	default:
		return tailscale.Profile{}, fmt.Errorf("%w: profile selector %q is ambiguous", ErrBadArguments, sel)
	}
}

// Connect validates the settings synchronously and runs the connection flow
// in the background. Errors returned here are reported to NM as the method's
// reply; later outcomes arrive as signals.
func (s *Service) Connect(ctx context.Context, set Settings) error {
	s.mu.Lock()
	if s.state == StateStarting || s.state == StateStarted {
		s.mu.Unlock()
		return fmt.Errorf("%w: already connecting or connected", ErrWrongState)
	}
	s.mu.Unlock()

	if set.Interface == "" {
		set.Interface = DefaultInterface
	}
	if strings.ContainsAny(set.Interface, "/ \t\n") || len(set.Interface) > 15 {
		return fmt.Errorf("%w: invalid interface name %q", ErrBadArguments, set.Interface)
	}
	profiles, err := s.be.Profiles(ctx)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}
	p, err := ResolveProfile(profiles, set.Profile)
	if err != nil {
		return err
	}

	bg, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.profileID = p.ID
	s.uuid = set.UUID
	s.released = false
	s.cancel = cancel
	s.done = make(chan struct{})
	s.mu.Unlock()

	s.setState(StateStarting)
	go s.run(bg, p.ID, set.Interface)
	return nil
}

func (s *Service) run(ctx context.Context, profileID, iface string) {
	defer close(s.done)
	if err := s.bringUp(ctx, profileID); err != nil {
		s.fail(FailureConnectFailed, err)
		return
	}
	st, err := s.waitRunning(ctx)
	if err != nil {
		if errors.Is(err, errLoginRequired) {
			s.fail(FailureLoginFailed, err)
		} else {
			s.fail(FailureConnectFailed, err)
		}
		return
	}
	cfg, err := s.buildConfig(iface, st.TailscaleIPs)
	if err != nil {
		s.fail(FailureBadIPConfig, err)
		return
	}
	s.nm.Config(cfg)
	s.setState(StateStarted)
	s.watch(ctx)
}

func (s *Service) bringUp(ctx context.Context, profileID string) error {
	cur, err := s.be.CurrentProfile(ctx)
	if err != nil {
		return fmt.Errorf("current profile: %w", err)
	}
	if cur.ID != profileID {
		if err := s.be.SwitchProfile(ctx, profileID); err != nil {
			return fmt.Errorf("switch profile: %w", err)
		}
	}
	if err := s.be.SetWantRunning(ctx, true); err != nil {
		return fmt.Errorf("set WantRunning=true: %w", err)
	}
	return nil
}

var errLoginRequired = errors.New("tailscale login required")

func (s *Service) waitRunning(ctx context.Context) (tailscale.Status, error) {
	deadline := time.NewTimer(s.opt.ConnectTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(s.opt.PollInterval)
	defer tick.Stop()
	for {
		st, err := s.be.Status(ctx)
		if err != nil {
			s.opt.Logf("status: %v", err)
		} else {
			switch st.BackendState {
			case tailscale.StateRunning:
				return st, nil
			case tailscale.StateNeedsLogin, tailscale.StateNeedsMachineAuth:
				return st, fmt.Errorf("%w (%s)", errLoginRequired, st.BackendState)
			}
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-deadline.C:
			return st, fmt.Errorf("timed out waiting for tailscaled to reach Running")
		case <-tick.C:
		}
	}
}

// buildConfig produces the NM "Config" dictionary. Addresses, routes and DNS
// stay with tailscaled, so has-ip4/has-ip6 are false. NM still requires an
// external gateway, so the node's own tailnet address is reported.
func (s *Service) buildConfig(iface string, tailnetIPs []netip.Addr) (map[string]any, error) {
	addrs, err := s.opt.InterfaceAddrs(iface)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", iface, err)
	}
	var gw netip.Addr
	for _, ip := range tailnetIPs {
		for _, a := range addrs {
			if a == ip {
				if ip.Is4() || !gw.IsValid() {
					gw = ip
				}
			}
		}
	}
	if !gw.IsValid() {
		return nil, fmt.Errorf("no Tailscale address is assigned to %s", iface)
	}
	cfg := map[string]any{
		"tundev":      iface,
		"can-persist": true,
		"has-ip4":     false,
		"has-ip6":     false,
	}
	if gw.Is4() {
		b := gw.As4()
		cfg["gateway"] = binary.NativeEndian.Uint32(b[:])
	} else {
		b := gw.As16()
		cfg["gateway"] = b[:]
	}
	return cfg, nil
}

func (s *Service) fail(reason uint32, err error) {
	s.opt.Logf("connection failed: %v", err)
	s.nm.Failure(reason)
	s.setState(StateStopped)
}

// watch follows the IPN bus and releases the connection when tailscaled is
// switched or stopped from outside NetworkManager.
func (s *Service) watch(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		ch, err := s.be.Watch(ctx)
		if err != nil {
			failures++
			if failures >= 15 {
				s.opt.Logf("ipn bus unreachable, releasing connection")
				s.release(ctx)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		failures = 0
		if !s.consume(ctx, ch) {
			return
		}
	}
}

// consume handles events until the stream ends (true, reconnect) or the
// watch must stop (false).
func (s *Service) consume(ctx context.Context, ch <-chan tailscale.Event) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case ev, ok := <-ch:
			if !ok {
				return true
			}
			if ev.State == nil && ev.Prefs == nil && ev.ErrMessage == nil {
				continue
			}
			if ev.State != nil && (*ev.State == ipnNeedsLogin || *ev.State == ipnNeedsMachineAuth) {
				s.opt.Logf("tailscaled needs login; stopping")
				s.nm.Failure(FailureLoginFailed)
				s.setState(StateStopped)
				return false
			}
			if s.ownershipLost(ctx) {
				s.release(ctx)
				return false
			}
		}
	}
}

// ownershipLost reports whether tailscaled no longer runs our profile.
func (s *Service) ownershipLost(ctx context.Context) bool {
	cur, err := s.be.CurrentProfile(ctx)
	if err != nil {
		return false
	}
	if cur.ID != s.profileID {
		return true
	}
	prefs, err := s.be.Prefs(ctx)
	if err != nil {
		return false
	}
	return !prefs.WantRunning
}

// release marks the connection as externally closed and asks NM to deactivate
// it, so GNOME shows a clean disconnect instead of a failure.
func (s *Service) release(ctx context.Context) {
	s.mu.Lock()
	s.released = true
	uuid := s.uuid
	s.mu.Unlock()
	s.opt.Logf("profile switched or stopped outside NetworkManager; deactivating %s", uuid)
	if err := s.deac.DeactivateByUUID(ctx, uuid); err != nil {
		s.opt.Logf("deactivate: %v", err)
	}
}

// Disconnect stops the tunnel if it still belongs to this connection and
// reports STOPPING/STOPPED.
func (s *Service) Disconnect(ctx context.Context) error {
	s.mu.Lock()
	cancel, done, released, profileID := s.cancel, s.done, s.released, s.profileID
	s.mu.Unlock()
	s.setState(StateStopping)
	if cancel != nil {
		cancel()
		<-done
	}
	if !released && profileID != "" {
		cur, err := s.be.CurrentProfile(ctx)
		if err == nil && cur.ID == profileID {
			if err := s.be.SetWantRunning(ctx, false); err != nil {
				s.opt.Logf("set WantRunning=false: %v", err)
			}
		}
	}
	s.setState(StateStopped)
	return nil
}

func systemInterfaceAddrs(name string) ([]netip.Addr, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ap, ok := netip.AddrFromSlice(ipn.IP); ok {
			out = append(out, ap.Unmap())
		}
	}
	return out, nil
}
