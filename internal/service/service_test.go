// SPDX-License-Identifier: GPL-2.0-or-later

package service

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/raubel1993/nm-tailscale/internal/tailscale"
)

// fakeBackend is an in-memory tailscaled.
type fakeBackend struct {
	mu          sync.Mutex
	profiles    []tailscale.Profile
	current     string
	wantRunning bool
	state       string
	ips         []netip.Addr
	calls       []string
	events      chan tailscale.Event
	watchErr    error
}

func newBackend() *fakeBackend {
	b := &fakeBackend{current: "ca23", state: tailscale.StateStopped, events: make(chan tailscale.Event, 8)}
	b.profiles = []tailscale.Profile{{ID: "ca23", Name: "finquers@example.com"}, {ID: "b2c5", Name: "sailo@example.com"}}
	b.profiles[0].NetworkProfile.DomainName = "vpn.finquers.com"
	b.profiles[0].UserProfile.LoginName = "finquers@example.com"
	b.profiles[1].NetworkProfile.DomainName = "vpn.sailo.ag"
	b.profiles[1].UserProfile.LoginName = "sailo@example.com"
	b.ips = []netip.Addr{netip.MustParseAddr("100.64.0.1")}
	return b
}

func (b *fakeBackend) record(c string) { b.mu.Lock(); b.calls = append(b.calls, c); b.mu.Unlock() }
func (b *fakeBackend) Profiles(context.Context) ([]tailscale.Profile, error) {
	b.record("profiles")
	return b.profiles, nil
}
func (b *fakeBackend) CurrentProfile(context.Context) (tailscale.Profile, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.profiles {
		if p.ID == b.current {
			return p, nil
		}
	}
	return tailscale.Profile{}, errors.New("no current")
}
func (b *fakeBackend) SwitchProfile(_ context.Context, id string) error {
	b.record("switch:" + id)
	b.mu.Lock()
	b.current = id
	b.mu.Unlock()
	return nil
}
func (b *fakeBackend) SetWantRunning(_ context.Context, on bool) error {
	if on {
		b.record("up")
	} else {
		b.record("down")
	}
	b.mu.Lock()
	b.wantRunning = on
	if on && b.state == tailscale.StateStopped {
		b.state = tailscale.StateRunning
	}
	if !on {
		b.state = tailscale.StateStopped
	}
	b.mu.Unlock()
	return nil
}
func (b *fakeBackend) Prefs(context.Context) (tailscale.Prefs, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return tailscale.Prefs{WantRunning: b.wantRunning}, nil
}
func (b *fakeBackend) Status(context.Context) (tailscale.Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return tailscale.Status{BackendState: b.state, TailscaleIPs: b.ips}, nil
}
func (b *fakeBackend) Watch(context.Context) (<-chan tailscale.Event, error) {
	if b.watchErr != nil {
		return nil, b.watchErr
	}
	return b.events, nil
}
func (b *fakeBackend) has(call string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.calls {
		if c == call {
			return true
		}
	}
	return false
}

type fakeNotifier struct {
	mu       sync.Mutex
	states   []uint32
	failures []uint32
	config   map[string]any
	ch       chan uint32
}

func newNotifier() *fakeNotifier { return &fakeNotifier{ch: make(chan uint32, 16)} }
func (n *fakeNotifier) StateChanged(s uint32) {
	n.mu.Lock()
	n.states = append(n.states, s)
	n.mu.Unlock()
	n.ch <- s
}
func (n *fakeNotifier) Config(c map[string]any) { n.mu.Lock(); n.config = c; n.mu.Unlock() }
func (n *fakeNotifier) Failure(r uint32) {
	n.mu.Lock()
	n.failures = append(n.failures, r)
	n.mu.Unlock()
}
func (n *fakeNotifier) waitState(t *testing.T, want uint32) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-n.ch:
			if s == want {
				return
			}
		case <-deadline:
			t.Fatalf("state %d never reached; states=%v", want, n.states)
		}
	}
}

