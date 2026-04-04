// Package demo provides discovery of lezz demo clusters via mDNS and the
// fixed /cluster registry endpoint. It intentionally has no dependency on
// lezz.go — the ClusterInfo shape is mirrored from the published JSON contract.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/discovery"
)

const (
	// MDNSService is the service type lezz demo registers.
	MDNSService = "_lezz-demo._tcp"

	// DiscoveryPort is the fixed well-known port for the /cluster registry.
	DiscoveryPort = 19100

	// DefaultTimeout is how long Browse waits before giving up.
	DefaultTimeout = 10 * time.Second

	// defaultInterval is the polling interval assigned to discovered endpoints.
	defaultInterval = 10 * time.Second
)

// ClusterInfo mirrors the JSON published by lezz demo's /cluster endpoint.
type ClusterInfo struct {
	Name    string `json:"name"`
	AlarmA  string `json:"alarm_a"`
	AlarmB  string `json:"alarm_b"`
	AdhdMCP string `json:"adhd_mcp"`
}

// Browse browses the LAN for a lezz demo registry and returns all registered
// clusters. It races two strategies:
//  1. mDNS browse for _lezz-demo._tcp — works when multicast is reliable.
//  2. Direct HTTP poll of localhost:DiscoveryPort/cluster — works when lezz
//     demo is on the same machine but mDNS is unavailable or slow.
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
			clusters, err := fetchRegistry(browseCtx, fmt.Sprintf("http://%s:%d/cluster", host, DiscoveryPort))
			ch <- result{clusters: clusters, err: err}
		case <-browseCtx.Done():
			ch <- result{err: browseCtx.Err()}
		}
	}()

	// Strategy 2: direct localhost HTTP poll (same machine, mDNS may be unreliable).
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-browseCtx.Done():
				ch <- result{err: browseCtx.Err()}
				return
			case <-ticker.C:
				clusters, err := fetchRegistry(browseCtx, fmt.Sprintf("http://127.0.0.1:%d/cluster", DiscoveryPort))
				if err == nil && len(clusters) > 0 {
					ch <- result{clusters: clusters}
					return
				}
			}
		}
	}()

	// Return whichever strategy wins; ignore errors unless both fail.
	var lastErr error
	for range 2 {
		r := <-ch
		if r.err == nil {
			return r.clusters, nil
		}
		lastErr = r.err
	}
	return nil, fmt.Errorf("no lezz demo found on the LAN within %s — is lezz demo running? (%w)", timeout, lastErr)
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
