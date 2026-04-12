package integration

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// startMCPServer starts an MCP server on a free port and returns the address
func startMCPServer(t *testing.T, cluster *lights.Cluster) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}

	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{
		Enabled: true,
		Addr:    addr,
	}

	server := mcpserver.NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("failed to start MCP server: %v", err)
	}

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
	})

	return addr
}

// doMCPCall sends a JSON-RPC request to the MCP server
func doMCPCall(t *testing.T, addr string, method string, params interface{}) map[string]interface{} {
	return DoJSONRPCCall(t, fmt.Sprintf("http://%s/mcp", addr), method, params)
}

// TestMCPServerLightsList verifies adhd.lights.list returns all lights
func TestMCPServerLightsList(t *testing.T) {
	cluster := lights.NewCluster()

	// Add some lights
	cluster.Add(&lights.Light{
		Name:    "api-server",
		Type:    "mcp",
		Status:  lights.StatusGreen,
		Details: "running",
	})
	cluster.Add(&lights.Light{
		Name:    "database",
		Type:    "smoke-alarm",
		Status:  lights.StatusYellow,
		Details: "degraded",
	})
	cluster.Add(&lights.Light{
		Name:    "cache",
		Type:    "mcp",
		Status:  lights.StatusRed,
		Details: "failed",
	})

	addr := startMCPServer(t, cluster)

	result := doMCPCall(t, addr, "adhd.lights.list", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	lightsArray, ok := resultMap["lights"].([]interface{})
	if !ok {
		t.Fatal("lights is not an array")
	}

	if len(lightsArray) != 3 {
		t.Errorf("expected 3 lights, got %d", len(lightsArray))
	}

	// Verify we can find each light
	lightNames := make(map[string]bool)
	for _, lightIface := range lightsArray {
		lightMap, ok := lightIface.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := lightMap["name"].(string); ok {
			lightNames[name] = true
		}
	}

	expectedNames := []string{"api-server", "database", "cache"}
	for _, name := range expectedNames {
		if !lightNames[name] {
			t.Errorf("light %q not found in list", name)
		}
	}
}

// TestMCPServerLightsGet verifies adhd.lights.get returns a single light
func TestMCPServerLightsGet(t *testing.T) {
	cluster := lights.NewCluster()

	cluster.Add(&lights.Light{
		Name:    "target-service",
		Type:    "mcp",
		Source:  "smoke-alarm",
		Status:  lights.StatusGreen,
		Details: "all good",
	})

	addr := startMCPServer(t, cluster)

	result := doMCPCall(t, addr, "adhd.lights.get", map[string]interface{}{
		"name": "target-service",
	})

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	if name, ok := resultMap["name"].(string); !ok || name != "target-service" {
		t.Error("name not correct")
	}

	if status, ok := resultMap["status"].(string); !ok || status != "green" {
		t.Error("status not correct")
	}

	if details, ok := resultMap["details"].(string); !ok || details != "all good" {
		t.Error("details not correct")
	}
}

// TestMCPServerStatus verifies adhd.status returns correct counts
func TestMCPServerStatus(t *testing.T) {
	cluster := lights.NewCluster()

	// Add lights with different statuses
	cluster.Add(&lights.Light{Name: "light-1", Status: lights.StatusGreen})
	cluster.Add(&lights.Light{Name: "light-2", Status: lights.StatusGreen})
	cluster.Add(&lights.Light{Name: "light-3", Status: lights.StatusRed})
	cluster.Add(&lights.Light{Name: "light-4", Status: lights.StatusYellow})
	cluster.Add(&lights.Light{Name: "light-5", Status: lights.StatusDark})

	addr := startMCPServer(t, cluster)

	result := doMCPCall(t, addr, "adhd.status", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	lights, ok := resultMap["lights"].(map[string]interface{})
	if !ok {
		t.Fatal("lights is not an object")
	}

	total := int(lights["total"].(float64))
	green := int(lights["green"].(float64))
	red := int(lights["red"].(float64))
	yellow := int(lights["yellow"].(float64))
	dark := int(lights["dark"].(float64))

	if total != 5 {
		t.Errorf("expected total=5, got %d", total)
	}
	if green != 2 {
		t.Errorf("expected green=2, got %d", green)
	}
	if red != 1 {
		t.Errorf("expected red=1, got %d", red)
	}
	if yellow != 1 {
		t.Errorf("expected yellow=1, got %d", yellow)
	}
	if dark != 1 {
		t.Errorf("expected dark=1, got %d", dark)
	}
}

// TestMCPServerAddLightDynamically verifies lights added after startup appear in queries
func TestMCPServerAddLightDynamically(t *testing.T) {
	cluster := lights.NewCluster()

	addr := startMCPServer(t, cluster)

	// Initial list should be empty
	result := doMCPCall(t, addr, "adhd.lights.list", nil)
	resultMap, _ := result["result"].(map[string]interface{})
	lightsArray, _ := resultMap["lights"].([]interface{})

	if len(lightsArray) != 0 {
		t.Errorf("expected 0 lights initially, got %d", len(lightsArray))
	}

	// Add a light
	cluster.Add(&lights.Light{
		Name:   "new-light",
		Type:   "test",
		Status: lights.StatusGreen,
	})

	// Query again
	result = doMCPCall(t, addr, "adhd.lights.list", nil)
	resultMap, _ = result["result"].(map[string]interface{})
	lightsArray, _ = resultMap["lights"].([]interface{})

	if len(lightsArray) != 1 {
		t.Errorf("expected 1 light after add, got %d", len(lightsArray))
	}

	// Verify we can get the specific light
	result = doMCPCall(t, addr, "adhd.lights.get", map[string]interface{}{
		"name": "new-light",
	})

	if _, ok := result["result"]; !ok {
		t.Error("adhd.lights.get failed after dynamic add")
	}
}

// TestMCPServerErrorCases verifies error handling
func TestMCPServerErrorCases(t *testing.T) {
	cluster := lights.NewCluster()
	cluster.Add(&lights.Light{Name: "existing", Status: lights.StatusGreen})

	addr := startMCPServer(t, cluster)

	// Test: get non-existent light
	result := doMCPCall(t, addr, "adhd.lights.get", map[string]interface{}{
		"name": "nonexistent",
	})

	if errMap, ok := result["error"].(map[string]interface{}); ok {
		code := int(errMap["code"].(float64))
		if code != -32602 {
			t.Errorf("expected error code -32602, got %d", code)
		}
	} else {
		t.Error("expected error for non-existent light")
	}

	// Test: missing name parameter
	result = doMCPCall(t, addr, "adhd.lights.get", map[string]interface{}{})

	if errMap, ok := result["error"].(map[string]interface{}); ok {
		code := int(errMap["code"].(float64))
		if code != -32602 {
			t.Errorf("expected error code -32602, got %d", code)
		}
	} else {
		t.Error("expected error for missing parameter")
	}
}

// TestMCPServerLightsPreserveMetadata verifies light metadata is preserved
func TestMCPServerLightsPreserveMetadata(t *testing.T) {
	cluster := lights.NewCluster()

	cluster.Add(&lights.Light{
		Name:        "service-with-metadata",
		Type:        "mcp",
		Source:      "smoke-alarm",
		Status:      lights.StatusYellow,
		Details:     "Degraded performance observed",
		LastUpdated: time.Now(),
	})

	addr := startMCPServer(t, cluster)

	result := doMCPCall(t, addr, "adhd.lights.get", map[string]interface{}{
		"name": "service-with-metadata",
	})

	resultMap, _ := result["result"].(map[string]interface{})

	if source, ok := resultMap["source"].(string); !ok || source != "smoke-alarm" {
		t.Error("source metadata not preserved")
	}

	if details, ok := resultMap["details"].(string); !ok || details != "Degraded performance observed" {
		t.Error("details not preserved")
	}

	if lightType, ok := resultMap["type"].(string); !ok || lightType != "mcp" {
		t.Error("type not preserved")
	}
}

// TestMCPServerEmptyCluster verifies behavior with no lights
func TestMCPServerEmptyCluster(t *testing.T) {
	cluster := lights.NewCluster()

	addr := startMCPServer(t, cluster)

	// List should return empty array
	result := doMCPCall(t, addr, "adhd.lights.list", nil)
	resultMap, _ := result["result"].(map[string]interface{})
	lightsArray, _ := resultMap["lights"].([]interface{})

	if lightsArray == nil || len(lightsArray) != 0 {
		t.Error("expected empty lights array for empty cluster")
	}

	// Status should show all zeros
	result = doMCPCall(t, addr, "adhd.status", nil)
	resultMap, _ = result["result"].(map[string]interface{})
	lights, _ := resultMap["lights"].(map[string]interface{})

	if total := int(lights["total"].(float64)); total != 0 {
		t.Errorf("expected total=0, got %d", total)
	}
}

// TestMCPServerInitializeCompliance verifies initialize response format
func TestMCPServerInitializeCompliance(t *testing.T) {
	cluster := lights.NewCluster()

	addr := startMCPServer(t, cluster)

	result := doMCPCall(t, addr, "initialize", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	// Check protocol version
	if version, ok := resultMap["protocolVersion"].(string); !ok || version != "2024-11-05" {
		t.Error("invalid protocol version")
	}

	// Check server info
	serverInfo, ok := resultMap["serverInfo"].(map[string]interface{})
	if !ok {
		t.Error("serverInfo is not an object")
	}

	if name, ok := serverInfo["name"].(string); !ok || name != "adhd" {
		t.Error("server name not set")
	}
}

// TestMCPServerToolsListCompliance verifies tools/list response format
func TestMCPServerToolsListCompliance(t *testing.T) {
	cluster := lights.NewCluster()

	addr := startMCPServer(t, cluster)

	result := doMCPCall(t, addr, "tools/list", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	tools, ok := resultMap["tools"].([]interface{})
	if !ok {
		t.Fatal("tools is not an array")
	}

	// Should have 17 ADHD tools
	if len(tools) != 17 {
		t.Errorf("expected 17 tools, got %d", len(tools))
	}

	// Verify each tool has required fields
	for i, toolIface := range tools {
		tool, ok := toolIface.(map[string]interface{})
		if !ok {
			t.Errorf("tool %d is not an object", i)
			continue
		}

		if _, ok := tool["name"]; !ok {
			t.Errorf("tool %d missing name field", i)
		}

		if _, ok := tool["description"]; !ok {
			t.Errorf("tool %d missing description field", i)
		}

		if _, ok := tool["inputSchema"]; !ok {
			t.Errorf("tool %d missing inputSchema field", i)
		}
	}
}

// TestMCPServerLargeCluster verifies performance with many lights
func TestMCPServerLargeCluster(t *testing.T) {
	cluster := lights.NewCluster()

	// Add 100 lights
	for i := 0; i < 100; i++ {
		cluster.Add(&lights.Light{
			Name:   fmt.Sprintf("light-%d", i),
			Type:   "test",
			Status: lights.StatusGreen,
		})
	}

	addr := startMCPServer(t, cluster)

	// Query list
	start := time.Now()
	result := doMCPCall(t, addr, "adhd.lights.list", nil)
	elapsed := time.Since(start)

	resultMap, _ := result["result"].(map[string]interface{})
	lightsArray, _ := resultMap["lights"].([]interface{})

	if len(lightsArray) != 100 {
		t.Errorf("expected 100 lights, got %d", len(lightsArray))
	}

	// Should be fast
	if elapsed > 1*time.Second {
		t.Logf("warning: list with 100 lights took %v", elapsed)
	}

	// Query status
	result = doMCPCall(t, addr, "adhd.status", nil)
	resultMap, _ = result["result"].(map[string]interface{})
	lights, _ := resultMap["lights"].(map[string]interface{})

	if total := int(lights["total"].(float64)); total != 100 {
		t.Errorf("expected total=100, got %d", total)
	}
}

// TestMCPServerConcurrentQueries verifies thread-safety
func TestMCPServerConcurrentQueries(t *testing.T) {
	cluster := lights.NewCluster()

	for i := 0; i < 10; i++ {
		cluster.Add(&lights.Light{
			Name:   fmt.Sprintf("light-%d", i),
			Status: lights.StatusGreen,
		})
	}

	addr := startMCPServer(t, cluster)

	// Send concurrent queries
	done := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			result := doMCPCall(t, addr, "adhd.lights.list", nil)
			if _, ok := result["result"]; !ok {
				done <- fmt.Errorf("query %d failed", id)
			} else {
				done <- nil
			}
		}(i)
	}

	// Collect results
	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}
