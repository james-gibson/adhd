package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// joinServer returns a server with a no-op join handler, which is the minimum
// adhd.cluster.join needs before it registers anything.
func joinServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(config.MCPServerConfig{Enabled: true, Addr: "127.0.0.1:0"}, lights.NewCluster())
	s.SetClusterJoinHandler(func(ClusterJoin) error { return nil })
	return s
}

// upstreamMCP serves one JSON-RPC method and answers with its own identity, so
// a test can tell which backend a dynamic tool actually reached.
func upstreamMCP(t *testing.T, identity string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]interface{}{"backend": identity},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func join(t *testing.T, s *Server, params map[string]interface{}) map[string]interface{} {
	t.Helper()
	res, jrpcErr := s.handleClusterJoin(context.Background(), params)
	if jrpcErr != nil {
		t.Fatalf("handleClusterJoin(%v): %+v", params["name"], jrpcErr)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("join result is %T", res)
	}
	return m
}

// callDynamic invokes a registered dynamic tool and returns its result.
func callDynamic(t *testing.T, s *Server, name string) map[string]interface{} {
	t.Helper()
	s.mu.RLock()
	var tool *dynamicTool
	for _, dt := range s.dynamicTools {
		if dt.name == name {
			tool = dt
			break
		}
	}
	s.mu.RUnlock()
	if tool == nil {
		t.Fatalf("tool %q not registered; registered: %v", name, dynamicNames(s))
	}
	res, jrpcErr := tool.handler(context.Background(), map[string]interface{}{"method": "tools/list"})
	if jrpcErr != nil {
		t.Fatalf("call %s: %+v", name, jrpcErr)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("call %s returned %T", name, res)
	}
	return m
}

func dynamicNames(s *Server) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.dynamicTools))
	for _, dt := range s.dynamicTools {
		names = append(names, dt.name)
	}
	return names
}

// TestClusterJoinRoutesTwoProvidersOfOneCapability is the adhd#40 scenario: two
// clusters each push a project called "grep", pointing at different MCP servers.
// Both must be reachable, and each must answer from its own backend.
func TestClusterJoinRoutesTwoProvidersOfOneCapability(t *testing.T) {
	s := joinServer(t)
	kb := upstreamMCP(t, "kb-synapse")
	personal := upstreamMCP(t, "personal-synapse")

	alpha := join(t, s, map[string]interface{}{
		"name": "alpha",
		"projects": []interface{}{
			map[string]interface{}{
				"name":    "grep",
				"route":   "20261019T210927-09001816",
				"mcp_url": kb.URL,
			},
		},
	})
	if got := alpha["projects_registered"]; got != 1 {
		t.Fatalf("alpha projects_registered = %v, want 1", got)
	}

	beta := join(t, s, map[string]interface{}{
		"name": "beta",
		"projects": []interface{}{
			map[string]interface{}{
				"name":    "grep",
				"route":   "20261020T101010-0a0b0c0d",
				"mcp_url": personal.URL,
			},
		},
	})
	if got := beta["projects_registered"]; got != 1 {
		t.Fatalf("beta projects_registered = %v, want 1 (the collision used to report 1 while registering nothing)", got)
	}

	for _, name := range []string{
		"adhd.project.20261019T210927-09001816.call",
		"adhd.project.20261020T101010-0a0b0c0d.call",
	} {
		if !containsString(dynamicNames(s), name) {
			t.Fatalf("missing %s; registered: %v", name, dynamicNames(s))
		}
	}

	if got := callDynamic(t, s, "adhd.project.20261019T210927-09001816.call")["backend"]; got != "kb-synapse" {
		t.Errorf("alpha route answered from %v, want kb-synapse", got)
	}
	if got := callDynamic(t, s, "adhd.project.20261020T101010-0a0b0c0d.call")["backend"]; got != "personal-synapse" {
		t.Errorf("beta route answered from %v, want personal-synapse", got)
	}
}

// TestClusterJoinLegacyNameCollisionIsReported covers callers that predate
// project.route: with no route there is no way to keep two providers apart, so
// the second join must say so instead of counting a tool it did not add.
func TestClusterJoinLegacyNameCollisionIsReported(t *testing.T) {
	s := joinServer(t)
	first := upstreamMCP(t, "first")
	second := upstreamMCP(t, "second")

	join(t, s, map[string]interface{}{
		"name": "alpha",
		"projects": []interface{}{
			map[string]interface{}{"name": "grep", "mcp_url": first.URL},
		},
	})
	beta := join(t, s, map[string]interface{}{
		"name": "beta",
		"projects": []interface{}{
			map[string]interface{}{"name": "grep", "mcp_url": second.URL},
		},
	})

	if got := beta["projects_registered"]; got != 0 {
		t.Errorf("projects_registered = %v, want 0", got)
	}
	conflicts, ok := beta["conflicts"].([]joinConflict)
	if !ok || len(conflicts) != 1 || conflicts[0].Tool != "adhd.project.grep.call" {
		t.Fatalf("conflicts = %v, want one entry for adhd.project.grep.call", beta["conflicts"])
	}
	if got := callDynamic(t, s, "adhd.project.grep.call")["backend"]; got != "first" {
		t.Errorf("grep route answered from %v, want first", got)
	}
}

