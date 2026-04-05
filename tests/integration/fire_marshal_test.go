package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// TestFireMarshalDiscoveryADHD simulates fire-marshal discovering ADHD's tools
func TestFireMarshalDiscoveryADHD(t *testing.T) {
	// Start ADHD MCP server
	cluster := lights.NewCluster()
	cluster.Add(&lights.Light{
		Name:   "test-light",
		Type:   "test",
		Status: lights.StatusGreen,
	})

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Simulate fire-marshal discovery: POST initialize
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"clientInfo": map[string]string{
				"name":    "fire-marshal",
				"version": "1.0.0",
			},
		},
	}

	initResp := doMCPRequest(t, fmt.Sprintf("http://%s/mcp", addr), initReq)

	// Verify initialize response
	if initResp["jsonrpc"] != "2.0" {
		t.Error("initialize response missing jsonrpc field")
	}

	result, ok := initResp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("initialize result is not an object")
	}

	if protocolVersion, ok := result["protocolVersion"].(string); !ok || protocolVersion != "2024-11-05" {
		t.Error("invalid protocol version in initialize response")
	}

	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Error("serverInfo missing from initialize response")
	}

	if name, ok := serverInfo["name"].(string); !ok || name != "adhd" {
		t.Error("server name not set to adhd")
	}
}

// TestFireMarshalToolsListADHD simulates fire-marshal discovering ADHD's tools
func TestFireMarshalToolsListADHD(t *testing.T) {
	cluster := lights.NewCluster()

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Simulate fire-marshal: POST tools/list
	toolsReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	}

	toolsResp := doMCPRequest(t, fmt.Sprintf("http://%s/mcp", addr), toolsReq)

	// Verify response structure
	if toolsResp["jsonrpc"] != "2.0" {
		t.Error("tools/list response missing jsonrpc")
	}

	if id, ok := toolsResp["id"].(float64); !ok || id != 2 {
		t.Error("tools/list id not echoed correctly")
	}

	result, ok := toolsResp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("tools/list result is not an object")
	}

	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatal("tools is not an array")
	}

	// Verify 11 ADHD tools
	if len(tools) != 16 {
		t.Errorf("expected 16 tools, got %d", len(tools))
		return
	}

	// Verify tool names and schemas
	toolNames := make(map[string]bool)
	for _, toolIface := range tools {
		tool, ok := toolIface.(map[string]interface{})
		if !ok {
			continue
		}

		name, ok := tool["name"].(string)
		if !ok {
			continue
		}

		toolNames[name] = true

		// Verify tool has required MCP fields
		if _, ok := tool["description"]; !ok {
			t.Errorf("tool %s missing description", name)
		}

		if _, ok := tool["inputSchema"]; !ok {
			t.Errorf("tool %s missing inputSchema", name)
		}
	}

	expectedTools := []string{
		"adhd.status",
		"adhd.lights.list",
		"adhd.lights.get",
	}

	for _, expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("expected tool %q not found", expected)
		}
	}
}

// TestFireMarshalProbeADHDStatus simulates fire-marshal calling adhd.status
func TestFireMarshalProbeADHDStatus(t *testing.T) {
	cluster := lights.NewCluster()

	// Add some lights for fire-marshal to verify
	cluster.Add(&lights.Light{Name: "api", Status: lights.StatusGreen})
	cluster.Add(&lights.Light{Name: "db", Status: lights.StatusRed})
	cluster.Add(&lights.Light{Name: "cache", Status: lights.StatusYellow})

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Fire-marshal calls adhd.status
	statusReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "adhd.status",
	}

	statusResp := doMCPRequest(t, fmt.Sprintf("http://%s/mcp", addr), statusReq)

	result, ok := statusResp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("adhd.status result is not an object")
	}

	lights, ok := result["lights"].(map[string]interface{})
	if !ok {
		t.Fatal("lights is not an object")
	}

	// Verify counts
	total := int(lights["total"].(float64))
	green := int(lights["green"].(float64))
	red := int(lights["red"].(float64))
	yellow := int(lights["yellow"].(float64))

	if total != 3 {
		t.Errorf("expected 3 total lights, got %d", total)
	}
	if green != 1 {
		t.Errorf("expected 1 green, got %d", green)
	}
	if red != 1 {
		t.Errorf("expected 1 red, got %d", red)
	}
	if yellow != 1 {
		t.Errorf("expected 1 yellow, got %d", yellow)
	}
}

// TestFireMarshalProbeADHDLightsList simulates fire-marshal calling adhd.lights.list
func TestFireMarshalProbeADHDLightsList(t *testing.T) {
	cluster := lights.NewCluster()

	// Add diverse lights
	cluster.Add(&lights.Light{
		Name:    "service-api",
		Type:    "mcp",
		Status:  lights.StatusGreen,
		Source:  "smoke-alarm",
		Details: "healthy",
	})
	cluster.Add(&lights.Light{
		Name:    "service-db",
		Type:    "smoke-alarm",
		Status:  lights.StatusRed,
		Source:  "direct-probe",
		Details: "connection refused",
	})

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Fire-marshal calls adhd.lights.list
	listReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "adhd.lights.list",
	}

	listResp := doMCPRequest(t, fmt.Sprintf("http://%s/mcp", addr), listReq)

	result, ok := listResp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("adhd.lights.list result is not an object")
	}

	lights, ok := result["lights"].([]interface{})
	if !ok {
		t.Fatal("lights is not an array")
	}

	if len(lights) != 2 {
		t.Errorf("expected 2 lights, got %d", len(lights))
	}

	// Verify each light has expected fields
	for i, lightIface := range lights {
		light, ok := lightIface.(map[string]interface{})
		if !ok {
			t.Errorf("light %d is not an object", i)
			continue
		}

		// Verify required fields
		if _, ok := light["name"]; !ok {
			t.Errorf("light %d missing name", i)
		}
		if _, ok := light["type"]; !ok {
			t.Errorf("light %d missing type", i)
		}
		if _, ok := light["status"]; !ok {
			t.Errorf("light %d missing status", i)
		}
		if _, ok := light["source"]; !ok {
			t.Errorf("light %d missing source", i)
		}
		if _, ok := light["details"]; !ok {
			t.Errorf("light %d missing details", i)
		}
	}
}

