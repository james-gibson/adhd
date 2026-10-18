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

// TestResolveSmokeAlarm guards the fix for #15: a config-file-driven headless
// deployment (launchd/systemd) must register from its smoke_alarm list, not only
// from the --smoke-alarm flag.
func TestResolveSmokeAlarm(t *testing.T) {
	t.Run("flag wins over config", func(t *testing.T) {
		cfg := &config.Config{
			SmokeAlarm: []config.SmokeAlarmEndpoint{{Name: "primary", Endpoint: "http://config:8088"}},
		}
		if got := resolveSmokeAlarm("http://flag:8088", cfg); got != "http://flag:8088" {
			t.Fatalf("expected flag value to win, got %q", got)
		}
	})

	t.Run("falls back to first configured endpoint", func(t *testing.T) {
		cfg := &config.Config{
			SmokeAlarm: []config.SmokeAlarmEndpoint{
				{Name: "primary", Endpoint: "http://first:8088"},
				{Name: "secondary", Endpoint: "http://second:8089"},
			},
		}
		if got := resolveSmokeAlarm("", cfg); got != "http://first:8088" {
			t.Fatalf("expected first configured endpoint, got %q", got)
		}
	})

	t.Run("empty when neither is set", func(t *testing.T) {
		if got := resolveSmokeAlarm("", &config.Config{}); got != "" {
			t.Fatalf("expected no registration target, got %q", got)
		}
	})
}
