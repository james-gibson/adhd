package mcpserver

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
)

// freeAddr returns a free TCP address
func freeAddr(t *testing.T) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// doJSONRPCCall sends a JSON-RPC request to the server
func doJSONRPCCall(t *testing.T, addr string, method string, params interface{}) map[string]interface{} {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	url := fmt.Sprintf("http://%s/mcp", addr)
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

// TestServerInitialize verifies the initialize method
func TestServerInitialize(t *testing.T) {
	cluster := lights.NewCluster()
	addr := freeAddr(t)

	cfg := config.MCPServerConfig{
		Enabled: true,
		Addr:    addr,
	}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond) // Give server time to start

	result := doJSONRPCCall(t, addr, "initialize", nil)

	// Verify JSON-RPC 2.0 compliance
	if jsonrpc, ok := result["jsonrpc"].(string); !ok || jsonrpc != "2.0" {
		t.Error("missing or invalid jsonrpc field")
	}

	if id, ok := result["id"].(float64); !ok || id != 1 {
		t.Error("id not echoed correctly")
	}

	// Verify no error
	if err, ok := result["error"]; ok && err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Verify result structure
	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	if protocolVersion, ok := resultMap["protocolVersion"].(string); !ok || protocolVersion != "2024-11-05" {
		t.Error("invalid or missing protocolVersion")
	}

	serverInfo, ok := resultMap["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatal("serverInfo is not an object")
	}

	if name, ok := serverInfo["name"].(string); !ok || name != "adhd" {
		t.Error("invalid or missing server name")
	}
}

// TestServerStatus verifies the adhd.status method
func TestServerStatus(t *testing.T) {
	cluster := lights.NewCluster()

	// Add some lights
	cluster.Add(&lights.Light{
		Name:   "light1",
		Type:   "test",
		Status: lights.StatusGreen,
	})
	cluster.Add(&lights.Light{
		Name:   "light2",
		Type:   "test",
		Status: lights.StatusRed,
	})
	cluster.Add(&lights.Light{
		Name:   "light3",
		Type:   "test",
		Status: lights.StatusYellow,
	})

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.status", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	summary, ok := resultMap["summary"].(map[string]interface{})
	if !ok {
		t.Fatal("summary is not an object")
	}

	total := int(summary["total"].(float64))
	green := int(summary["green"].(float64))
	red := int(summary["red"].(float64))
	yellow := int(summary["yellow"].(float64))

	if total != 3 {
		t.Errorf("expected total=3, got %d", total)
	}
	if green != 1 {
		t.Errorf("expected green=1, got %d", green)
	}
	if red != 1 {
		t.Errorf("expected red=1, got %d", red)
	}
	if yellow != 1 {
		t.Errorf("expected yellow=1, got %d", yellow)
	}
}

// TestServerLightsList verifies the adhd.lights.list method
func TestServerLightsList(t *testing.T) {
	cluster := lights.NewCluster()

	cluster.Add(&lights.Light{
		Name:    "test-light",
		Type:    "test",
		Status:  lights.StatusGreen,
		Details: "all good",
	})

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.lights.list", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	lights, ok := resultMap["lights"].([]interface{})
	if !ok {
		t.Fatal("lights is not an array")
	}

	if len(lights) != 1 {
		t.Errorf("expected 1 light, got %d", len(lights))
	}

	light, ok := lights[0].(map[string]interface{})
	if !ok {
		t.Fatal("light is not an object")
	}

	if name, ok := light["name"].(string); !ok || name != "test-light" {
		t.Error("invalid light name")
	}

	if status, ok := light["status"].(string); !ok || status != "green" {
		t.Error("invalid light status")
	}
}

// TestServerLightsGet verifies the adhd.lights.get method
func TestServerLightsGet(t *testing.T) {
	cluster := lights.NewCluster()

	cluster.Add(&lights.Light{
		Name:    "my-light",
		Type:    "mcp",
		Status:  lights.StatusRed,
		Details: "error occurred",
	})

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.lights.get", map[string]interface{}{
		"name": "my-light",
	})

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	if name, ok := resultMap["name"].(string); !ok || name != "my-light" {
		t.Error("invalid name in response")
	}

	if status, ok := resultMap["status"].(string); !ok || status != "red" {
		t.Error("invalid status in response")
	}
}

