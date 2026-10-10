package demo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestBrowseFindsLocalRegistry is a regression test for the localhost HTTP
// fallback in Browse. The fallback previously polled a non-resolvable
// mDNS-style hostname ("_smoke-alarm.tcp") instead of the loopback address, so
// discovery always failed even when a registry was listening on the same
// machine. This test starts a registry on 127.0.0.1:DiscoveryPort and asserts
// Browse finds it.
//
// It skips when the well-known discovery port is already in use, since that
// means a real registry (or another test) owns it.
func TestBrowseFindsLocalRegistry(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", DiscoveryPort)

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"local":{"name":"local","alarm_a":"http://127.0.0.1:5100/status","alarm_b":"http://127.0.0.1:5101/status"}}`)
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("discovery port %s unavailable: %v", addr, err)
	}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clusters, err := Browse(ctx, 3*time.Second)
	if err != nil {
		t.Fatalf("Browse returned error: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d: %+v", len(clusters), clusters)
	}
	if clusters[0].Name != "local" {
		t.Fatalf("expected cluster %q, got %q", "local", clusters[0].Name)
	}
	if clusters[0].AlarmA != "http://127.0.0.1:5100/status" {
		t.Fatalf("unexpected alarm_a: %q", clusters[0].AlarmA)
	}
}
