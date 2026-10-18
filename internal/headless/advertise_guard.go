package headless

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrUnguardedEndpoint is returned by GuardAdvertisedEndpoint when adhd is
// about to register with smoke-alarm and advertise itself over mDNS while its
// MCP endpoint is reachable beyond loopback without any inbound auth.
//
// adhd has no inbound authentication (handleMCPRPC injects the caller IP for
// audit/honeypot purposes only), so advertising a non-loopback MCP address
// names an unauthenticated MCP surface to the LAN — and as a monitored target
// to smoke-alarm. That is the -42i "alarm offense" the federation is right to
// yell about, so it must be impossible to ship by accident.
var ErrUnguardedEndpoint = errors.New("refusing to advertise an unauthenticated MCP endpoint beyond loopback")

// GuardAdvertisedEndpoint is the fail-closed preflight for isotope
// registration and mDNS advertisement. It answers one question: would the
// address adhd is about to advertise be reachable by a LAN peer without
// credentials?
//
//   - A loopback bind (127.0.0.1, ::1, localhost) is inert to the LAN, so it is
//     safe to advertise; no probe is needed.
//   - Any other bind is an unauthenticated surface on every interface it
//     listens on. It is advertised only if a live probe proves an auth gateway
//     answers on it (401/403). Any other answer — including a refused
//     connection — leaves the offense unproven-but-unrefuted, and the guard
//     refuses: the bind is asserted by our own config, so "unreachable" is not
//     evidence of safety.
//
// Callers must treat a non-nil error as "do not emit the advertisement and do
// not register this endpoint"; Server.RegisterAsIsotope does exactly that. The
// explicit --force override skips this guard (see Server.SetForceAdvertise) for
// the deliberate LAN-discovery case.
func GuardAdvertisedEndpoint(ctx context.Context, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid MCP address %q: %w", addr, err)
	}

	if isLoopbackHost(host) {
		return nil
	}

	// Bound beyond loopback: probe the effect rather than trust the intent.
	probeTarget := net.JoinHostPort(probeHost(host), port)
	code, err := probeMCPInitialize(ctx, probeTarget)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %s is bound beyond loopback and did not answer a probe (%v); bind mcp_server.addr to loopback (127.0.0.1:PORT), put an auth gateway in front, or pass --force",
			ErrUnguardedEndpoint, addr, err)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		// An auth gateway is answering — safe to advertise.
		return nil
	default:
		return fmt.Errorf("%w: %s answered HTTP %d without auth; bind mcp_server.addr to loopback (127.0.0.1:PORT), put an auth gateway in front, or pass --force",
			ErrUnguardedEndpoint, addr, code)
	}
}

// isLoopbackHost reports whether host names the loopback interface. The empty
// host (":9090") and "0.0.0.0"/"::" are wildcards, not loopback.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// probeHost maps a wildcard bind host onto a concrete loopback address to
// probe: a listener on the unspecified address answers on loopback too.
func probeHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return host
}

// probeMCPInitialize sends an unauthenticated JSON-RPC initialize to
// http://<hostPort>/mcp and returns the HTTP status code. A few short retries
// absorb the startup race between Server.Start and isotope registration.
func probeMCPInitialize(ctx context.Context, hostPort string) (int, error) {
	url := "http://" + hostPort + "/mcp"
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	client := &http.Client{Timeout: 3 * time.Second}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		_ = resp.Body.Close()
		return resp.StatusCode, nil
	}
	return 0, lastErr
}
