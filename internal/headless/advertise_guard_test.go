package headless

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGuardAdvertisedEndpointLoopback verifies that advertising a loopback-bound
// MCP endpoint is allowed: a loopback listener is inert to the LAN, so a
// discovery advertisement cannot expose it.
func TestGuardAdvertisedEndpointLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9090", "localhost:9090", "[::1]:9090"} {
		t.Run(addr, func(t *testing.T) {
			if err := GuardAdvertisedEndpoint(context.Background(), addr); err != nil {
				t.Fatalf("loopback addr %q should be advertisable, got: %v", addr, err)
			}
		})
	}
}

// TestGuardAdvertisedEndpointRefusesUnauthWildcard verifies the canonical
// offense: a ":PORT" (all-interfaces) bind that answers a JSON-RPC call without
// auth must not be advertised.
func TestGuardAdvertisedEndpointRefusesUnauthWildcard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}

	err = GuardAdvertisedEndpoint(context.Background(), ":"+port)
	if !errors.Is(err, ErrUnguardedEndpoint) {
		t.Fatalf("expected ErrUnguardedEndpoint for an unauthenticated wildcard bind, got: %v", err)
	}
}

// TestGuardAdvertisedEndpointAllowsAuthGuarded verifies the exception: a
// non-loopback endpoint that an auth gateway answers with 401/403 may be
// advertised, because a caller without credentials cannot drive it.
func TestGuardAdvertisedEndpointAllowsAuthGuarded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener addr: %v", err)
	}

	if err := GuardAdvertisedEndpoint(context.Background(), ":"+port); err != nil {
		t.Fatalf("auth-guarded endpoint should be advertisable, got: %v", err)
	}
}

// TestGuardAdvertisedEndpointRefusesUnreachableNonLoopback documents the
// deliberate difference from the public-URL guard: for the isotope
// advertisement, "no response" is not evidence of safety. The non-loopback bind
// is asserted by our own config, so an unreachable endpoint is still refused.
func TestGuardAdvertisedEndpointRefusesUnreachableNonLoopback(t *testing.T) {
	err := GuardAdvertisedEndpoint(context.Background(), "0.0.0.0:1")
	if !errors.Is(err, ErrUnguardedEndpoint) {
		t.Fatalf("expected ErrUnguardedEndpoint for an unreachable non-loopback bind, got: %v", err)
	}
}

// TestServerSetForceAdvertise verifies the explicit --force override state on
// the server.
func TestServerSetForceAdvertise(t *testing.T) {
	s := &Server{}
	s.SetForceAdvertise(true)
	if !s.forceAdvert {
		t.Fatal("SetForceAdvertise(true) did not set the override")
	}
	s.SetForceAdvertise(false)
	if s.forceAdvert {
		t.Fatal("SetForceAdvertise(false) did not clear the override")
	}
}