// TestServerLightsGetNotFound verifies error handling for non-existent lights
func TestServerLightsGetNotFound(t *testing.T) {
	cluster := lights.NewCluster()

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.lights.get", map[string]interface{}{
		"name": "nonexistent",
	})

	// Should have error field
	errMap, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatal("error field is not an object")
	}

	code, ok := errMap["code"].(float64)
	if !ok || code != -32602 {
		t.Errorf("expected error code -32602, got %v", code)
	}
}

// TestServerLightsGetMissingName verifies error handling for missing params
func TestServerLightsGetMissingName(t *testing.T) {
	cluster := lights.NewCluster()

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.lights.get", map[string]interface{}{})

	// Should have error field
	errMap, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error, got none")
	}

	code, ok := errMap["code"].(float64)
	if !ok || code != -32602 {
		t.Errorf("expected error code -32602, got %v", code)
	}
}

// TestServerToolsList verifies the tools/list method
func TestServerToolsList(t *testing.T) {
	cluster := lights.NewCluster()

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "tools/list", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	tools, ok := resultMap["tools"].([]interface{})
	if !ok {
		t.Fatal("tools is not an array")
	}

	// Should have 11 ADHD tools
	if len(tools) != 11 {
		t.Errorf("expected 11 tools, got %d", len(tools))
	}

	// Verify tool names
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		if name, ok := toolMap["name"].(string); ok {
			toolNames[name] = true
		}
	}

	expectedTools := map[string]bool{
		"adhd.status":            true,
		"adhd.lights.list":       true,
		"adhd.lights.get":        true,
		"adhd.isotope.instance":  true,
		"adhd.rung.respond":      true,
		"adhd.rung.verify":       true,
		"adhd.rung.challenge":    true,
	}

	for expected := range expectedTools {
		if !toolNames[expected] {
			t.Errorf("missing expected tool: %s", expected)
		}
	}
}

// TestServerPing verifies the ping method
func TestServerPing(t *testing.T) {
	cluster := lights.NewCluster()

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "ping", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	pong, ok := resultMap["pong"].(bool)
	if !ok || !pong {
		t.Error("expected pong=true")
	}
}

// TestServerUnknownMethod verifies error handling for unknown methods
func TestServerUnknownMethod(t *testing.T) {
	cluster := lights.NewCluster()

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "unknown.method", nil)

	// Should have error field
	errMap, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected error for unknown method")
	}

	code, ok := errMap["code"].(float64)
	if !ok || code != -32601 {
		t.Errorf("expected error code -32601, got %v", code)
	}
}

// TestServerDisabledServer verifies behavior when server is disabled
func TestServerDisabledServer(t *testing.T) {
	cluster := lights.NewCluster()

	cfg := config.MCPServerConfig{
		Enabled: false,
		Addr:    "127.0.0.1:9999",
	}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start should not error even when disabled: %v", err)
	}
	_ = server.Shutdown(context.Background())

	// No error expected, server should just skip startup
}

// TestServerJSONRPC20Compliance verifies JSON-RPC 2.0 compliance
func TestServerJSONRPC20Compliance(t *testing.T) {
	cluster := lights.NewCluster()

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Test that ID is echoed back correctly
	testCases := []struct {
		id       interface{}
		idString string
	}{
		{1, "1"},
		{"string-id", "string-id"},
		{999, "999"},
	}

	for _, tc := range testCases {
		req := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      tc.id,
			"method":  "ping",
		}

		body, _ := json.Marshal(req)
		url := fmt.Sprintf("http://%s/mcp", addr)
		resp, _ := http.Post(url, "application/json", bytes.NewReader(body))

		var result map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()

		// ID can be any JSON value, just verify it's echoed
		if result["id"] == nil {
			t.Errorf("ID not echoed for %v", tc.idString)
		}

		if result["jsonrpc"] != "2.0" {
			t.Error("jsonrpc field not set to 2.0")
		}
	}
}

// TestServerMultipleLights verifies correct behavior with multiple lights
func TestServerMultipleLights(t *testing.T) {
	cluster := lights.NewCluster()

	for i := 1; i <= 5; i++ {
		cluster.Add(&lights.Light{
			Name:   fmt.Sprintf("light-%d", i),
			Type:   "test",
			Status: lights.StatusGreen,
		})
	}

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.lights.list", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	lights, ok := resultMap["lights"].([]interface{})
	if !ok {
		t.Fatal("lights is not an array")
	}

	if len(lights) != 5 {
		t.Errorf("expected 5 lights, got %d", len(lights))
	}
}

