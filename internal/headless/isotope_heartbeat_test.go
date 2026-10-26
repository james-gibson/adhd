package headless

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/isotope"
)

// registryBody is the registration as it arrives: the shared fields plus the
// project identity adhd publishes.
type registryBody struct {
	Name         string `json:"name"`
	ProjectRoute string `json:"project_route"`
	Contract     string `json:"contract"`
	DependsOn    []struct {
		ProjectRoute string `json:"project_route"`
		Contract     string `json:"contract"`
	} `json:"depends_on"`
}

// registryStub records the heartbeat traffic adhd sends to smoke-alarm.
type registryStub struct {
	posts   atomic.Int32
	deletes atomic.Int32
	names   chan string
	bodies  chan registryBody
}

func newRegistryStub(t *testing.T) (*registryStub, *httptest.Server) {
	t.Helper()

	stub := &registryStub{names: make(chan string, 16), bodies: make(chan registryBody, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("/isotope/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var reg registryBody
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.posts.Add(1)
		stub.names <- reg.Name
		stub.bodies <- reg
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(isotope.Record{Name: reg.Name, Endpoint: "127.0.0.1:9999", TrustRung: 2})
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
		cfg: &config.Config{
			MCPServer: config.MCPServerConfig{Addr: "127.0.0.1:9999"},
			Project:   config.ProjectConfig{Route: "01JADHD"},
			SmokeAlarm: []config.SmokeAlarmEndpoint{{
				Endpoint:     "http://127.0.0.1:6100",
				ProjectRoute: "01JSMOKE",
				Contract:     "smoke-alarm-isotope-registry-v1:bb021b6028b247ed",
			}},
		},
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
		body := <-stub.bodies
		if body.ProjectRoute != "01JADHD" {
			t.Fatalf("renewal lost the project route: %q", body.ProjectRoute)
		}
		if len(body.DependsOn) != 1 ||
			body.DependsOn[0].ProjectRoute != "01JSMOKE" ||
			body.DependsOn[0].Contract != "smoke-alarm-isotope-registry-v1:bb021b6028b247ed" {
			t.Fatalf("renewal lost the dependency pin: %+v", body.DependsOn)
		}
	}
}

// The route is the project's stable id: config wins, then LEZZ_PROJECT_ROUTE,
// then seed.json's project.route (seed.id only as the legacy alias).
func TestProjectRoutePrecedence(t *testing.T) {
	dir := t.TempDir()
	seed := `{"version":1,"seed":{"id":"LEGACYID","surface":"base"},"project":{"name":"adhd","route":"01JSEED"}}`
	if err := os.WriteFile(filepath.Join(dir, "seed.json"), []byte(seed), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	t.Chdir(dir)
	t.Setenv("LEZZ_PROJECT_ROUTE", "")

	fromSeed := &Server{cfg: &config.Config{}}
	if got := fromSeed.projectRoute(); got != "01JSEED" {
		t.Fatalf("route = %q, want the seed file's project.route", got)
	}

	t.Setenv("LEZZ_PROJECT_ROUTE", "01JENV")
	if got := fromSeed.projectRoute(); got != "01JENV" {
		t.Fatalf("route = %q, want the environment override", got)
	}

	configured := &Server{cfg: &config.Config{Project: config.ProjectConfig{Route: "01JCONFIG"}}}
	if got := configured.projectRoute(); got != "01JCONFIG" {
		t.Fatalf("route = %q, want the configured route", got)
	}
}

// The legacy alias is read only when project.route is absent.
func TestSeedRouteFallsBackToLegacyID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seed.json")
	if err := os.WriteFile(path, []byte(`{"seed":{"id":"LEGACYID"},"project":{"name":"adhd"}}`), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if got := seedRouteFromFile(path); got != "LEGACYID" {
		t.Fatalf("route = %q, want the legacy seed.id", got)
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
