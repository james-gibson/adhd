// Package registry resolves an isotope name to the endpoint it currently
// answers on, through the lab's own registry (a smoke-alarm: GET
// /isotope/list).
//
// It is the Go half of the resolution smoke-alarm's --target-session does in
// kb/scripts: a name is looked up out of band, so no caller has to know a port.
// A registration is a heartbeat — the registry sweeps a name it has not heard
// from — so resolution is repeated rather than cached: a restarted surface
// answers on a new port and the next tick finds it (adhd#62).
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound is returned when the registry answered but holds no isotope of
// that name. It is distinct from an unreachable or malformed registry: "the
// name is not registered" is the surface being gone, while "the registry did
// not answer" is only the observer losing its directory.
var ErrNotFound = errors.New("isotope not in the registry")

// DefaultTimeout bounds one registry lookup when the caller supplies none.
const DefaultTimeout = 5 * time.Second

// Client resolves names against one registry. It holds no cache on purpose.
type Client struct {
	baseURL string
	timeout time.Duration
}

// NewClient creates a resolver for the registry at baseURL (e.g.
// "http://127.0.0.1:6100"). An empty baseURL is not an error here: every
// Resolve reports it, so a missing configuration shows on the light instead of
// taking the process down.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: strings.TrimSpace(baseURL),
		timeout: timeout,
	}
}

// Record is the part of an isotope record this package reads. The registry
// returns more; only the address is needed to reach a named surface.
type Record struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

// Resolve returns the MCP endpoint of the isotope named name, as a URL that
// mcpclient can POST to. The endpoint is looked up on every call, so a name
// that moves is followed rather than remembered.
func (c *Client) Resolve(ctx context.Context, name string) (string, error) {
	if c.baseURL == "" {
		return "", fmt.Errorf("cannot resolve %q: no registry configured", name)
	}

	records, err := c.list(ctx)
	if err != nil {
		return "", err
	}

	for _, rec := range records {
		if rec.Name != name {
			continue
		}
		if strings.TrimSpace(rec.Endpoint) == "" {
			return "", fmt.Errorf("%w: %q has an empty endpoint at %s", ErrNotFound, name, c.baseURL)
		}
		return ProbeURL(rec.Endpoint), nil
	}

	return "", fmt.Errorf("%w: %q at %s", ErrNotFound, name, c.baseURL)
}

// list reads the registry's isotope list.
func (c *Client) list(ctx context.Context) ([]Record, error) {
	endpoint := strings.TrimRight(c.baseURL, "/") + "/isotope/list"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("registry %s: build request: %w", c.baseURL, err)
	}

	client := &http.Client{Timeout: c.timeout}
	resp, err := client.Do(req) //nolint:gosec // G704: the registry is the configured alarm endpoint
	if err != nil {
		return nil, fmt.Errorf("registry %s: %w", c.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry %s: isotope list answered %d", c.baseURL, resp.StatusCode)
	}

	var records []Record
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return nil, fmt.Errorf("registry %s: decode isotope list: %w", c.baseURL, err)
	}

	return records, nil
}

// ProbeURL turns a registered endpoint into a URL a probed surface answers MCP
// on. The lab registers bare host:port — a surface publishes where it binds, not
// which path it serves — so the scheme and the /mcp path are the resolution's
// job, exactly as kb/scripts/smoke-alarm/spin.sh does for --target-session. An
// endpoint that already carries a scheme or a path keeps it.
func ProbeURL(endpoint string) string {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/mcp"
	}
	return u.String()
}
