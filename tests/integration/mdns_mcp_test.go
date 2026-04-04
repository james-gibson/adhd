package integration

import (
	"context"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/dashboard"
	"github.com/james-gibson/adhd/internal/discovery"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// fakeMDNSBrowser is a test double for discovery.Browser.
// It emits the provided instances immediately, then blocks.
type fakeMDNSBrowser struct {
	ch chan discovery.Instance
}

func newFakeMDNSBrowser(instances ...discovery.Instance) *fakeMDNSBrowser {
	ch := make(chan discovery.Instance, len(instances))
	for _, inst := range instances {
		ch <- inst
	}
	return &fakeMDNSBrowser{ch: ch}
}

func (f *fakeMDNSBrowser) Browse(_ context.Context, _ string) (<-chan discovery.Instance, error) {
	return f.ch, nil
}

// startMDNSDashboard creates a dashboard with a fake browser, injects the
// provided discovery events via Update(), and starts an MCP server backed by
// the same cluster. Returns the MCP base URL and a cleanup function.
func startMDNSDashboard(t *testing.T, instances ...discovery.Instance) string {
	t.Helper()

	fb := newFakeMDNSBrowser(instances...)
	m := dashboard.NewBubbleTeaDashboardWithBrowser(&config.Config{}, fb)

	// Drive discovery events into the cluster through Update() — this is the
	// same path the Bubble Tea runtime takes when a SmokeAlarmDiscoveredMsg arrives.
	for _, inst := range instances {
		var msg interface{}
		if inst.Removed {
			msg = dashboard.SmokeAlarmRemovedMsg{Hostname: inst.Hostname}
		} else {
			msg = dashboard.SmokeAlarmDiscoveredMsg{
				Hostname: inst.Hostname,
				Addr:     inst.Addr,
				Port:     inst.Port,
			}
		}
		m.Update(msg)
	}

	addr := FreeAddr(t)
	mcpSrv := mcpserver.NewServer(config.MCPServerConfig{Enabled: true, Addr: addr}, m.Cluster())
	if err := mcpSrv.Start(context.Background()); err != nil {
		t.Fatalf("mcpserver.Start: %v", err)
	}
	t.Cleanup(func() { _ = mcpSrv.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond) // let the server bind

	return "http://" + addr + "/mcp"
}

// findLight searches an adhd.lights.list response for a light with the given name.
func findLight(t *testing.T, resp map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("adhd.lights.list: result is not an object")
	}
	lightList, ok := result["lights"].([]interface{})
	if !ok {
		t.Fatalf("adhd.lights.list: lights is not an array")
	}
	for _, item := range lightList {
		l, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if l["name"] == name {
			return l
		}
	}
	return nil
}

// ── light creation reflected in MCP surface ───────────────────────────────────

// Scenario: light created for a smoke-alarm that comes online — visible via MCP
func TestMDNSDiscoveredLightAppearsInMCPList(t *testing.T) {
	url := startMDNSDashboard(t, discovery.Instance{Hostname: "host-c", Addr: "10.0.0.3", Port: 80})

	resp := DoJSONRPCCall(t, url, "adhd.lights.list", nil)
	light := findLight(t, resp, "smoke-alarm:host-c")
	if light == nil {
		t.Fatal("expected smoke-alarm:host-c in adhd.lights.list after mDNS discovery")
	}
}

// Scenario: mDNS-discovered light has source "mdns" in the MCP response
func TestMDNSDiscoveredLightSourceIsMdnsInMCP(t *testing.T) {
	url := startMDNSDashboard(t, discovery.Instance{Hostname: "host-a"})

	resp := DoJSONRPCCall(t, url, "adhd.lights.list", nil)
	light := findLight(t, resp, "smoke-alarm:host-a")
	if light == nil {
		t.Fatal("light not found")
	}
	if light["source"] != "mdns" {
		t.Errorf("expected source mdns, got %q", light["source"])
	}
}

// Scenario: mDNS-discovered light starts dark — dark status surfaced via MCP
func TestMDNSDiscoveredLightStatusIsDarkInMCP(t *testing.T) {
	url := startMDNSDashboard(t, discovery.Instance{Hostname: "host-a"})

	resp := DoJSONRPCCall(t, url, "adhd.lights.list", nil)
	light := findLight(t, resp, "smoke-alarm:host-a")
	if light == nil {
		t.Fatal("light not found")
	}
	if light["status"] != "dark" {
		t.Errorf("expected status dark, got %q", light["status"])
	}
}

// Scenario: adhd.lights.get returns the mDNS-discovered light by name
func TestMDNSDiscoveredLightGetByName(t *testing.T) {
	url := startMDNSDashboard(t, discovery.Instance{Hostname: "primary", Addr: "10.0.0.1"})

	resp := DoJSONRPCCall(t, url, "adhd.lights.get", map[string]interface{}{"name": "smoke-alarm:primary"})
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("adhd.lights.get: no result (resp=%v)", resp)
	}
	if result["name"] != "smoke-alarm:primary" {
		t.Errorf("expected name smoke-alarm:primary, got %q", result["name"])
	}
	if result["source"] != "mdns" {
		t.Errorf("expected source mdns, got %q", result["source"])
	}
	if result["status"] != "dark" {
		t.Errorf("expected status dark, got %q", result["status"])
	}
}

