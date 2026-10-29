package localprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// surface is a stand-in for a session's MCP surface. With session=false it
// answers the handshake the way a surface whose session is gone does: -32001,
// while the process is still listening.
type surface struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
}

func newSurface(t *testing.T, session bool) *surface {
	t.Helper()

	s := &surface{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()

		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		if !session && req.Method == "initialize" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"no session here: 42"}}`))
			return
		}
		if req.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *surface) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// fakeRegistry is a stand-in for the alarm's GET /isotope/list.
type fakeRegistry struct {
	*httptest.Server
	mu      sync.Mutex
	records []map[string]string
	lookups int
	status  int
}

func newFakeRegistry(t *testing.T) *fakeRegistry {
	t.Helper()

	rg := &fakeRegistry{status: http.StatusOK}
	rg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/isotope/list" {
			http.NotFound(w, r)
			return
		}
		rg.mu.Lock()
		rg.lookups++
		status := rg.status
		records := append([]map[string]string(nil), rg.records...)
		rg.mu.Unlock()

		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(records)
	}))
	t.Cleanup(rg.Close)
	return rg
}

func (rg *fakeRegistry) set(name, endpoint string) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	for i := range rg.records {
		if rg.records[i]["name"] == name {
			rg.records[i]["endpoint"] = endpoint
			return
		}
	}
	rg.records = append(rg.records, map[string]string{"name": name, "endpoint": endpoint})
}

func (rg *fakeRegistry) lookupsCount() int {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.lookups
}

// hosted turns an httptest URL into the bare host:port a surface registers.
func hosted(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

func namedBinary() *config.Binary {
	return &config.Binary{
		Name:     "workspace",
		Endpoint: "workspace-42",
		Timeout:  2 * time.Second,
		Features: []config.Feature{{Name: "workspace-session"}},
	}
}

func TestNamedEndpointIsResolvedOnEveryProbeSoAMovedSurfaceIsFollowed(t *testing.T) {
	sess := newSurface(t, true)
	rg := newFakeRegistry(t)
	rg.set("workspace-42", hosted(sess.Server))

	prober := NewWithRegistry(namedBinary(), "", rg.URL)
	light := lights.New("workspace-session", "feature")

	result := prober.Probe(context.Background())
	prober.UpdateLight(light, result)

	if !result.LocalHealthy {
		t.Fatalf("first probe unhealthy: %s", result.LocalError)
	}
	if got := light.GetStatus(); got != lights.StatusGreen {
		t.Fatalf("light = %s, want green", got)
	}

	// The session restarts: same name, new ephemeral port.
	sess.Close()
	moved := newSurface(t, true)
	rg.set("workspace-42", hosted(moved.Server))

	result = prober.Probe(context.Background())
	prober.UpdateLight(light, result)

	if !result.LocalHealthy {
		t.Fatalf("probe after the move unhealthy: %s", result.LocalError)
	}
	if moved.hits() == 0 {
		t.Error("the moved surface was never probed: the endpoint was cached, not resolved")
	}
	if got := light.GetStatus(); got != lights.StatusGreen {
		t.Errorf("light after the move = %s, want green", got)
	}
	if rg.lookupsCount() < 2 {
		t.Errorf("registry lookups = %d, want one per probe", rg.lookupsCount())
	}
}

func TestANameTheRegistryDoesNotHoldIsRed(t *testing.T) {
	rg := newFakeRegistry(t)
	rg.set("someone-else", "127.0.0.1:1")

	prober := NewWithRegistry(namedBinary(), "", rg.URL)
	light := lights.New("workspace-session", "feature")

	result := prober.Probe(context.Background())
	prober.UpdateLight(light, result)

	if !result.NameNotFound {
		t.Errorf("NameNotFound = false, want true: %+v", result)
	}
	if got := light.GetStatus(); got != lights.StatusRed {
		t.Errorf("light = %s, want red: a name the registry does not hold is a surface that is gone", got)
	}
	if details := light.GetDetails(); !strings.Contains(details, "not in the registry") {
		t.Errorf("details = %q, want the reason the name did not resolve", details)
	}
}

func TestAnUnreachableRegistryIsYellowNotRed(t *testing.T) {
	rg := newFakeRegistry(t)
	rg.set("workspace-42", "127.0.0.1:1")
	rg.mu.Lock()
	rg.status = http.StatusInternalServerError
	rg.mu.Unlock()

	prober := NewWithRegistry(namedBinary(), "", rg.URL)
	light := lights.New("workspace-session", "feature")

	result := prober.Probe(context.Background())
	prober.UpdateLight(light, result)

	if result.NameNotFound {
		t.Error("an alarm that is not answering is not a missing name")
	}
	if got := light.GetStatus(); got != lights.StatusYellow {
		t.Errorf("light = %s, want yellow: the observer lost its directory, the surface may be fine", got)
	}
}

func TestASessionlessSurfaceReadsAsUnhealthyNotAbsentProcess(t *testing.T) {
	sess := newSurface(t, false)
	rg := newFakeRegistry(t)
	rg.set("workspace-42", hosted(sess.Server))

	prober := NewWithRegistry(namedBinary(), "", rg.URL)
	light := lights.New("workspace-session", "feature")

	result := prober.Probe(context.Background())
	prober.UpdateLight(light, result)

	if result.LocalHealthy {
		t.Fatal("a surface answering -32001 is not healthy")
	}
	if !strings.Contains(result.LocalError, "no session here") {
		t.Errorf("local error = %q, want the surface's own session verdict", result.LocalError)
	}
	if got := light.GetStatus(); got != lights.StatusRed {
		t.Errorf("light = %s, want red", got)
	}
}

func TestAStaticEndpointIsProbedWithoutTouchingTheRegistry(t *testing.T) {
	sess := newSurface(t, true)
	rg := newFakeRegistry(t)

	bin := namedBinary()
	bin.Endpoint = sess.URL // an explicit URL, not a name

	prober := NewWithRegistry(bin, "", rg.URL)
	light := lights.New("workspace-session", "feature")

	result := prober.Probe(context.Background())
	prober.UpdateLight(light, result)

	if !result.LocalHealthy {
		t.Fatalf("probe unhealthy: %s", result.LocalError)
	}
	if got := light.GetStatus(); got != lights.StatusGreen {
		t.Errorf("light = %s, want green", got)
	}
	if n := rg.lookupsCount(); n != 0 {
		t.Errorf("registry lookups = %d, want 0 for a fixed endpoint", n)
	}
}
