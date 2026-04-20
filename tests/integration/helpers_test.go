package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/smokelink"
)

var testHTTPClient = &http.Client{Timeout: 5 * time.Second}

func waitForServer(addr string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("server at %s not ready after %v", addr, maxWait)
}

// mockStatusResponse mimics ocd-smoke-alarm /status endpoint response
type mockStatusResponse struct {
	Service string             `json:"service"`
	Live    bool               `json:"live"`
	Ready   bool               `json:"ready"`
	Targets []mockTargetStatus `json:"targets"`
	Summary mockStatusSummary  `json:"summary"`
}

type mockTargetStatus struct {
	ID         string `json:"id"`
	Protocol   string `json:"protocol,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	State      string `json:"state"`
	Severity   string `json:"severity,omitempty"`
	Message    string `json:"message,omitempty"`
	Regression bool   `json:"regression"`
	CheckedAt  string `json:"checked_at"`
	LatencyMs  int    `json:"latency_ms"`
}

type mockStatusSummary struct {
	Total      int `json:"total"`
	Healthy    int `json:"healthy"`
	Degraded   int `json:"degraded"`
	Unhealthy  int `json:"unhealthy"`
	Outage     int `json:"outage"`
	Regression int `json:"regression"`
	Unknown    int `json:"unknown"`
}

// MockSmokeAlarmServer creates a mock ocd-smoke-alarm /status endpoint
// Returns the server URL and a function to update the targets
func MockSmokeAlarmServer(t *testing.T, initialTargets []mockTargetStatus) (
	*httptest.Server,
	func(targets []mockTargetStatus),
) {
	var mu sync.RWMutex
	currentTargets := initialTargets

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			mu.RLock()
			targets := make([]mockTargetStatus, len(currentTargets))
			copy(targets, currentTargets)
			mu.RUnlock()

			summary := mockStatusSummary{Total: len(targets)}
			for _, t := range targets {
				summary.Total++
				switch t.State {
				case "healthy":
					summary.Healthy++
				case "degraded":
					summary.Degraded++
				case "unhealthy":
					summary.Unhealthy++
				case "outage":
					summary.Outage++
				case "regression":
					summary.Regression++
				default:
					summary.Unknown++
				}
			}

			resp := mockStatusResponse{
				Service: "smoke-alarm-mock",
				Live:    true,
				Ready:   true,
				Targets: targets,
				Summary: summary,
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			http.NotFound(w, r)
		}
	}))

	t.Cleanup(server.Close)

	return server, func(targets []mockTargetStatus) {
		mu.Lock()
		currentTargets = targets
		mu.Unlock()
	}
}

// FreeAddr returns a free TCP address (port 0)
func FreeAddr(t *testing.T) string {
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// WaitForLightUpdate waits for a LightUpdate from a channel with timeout
func WaitForLightUpdate(t *testing.T, ch chan smokelink.LightUpdate, timeout time.Duration) smokelink.LightUpdate {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case update := <-ch:
		return update
	case <-ctx.Done():
		t.Fatalf("timeout waiting for light update after %v", timeout)
		return smokelink.LightUpdate{}
	}
}

// WaitForLightWithStatus waits for a specific light to appear with a given status
func WaitForLightWithStatus(t *testing.T, ch chan smokelink.LightUpdate, name string, want lights.Status, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for light %q with status %v", name, want)
		}

		select {
		case update := <-ch:
			if update.TargetID == name && update.Status == want {
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// MockMCPServer creates a mock MCP endpoint that responds to initialize + tools/list
func MockMCPServer(t *testing.T) *httptest.Server {
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
						"name":    "mock-mcp",
						"version": "0.1.0",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "tools/list":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]interface{}{
					"tools": []map[string]interface{}{
						{"name": "tool-a", "description": "Mock tool A"},
						{"name": "tool-b", "description": "Mock tool B"},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"error": map[string]interface{}{
					"code":    -32601,
					"message": fmt.Sprintf("method not found: %s", method),
				},
			}
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))

	t.Cleanup(server.Close)
	return server
}

// DoJSONRPCCall sends a JSON-RPC request to an endpoint and returns the response
func DoJSONRPCCall(t *testing.T, endpoint string, method string, params interface{}) map[string]interface{} {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var resp *http.Response
	for attempt := 0; attempt < 5; attempt++ {
		resp, err = testHTTPClient.Post(endpoint, "application/json", bytes.NewReader(bodyBytes))
		if err == nil {
			break
		}
		if attempt < 4 {
			time.Sleep(50 * time.Millisecond)
		}
	}
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
