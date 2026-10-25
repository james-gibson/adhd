package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startLocalRegistryFixture serves the two endpoints a localhost registry
// needs: /cluster advertises one cluster whose own address stands in for a live
// alarm, and /healthz answers for that alarm (see hasLiveAlarm). It skips when
// the well-known port is already in use, since that means a real registry (or
// another test) owns it.
func startLocalRegistryFixture(t *testing.T) (alarmURL string) {
	t.Helper()

	addr := fmt.Sprintf("127.0.0.1:%d", DiscoveryPort)
	alarmURL = "http://" + addr

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"local":{"name":"local","alarm_a":%q,"alarm_b":%q}}`, alarmURL, alarmURL)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("discovery port %s unavailable: %v", addr, err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(listener) }()

	return alarmURL
}

// TestBrowseLocalRegistryFindsLoopbackRegistry is the regression test for the
// localhost HTTP fallback: it used to poll a non-resolvable mDNS-style hostname
// ("_smoke-alarm.tcp") instead of the loopback address, so discovery failed even
// with a registry listening on the same machine.
//
// It targets the fallback directly rather than Browse. Browse races mDNS against
// the fallback and returns whichever answers first, so on a machine with a live
// demo cluster on the LAN the fallback is never consulted and its URL is not
// exercised at all — a Browse-level assertion cannot pin this on such a machine.
func TestBrowseLocalRegistryFindsLoopbackRegistry(t *testing.T) {
	alarmURL := startLocalRegistryFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clusters, err := browseLocalRegistry(ctx)
	if err != nil {
		t.Fatalf("browseLocalRegistry returned error: %v", err)
	}
	if len(clusters) != 1 {
		t.Fatalf("expected the fixture's 1 cluster, got %d: %+v", len(clusters), clusters)
	}
	if clusters[0].Name != "local" {
		t.Fatalf("expected cluster %q, got %q", "local", clusters[0].Name)
	}
	if clusters[0].AlarmA != alarmURL {
		t.Fatalf("unexpected alarm_a: %q", clusters[0].AlarmA)
	}
}

// TestBrowseFindsClusters is the Browse-level contract: however discovery
// answers — the fixture registry on loopback on a quiet machine, or real lezz
// demo clusters on the LAN — it must return usable clusters rather than fail, or
// hand back an entry a caller cannot configure adhd with.
//
// This deliberately does not assert "exactly one cluster". On a developer
// machine with demo clusters running, mDNS legitimately finds all of them; that
// is the code working, not the test's environment leaking in.
func TestBrowseFindsClusters(t *testing.T) {
	alarmURL := startLocalRegistryFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clusters, err := Browse(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("Browse returned error: %v", err)
	}
	if len(clusters) == 0 {
		t.Fatal("Browse returned no clusters and no error")
	}
	for _, c := range clusters {
		if c.Name == "" {
			t.Errorf("cluster has no name: %+v", c)
		}
		if c.AlarmA == "" && c.AlarmB == "" {
			t.Errorf("cluster %q advertises no alarm to configure: %+v", c.Name, c)
		}
	}

	// On a quiet machine the fixture is what discovery finds, so the fields are
	// exact; on a busy one it only has to be one usable cluster among many.
	if len(clusters) == 1 {
		if clusters[0].Name != "local" || clusters[0].AlarmA != alarmURL {
			t.Fatalf("expected the fixture cluster %q with alarm_a %q, got %+v", "local", alarmURL, clusters[0])
		}
	}
}

// TestBrowseIgnoresGhostRegistry asserts the localhost fallback rejects a
// registry whose cluster is gone. A registry process can outlive the cluster it
// advertises; trusting it makes adhd --demo build lights that can never go
// green, which looks like an adhd failure but is really a stale entry.
//
// The assertion tolerates a real LAN registry answering mDNS (this test cannot
// stop one) — what must never happen is Browse returning the ghost.
func TestBrowseIgnoresGhostRegistry(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", DiscoveryPort)

	// alarm_a points at a port nobody listens on: a dead cluster.
	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve dead port: %v", err)
	}
	deadAddr := deadListener.Addr().String()
	_ = deadListener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"ghost":{"name":"ghost","alarm_a":"http://%s"}}`, deadAddr)
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

	clusters, err := Browse(ctx, 1500*time.Millisecond)
	for _, c := range clusters {
		if c.Name == "ghost" {
			t.Fatalf("Browse trusted a ghost registry: %+v (err=%v)", c, err)
		}
	}
	if err == nil && len(clusters) == 0 {
		t.Fatalf("Browse returned neither clusters nor an error")
	}
	if err != nil && !strings.Contains(err.Error(), "stale registry entry") {
		t.Fatalf("error should name the stale registry entry, got: %v", err)
	}
}

