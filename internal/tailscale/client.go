// Package tailscale is a minimal client for the tailscaled LocalAPI over its
// Unix socket. Only the endpoints the plugin needs are implemented; it never
// logs out or deletes profiles.
package tailscale

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// DefaultSocket is the tailscaled control socket on Linux.
const DefaultSocket = "/var/run/tailscale/tailscaled.sock"

// baseURL is the pseudo host tailscaled requires in the Host header.
const baseURL = "http://local-tailscaled.sock/localapi/v0/"

// Backend states reported by tailscaled (ipn.State string values).
const (
	StateNeedsLogin       = "NeedsLogin"
	StateNeedsMachineAuth = "NeedsMachineAuth"
	StateStopped          = "Stopped"
	StateStarting         = "Starting"
	StateRunning          = "Running"
)

// Profile is the subset of ipn.LoginProfile the plugin uses.
type Profile struct {
	ID             string
	Name           string
	NetworkProfile struct {
		MagicDNSName string
		DomainName   string
	}
	UserProfile struct {
		LoginName   string
		DisplayName string
	}
}

// Status is the subset of ipnstate.Status the plugin uses.
type Status struct {
	BackendState string
	TailscaleIPs []netip.Addr
}

// Prefs is the subset of ipn.Prefs the plugin uses.
type Prefs struct {
	WantRunning bool
}

// Event is one notification from the IPN bus. Pointer fields are nil when the
// notification did not carry them.
type Event struct {
	State      *int
	Prefs      *Prefs
	ErrMessage *string
}

// Client talks to tailscaled. The zero value is not usable; use New.
type Client struct {
	http *http.Client
}

// New returns a client that dials the given Unix socket.
func New(socket string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}
	return &Client{http: &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: requestTimeout}}
}

// requestTimeout bounds every non-streaming LocalAPI call.
const requestTimeout = 15 * time.Second

// APIError is a non-2xx response from the LocalAPI.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("localapi: HTTP %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Profiles lists the login profiles known to tailscaled.
func (c *Client) Profiles(ctx context.Context) ([]Profile, error) {
	var ps []Profile
	if err := c.do(ctx, http.MethodGet, "profiles/", nil, &ps); err != nil {
		return nil, err
	}
	return ps, nil
}

// CurrentProfile returns the active login profile.
func (c *Client) CurrentProfile(ctx context.Context) (Profile, error) {
	var p Profile
	err := c.do(ctx, http.MethodGet, "profiles/current", nil, &p)
	return p, err
}

// SwitchProfile makes the profile with the given ID current. The ID must come
// from Profiles; it is escaped before being placed in the URL.
func (c *Client) SwitchProfile(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("localapi: empty profile id")
	}
	return c.do(ctx, http.MethodPost, "profiles/"+url.PathEscape(id), nil, nil)
}

// SetWantRunning turns the tunnel on or off for the current profile.
func (c *Client) SetWantRunning(ctx context.Context, on bool) error {
	body, _ := json.Marshal(map[string]bool{"WantRunningSet": true, "WantRunning": on})
	return c.do(ctx, http.MethodPatch, "prefs", body, nil)
}

// Prefs returns the current profile's preferences.
func (c *Client) Prefs(ctx context.Context) (Prefs, error) {
	var p Prefs
	err := c.do(ctx, http.MethodGet, "prefs", nil, &p)
	return p, err
}

// Status returns the backend status without the peer list.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, "status?peers=false", nil, &s)
	return s, err
}

// Watch streams IPN bus notifications until ctx is cancelled or the stream
// breaks. The channel is closed when the stream ends.
func (c *Client) Watch(ctx context.Context) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"watch-ipn-bus?mask=0", nil)
	if err != nil {
		return nil, err
	}
	// Streaming: no overall timeout; the context ends the watch.
	stream := &http.Client{Transport: c.http.Transport}
	resp, err := stream.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	ch := make(chan Event, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
