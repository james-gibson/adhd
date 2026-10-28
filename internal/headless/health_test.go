package headless

import (
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

// testSurface answers the MCP handshake as a live session's surface does.
func testSurface(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		if req.Method == "tools/list" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testRegistry serves GET /isotope/list from a mutable record set.
type testRegistry struct {
	*httptest.Server
	mu      sync.Mutex
	records []map[string]string
}

func newTestRegistry(t *testing.T) *testRegistry {
	t.Helper()

	rg := &testRegistry{}
	rg.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/isotope/list" {
			http.NotFound(w, r)
			return
		}
		rg.mu.Lock()
		records := append([]map[string]string(nil), rg.records...)
		rg.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(records)
	}))
	t.Cleanup(rg.Close)
	return rg
}

func (rg *testRegistry) set(name, endpoint string) {
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

func waitForStatus(t *testing.T, light *lights.Light, want lights.Status) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if light.GetStatus() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("light %q = %s (%s), want %s", light.Name, light.GetStatus(), light.GetDetails(), want)
}

// Headless mode is the deployment shape a per-workspace adhd runs in. If the
// local observer only starts in the TUI, a workspace's own adhd never probes its
// session and "each workspace is watched" is only the alarm's remote check
// (adhd#62).
func TestHeadlessStartsTheLocalObserverForANamedBinary(t *testing.T) {
	rg := newTestRegistry(t)

	cfg := config.DefaultConfig()
	// The alarm doubles as the registry here; leaving remote_smoke_alarm empty
	// keeps the consensus check out of the way so the light is the local verdict.
	cfg.SmokeAlarm = []config.SmokeAlarmEndpoint{{Name: "registry", Endpoint: rg.URL, Interval: time.Hour}}
	cfg.Features.Binaries = []config.Binary{{
		Name:     "workspace",
		Endpoint: "workspace-42", // a name, not a URL
		Interval: 20 * time.Millisecond,
		Timeout:  time.Second,
		Features: []config.Feature{{Name: "workspace-session"}},
	}}

	server := New(cfg)
	t.Cleanup(server.cancel)

	if err := server.Start(""); err != nil {
		t.Fatalf("headless Start: %v", err)
	}

	light := server.GetCluster().GetByName("workspace-session")
	if light == nil {
		t.Fatal("no light for the configured feature")
	}

	// The registry does not hold the name yet: the surface is gone, and the
	// light must say so rather than keep the green it was initialised with.
	waitForStatus(t, light, lights.StatusRed)
	if details := light.GetDetails(); !strings.Contains(details, "not in the registry") {
		t.Errorf("details = %q, want the unresolved name", details)
	}

	// The session starts and publishes itself by name, on a port the config
	// never knew.
	surface := testSurface(t)
	rg.set("workspace-42", strings.TrimPrefix(surface.URL, "http://"))

	waitForStatus(t, light, lights.StatusGreen)
}