// TestClusterJoinRejoinReplacesEndpoint keeps the rejoin honest: the route is
// the project's identity, so a new mcp_url is the same project on a new port,
// not a second one.
func TestClusterJoinRejoinReplacesEndpoint(t *testing.T) {
	s := joinServer(t)
	before := upstreamMCP(t, "before")
	after := upstreamMCP(t, "after")

	join(t, s, map[string]interface{}{
		"name": "alpha",
		"projects": []interface{}{
			map[string]interface{}{"name": "grep", "route": "route-1", "mcp_url": before.URL},
		},
	})
	rejoin := join(t, s, map[string]interface{}{
		"name": "alpha",
		"projects": []interface{}{
			map[string]interface{}{"name": "grep", "route": "route-1", "mcp_url": after.URL},
		},
	})

	if got := rejoin["projects_registered"]; got != 1 {
		t.Errorf("rejoin projects_registered = %v, want 1", got)
	}
	if names := dynamicNames(s); len(names) != 1 {
		t.Fatalf("dynamic tools = %v, want one", names)
	}
	if got := callDynamic(t, s, "adhd.project.route-1.call")["backend"]; got != "after" {
		t.Errorf("route answered from %v, want after — a rejoin must move the tool", got)
	}
}

// TestClusterJoinCountsMatchToolsList pins the invariant that matters: what a
// join reports is what tools/list will show. Repeats are reported separately.
func TestClusterJoinCountsMatchToolsList(t *testing.T) {
	s := joinServer(t)
	up := upstreamMCP(t, "up")

	params := map[string]interface{}{
		"name": "demo-1234",
		"github_repos": []interface{}{
			"james-gibson/tuner",
			"james-gibson/tuner",
		},
		"projects": []interface{}{
			map[string]interface{}{"name": "grep", "route": "route-1", "mcp_url": up.URL},
			map[string]interface{}{"name": "grep", "route": "route-1", "mcp_url": up.URL},
		},
		"honeypot-tools": []interface{}{
			map[string]interface{}{"name": "adhd.debug.vault", "description": "decoy"},
			map[string]interface{}{"name": "adhd.debug.vault", "description": "decoy"},
		},
	}

	res := join(t, s, params)
	for _, tc := range []struct {
		field string
		want  int
	}{
		{"repos_registered", 1},
		{"projects_registered", 1},
		{"honeypots_set", 1},
		{"repos_unchanged", 1},
		{"projects_unchanged", 1},
		{"honeypots_unchanged", 1},
	} {
		if got := res[tc.field]; got != tc.want {
			t.Errorf("%s = %v, want %d", tc.field, got, tc.want)
		}
	}
	if n := len(dynamicNames(s)); n != 3 {
		t.Errorf("dynamic tools = %v, want 3", dynamicNames(s))
	}
	if _, ok := res["conflicts"]; ok {
		t.Errorf("conflicts reported for identical repeats: %v", res["conflicts"])
	}
}

// TestProjectRouteFieldPrecedence pins the wire field order from ADR 0003:
// project.route, then the legacy seed.id, then the display name.
func TestProjectRouteFieldPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		pj       map[string]interface{}
		want     string
		explicit bool
	}{
		{"route", map[string]interface{}{"name": "grep", "route": "r-1", "id": "old"}, "r-1", true},
		{"legacy id", map[string]interface{}{"name": "grep", "id": "old"}, "old", true},
		{"name only", map[string]interface{}{"name": "grep"}, "grep", false},
		{"nothing", map[string]interface{}{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, explicit := projectRoute(tc.pj)
			if got != tc.want || explicit != tc.explicit {
				t.Errorf("projectRoute = (%q, %v), want (%q, %v)", got, explicit, tc.want, tc.explicit)
			}
		})
	}
}

// TestSanitizeProjectRoute keeps the tool name addressable: idPattern allows
// only ^[A-Za-z0-9_.-]+$, and an unusable route falls back to the name.
func TestSanitizeProjectRoute(t *testing.T) {
	cases := []struct{ route, name, want string }{
		{"20261019T210927-09001816", "grep", "20261019T210927-09001816"},
		{"bad route!!", "grep", "bad-route"},
		{"", "grep", "grep"},
		{"", "", "project"},
	}
	for _, tc := range cases {
		if got := sanitizeProjectRoute(tc.route, tc.name); got != tc.want {
			t.Errorf("sanitizeProjectRoute(%q, %q) = %q, want %q", tc.route, tc.name, got, tc.want)
		}
	}
	if strings.ContainsAny(sanitizeProjectRoute("a:b/c", ""), ":/") {
		t.Error("sanitizeProjectRoute left a character that idPattern rejects")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
