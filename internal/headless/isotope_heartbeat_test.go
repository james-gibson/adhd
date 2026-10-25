package headless

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/isotope"
)

// registryStub records the heartbeat traffic adhd sends to smoke-alarm.
type registryStub struct {
	posts   atomic.Int32
	deletes atomic.Int32
	names   chan string
}

func newRegistryStub(t *testing.T) (*registryStub, *httptest.Server) {
	t.Helper()

	stub := &registryStub{names: make(chan string, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/isotope/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var reg isotope.Registration
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.posts.Add(1)
		stub.names <- reg.Name
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(isotope.Record{Name: reg.Name, Role: reg.Role, Endpoint: reg.Endpoint, TrustRung: 2})
	})
	mux.HandleFunc("/isotope/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		stub.deletes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return stub, srv
}

// smoke-alarm sweeps a name that stops renewing, so adhd must re-register under
// the same instance label (smoke-alarm#35). The heartbeat is not the initial
// registration: it runs on its own cadence and survives a failed beat.
func TestServeIsotopeHeartbeatRenewsRegistration(t *testing.T) {
	stub, registry := newRegistryStub(t)

	server := &Server{
		cfg:  &config.Config{MCPServer: config.MCPServerConfig{Addr: "127.0.0.1:9999"}},
		role: RolePrime,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		server.ServeIsotopeHeartbeat(ctx, registry.URL, 10*time.Millisecond)
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for stub.posts.Load() < 2 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("heartbeat posted %d time(s), want at least 2", stub.posts.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop after cancellation")
	}

	wantName := server.instanceLabel()
	for i := 0; i < 2; i++ {
		if got := <-stub.names; got != wantName {
			t.Fatalf("renewal registered %q, want the instance label %q", got, wantName)
		}
	}
}

// A node that gets to shut down should not leave its name in the registry.
func TestDeregisterIsotopeDeletesTheInstanceLabel(t *testing.T) {
	stub, registry := newRegistryStub(t)

	server := &Server{
		cfg:  &config.Config{MCPServer: config.MCPServerConfig{Addr: "127.0.0.1:9999"}},
		role: RolePrime,
	}
	server.DeregisterIsotope(registry.URL)

	if got := stub.deletes.Load(); got != 1 {
		t.Fatalf("deregistered %d time(s), want 1", got)
	}
}

// Deregistration is best-effort: a registry that is already gone must not panic
// or block shutdown.
func TestDeregisterIsotopeToleratesMissingRegistry(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := registry.URL
	registry.Close()

	server := &Server{
		cfg:  &config.Config{MCPServer: config.MCPServerConfig{Addr: "127.0.0.1:9999"}},
		role: RolePrime,
	}
	server.DeregisterIsotope(url) // must not panic
}

// registrationRole is shared by the initial registration and every heartbeat.
func TestRegistrationRole(t *testing.T) {
	for _, tc := range []struct {
		role      IsotopeRole
		primePlus bool
		wantRole  IsotopeRole
	}{
		{role: RoleStandalone, primePlus: false, wantRole: RolePrime},
		{role: RoleStandalone, primePlus: true, wantRole: RolePrimePlus},
		{role: RolePrimePlus, primePlus: false, wantRole: RolePrimePlus},
	} {
		s := &Server{role: tc.role, isPrimePlus: tc.primePlus}
		if got := s.registrationRole(); got != tc.wantRole {
			t.Errorf("role=%s primePlus=%v: got %s, want %s", tc.role, tc.primePlus, got, tc.wantRole)
		}
	}
}