// Scenario: multiple smoke-alarms each appear in adhd.lights.list
func TestMDNSMultipleDiscoveredLightsInMCP(t *testing.T) {
	url := startMDNSDashboard(t,
		discovery.Instance{Hostname: "host-1"},
		discovery.Instance{Hostname: "host-2"},
		discovery.Instance{Hostname: "host-3"},
	)

	resp := DoJSONRPCCall(t, url, "adhd.lights.list", nil)
	for _, h := range []string{"host-1", "host-2", "host-3"} {
		if findLight(t, resp, "smoke-alarm:"+h) == nil {
			t.Errorf("expected smoke-alarm:%s in adhd.lights.list", h)
		}
	}
}

// Scenario: adhd.status dark count includes mDNS-discovered lights
func TestMDNSDiscoveredLightsCountedInStatus(t *testing.T) {
	url := startMDNSDashboard(t,
		discovery.Instance{Hostname: "alarm-a"},
		discovery.Instance{Hostname: "alarm-b"},
	)

	resp := DoJSONRPCCall(t, url, "adhd.status", nil)
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("adhd.status: no result")
	}
	summary, ok := result["summary"].(map[string]interface{})
	if !ok {
		t.Fatalf("adhd.status: no summary")
	}
	dark := int(summary["dark"].(float64))
	if dark < 2 {
		t.Errorf("expected at least 2 dark lights (the 2 mDNS-discovered ones), got %d", dark)
	}
}

// ── removal reflected in MCP surface ─────────────────────────────────────────

// Scenario: deregistered smoke-alarm light is absent from adhd.lights.list
func TestMDNSRemovedLightAbsentFromMCPList(t *testing.T) {
	fb := newFakeMDNSBrowser()
	m := dashboard.NewBubbleTeaDashboardWithBrowser(&config.Config{}, fb)

	// Discover then remove
	m.Update(dashboard.SmokeAlarmDiscoveredMsg{Hostname: "host-b"})
	m.Update(dashboard.SmokeAlarmRemovedMsg{Hostname: "host-b"})

	addr := FreeAddr(t)
	mcpSrv := mcpserver.NewServer(config.MCPServerConfig{Enabled: true, Addr: addr}, m.Cluster())
	if err := mcpSrv.Start(context.Background()); err != nil {
		t.Fatalf("mcpserver.Start: %v", err)
	}
	t.Cleanup(func() { _ = mcpSrv.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond)

	resp := DoJSONRPCCall(t, "http://"+addr+"/mcp", "adhd.lights.list", nil)
	if findLight(t, resp, "smoke-alarm:host-b") != nil {
		t.Error("expected smoke-alarm:host-b to be absent after mDNS deregistration")
	}
}

// ── coexistence with static config ────────────────────────────────────────────

// Scenario: mDNS does not create a duplicate light for a statically-configured instance
func TestMDNSNoDuplicateForConfigLightInMCP(t *testing.T) {
	fb := newFakeMDNSBrowser()
	m := dashboard.NewBubbleTeaDashboardWithBrowser(&config.Config{}, fb)

	// Pre-inject a config light, then announce the same hostname via mDNS
	m.Update(dashboard.SmokeAlarmDiscoveredMsg{Hostname: "host-a"}) // mdns
	// Mark it as config-sourced (simulate what NewBubbleTeaDashboard does for static config)
	if l := m.Cluster().GetByName("smoke-alarm:host-a"); l != nil {
		l.Source = "config"
	}
	m.Update(dashboard.SmokeAlarmDiscoveredMsg{Hostname: "host-a"}) // same hostname via mDNS again

	addr := FreeAddr(t)
	mcpSrv := mcpserver.NewServer(config.MCPServerConfig{Enabled: true, Addr: addr}, m.Cluster())
	if err := mcpSrv.Start(context.Background()); err != nil {
		t.Fatalf("mcpserver.Start: %v", err)
	}
	t.Cleanup(func() { _ = mcpSrv.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond)

	resp := DoJSONRPCCall(t, "http://"+addr+"/mcp", "adhd.lights.list", nil)
	result := resp["result"].(map[string]interface{})
	lightList := result["lights"].([]interface{})

	count := 0
	for _, item := range lightList {
		if l, ok := item.(map[string]interface{}); ok && l["name"] == "smoke-alarm:host-a" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 light named smoke-alarm:host-a, got %d", count)
	}
	// Source must remain config
	light := findLight(t, resp, "smoke-alarm:host-a")
	if light["source"] != "config" {
		t.Errorf("expected source config (static takes precedence), got %q", light["source"])
	}
}
