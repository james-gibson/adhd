package headless

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/mcpserver"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// newAlarmStub serves a smoke-alarm /status with the two targets a demo alarm
// reports: the peer route and the alarm's own health check.
func newAlarmStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		resp := smokelink.StatusResponse{
			Service: "stub",
			Live:    true,
			Ready:   true,
			Targets: []smokelink.TargetStatus{
				{ID: "peer", State: "healthy", CheckedAt: time.Now().Format(time.RFC3339)},
				{ID: "self-health-check", State: "healthy", CheckedAt: time.Now().Format(time.RFC3339)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startWatching wires the headless watcher the way Start() does, without
// opening an MCP port or a log file: the config's alarms are polled and their
// updates drained into the cluster, which is what adhd.lights.list reads.
func startWatching(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	s := New(cfg)
	t.Cleanup(s.cancel)

	s.watchUpdates = make(chan smokelink.LightUpdate, 64)
	s.smokeWatcher = smokelink.NewWatcher(cfg.SmokeAlarm)
	s.smokeWatcher.Start(s.ctx, s.watchUpdates)
	go s.drainWatcherUpdates(s.watchUpdates)
	return s
}

func smokeLights(s *Server) []string {
	var names []string
	for _, l := range s.cluster.All() {
		if strings.HasPrefix(l.Name, "smoke:") {
			names = append(names, l.Name)
		}
	}
	sort.Strings(names)
	return names
}

func waitForSmokeLights(t *testing.T, s *Server, want int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		names := smokeLights(s)
		if len(names) >= want {
			return names
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d smoke lights, have %v", want, names)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// alarmLights is what one alarm reports: two routes on one node.
func alarmLights(prefix string) []string {
	return []string{
		"smoke:" + prefix + "/peer",
		"smoke:" + prefix + "/self-health-check",
	}
}

// alarmPair is the four lights two alarms report: two routes on two nodes.
func alarmPair(prefixA, prefixB string) []string {
	names := append(alarmLights(prefixA), alarmLights(prefixB)...)
	sort.Strings(names)
	return names
}

// TestSelfJoinDoesNotDoubleTheLights is adhd#49. `lezz demo` pushes its own
// cluster at its own dashboard; the join carries the same two alarms the local
// config already watches. Adopting them again under "<cluster>/alarm-a" made one
// probe two lights, so two alarms reported eight lights instead of four. The
// join's projects still have to be registered — only the watchers are skipped.
func TestSelfJoinDoesNotDoubleTheLights(t *testing.T) {
	alarmA := newAlarmStub(t)
	alarmB := newAlarmStub(t)

	cfg := config.DefaultConfig()
	cfg.SmokeAlarm = []config.SmokeAlarmEndpoint{
		{Name: "alarm-a", Endpoint: alarmA.URL, Interval: 50 * time.Millisecond},
		{Name: "alarm-b", Endpoint: alarmB.URL, Interval: 50 * time.Millisecond},
	}
	s := startWatching(t, cfg)

	want := alarmPair("alarm-a", "alarm-b")
	if got := waitForSmokeLights(t, s, len(want)); !equalNames(got, want) {
		t.Fatalf("before the join: lights = %v, want %v", got, want)
	}

	// The payload lezz sends: its own cluster, its own alarms, and its own MCP
	// endpoint. It is a self-join, so no watchers are adopted.
	if err := s.handleClusterJoin(mcpserver.ClusterJoin{
		Name:      "demo-61384",
		AlarmA:    alarmA.URL,
		AlarmB:    alarmB.URL,
		AdhdMCP:   "http://127.0.0.1:19090/mcp",
		Self:      true,
		SelfProof: mcpserver.SelfJoinByIsotope,
	}); err != nil {
		t.Fatalf("handleClusterJoin: %v", err)
	}

	// Let several poll cycles pass: a second adoption would surface within one.
	time.Sleep(250 * time.Millisecond)

	got := smokeLights(s)
	if !equalNames(got, want) {
		t.Fatalf("a self-join changed the lights: %v (want %v)", got, want)
	}
}

// TestRepeatedJoinDoesNotStartASecondPoller is the non-self half of adhd#49: a
// cluster that joins twice (a restart, a retry) must not double its own lights.
// The same name at the same endpoint is one poller.
func TestRepeatedJoinDoesNotStartASecondPoller(t *testing.T) {
	localA := newAlarmStub(t)
	localB := newAlarmStub(t)
	clusterA := newAlarmStub(t)
	clusterB := newAlarmStub(t)

	cfg := config.DefaultConfig()
	cfg.SmokeAlarm = []config.SmokeAlarmEndpoint{
		{Name: "local-a", Endpoint: localA.URL, Interval: 50 * time.Millisecond},
		{Name: "local-b", Endpoint: localB.URL, Interval: 50 * time.Millisecond},
	}
	s := startWatching(t, cfg)
	s.joinPollInterval = 50 * time.Millisecond

	want := alarmPair("local-a", "local-b")
	waitForSmokeLights(t, s, len(want))

	join := mcpserver.ClusterJoin{
		Name:    "demo-61384",
		AlarmA:  clusterA.URL,
		AlarmB:  clusterB.URL,
		AdhdMCP: "http://127.0.0.1:19091/mcp",
	}
	for range 2 {
		if err := s.handleClusterJoin(join); err != nil {
			t.Fatalf("handleClusterJoin: %v", err)
		}
	}

	want = append(want, alarmPair("demo-61384/alarm-a", "demo-61384/alarm-b")...)
	sort.Strings(want)
	waitForSmokeLights(t, s, len(want))

	// Several poll cycles: a second poller for the joint cluster would add a
	// second source only under the same names, so the count is the assertion.
	time.Sleep(250 * time.Millisecond)
	if got := smokeLights(s); !equalNames(got, want) {
		t.Fatalf("a repeated join changed the lights: %v (want %v)", got, want)
	}
}

// TestAnAliasedAlarmKeepsItsOwnLight pins the boundary the fix must not cross.
// Endpoint equality is not route equality: an address is shared by design, and
// the alarms are a counter-rotating replica set. A second claim on one address
// is a second light, because the name is what keeps the replica auditable.
func TestAnAliasedAlarmKeepsItsOwnLight(t *testing.T) {
	alarm := newAlarmStub(t)

	cfg := config.DefaultConfig()
	cfg.SmokeAlarm = []config.SmokeAlarmEndpoint{
		{Name: "alarm-a", Endpoint: alarm.URL, Interval: 50 * time.Millisecond},
	}
	s := startWatching(t, cfg)
	s.joinPollInterval = 50 * time.Millisecond

	waitForSmokeLights(t, s, len(alarmLights("alarm-a")))

	if err := s.handleClusterJoin(mcpserver.ClusterJoin{
		Name:    "kb",
		AlarmA:  alarm.URL,
		AlarmB:  "",
		AdhdMCP: "http://127.0.0.1:29090/mcp",
	}); err != nil {
		t.Fatalf("handleClusterJoin: %v", err)
	}

	want := append(alarmLights("alarm-a"), alarmLights("kb/alarm-a")...)
	sort.Strings(want)
	if got := waitForSmokeLights(t, s, len(want)); !equalNames(got, want) {
		t.Fatalf("an aliased alarm lost its own light: %v (want %v)", got, want)
	}
}
