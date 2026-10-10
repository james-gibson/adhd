package main

import (
	"testing"

	"github.com/james-gibson/adhd/internal/config"
)

// TestEnableMCPForAddr guards the fix for lezz auto/demo: an explicit
// --mcp-addr must enable the hosted MCP server even when the loaded or
// discovered config left it disabled (demo.ConfigFromClusters returns the
// default config, where mcp_server.enabled defaults to false).
func TestEnableMCPForAddr(t *testing.T) {
	t.Run("empty addr leaves config untouched", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.MCPServer.Enabled = false
		cfg.MCPServer.Addr = ":1234"

		enableMCPForAddr(cfg, "")

		if cfg.MCPServer.Enabled {
			t.Fatal("expected MCP server to stay disabled when --mcp-addr is empty")
		}
		if cfg.MCPServer.Addr != ":1234" {
			t.Fatalf("expected addr to be unchanged, got %q", cfg.MCPServer.Addr)
		}
	})

	t.Run("addr enables and overrides", func(t *testing.T) {
		cfg := config.DefaultConfig()
		if cfg.MCPServer.Enabled {
			t.Fatal("precondition failed: default config should leave MCP disabled")
		}

		enableMCPForAddr(cfg, ":59999")

		if !cfg.MCPServer.Enabled {
			t.Fatal("expected MCP server to be enabled when --mcp-addr is set")
		}
		if cfg.MCPServer.Addr != ":59999" {
			t.Fatalf("expected addr :59999, got %q", cfg.MCPServer.Addr)
		}
	})
}