// TestHasLiveAlarm covers the ghost gate directly: a cluster is live while one
// of its alarms answers a health probe.
func TestHasLiveAlarm(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer live.Close()

	deadListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve dead port: %v", err)
	}
	deadAddr := deadListener.Addr().String()
	_ = deadListener.Close()

	ctx := context.Background()

	cases := []struct {
		name     string
		clusters []ClusterInfo
		want     bool
	}{
		{"live alarm_a", []ClusterInfo{{Name: "a", AlarmA: live.URL}}, true},
		{"live alarm_b only", []ClusterInfo{{Name: "a", AlarmB: live.URL}}, true},
		{"second cluster live", []ClusterInfo{{Name: "a", AlarmA: "http://" + deadAddr}, {Name: "b", AlarmA: live.URL}}, true},
		{"all dead", []ClusterInfo{{Name: "a", AlarmA: "http://" + deadAddr}}, false},
		{"no alarms advertised", []ClusterInfo{{Name: "a"}}, false},
		{"no clusters", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasLiveAlarm(ctx, tc.clusters); got != tc.want {
				t.Fatalf("hasLiveAlarm = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAlarmLiveFallsBackToLoopback covers the case lezz actually produces: the
// cluster advertises the machine's outbound IP, which a host does not always
// route back to itself. The alarm is reachable, so the gate must not treat the
// cluster as a ghost.
func TestAlarmLiveFallsBackToLoopback(t *testing.T) {
	if hasLiveAlarm(context.Background(), []ClusterInfo{{Name: "c", AlarmA: "http://127.0.0.1:1"}}) {
		t.Fatal("port 1 must not read as a live alarm")
	}

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer live.Close()

	port := strings.TrimPrefix(live.URL, "http://127.0.0.1:")
	// 192.0.2.0/24 is TEST-NET-1: guaranteed unroutable, so only the loopback
	// fallback can make this alarm read as live.
	advertised := "http://192.0.2.10:" + port
	if !hasLiveAlarm(context.Background(), []ClusterInfo{{Name: "c", AlarmA: advertised}}) {
		t.Fatalf("alarm advertised as %s should be found via loopback", advertised)
	}
}

// TestRegistryProjectIDIsParsed covers the published registry contract: lezz
// writes projects[].id — the seed id that namespaces the proxy tool — so the
// mirrored ProjectEntry must carry it. Without the field the id is silently
// dropped and every project is namespaced by its display name.
func TestRegistryProjectIDIsParsed(t *testing.T) {
	const body = `{"name":"demo-001","projects":[{"id":"01JABCDEF","name":"tuner","repo":"james-gibson/tuner","mcp_url":"http://127.0.0.1:9200/mcp"}]}`

	var info ClusterInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatalf("decode registry entry: %v", err)
	}
	if len(info.Projects) != 1 {
		t.Fatalf("projects = %+v, want 1", info.Projects)
	}
	if info.Projects[0].ID != "01JABCDEF" {
		t.Fatalf("project id dropped: %+v", info.Projects[0])
	}
	if got := info.Projects[0].routingID(); got != "01JABCDEF" {
		t.Fatalf("legacy id should stand in for the route, got %q", got)
	}
}

// TestRegistryProjectRoutePreferred covers the ADR lezz.go 0003 vocabulary:
// route is the current field and id is the legacy alias, so a payload carrying
// both routes on the route.
func TestRegistryProjectRoutePreferred(t *testing.T) {
	const body = `{"name":"demo-001","projects":[{"route":"route-a","id":"id-b","name":"tuner","mcp_url":"http://127.0.0.1:9200/mcp"}]}`

	var info ClusterInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatalf("decode registry entry: %v", err)
	}
	if len(info.Projects) != 1 {
		t.Fatalf("projects = %+v, want 1", info.Projects)
	}
	if got := info.Projects[0].routingID(); got != "route-a" {
		t.Fatalf("routingID = %q, want route-a", got)
	}
}

// TestConfigFromClustersSurfacesProjects covers the automated project-context
// path: a cluster advertising projects must have them carried in the config's
// DiscoveredClusters. Without this, adhd --demo builds a config from the
// registry and silently drops the cluster's adhd_mcp endpoint and projects, so
// no adhd.project.<id>.call proxy tool is ever registered.
func TestConfigFromClustersSurfacesProjects(t *testing.T) {
	cfg := ConfigFromClusters([]ClusterInfo{
		{
			Name:    "demo-001",
			AlarmA:  "http://192.168.1.10:9001",
			AdhdMCP: "http://192.168.1.10:9100/mcp",
			Projects: []ProjectEntry{
				{ID: "01JABCDEF", Name: "tuner", Repo: "james-gibson/tuner", MCPURL: "http://192.168.1.10:9200/mcp"},
				{Name: "legacy", Repo: "james-gibson/legacy", MCPURL: "http://192.168.1.10:9300/mcp"},
			},
		},
	})

	// The alarm endpoints keep the shape they always had.
	if len(cfg.SmokeAlarm) != 1 || cfg.SmokeAlarm[0].Name != "alarm-a" {
		t.Fatalf("SmokeAlarm = %+v, want one alarm-a endpoint", cfg.SmokeAlarm)
	}

	if len(cfg.DiscoveredClusters) != 1 {
		t.Fatalf("DiscoveredClusters = %+v, want 1", cfg.DiscoveredClusters)
	}
	dc := cfg.DiscoveredClusters[0]
	if dc.Name != "demo-001" {
		t.Errorf("cluster name = %q, want demo-001", dc.Name)
	}
	if dc.AdhdMCP != "http://192.168.1.10:9100/mcp" {
		t.Errorf("adhd_mcp dropped: %q", dc.AdhdMCP)
	}
	if len(dc.Projects) != 2 {
		t.Fatalf("projects = %+v, want 2", dc.Projects)
	}
	if dc.Projects[0].Route != "01JABCDEF" || dc.Projects[0].Name != "tuner" ||
		dc.Projects[0].Repo != "james-gibson/tuner" || dc.Projects[0].MCPURL != "http://192.168.1.10:9200/mcp" {
		t.Errorf("project identity dropped: %+v", dc.Projects[0])
	}
	// A project without a seed id keeps it empty: the route falls back to the
	// display name at registration time, and only then is it non-explicit.
	if dc.Projects[1].Route != "" {
		t.Errorf("absent route should stay empty, got %q", dc.Projects[1].Route)
	}
}

// TestConfigFromClustersWithoutProjectsKeepsClusterIdentity asserts the MCP
// endpoint is still surfaced for a cluster that publishes no projects — the
// peer remains addressable for path negotiation.
func TestConfigFromClustersWithoutProjectsKeepsClusterIdentity(t *testing.T) {
	cfg := ConfigFromClusters([]ClusterInfo{{Name: "demo-001", AdhdMCP: "http://192.168.1.10:9100/mcp"}})
	if len(cfg.DiscoveredClusters) != 1 {
		t.Fatalf("DiscoveredClusters = %+v, want 1", cfg.DiscoveredClusters)
	}
	if got := cfg.DiscoveredClusters[0].AdhdMCP; got != "http://192.168.1.10:9100/mcp" {
		t.Fatalf("adhd_mcp = %q", got)
	}
	if got := cfg.DiscoveredClusters[0].Projects; len(got) != 0 {
		t.Fatalf("projects = %+v, want none", got)
	}
}

func TestRegistryURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 0, "http://127.0.0.1:19100/cluster"},
		{"192.168.1.5", DiscoveryPort, "http://192.168.1.5:19100/cluster"},
		{"192.168.1.5", 20500, "http://192.168.1.5:20500/cluster"},
	}
	for _, tc := range cases {
		if got := RegistryURL(tc.host, tc.port); got != tc.want {
			t.Errorf("RegistryURL(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
	}
}

// TestRegistryURLForAlarm is a regression test for peer discovery deriving a
// malformed registry URL. Appending the discovery port to an alarm URL produced
// "http://host:5100:19100/cluster", which never resolved.
func TestRegistryURLForAlarm(t *testing.T) {
	cases := []struct {
		name  string
		alarm string
		want  string
	}{
		{"alarm base url", "http://192.168.7.18:5100", "http://192.168.7.18:19100/cluster"},
		{"alarm with status path", "http://10.0.0.5:18080/status", "http://10.0.0.5:19100/cluster"},
		{"https", "https://10.0.0.7:18080", "http://10.0.0.7:19100/cluster"},
		{"host port only", "127.0.0.1:9000", "http://127.0.0.1:19100/cluster"},
		{"host only", "127.0.0.1", "http://127.0.0.1:19100/cluster"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RegistryURLForAlarm(tc.alarm); got != tc.want {
				t.Fatalf("RegistryURLForAlarm(%q) = %q, want %q", tc.alarm, got, tc.want)
			}
		})
	}
}
