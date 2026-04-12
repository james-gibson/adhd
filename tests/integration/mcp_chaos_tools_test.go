package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// TestMCPToolsChaos validates tool availability and compliance under random conditions
// This chaos test continuously invokes tools to catch regressions early
func TestMCPToolsChaos(t *testing.T) {
	// Get a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	// Create cluster and MCP server
	cluster := lights.NewCluster()
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	server := mcpserver.NewServer(cfg, cluster)

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	if err := waitForServer(addr, 2*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}

	endpoint := addr

	// Get initial tool list
	toolsList := callMCP(t, endpoint, "tools/list", nil)
	if toolsList == nil {
		t.Fatal("tools/list not found")
	}

	tools, ok := toolsList["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		t.Fatal("tools/list returned invalid or empty tools array")
	}

	toolNames := extractToolNames(tools)
	t.Logf("Discovered %d tools: %v", len(toolNames), toolNames)

	// Define chaos scenarios
	scenarios := []struct {
		name string
		test func(t *testing.T, endpoint string, toolNames []string)
	}{
		{
			name: "rapid tool invocation",
			test: testRapidInvocation,
		},
		{
			name: "random tool invocation",
			test: testRandomToolInvocation,
		},
		{
			name: "tool list consistency",
			test: testToolListConsistency,
		},
		{
			name: "error response compliance",
			test: testErrorResponseCompliance,
		},
		{
			name: "response format validation",
			test: testResponseFormatValidation,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			scenario.test(t, endpoint, toolNames)
		})
	}
}

// testRapidInvocation invokes all tools in quick succession
func testRapidInvocation(t *testing.T, endpoint string, toolNames []string) {
	for _, toolName := range toolNames {
		respBody := makeRawMCPCall(t, endpoint, "tools/call", map[string]interface{}{
			"name": toolName,
		})

		var response map[string]interface{}
		if err := json.Unmarshal(respBody, &response); err != nil {
			t.Errorf("tool %s: invalid JSON response: %v", toolName, err)
			continue
		}

		if _, ok := response["error"]; !ok && response["result"] == nil {
			t.Errorf("tool %s: response missing both error and result", toolName)
		}

		// Verify JSON-RPC envelope
		if jsonrpc, ok := response["jsonrpc"].(string); !ok || jsonrpc != "2.0" {
			t.Errorf("tool %s: missing jsonrpc field", toolName)
		}
	}
}

// testRandomToolInvocation invokes tools in random order with variable inputs
func testRandomToolInvocation(t *testing.T, endpoint string, toolNames []string) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	iterations := 50

	toolInputs := map[string][]map[string]interface{}{
		"adhd.status":           {{}, nil},
		"adhd.lights.list":      {{}, nil},
		"adhd.lights.get":       {{"name": "nonexistent"}, {}},
		"adhd.isotope.status":   {{}, nil},
		"adhd.isotope.peers":    {{}, nil},
		"adhd.isotope.instance": {{}, nil},
	}

	for i := 0; i < iterations; i++ {
		toolName := toolNames[rng.Intn(len(toolNames))]
		var input interface{}

		if inputs, ok := toolInputs[toolName]; ok && len(inputs) > 0 {
			input = inputs[rng.Intn(len(inputs))]
		}

		respBody := makeRawMCPCall(t, endpoint, "tools/call", map[string]interface{}{
			"name":  toolName,
			"input": input,
		})

		var response map[string]interface{}
		if err := json.Unmarshal(respBody, &response); err != nil {
			t.Logf("warning: tool %s iteration %d: invalid JSON response: %v", toolName, i, err)
		}
	}

	t.Logf("Completed %d random tool invocations successfully", iterations)
}

// testToolListConsistency verifies tools/list returns the same tools repeatedly
func testToolListConsistency(t *testing.T, endpoint string, toolNames []string) {
	// Call tools/list multiple times
	for i := 0; i < 5; i++ {
		current := callMCP(t, endpoint, "tools/list", nil)
		if current == nil {
			t.Fatal("tools/list failed on iteration", i)
		}

		currentTools, ok := current["tools"].([]interface{})
		if !ok {
			t.Fatalf("iteration %d: tools field missing or invalid", i)
		}

		currentNames := extractToolNames(currentTools)
		if len(currentNames) != len(toolNames) {
			t.Errorf("iteration %d: tool count mismatch (expected %d, got %d)",
				i, len(toolNames), len(currentNames))
		}
	}
}

