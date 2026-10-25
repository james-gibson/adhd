// Package demo provides discovery of lezz demo clusters via mDNS and the
// fixed /cluster registry endpoint. It intentionally has no dependency on
// lezz.go — the ClusterInfo shape is mirrored from the published JSON contract.
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/discovery"
)

const (
	// MDNSService is the service type lezz demo registers.
	MDNSService = "_lezz-demo._tcp"

	// DiscoveryPort is the well-known port for the /cluster registry. It is the
	// default, not a guarantee: lezz advertises the registry's actual port in
	// its mDNS SRV record, so discovery follows the SRV port when it has one.
	DiscoveryPort = 19100

	// DefaultTimeout is how long Browse waits before giving up.
	DefaultTimeout = 10 * time.Second

	// defaultInterval is the polling interval assigned to discovered endpoints.
	defaultInterval = 10 * time.Second

	// pollInterval is how often the localhost fallback re-probes the registry.
	pollInterval = 500 * time.Millisecond

	// livenessTimeout bounds a single alarm health probe.
	livenessTimeout = time.Second
)

// RegistryURL returns the /cluster registry URL for host. When port is zero the
// well-known DiscoveryPort is used, so a caller that only knows the host still
// gets a valid URL.
func RegistryURL(host string, port int) string {
	if port <= 0 {
		port = DiscoveryPort
	}
	return fmt.Sprintf("http://%s:%d/cluster", host, port)
}

// RegistryURLForAlarm derives the registry URL for a cluster from one of its
// alarm URLs (e.g. "http://192.168.7.18:5100"). The alarm's port is replaced,
// never appended: "http://host:5100:19100/cluster" is not a URL and never
// resolved. Returns "" when no host can be recovered.
func RegistryURLForAlarm(alarmURL string) string {
	host := alarmURL
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return ""
	}
	return RegistryURL(host, 0)
}

// ProjectEntry describes a project within a cluster that exposes an MCP server.
type ProjectEntry struct {
	Name   string `json:"name"`    // short name, e.g. "tuner", "adhd"
	Repo   string `json:"repo"`    // GitHub slug, e.g. "james-gibson/tuner"
	MCPURL string `json:"mcp_url"` // base URL of the project's MCP server
}

// ClusterInfo mirrors the JSON published by lezz demo's /cluster endpoint.
type ClusterInfo struct {
	Name        string         `json:"name"`
	AlarmA      string         `json:"alarm_a"`
	AlarmB      string         `json:"alarm_b"`
	AdhdMCP     string         `json:"adhd_mcp"`
	GithubRepos []string       `json:"github_repos,omitempty"`
	Projects    []ProjectEntry `json:"projects,omitempty"`
}

// Browse browses the LAN for a lezz demo registry and returns all registered
// clusters. It races two strategies:
//  1. mDNS browse for _lezz-demo._tcp — works when multicast is reliable. The
//     registry's port comes from the SRV record, falling back to DiscoveryPort
//     only when the record omits it.
//  2. Direct HTTP poll of localhost:DiscoveryPort/cluster — works when lezz
//     demo is on the same machine but mDNS is unavailable or slow.
//
// Strategy 2 is a fallback with no advertiser vouching for it, so it accepts a
// registry only while at least one advertised alarm answers a health probe.
// Without that gate a registry process that outlived its cluster — a ghost —
// would be trusted, and every light built from it would stay red: adhd --demo
// would look broken when the real cause is a stale registry entry. An mDNS
// answer needs no such proof: an active advertisement is itself evidence the
// advertiser is running.
//
// Returns an error if neither finds anything within timeout.
func Browse(ctx context.Context, timeout time.Duration) ([]ClusterInfo, error) {
	browseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		clusters []ClusterInfo
		err      error
	}
	ch := make(chan result, 2)

	// Strategy 1: mDNS.
	go func() {
		browser := discovery.NewBrowser()
		mdnsCh, err := browser.Browse(browseCtx, MDNSService)
		if err != nil {
			ch <- result{err: fmt.Errorf("mDNS browse: %w", err)}
			return
		}
		select {
		case instance, ok := <-mdnsCh:
			if !ok {
				ch <- result{err: fmt.Errorf("mDNS browse closed before finding a lezz demo")}
				return
			}
			host := instance.Addr
			if host == "" {
				host = instance.Hostname
			}
			clusters, err := fetchRegistry(browseCtx, RegistryURL(host, instance.Port))
			ch <- result{clusters: clusters, err: err}
		case <-browseCtx.Done():
			ch <- result{err: browseCtx.Err()}
		}
	}()

	// Strategy 2: direct localhost HTTP poll (same machine, mDNS may be
	// unreliable). It lives in its own function so the regression it exists for
	// — the registry URL — can be tested without a LAN mDNS answer racing it.
	go func() {
		clusters, err := browseLocalRegistry(browseCtx)
		ch <- result{clusters: clusters, err: err}
	}()

	// Return whichever strategy wins; ignore errors unless both fail.
	var lastErr error
	sawStaleRegistry := false
	for range 2 {
		r := <-ch
		if r.err == nil {
			return r.clusters, nil
		}
		if errors.Is(r.err, errStaleLocalRegistry) {
			sawStaleRegistry = true
		}
		lastErr = r.err
	}
	err := fmt.Errorf("no lezz demo found on the LAN within %s — is lezz demo running? (%w)", timeout, lastErr)
	if sawStaleRegistry {
		return nil, fmt.Errorf("%w; the local registry at %s advertised clusters but none of their alarms answered a health probe — stale registry entry", err, RegistryURL("127.0.0.1", 0))
	}
	return nil, err
}