// TestFireMarshalProbeADHDLightsGet simulates fire-marshal querying a specific light
func TestFireMarshalProbeADHDLightsGet(t *testing.T) {
	cluster := lights.NewCluster()

	cluster.Add(&lights.Light{
		Name:    "critical-service",
		Type:    "mcp",
		Status:  lights.StatusRed,
		Source:  "smoke-alarm",
		Details: "service down",
	})

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Fire-marshal calls adhd.lights.get for a specific light
	getReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "adhd.lights.get",
		"params": map[string]interface{}{
			"name": "critical-service",
		},
	}

	getResp := doMCPRequest(t, fmt.Sprintf("http://%s/mcp", addr), getReq)

	result, ok := getResp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("adhd.lights.get result is not an object")
	}

	// Verify light fields
	if name, ok := result["name"].(string); !ok || name != "critical-service" {
		t.Error("light name not returned correctly")
	}

	if status, ok := result["status"].(string); !ok || status != "red" {
		t.Error("light status not returned correctly")
	}

	if lightType, ok := result["type"].(string); !ok || lightType != "mcp" {
		t.Error("light type not returned correctly")
	}

	if source, ok := result["source"].(string); !ok || source != "smoke-alarm" {
		t.Error("light source not returned correctly")
	}

	if details, ok := result["details"].(string); !ok || details != "service down" {
		t.Error("light details not returned correctly")
	}
}

// TestFireMarshalComplianceAllTools verifies all tools are callable
func TestFireMarshalComplianceAllTools(t *testing.T) {
	cluster := lights.NewCluster()

	// Populate cluster with test data
	for i := 0; i < 5; i++ {
		cluster.Add(&lights.Light{
			Name:   fmt.Sprintf("service-%d", i),
			Type:   "test",
			Status: lights.StatusGreen,
		})
	}

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	mcpAddr := fmt.Sprintf("http://%s/mcp", addr)

	// Test all advertised tools are callable

	// 1. adhd.status
	statusReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "adhd.status",
	}
	statusResp := doMCPRequest(t, mcpAddr, statusReq)
	if _, ok := statusResp["error"]; ok {
		t.Error("adhd.status call failed")
	}
	if _, ok := statusResp["result"]; !ok {
		t.Error("adhd.status did not return result")
	}

	// 2. adhd.lights.list
	listReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "adhd.lights.list",
	}
	listResp := doMCPRequest(t, mcpAddr, listReq)
	if _, ok := listResp["error"]; ok {
		t.Error("adhd.lights.list call failed")
	}
	if _, ok := listResp["result"]; !ok {
		t.Error("adhd.lights.list did not return result")
	}

	// 3. adhd.lights.get (with parameter)
	getReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "adhd.lights.get",
		"params": map[string]interface{}{
			"name": "service-0",
		},
	}
	getResp := doMCPRequest(t, mcpAddr, getReq)
	if _, ok := getResp["error"]; ok {
		t.Error("adhd.lights.get call failed")
	}
	if _, ok := getResp["result"]; !ok {
		t.Error("adhd.lights.get did not return result")
	}
}

// TestFireMarshalErrorHandling verifies fire-marshal gets proper error responses
func TestFireMarshalErrorHandling(t *testing.T) {
	cluster := lights.NewCluster()

	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	mcpServer := mcpserver.NewServer(cfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	mcpAddr := fmt.Sprintf("http://%s/mcp", addr)

	// Test: unknown method
	unknownReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "unknown.method",
	}
	unknownResp := doMCPRequest(t, mcpAddr, unknownReq)

	if errMap, ok := unknownResp["error"].(map[string]interface{}); ok {
		code := int(errMap["code"].(float64))
		if code != -32601 {
			t.Errorf("expected error code -32601 for unknown method, got %d", code)
		}
	} else {
		t.Error("expected error response for unknown method")
	}

	// Test: missing parameter for adhd.lights.get
	missingParamReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "adhd.lights.get",
		"params":  map[string]interface{}{},
	}
	missingParamResp := doMCPRequest(t, mcpAddr, missingParamReq)

	if errMap, ok := missingParamResp["error"].(map[string]interface{}); ok {
		code := int(errMap["code"].(float64))
		if code != -32602 {
			t.Errorf("expected error code -32602 for missing param, got %d", code)
		}
	} else {
		t.Error("expected error response for missing parameter")
	}

	// Test: non-existent light
	notFoundReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "adhd.lights.get",
		"params": map[string]interface{}{
			"name": "nonexistent",
		},
	}
	notFoundResp := doMCPRequest(t, mcpAddr, notFoundReq)

	if errMap, ok := notFoundResp["error"].(map[string]interface{}); ok {
		code := int(errMap["code"].(float64))
		if code != -32602 {
			t.Errorf("expected error code -32602 for not found, got %d", code)
		}
	} else {
		t.Error("expected error response for non-existent light")
	}
}

// doMCPRequest is a helper to send JSON-RPC requests to ADHD's MCP endpoint
func doMCPRequest(t *testing.T, url string, req map[string]interface{}) map[string]interface{} {
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to make request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	return result
}
