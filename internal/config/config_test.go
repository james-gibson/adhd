package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadDiscoveredClusters covers the config shape that carries project
// context from `adhd --demo` into a later run: a discovered cluster's adhd_mcp
// endpoint and its projects must survive a YAML round trip, because the proxy
// tools are registered from these values at startup.
func TestLoadDiscoveredClusters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adhd.yaml")
	const body = `mcp_server:
  enabled: true
  addr: ":19777"
discovered_clusters:
  - name: demo-001
    adhd_mcp: http://127.0.0.1:9100/mcp
    projects:
      - id: 01JABCDEF
        name: tuner
        repo: james-gibson/tuner
        mcp_url: http://127.0.0.1:9200/mcp
      - name: legacy
        mcp_url: http://127.0.0.1:9300/mcp
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.DiscoveredClusters) != 1 {
		t.Fatalf("DiscoveredClusters = %+v, want 1", cfg.DiscoveredClusters)
	}
	dc := cfg.DiscoveredClusters[0]
	if dc.Name != "demo-001" || dc.AdhdMCP != "http://127.0.0.1:9100/mcp" {
		t.Fatalf("cluster identity lost: %+v", dc)
	}
	if len(dc.Projects) != 2 {
		t.Fatalf("projects = %+v, want 2", dc.Projects)
	}
	if dc.Projects[0].ID != "01JABCDEF" || dc.Projects[0].MCPURL != "http://127.0.0.1:9200/mcp" {
		t.Errorf("project identity lost: %+v", dc.Projects[0])
	}
	if dc.Projects[1].ID != "" {
		t.Errorf("absent id should stay empty, got %q", dc.Projects[1].ID)
	}
}
