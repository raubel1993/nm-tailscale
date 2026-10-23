package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeDaemon struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []string
	bodies map[string]string
	events []string
	hold   chan struct{}
}

func newFake(t *testing.T) (*fakeDaemon, *Client) {
	t.Helper()
	// Short socket path: Unix socket paths are limited to ~108 bytes.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("cannot listen on unix socket: %v", err)
	}
	f := &fakeDaemon{t: t, bodies: map[string]string{}, hold: make(chan struct{})}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { close(f.hold); srv.Close() })
	return f, New(sock)
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != "local-tailscaled.sock" {
		http.Error(w, "bad host", http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.RequestURI()
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.bodies[key] = string(body)
	f.mu.Unlock()
	switch key {
	case "GET /localapi/v0/profiles/":
		fmt.Fprint(w, `[{"ID":"ca23","Name":"finquers@example.com","NetworkProfile":{"DomainName":"vpn.finquers.com"},"UserProfile":{"LoginName":"finquers@example.com"}},
		{"ID":"b2c5","Name":"sailo@example.com","NetworkProfile":{"DomainName":"vpn.sailo.ag"},"UserProfile":{"LoginName":"sailo@example.com"}}]`)
	case "GET /localapi/v0/profiles/current":
		fmt.Fprint(w, `{"ID":"ca23","Name":"finquers@example.com"}`)
	case "POST /localapi/v0/profiles/b2c5":
		w.WriteHeader(http.StatusNoContent)
	case "PATCH /localapi/v0/prefs":
		fmt.Fprint(w, `{"WantRunning":true}`)
	case "GET /localapi/v0/prefs":
		fmt.Fprint(w, `{"WantRunning":false}`)
	case "GET /localapi/v0/status?peers=false":
		fmt.Fprint(w, `{"BackendState":"Running","TailscaleIPs":["100.64.0.1","fd7a:115c:a1e0::1"]}`)
	case "GET /localapi/v0/watch-ipn-bus?mask=0":
		fl, _ := w.(http.Flusher)
		for _, e := range f.events {
			fmt.Fprintln(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
		<-f.hold
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (f *fakeDaemon) called(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

func TestProfilesParsesIdentityFields(t *testing.T) {
	_, c := newFake(t)
	ps, err := c.Profiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[1].ID != "b2c5" || ps[1].NetworkProfile.DomainName != "vpn.sailo.ag" || ps[1].UserProfile.LoginName != "sailo@example.com" {
		t.Fatalf("unexpected profiles: %+v", ps)
	}
}

func TestCurrentProfile(t *testing.T) {
	_, c := newFake(t)
	p, err := c.CurrentProfile(context.Background())
	if err != nil || p.ID != "ca23" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestSwitchProfilePostsToProfileID(t *testing.T) {
	f, c := newFake(t)
	if err := c.SwitchProfile(context.Background(), "b2c5"); err != nil {
		t.Fatal(err)
	}
	if !f.called("POST /localapi/v0/profiles/b2c5") {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestSwitchProfileEscapesID(t *testing.T) {
	f, c := newFake(t)
	err := c.SwitchProfile(context.Background(), "../logout")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("expected 404 for escaped id, got %v", err)
	}
	if f.called("POST /localapi/v0/logout") {
		t.Fatal("path traversal reached another endpoint")
	}
}

func TestSwitchProfileRejectsEmptyID(t *testing.T) {
	_, c := newFake(t)
	if err := c.SwitchProfile(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestSetWantRunningPatchesMaskedPrefs(t *testing.T) {
	f, c := newFake(t)
	if err := c.SetWantRunning(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var got map[string]bool
	if err := json.Unmarshal([]byte(f.bodies["PATCH /localapi/v0/prefs"]), &got); err != nil {
		t.Fatal(err)
	}
	if !got["WantRunningSet"] || got["WantRunning"] {
		t.Fatalf("unexpected body: %v", got)
	}
}

func TestStatusParsesBackendStateAndIPs(t *testing.T) {
	_, c := newFake(t)
	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.BackendState != StateRunning || len(s.TailscaleIPs) != 2 || !s.TailscaleIPs[0].Is4() {
		t.Fatalf("unexpected status: %+v", s)
	}
}

func TestWatchStreamsEvents(t *testing.T) {
	f, c := newFake(t)
	f.events = []string{`{"State":6}`, `{"Prefs":{"WantRunning":false}}`, `not json`, `{"ErrMessage":"boom"}`}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	timeout := time.After(3 * time.Second)
	for len(got) < 3 {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-timeout:
			t.Fatalf("timed out with %d events", len(got))
		}
	}
	if got[0].State == nil || *got[0].State != 6 {
		t.Fatalf("event 0: %+v", got[0])
	}
	if got[1].Prefs == nil || got[1].Prefs.WantRunning {
		t.Fatalf("event 1: %+v", got[1])
	}
	if got[2].ErrMessage == nil || *got[2].ErrMessage != "boom" {
		t.Fatalf("event 2: %+v", got[2])
	}
}

func TestNon2xxIsAPIError(t *testing.T) {
	_, c := newFake(t)
	err := c.do(context.Background(), http.MethodGet, "nope", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("got %v", err)
	}
}