// errStaleLocalRegistry marks a localhost registry that answered with clusters
// whose alarms are all dead. It is a distinct error so Browse can name the ghost
// in its message instead of suggesting no registry is running at all.
var errStaleLocalRegistry = errors.New("local registry advertised clusters but none of their alarms answered a health probe")

// browseLocalRegistry is Browse's localhost fallback: poll the well-known
// registry until one answers with a cluster that has at least one live alarm.
//
// The URL is the point. This fallback used to poll an mDNS-style hostname that
// could never resolve, so discovery failed even with a registry on the same
// machine; keeping the poll here — rather than inline in Browse's race — is what
// lets a test pin that without a real LAN cluster deciding who wins.
func browseLocalRegistry(ctx context.Context) ([]ClusterInfo, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	stale := false
	for {
		select {
		case <-ctx.Done():
			if stale {
				return nil, fmt.Errorf("%w: %w", errStaleLocalRegistry, ctx.Err())
			}
			return nil, ctx.Err()
		case <-ticker.C:
			clusters, err := fetchRegistry(ctx, RegistryURL("127.0.0.1", 0))
			if err != nil || len(clusters) == 0 {
				continue
			}
			if !hasLiveAlarm(ctx, clusters) {
				stale = true
				continue
			}
			return clusters, nil
		}
	}
}

// hasLiveAlarm reports whether at least one alarm advertised by these clusters
// answers a health probe — the ghost test for the localhost fallback, and the
// evidence a discovered cluster is worth configuring.
func hasLiveAlarm(ctx context.Context, clusters []ClusterInfo) bool {
	for _, c := range clusters {
		for _, alarm := range []string{c.AlarmA, c.AlarmB} {
			if alarm != "" && alarmLive(ctx, alarm) {
				return true
			}
		}
	}
	return false
}

// alarmLive reports whether an alarm base URL answers a health probe, trying the
// advertised host first and loopback second: lezz advertises the machine's
// outbound IP, which a host does not always route back to itself.
func alarmLive(ctx context.Context, alarm string) bool {
	base := strings.TrimRight(alarm, "/")
	if probeHealth(ctx, base) {
		return true
	}
	loopback := loopbackHost(base)
	return loopback != "" && probeHealth(ctx, loopback)
}

// probeHealth GETs <base>/healthz and reports whether the alarm answered 200.
// /healthz is the same liveness probe lezz waits on before starting adhd.
func probeHealth(ctx context.Context, base string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, livenessTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/healthz", http.NoBody)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{Timeout: livenessTimeout}).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// loopbackHost rewrites an http URL's host to 127.0.0.1, keeping its port.
// Returns "" when base carries no port to keep.
func loopbackHost(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Port() == "" {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%s", u.Port())
}

// ConfigFromClusters builds a *config.Config whose SmokeAlarm endpoints are
// populated from the discovered clusters. Each cluster contributes alarm_a and
// alarm_b; names are prefixed with the cluster name when there are multiple
// clusters to avoid collisions.
func ConfigFromClusters(clusters []ClusterInfo) *config.Config {
	cfg := config.DefaultConfig()
	for _, c := range clusters {
		prefix := ""
		if len(clusters) > 1 {
			prefix = c.Name + "/"
		}
		if c.AlarmA != "" {
			cfg.SmokeAlarm = append(cfg.SmokeAlarm, config.SmokeAlarmEndpoint{
				Name:     prefix + "alarm-a",
				Endpoint: c.AlarmA,
				Interval: defaultInterval,
			})
		}
		if c.AlarmB != "" {
			cfg.SmokeAlarm = append(cfg.SmokeAlarm, config.SmokeAlarmEndpoint{
				Name:     prefix + "alarm-b",
				Endpoint: c.AlarmB,
				Interval: defaultInterval,
			})
		}
	}
	return cfg
}

// FetchRegistry retrieves all registered clusters from a /cluster endpoint.
// Safe to call repeatedly — used by the dashboard to poll for newly-joined clusters.
func FetchRegistry(ctx context.Context, url string) ([]ClusterInfo, error) {
	return fetchRegistry(ctx, url)
}

// fetchRegistry retrieves the full cluster registry from a /cluster endpoint.
func fetchRegistry(ctx context.Context, url string) ([]ClusterInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch cluster registry: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var m map[string]ClusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode cluster registry: %w", err)
	}
	clusters := make([]ClusterInfo, 0, len(m))
	for _, v := range m {
		clusters = append(clusters, v)
	}
	return clusters, nil
}
