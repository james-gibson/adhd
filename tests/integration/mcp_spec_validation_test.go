package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// TestMCPSpecCompliance validates that the ADHD MCP server implements the MCP specification
// This is the kind of check that fire-marshal should perform before deployment
func TestMCPSpecCompliance(t *testing.T) {
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

	endpoint := addr

	// Test 1: tools/list must be exposed
	toolsList := callMCP(t, endpoint, "tools/list", nil)
	if toolsList == nil {
		t.Fatal("tools/list not found")
	}

	// Test 2: tools/list response must contain tools
	tools, ok := toolsList["tools"].([]interface{})
	if !ok {
		t.Fatal("tools/list response missing 'tools' field")
	}
	if len(tools) == 0 {
		t.Fatal("tools/list returned empty tools array")
	}

	// Test 3: tools/call MUST be exposed if tools/list is exposed
	// This is the check that would have caught the original bug
	toolsCallResp := callMCP(t, endpoint, "tools/call", map[string]interface{}{
		"name": "adhd.status",
	})

	if toolsCallResp == nil {
		t.Fatal("SPEC VIOLATION: tools/list exposed but tools/call not found (MCP spec requires both)")
	}

	// Test 4: tools/call response must have proper MCP format (content array)
	content, ok := toolsCallResp["content"]
	if !ok {
		t.Fatal("tools/call response missing 'content' field (MCP spec required)")
	}
	if _, ok := content.([]interface{}); !ok {
		t.Fatal("tools/call 'content' field must be an array")
	}

	// Test 5: All tools listed must be reachable via tools/call
	// (even if they fail due to missing required parameters)
	for _, toolRaw := range tools {
		toolMap, ok := toolRaw.(map[string]interface{})
		if !ok {
			continue
		}
		toolName, ok := toolMap["name"].(string)
		if !ok {
			continue
		}

		// Call tools/call with the tool name
		// It's OK if the tool fails validation (missing params) — we're just checking it's reachable
		respBody := makeRawMCPCall(t, endpoint, "tools/call", map[string]interface{}{
			"name": toolName,
		})

		var response map[string]interface{}
		if err := json.Unmarshal(respBody, &response); err != nil {
			t.Errorf("SPEC VIOLATION: tool '%s' response invalid JSON: %v", toolName, err)
			continue
		}

		// Check if it's a method not found error (-32601)
		if errField, ok := response["error"]; ok && errField != nil {
			if errMap, ok := errField.(map[string]interface{}); ok {
				if code, ok := errMap["code"].(float64); ok && code == -32601 {
					t.Errorf("SPEC VIOLATION: tool '%s' listed in tools/list but not callable via tools/call", toolName)
				}
			}
		}
	}

	// Test 6: All responses must include "jsonrpc": "2.0"
	resp := callMCP(t, endpoint, "adhd.status", nil)
	if resp == nil {
		t.Fatal("adhd.status failed")
	}

	// Note: The jsonrpc field is in the outer response, not this map
	// but we verify through the raw request/response below

	// Test 7: Verify raw JSON-RPC response has proper envelope
	respBody := makeRawMCPCall(t, endpoint, "adhd.status", nil)
	var envelope map[string]interface{}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}

	if version, ok := envelope["jsonrpc"].(string); !ok || version != "2.0" {
		t.Fatal("SPEC VIOLATION: response missing 'jsonrpc': '2.0' (JSON-RPC 2.0 required)")
	}

	if _, ok := envelope["result"]; !ok {
		if _, ok := envelope["error"]; !ok {
			t.Fatal("SPEC VIOLATION: response must have either 'result' or 'error' field")
		}
	}

	if _, ok := envelope["id"]; !ok {
		t.Fatal("SPEC VIOLATION: response missing 'id' field (JSON-RPC 2.0 required)")
	}

	t.Log("✓ ADHD MCP server passes spec compliance validation")
}

func callMCP(t *testing.T, endpoint string, method string, params interface{}) map[string]interface{} {
	body := makeRawMCPCall(t, endpoint, method, params)

	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Logf("failed to parse response: %v", err)
		return nil
	}

	// Check for errors
	if errField, ok := response["error"]; ok && errField != nil {
		// Method not found is a valid error for spec validation
		if errMap, ok := errField.(map[string]interface{}); ok {
			if code, ok := errMap["code"].(float64); ok && code == -32601 {
				return nil
			}
		}
		t.Logf("RPC error: %v", errField)
		return nil
	}

	result, ok := response["result"]
	if !ok {
		return nil
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil
	}

	return resultMap
}

func makeRawMCPCall(t *testing.T, endpoint string, method string, params interface{}) []byte {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}

	body, _ := json.Marshal(reqBody)
	resp, err := http.Post("http://"+endpoint+"/mcp", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to call MCP: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var respBody bytes.Buffer
	if _, err := respBody.ReadFrom(resp.Body); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	return respBody.Bytes()
}