type fakeDeactivator struct {
	mu    sync.Mutex
	uuids []string
	ch    chan string
}

func newDeactivator() *fakeDeactivator { return &fakeDeactivator{ch: make(chan string, 4)} }
func (d *fakeDeactivator) DeactivateByUUID(_ context.Context, uuid string) error {
	d.mu.Lock()
	d.uuids = append(d.uuids, uuid)
	d.mu.Unlock()
	d.ch <- uuid
	return nil
}

func newService(b *fakeBackend, n *fakeNotifier, d *fakeDeactivator) *Service {
	return New(b, n, d, Options{
		ConnectTimeout: 300 * time.Millisecond,
		PollInterval:   10 * time.Millisecond,
		InterfaceAddrs: func(string) ([]netip.Addr, error) { return b.ips, nil },
		Logf:           func(string, ...any) {},
	})
}

var set = Settings{UUID: "uuid-1", Profile: "vpn.sailo.ag"}

func TestConnectSwitchesProfileAndReportsStarted(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), Settings{UUID: "uuid-1", Profile: "vpn.sailo.ag"}); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	if !b.has("switch:b2c5") || !b.has("up") {
		t.Fatalf("calls: %v", b.calls)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.config["tundev"] != DefaultInterface || n.config["has-ip4"] != false || n.config["has-ip6"] != false || n.config["can-persist"] != true {
		t.Fatalf("config: %v", n.config)
	}
	want := binary.NativeEndian.Uint32([]byte{100, 64, 0, 1})
	if n.config["gateway"] != want {
		t.Fatalf("gateway: %v want %v", n.config["gateway"], want)
	}
	if n.states[0] != StateStarting {
		t.Fatalf("first state %d", n.states[0])
	}
}

func TestConnectDoesNotSwitchWhenProfileIsCurrent(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), Settings{UUID: "u", Profile: "ca23"}); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	if b.has("switch:ca23") {
		t.Fatalf("unexpected switch: %v", b.calls)
	}
}

func TestConnectRejectsUnknownAndAmbiguousProfile(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	err := s.Connect(context.Background(), Settings{Profile: "nobody"})
	if !errors.Is(err, ErrBadArguments) {
		t.Fatalf("got %v", err)
	}
	b.profiles[1].Name = "finquers@example.com"
	err = s.Connect(context.Background(), Settings{Profile: "finquers@example.com"})
	if !errors.Is(err, ErrBadArguments) {
		t.Fatalf("ambiguous: got %v", err)
	}
	if len(n.states) != 0 || b.has("up") {
		t.Fatal("rejected connect must not touch tailscaled or emit states")
	}
}

func TestConnectRejectsEmptyProfileAndBadInterface(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), Settings{Profile: ""}); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("empty: %v", err)
	}
	if err := s.Connect(context.Background(), Settings{Profile: "vpn.sailo.ag", Interface: "../x"}); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("iface: %v", err)
	}
}

func TestLoginRequiredReportsLoginFailed(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	b.state = tailscale.StateNeedsLogin
	b.mu.Lock()
	b.wantRunning = true
	b.mu.Unlock()
	s := newService(b, n, d)
	// SetWantRunning(true) keeps NeedsLogin because state is not Stopped.
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStopped)
	if len(n.failures) != 1 || n.failures[0] != FailureLoginFailed {
		t.Fatalf("failures: %v", n.failures)
	}
}

func TestTimeoutReportsConnectFailed(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	b.state = tailscale.StateStarting
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStopped)
	if len(n.failures) != 1 || n.failures[0] != FailureConnectFailed {
		t.Fatalf("failures: %v", n.failures)
	}
}

func TestMissingInterfaceAddressReportsBadIPConfig(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := New(b, n, d, Options{ConnectTimeout: 300 * time.Millisecond, PollInterval: 10 * time.Millisecond,
		InterfaceAddrs: func(string) ([]netip.Addr, error) { return nil, nil }, Logf: func(string, ...any) {}})
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStopped)
	if len(n.failures) != 1 || n.failures[0] != FailureBadIPConfig {
		t.Fatalf("failures: %v", n.failures)
	}
}

