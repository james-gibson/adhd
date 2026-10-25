package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
)

// TestRegisterDiscoveredClustersWiresProjectsAndPeer covers the config-driven
// half of project propagation: clusters discovered by `adhd --demo` are carried
// in the config, so the server must register their project proxy tools and
// record the cluster's MCP endpoint without an adhd.cluster.join push.
func TestRegisterDiscoveredClustersWiresProjectsAndPeer(t *testing.T) {
	clusters := []config.DiscoveredCluster{{
		Name:    "demo-001",
		AdhdMCP: "http://127.0.0.1:9100/mcp",
		Projects: []config.ClusterProject{
			{ID: "01JABCDEF", Name: "tuner", Repo: "james-gibson/tuner", MCPURL: "http://127.0.0.1:9200/mcp"},
			{Name: "", MCPURL: "http://127.0.0.1:9300/mcp"}, // anonymous: dropped
		},
	}}

	s := &Server{}
	s.RegisterDiscoveredClusters(clusters)

	data, err := json.Marshal(s.handleToolsList())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"adhd.project.01JABCDEF.call"`) {
		t.Fatalf("project tool not registered: %s", data)
	}

	peer, ok := s.clusterPeers["demo-001"]
	if !ok {
		t.Fatalf("cluster peer not recorded: %+v", s.clusterPeers)
	}
	if peer.Endpoint != "http://127.0.0.1:9100/mcp" {
		t.Errorf("peer endpoint = %q", peer.Endpoint)
	}

	// Re-registration must not duplicate the dynamic tool.
	s.RegisterDiscoveredClusters(clusters)
	data, err = json.Marshal(s.handleToolsList())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"adhd.project.01JABCDEF.call"`); n != 1 {
		t.Fatalf("tool registered %d times, want 1: %s", n, data)
	}
}

// TestRegisterDiscoveredClusterMovesProjectOnNewEndpoint covers a lezz demo
// restart: the seed id is the identity, so the same project on a new URL replaces
// its tool instead of leaving a stale proxy pointing at a dead port.
func TestRegisterDiscoveredClusterMovesProjectOnNewEndpoint(t *testing.T) {
	s := &Server{}
	project := config.ClusterProject{ID: "01JABCDEF", Name: "tuner", MCPURL: "http://127.0.0.1:9200/mcp"}
	s.RegisterDiscoveredClusters([]config.DiscoveredCluster{{
		Name: "demo-001", AdhdMCP: "http://127.0.0.1:9100/mcp", Projects: []config.ClusterProject{project},
	}})

	project.MCPURL = "http://127.0.0.1:9400/mcp"
	s.RegisterDiscoveredClusters([]config.DiscoveredCluster{{
		Name: "demo-001", AdhdMCP: "http://127.0.0.1:9100/mcp", Projects: []config.ClusterProject{project},
	}})

	data, err := json.Marshal(s.handleToolsList())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), `"adhd.project.01JABCDEF.call"`); n != 1 {
		t.Fatalf("tool registered %d times, want 1: %s", n, data)
	}
	for _, tool := range s.dynamicTools {
		if tool.name == "adhd.project.01JABCDEF.call" && tool.origin != "http://127.0.0.1:9400/mcp" {
			t.Fatalf("tool still points at %q", tool.origin)
		}
	}
}

// TestRegisterDiscoveredClusterWithoutProjects covers a cluster that fronts no
// project MCP servers: its peer is still recorded for path negotiation, and no
// proxy tool is created.
func TestRegisterDiscoveredClusterWithoutProjects(t *testing.T) {
	s := &Server{}
	s.RegisterDiscoveredClusters([]config.DiscoveredCluster{{
		Name:    "demo-002",
		AdhdMCP: "http://127.0.0.1:9100/mcp",
	}})

	if _, ok := s.clusterPeers["demo-002"]; !ok {
		t.Fatalf("cluster peer not recorded: %+v", s.clusterPeers)
	}
	data, err := json.Marshal(s.handleToolsList())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "adhd.project.demo-002.call") {
		t.Fatalf("unexpected project tool: %s", data)
	}
}