// testErrorResponseCompliance verifies error responses follow JSON-RPC 2.0 spec
func testErrorResponseCompliance(t *testing.T, endpoint string, toolNames []string) {
	// Test with invalid tool name
	respBody := makeRawMCPCall(t, endpoint, "tools/call", map[string]interface{}{
		"name": "nonexistent.tool.xyz",
	})

	var response map[string]interface{}
	if err := json.Unmarshal(respBody, &response); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}

	// Should have an error
	errField, hasError := response["error"]
	if !hasError || errField == nil {
		t.Fatal("expected error response for non-existent tool")
	}

	errMap, ok := errField.(map[string]interface{})
	if !ok {
		t.Fatal("error field is not an object")
	}

	// Verify error structure (JSON-RPC 2.0 spec)
	if _, hasCode := errMap["code"]; !hasCode {
		t.Error("error missing 'code' field")
	}
	if _, hasMsg := errMap["message"]; !hasMsg {
		t.Error("error missing 'message' field")
	}
	if _, hasID := response["id"]; !hasID {
		t.Error("response missing 'id' field")
	}
	if _, hasJSONRPC := response["jsonrpc"]; !hasJSONRPC {
		t.Error("response missing 'jsonrpc' field")
	}
}

// testResponseFormatValidation verifies tools/call responses follow MCP format
func testResponseFormatValidation(t *testing.T, endpoint string, toolNames []string) {
	for _, toolName := range []string{"adhd.status", "adhd.lights.list", "adhd.isotope.peers"} {
		respBody := makeRawMCPCall(t, endpoint, "tools/call", map[string]interface{}{
			"name": toolName,
		})

		var response map[string]interface{}
		if err := json.Unmarshal(respBody, &response); err != nil {
			t.Errorf("%s: invalid JSON: %v", toolName, err)
			continue
		}

		// For successful responses, check MCP format
		if result, ok := response["result"]; ok && result != nil {
			resultMap, ok := result.(map[string]interface{})
			if !ok {
				t.Errorf("%s: result is not an object", toolName)
				continue
			}

			// tools/call responses should have "content" field with array of content blocks
			content, hasContent := resultMap["content"]
			if !hasContent {
				t.Errorf("%s: result missing 'content' field (MCP spec required)", toolName)
				continue
			}

			contentArray, ok := content.([]interface{})
			if !ok {
				t.Errorf("%s: 'content' field is not an array", toolName)
				continue
			}

			// Each content block should have "type" and "text"
			for i, block := range contentArray {
				blockMap, ok := block.(map[string]interface{})
				if !ok {
					t.Errorf("%s: content[%d] is not an object", toolName, i)
					continue
				}

				if _, hasType := blockMap["type"]; !hasType {
					t.Errorf("%s: content[%d] missing 'type' field", toolName, i)
				}
				if _, hasText := blockMap["text"]; !hasText {
					t.Errorf("%s: content[%d] missing 'text' field", toolName, i)
				}
			}
		}
	}
}

// extractToolNames extracts tool names from tools array
func extractToolNames(tools []interface{}) []string {
	var names []string
	for _, toolRaw := range tools {
		if toolMap, ok := toolRaw.(map[string]interface{}); ok {
			if name, ok := toolMap["name"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names
}

// TestMCPToolsUnderConcurrentLoad simulates concurrent tool access
func TestMCPToolsUnderConcurrentLoad(t *testing.T) {
	// Get a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	// Create cluster and MCP server
	cluster := lights.NewCluster()
	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	server := mcpserver.NewServer(cfg, cluster)

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	if err := waitForServer(addr, 2*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}

	// Get tool list
	toolsList := callMCP(t, addr, "tools/list", nil)
	tools, _ := toolsList["tools"].([]interface{})
	toolNames := extractToolNames(tools)

	// Concurrent stress test
	const numGoroutines = 20
	const callsPerGoroutine = 50

	done := make(chan error, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		go func(goroutineID int) {
			// Each goroutine gets its own RNG to avoid races
			localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(goroutineID)))
			for c := 0; c < callsPerGoroutine; c++ {
				toolName := toolNames[localRand.Intn(len(toolNames))]
				respBody := makeRawMCPCall(t, addr, "tools/call", map[string]interface{}{
					"name": toolName,
				})

				var response map[string]interface{}
				if err := json.Unmarshal(respBody, &response); err != nil {
					done <- fmt.Errorf("goroutine %d call %d: JSON error: %w", goroutineID, c, err)
					return
				}

				// Verify response has required fields
				if _, hasResult := response["result"]; !hasResult {
					if _, hasError := response["error"]; !hasError {
						done <- fmt.Errorf("goroutine %d call %d: missing result or error", goroutineID, c)
						return
					}
				}
			}
			done <- nil
		}(g)
	}

	// Collect results
	var failures []error
	for i := 0; i < numGoroutines; i++ {
		if err := <-done; err != nil {
			failures = append(failures, err)
		}
	}

	if len(failures) > 0 {
		t.Errorf("concurrent load test had %d failures:", len(failures))
		for _, err := range failures[:min(5, len(failures))] {
			t.Logf("  %v", err)
		}
	} else {
		t.Logf("✓ Completed %d concurrent goroutines × %d calls = %d total tool invocations",
			numGoroutines, callsPerGoroutine, numGoroutines*callsPerGoroutine)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