func TestExternalSwitchDeactivatesThroughNM(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	b.mu.Lock()
	b.current = "ca23"
	b.mu.Unlock()
	st := 6
	b.events <- tailscale.Event{State: &st}
	select {
	case u := <-d.ch:
		if u != "uuid-1" {
			t.Fatalf("deactivated %q", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NM deactivation never requested")
	}
	if len(n.failures) != 0 {
		t.Fatalf("external switch must not be a failure: %v", n.failures)
	}
	// NM then calls Disconnect: tailscaled must be left alone.
	if err := s.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.has("down") {
		t.Fatalf("released connection must not stop tailscaled: %v", b.calls)
	}
	n.waitState(t, StateStopped)
}

func TestExternalDownDeactivatesThroughNM(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	b.mu.Lock()
	b.wantRunning = false
	b.mu.Unlock()
	b.events <- tailscale.Event{Prefs: &tailscale.Prefs{WantRunning: false}}
	select {
	case <-d.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("NM deactivation never requested")
	}
}

func TestUnrelatedEventsAreIgnored(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	st := 6
	b.events <- tailscale.Event{State: &st}
	b.events <- tailscale.Event{}
	select {
	case u := <-d.ch:
		t.Fatalf("unexpected deactivation of %s", u)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNeedsLoginWhileConnectedFails(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	st := ipnNeedsLogin
	b.events <- tailscale.Event{State: &st}
	n.waitState(t, StateStopped)
	if len(n.failures) != 1 || n.failures[0] != FailureLoginFailed {
		t.Fatalf("failures: %v", n.failures)
	}
}

func TestDisconnectStopsOwnProfileOnly(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	if err := s.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !b.has("down") {
		t.Fatalf("expected WantRunning=false: %v", b.calls)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	last := n.states[len(n.states)-2:]
	if last[0] != StateStopping || last[1] != StateStopped {
		t.Fatalf("states: %v", n.states)
	}
}

func TestDisconnectLeavesOtherProfileAlone(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	// Someone switched profiles but the watcher has not noticed yet.
	b.mu.Lock()
	b.current = "ca23"
	b.mu.Unlock()
	if err := s.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.has("down") {
		t.Fatalf("must not stop a profile we do not own: %v", b.calls)
	}
}

func TestConnectTwiceIsWrongState(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	if err := s.Connect(context.Background(), set); !errors.Is(err, ErrWrongState) {
		t.Fatalf("got %v", err)
	}
}

func TestResolveProfileMatchesCaseInsensitive(t *testing.T) {
	b := newBackend()
	for _, sel := range []string{"b2c5", "SAILO@example.com", "VPN.sailo.AG"} {
		p, err := ResolveProfile(b.profiles, sel)
		if err != nil || p.ID != "b2c5" {
			t.Fatalf("%q: %+v %v", sel, p, err)
		}
	}
}

func TestDisconnectWhileStartingEmitsNoFailure(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	b.state = tailscale.StateStarting // never reaches Running
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarting)
	if err := s.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.failures) != 0 {
		t.Fatalf("user-initiated disconnect must not report a failure: %v", n.failures)
	}
	stopped := 0
	for _, st := range n.states {
		if st == StateStopped {
			stopped++
		}
	}
	if stopped != 1 {
		t.Fatalf("expected exactly one STOPPED, states=%v", n.states)
	}
}

func TestConcurrentDisconnectDoesNotPanic(t *testing.T) {
	b, n, d := newBackend(), newNotifier(), newDeactivator()
	s := newService(b, n, d)
	if err := s.Connect(context.Background(), set); err != nil {
		t.Fatal(err)
	}
	n.waitState(t, StateStarted)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Disconnect(context.Background()) }()
	}
	wg.Wait()
	<-s.Stopped
}
