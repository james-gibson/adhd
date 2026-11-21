package health

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

// surface is a stand-in for an MCP surface on the wire. A workspace session has
// two of them when an agent publishes its own (lab-safety#37): the session's,
// which a separate process serves, and the agent's, which dies when the agent
// does.
type surface struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
}

func newSurface(t *testing.T) *surface {
	t.Helper()

	s := &surface{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		s.mu.Unlock()

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
	t.Cleanup(s.Close)
	return s
}

func (s *surface) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// registry serves GET /isotope/list from a mutable record set: the observer's
// view of which names are alive right now.
type registry struct {
	*httptest.Server
	mu      sync.Mutex
	records []map[string]string
}

func newRegistry(t *testing.T) *registry {
	t.Helper()

	rg := &registry{}
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

func (rg *registry) set(name string, srv *surface) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	endpoint := strings.TrimPrefix(srv.URL, "http://")
	for i := range rg.records {
		if rg.records[i]["name"] == name {
			rg.records[i]["endpoint"] = endpoint
			return
		}
	}
	rg.records = append(rg.records, map[string]string{"name": name, "endpoint": endpoint})
}

// remove is how a dead process leaves the registry: no record, no name.
func (rg *registry) remove(name string) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	kept := rg.records[:0]
	for _, rec := range rg.records {
		if rec["name"] != name {
			kept = append(kept, rec)
		}
	}
	rg.records = kept
}

// waitForLight waits for a light to exist and reach want. The monitor creates a
// light on its first tick, so absence is a retry, not a failure.
func waitForLight(t *testing.T, cluster *lights.Cluster, name string, want lights.Status) *lights.Light {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if light := cluster.GetByName(name); light != nil && light.GetStatus() == want {
			return light
		}
		time.Sleep(5 * time.Millisecond)
	}

	light := cluster.GetByName(name)
	if light == nil {
		t.Fatalf("no light named %q appeared", name)
	}
	t.Fatalf("light %q = %s (%s), want %s", name, light.GetStatus(), light.GetDetails(), want)
	return nil
}

// A workspace session is two processes, and they fail independently: the
// surface can keep answering after the agent has died, and a live agent can be
// attached to a session the surface has already lost. adhd#63 is that the
// observer reached only one of them.
//
// The shape the lab took is not a second probe kind: the agent serves the
// session's surface in its own process and registers under its own name
// (workspace-agent-42, lab-safety#37). So both processes are watchable through
// the one contract the observer has — a name, resolved on every tick — and the
// two lights stay two lights. A death on either side colours its own light and
// leaves the other alone.
func TestAnAgentAndItsSurfaceAreWatchedAsTwoSignals(t *testing.T) {
	rg := newRegistry(t)
	sessionSurface := newSurface(t)
	agentSurface := newSurface(t)

	rg.set("workspace-42", sessionSurface)
	rg.set("workspace-agent-42", agentSurface)

	cluster := lights.NewCluster()
	monitor := NewWithAlarms([]config.Binary{
		{
			Name:     "workspace",
			Endpoint: "workspace-42",
			Interval: 20 * time.Millisecond,
			Timeout:  time.Second,
			Features: []config.Feature{{Name: "workspace-session"}},
		},
		{
			Name:     "workspace-agent",
			Endpoint: "workspace-agent-42",
			Interval: 20 * time.Millisecond,
			Timeout:  time.Second,
			Features: []config.Feature{{Name: "workspace-agent"}},
		},
	}, cluster, "", rg.URL)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	monitor.Start(ctx)

	waitForLight(t, cluster, "workspace-session", lights.StatusGreen)
	waitForLight(t, cluster, "workspace-agent", lights.StatusGreen)

	if got := cluster.Count(); got != 2 {
		t.Fatalf("lights = %d, want 2: the surface and the agent are separate signals", got)
	}

	// The agent exits. Its own surface goes with it and its record leaves the
	// registry; the session's surface is another process and keeps answering.
	agentSurface.Close()
	rg.remove("workspace-agent-42")

	light := waitForLight(t, cluster, "workspace-agent", lights.StatusRed)
	if details := light.GetDetails(); !strings.Contains(details, "workspace-agent-42") {
		t.Errorf("agent details = %q, want the name that is gone", details)
	}
	if got := cluster.GetByName("workspace-session").GetStatus(); got != lights.StatusGreen {
		t.Errorf("surface light = %s after the agent exited, want green: one death, not two", got)
	}
	if sessionSurface.hits() == 0 {
		t.Error("the session surface was never probed")
	}
}