// TestServerStatusSummaryAllStatuses verifies status summary with all status types
func TestServerStatusSummaryAllStatuses(t *testing.T) {
	cluster := lights.NewCluster()

	statuses := []lights.Status{
		lights.StatusGreen,
		lights.StatusGreen,
		lights.StatusRed,
		lights.StatusYellow,
		lights.StatusDark,
	}

	for i, status := range statuses {
		cluster.Add(&lights.Light{
			Name:   fmt.Sprintf("light-%d", i),
			Type:   "test",
			Status: status,
		})
	}

	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}

	server := NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.status", nil)

	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}

	summary, ok := resultMap["summary"].(map[string]interface{})
	if !ok {
		t.Fatal("summary is not an object")
	}

	if int(summary["total"].(float64)) != 5 {
		t.Error("incorrect total")
	}
	if int(summary["green"].(float64)) != 2 {
		t.Error("incorrect green count")
	}
	if int(summary["red"].(float64)) != 1 {
		t.Error("incorrect red count")
	}
	if int(summary["yellow"].(float64)) != 1 {
		t.Error("incorrect yellow count")
	}
	if int(summary["dark"].(float64)) != 1 {
		t.Error("incorrect dark count")
	}
}

// TestRungValidationInstanceIsotope verifies adhd.isotope.instance returns a stable isotope
func TestRungValidationInstanceIsotope(t *testing.T) {
	cluster := lights.NewCluster()
	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	server := NewServer(cfg, cluster)

	isotope := "test-isotope-abc123"
	server.SetInstanceIdentity(isotope,
		func(featureID, nonce string) string { return "receipt:" + featureID + ":" + nonce },
		func(receipt, featureID, nonce string) bool { return receipt == "receipt:"+featureID+":"+nonce },
	)

	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	result := doJSONRPCCall(t, addr, "adhd.isotope.instance", nil)
	resultMap, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatal("result is not an object")
	}
	got, ok := resultMap["isotope"].(string)
	if !ok || got != isotope {
		t.Errorf("expected isotope %q, got %v", isotope, resultMap["isotope"])
	}
}

// TestRungValidationRespondAndVerify verifies the respond/verify round-trip
func TestRungValidationRespondAndVerify(t *testing.T) {
	cluster := lights.NewCluster()
	addr := freeAddr(t)
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	server := NewServer(cfg, cluster)

	server.SetInstanceIdentity("isotope-x",
		func(featureID, nonce string) string { return "RECEIPT:" + featureID + ":" + nonce },
		func(receipt, featureID, nonce string) bool { return receipt == "RECEIPT:"+featureID+":"+nonce },
	)

	if err := server.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	// Respond to a challenge
	respondResult := doJSONRPCCall(t, addr, "adhd.rung.respond", map[string]interface{}{
		"feature_id": "adhd/lights-status",
		"nonce":      "nonce-001",
	})
	respondMap, ok := respondResult["result"].(map[string]interface{})
	if !ok {
		t.Fatal("respond result is not an object")
	}
	receipt, _ := respondMap["receipt"].(string)
	if receipt == "" {
		t.Fatal("respond returned empty receipt")
	}

	// Verify the receipt
	verifyResult := doJSONRPCCall(t, addr, "adhd.rung.verify", map[string]interface{}{
		"receipt":    receipt,
		"feature_id": "adhd/lights-status",
		"nonce":      "nonce-001",
	})
	verifyMap, ok := verifyResult["result"].(map[string]interface{})
	if !ok {
		t.Fatal("verify result is not an object")
	}
	valid, _ := verifyMap["valid"].(bool)
	if !valid {
		t.Error("expected valid=true for correct receipt")
	}

	// Verify with wrong nonce — must fail
	replayResult := doJSONRPCCall(t, addr, "adhd.rung.verify", map[string]interface{}{
		"receipt":    receipt,
		"feature_id": "adhd/lights-status",
		"nonce":      "different-nonce",
	})
	replayMap, ok := replayResult["result"].(map[string]interface{})
	if !ok {
		t.Fatal("replay verify result is not an object")
	}
	if replayMap["valid"].(bool) {
		t.Error("expected valid=false for replayed receipt with different nonce")
	}
}
