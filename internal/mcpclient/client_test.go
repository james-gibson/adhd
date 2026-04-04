package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeSuccessful verifies successful initialization and tools/list
func TestProbeSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		method, ok := req["method"].(string)
		if !ok {
			http.Error(w, "missing method", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"serverInfo": map[string]string{
						"name":    "test-mcp",
						"version": "0.1.0",
					},
				},
			}
			json.NewEncoder(w).Encode(resp)

		case "tools/list":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"tools": []map[string]string{
						{"name": "tool-a", "description": "Mock tool A"},
						{"name": "tool-b", "description": "Mock tool B"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)

		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if !result.Healthy {
		t.Error("expected healthy=true, got false")
	}
	if result.ToolCount != 2 {
		t.Errorf("expected 2 tools, got %d", result.ToolCount)
	}
	if len(result.Tools) != 2 {
		t.Errorf("expected Tools array length 2, got %d", len(result.Tools))
	}
	if result.Tools[0] != "tool-a" || result.Tools[1] != "tool-b" {
		t.Errorf("unexpected tool names: %v", result.Tools)
	}
	if result.Latency == 0 {
		t.Error("latency should be non-zero")
	}
	if result.Error != "" {
		t.Errorf("expected no error, got: %s", result.Error)
	}
}

// TestProbeInitializeFails verifies handling of initialize failures
func TestProbeInitializeFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			// Return JSON-RPC error
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"error": map[string]interface{}{
					"code":    -32000,
					"message": "server error",
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe returned error, expected ProbeResult with error field: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false")
	}
	if result.ToolCount != 0 {
		t.Errorf("expected toolCount=0, got %d", result.ToolCount)
	}
	if result.Error == "" {
		t.Error("expected error message in result")
	}
	if result.Latency == 0 {
		t.Error("latency should be measured even on error")
	}
}

// TestProbeToolsListFails verifies handling of tools/list failures
func TestProbeToolsListFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"serverInfo":      map[string]string{"name": "test"},
				},
			}
			json.NewEncoder(w).Encode(resp)

		case "tools/list":
			// Return JSON-RPC error
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"error": map[string]interface{}{
					"code":    -32001,
					"message": "tools not available",
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false when tools/list fails")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

// TestProbeHTTPError verifies handling of HTTP errors
func TestProbeHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe returned error, expected ProbeResult: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false on HTTP error")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

// TestProbeUnreachableEndpoint verifies handling of unreachable endpoints
func TestProbeUnreachableEndpoint(t *testing.T) {
	client := NewHTTPClient("http://127.0.0.1:1", 500*time.Millisecond)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe returned error, expected ProbeResult: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false")
	}
	if result.Error == "" {
		t.Error("expected error message for unreachable endpoint")
	}
}

// TestProbeMeasuresLatency verifies latency measurement
func TestProbeMeasuresLatency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]interface{}{"protocolVersion": "2024-11-05"},
			}
			json.NewEncoder(w).Encode(resp)

		case "tools/list":
			time.Sleep(50 * time.Millisecond) // Simulate some latency
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]interface{}{"tools": []map[string]string{}},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, _ := client.Probe(context.Background())

	if result.Latency < 50*time.Millisecond {
		t.Errorf("latency too low: %v, expected >= 50ms", result.Latency)
	}
}

// TestProbeEmptyToolsList verifies handling of empty tools list
func TestProbeEmptyToolsList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]interface{}{"protocolVersion": "2024-11-05"},
			}
			json.NewEncoder(w).Encode(resp)

		case "tools/list":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]interface{}{"tools": []map[string]string{}},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, _ := client.Probe(context.Background())

	if !result.Healthy {
		t.Error("expected healthy=true even with no tools")
	}
	if result.ToolCount != 0 {
		t.Errorf("expected 0 tools, got %d", result.ToolCount)
	}
}

// TestProberWrapper verifies the Prober struct integration
func TestProberWrapper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]interface{}{"protocolVersion": "2024-11-05"},
			}
			json.NewEncoder(w).Encode(resp)

		case "tools/list":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"tools": []map[string]string{
						{"name": "my-tool"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	prober := NewProber("test-prober", server.URL, 5*time.Second)
	result, err := prober.ProbeOnce(context.Background())

	if err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}
	if !result.Healthy {
		t.Error("expected healthy=true")
	}
	if result.ToolCount != 1 {
		t.Errorf("expected 1 tool, got %d", result.ToolCount)
	}
}

// TestProbeContextTimeout verifies that context timeout is respected
func TestProbeContextTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		if method == "tools/list" {
			time.Sleep(2 * time.Second) // Sleep longer than timeout
		}

		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]interface{}{},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	client := NewHTTPClient(server.URL, 3*time.Second)
	result, err := client.Probe(ctx)

	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false on context timeout")
	}
}

// TestProbeInvalidEndpoint verifies handling of invalid endpoint URLs
func TestProbeInvalidEndpoint(t *testing.T) {
	client := NewHTTPClient("http://invalid..endpoint", 1*time.Second)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe returned error, expected ProbeResult: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false for invalid endpoint")
	}
}

// TestProbeMultipleTools verifies correct handling of multiple tools
func TestProbeMultipleTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)

		w.Header().Set("Content-Type", "application/json")

		switch method {
		case "initialize":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]interface{}{"protocolVersion": "2024-11-05"},
			}
			json.NewEncoder(w).Encode(resp)

		case "tools/list":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"tools": []map[string]string{
						{"name": "tool-1"},
						{"name": "tool-2"},
						{"name": "tool-3"},
						{"name": "tool-4"},
						{"name": "tool-5"},
					},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, 5*time.Second)
	result, _ := client.Probe(context.Background())

	if result.ToolCount != 5 {
		t.Errorf("expected 5 tools, got %d", result.ToolCount)
	}
	if len(result.Tools) != 5 {
		t.Errorf("expected Tools array length 5, got %d", len(result.Tools))
	}
	for i := 1; i <= 5; i++ {
		expectedName := fmt.Sprintf("tool-%d", i)
		if result.Tools[i-1] != expectedName {
			t.Errorf("tool %d: expected %q, got %q", i, expectedName, result.Tools[i-1])
		}
	}
}

// TestProbeWithoutResponse verifies handling of servers that don't respond
func TestProbeWithoutResponse(t *testing.T) {
	// Create a listener but never accept connections
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	client := NewHTTPClient("http://"+addr, 500*time.Millisecond)
	result, err := client.Probe(context.Background())

	if err != nil {
		t.Fatalf("Probe returned error, expected ProbeResult: %v", err)
	}
	if result.Healthy {
		t.Error("expected healthy=false")
	}
}
