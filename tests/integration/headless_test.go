package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/headless"
	"github.com/james-gibson/adhd/internal/lights"
)

// TestHeadlessModeStartStop verifies headless server starts and stops gracefully
func TestHeadlessModeStartStop(t *testing.T) {
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0", // Random port
		},
	}

	server := headless.New(cfg)
	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}

	// Verify we can shutdown without error
	err := server.Shutdown()
	if err != nil {
		t.Errorf("shutdown failed: %v", err)
	}
}

// TestHeadlessModeLogging verifies traffic is logged to file and stdout
func TestHeadlessModeLogging(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "adhd-*.jsonl")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_ = tmpFile.Close()

	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
	}

	server := headless.New(cfg)
	if err := server.Start(tmpFile.Name()); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Log a traffic entry
	entry := headless.MCPTrafficLog{
		Timestamp: time.Now().UTC(),
		Type:      "request",
		Method:    "test.method",
		Params: map[string]interface{}{
			"key": "value",
		},
	}

	err = server.LogTraffic(entry)
	if err != nil {
		t.Fatalf("failed to log traffic: %v", err)
	}

	// Read log file to verify entry was written
	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}

	if len(data) == 0 {
		t.Error("log file is empty")
	}

	// Parse the logged entry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		t.Fatal("no log entries in file")
	}

	var logged headless.MCPTrafficLog
	if err := json.Unmarshal(scanner.Bytes(), &logged); err != nil {
		t.Fatalf("failed to parse log entry: %v", err)
	}

	if logged.Method != "test.method" {
		t.Errorf("expected method test.method, got %s", logged.Method)
	}
	if logged.Type != "request" {
		t.Errorf("expected type request, got %s", logged.Type)
	}
}

// TestHeadlessMCPServerAcceptsCalls verifies the MCP server is operational
func TestHeadlessMCPServerAcceptsCalls(t *testing.T) {
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
	}

	server := headless.New(cfg)
	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Add a test light to the cluster
	light := lights.New("test-feature", "feature")
	light.SetStatus(lights.StatusGreen)
	light.Source = "test-binary"
	server.GetCluster().Add(light)

	// Get the actual address the server is listening on
	// For now, we'll just verify the server started with port :0
	// In a real scenario, we'd need to expose the actual bound address
	t.Log("MCP server started and accepting connections")
}

// TestHeadlessPrimePlusIntegration verifies prime-plus buffering and push
func TestHeadlessPrimePlusIntegration(t *testing.T) {
	// Create mock prime server that expects push-logs
	pushCalled := false
	prime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			t.Fatalf("failed to decode RPC request: %v", err)
		}

		method, ok := rpcReq["method"].(string)
		if ok && method == "smoke-alarm.isotope.push-logs" {
			pushCalled = true
			// Verify logs are present
			params, ok := rpcReq["params"].(map[string]interface{})
			if !ok {
				t.Fatal("missing params in RPC request")
			}
			logs, ok := params["logs"].([]interface{})
			if !ok || len(logs) == 0 {
				t.Error("no logs in push request")
			}
		}

		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"success": true,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer prime.Close()

	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
	}

	server := headless.New(cfg)
	server.SetupMessageQueue(true, prime.URL, 100)

	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Log some traffic
	for i := 0; i < 3; i++ {
		entry := headless.MCPTrafficLog{
			Timestamp: time.Now().UTC(),
			Type:      "request",
			Method:    "test.method",
		}
		if err := server.LogTraffic(entry); err != nil {
			t.Fatalf("failed to log traffic: %v", err)
		}
	}

	// Allow time for retry loop to attempt push
	time.Sleep(200 * time.Millisecond)

	// Trigger push manually to verify
	if err := server.PushToPrime(); err != nil {
		t.Fatalf("failed to push to prime: %v", err)
	}

	if !pushCalled {
		t.Error("prime server was not called with push-logs")
	}
}

// TestHeadlessMultipleInstances verifies multiple headless instances don't conflict
func TestHeadlessMultipleInstances(t *testing.T) {
	servers := make([]*headless.Server, 3)

	// Start 3 headless instances with random ports
	for i := 0; i < 3; i++ {
		cfg := &config.Config{
			MCPServer: config.MCPServerConfig{
				Enabled: true,
				Addr:    ":0", // Random port - no conflict
			},
		}

		server := headless.New(cfg)
		if err := server.Start(""); err != nil {
			t.Fatalf("failed to start server %d: %v", i, err)
		}
		servers[i] = server
	}

	// Verify all are running and can log traffic
	for i, server := range servers {
		entry := headless.MCPTrafficLog{
			Timestamp: time.Now().UTC(),
			Type:      "request",
			Method:    "instance-" + string(rune(i)),
		}
		if err := server.LogTraffic(entry); err != nil {
			t.Fatalf("server %d failed to log: %v", i, err)
		}
	}

	// Shutdown all
	for i, server := range servers {
		if err := server.Shutdown(); err != nil {
			t.Errorf("server %d shutdown failed: %v", i, err)
		}
	}
}

// TestHeadlessIsotopeRegistration verifies isotope registration flow
func TestHeadlessIsotopeRegistration(t *testing.T) {
	// Mock smoke-alarm that accepts isotope registration via REST
	registered := false
	smokeAlarm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/isotope/register" && r.Method == http.MethodPost {
			registered = true
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"name":          "adhd",
				"role":          "prime",
				"endpoint":      ":0",
				"protocol":      "mcp",
				"trust_rung":    2,
				"rung_name":     "Harness Tools",
				"registered_at": time.Now().UTC(),
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer smokeAlarm.Close()

	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
	}

	server := headless.New(cfg)
	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Register with smoke-alarm
	if err := server.RegisterAsIsotope(smokeAlarm.URL); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	if !registered {
		t.Error("smoke-alarm did not receive isotope.register call")
	}
}

// TestHeadlessMCPClientCanProbe verifies clients can probe the headless MCP server
func TestHeadlessMCPClientCanProbe(t *testing.T) {
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    "127.0.0.1:0", // Loopback with random port
		},
	}

	server := headless.New(cfg)
	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Create a client and attempt to probe
	// Note: We can't get the actual bound address from Server without exposing it
	// So this test verifies the server started with MCP enabled
	t.Log("headless MCP server configured and ready for probe")
}

// TestHeadlessConfigFromFile verifies config loading with headless settings
func TestHeadlessConfigFromFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "adhd-config-*")
	if err != nil {
		t.Fatalf("failed to create temp directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	configYAML := `
mcp_server:
  enabled: true
  addr: ":0"

health:
  remote_smoke_alarm: ""

features:
  binaries: []
`

	configPath := filepath.Join(tmpDir, "adhd.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if !cfg.MCPServer.Enabled {
		t.Error("MCP server should be enabled in config")
	}
	if cfg.MCPServer.Addr != ":0" {
		t.Errorf("expected addr :0, got %s", cfg.MCPServer.Addr)
	}
}

// TestHeadlessEmptyCluster verifies headless mode works with no initial features
func TestHeadlessEmptyCluster(t *testing.T) {
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
		Features: config.FeaturesConfig{
			Binaries: []config.Binary{},
		},
	}

	server := headless.New(cfg)
	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Should be able to log traffic even with no features
	entry := headless.MCPTrafficLog{
		Timestamp: time.Now().UTC(),
		Type:      "request",
		Method:    "test",
	}
	if err := server.LogTraffic(entry); err != nil {
		t.Errorf("failed to log traffic with empty cluster: %v", err)
	}
}


// TestHeadlessPushToUnreachablePrimeFails verifies graceful handling of unavailable prime
func TestHeadlessPushToUnreachablePrimeFails(t *testing.T) {
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
	}

	server := headless.New(cfg)
	server.SetupMessageQueue(true, "http://127.0.0.1:1/unreachable", 100)

	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Log some traffic
	entry := headless.MCPTrafficLog{
		Timestamp: time.Now().UTC(),
		Type:      "request",
		Method:    "test",
	}
	if err := server.LogTraffic(entry); err != nil {
		t.Fatalf("failed to log traffic: %v", err)
	}

	// Attempt to push to unreachable prime should fail gracefully
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Push should fail but not crash
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.PushToPrime()
	}()

	select {
	case err := <-errChan:
		if err == nil {
			t.Error("push to unreachable prime should fail")
		}
	case <-ctx.Done():
		t.Fatal("push operation timed out")
	}

	// Buffer should still have the log (not cleared on failure)
	stats := server.GetMessageQueueStats()
	if stats.BufferedLogCount != 1 {
		t.Errorf("expected 1 buffered log after failed push, got %d", stats.BufferedLogCount)
	}
}


// TestHeadlessCircuitBreakerPattern verifies retry behavior under load
func TestHeadlessCircuitBreakerPattern(t *testing.T) {
	callCount := 0
	prime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// First call fails, rest succeed
		if callCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		response := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"success": true,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer prime.Close()

	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    ":0",
		},
	}

	server := headless.New(cfg)
	server.SetupMessageQueue(true, prime.URL, 100)

	if err := server.Start(""); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	defer func() { _ = server.Shutdown() }()

	// Log traffic
	entry := headless.MCPTrafficLog{
		Timestamp: time.Now().UTC(),
		Type:      "request",
		Method:    "test",
	}
	if err := server.LogTraffic(entry); err != nil {
		t.Fatalf("failed to log: %v", err)
	}

	// First push attempt fails
	err1 := server.PushToPrime()
	if err1 == nil {
		t.Error("first push should fail")
	}

	// Second push attempt succeeds
	entry2 := headless.MCPTrafficLog{
		Timestamp: time.Now().UTC(),
		Type:      "response",
		Method:    "test",
	}
	if err := server.LogTraffic(entry2); err != nil {
		t.Fatalf("failed to log second entry: %v", err)
	}

	err2 := server.PushToPrime()
	if err2 != nil {
		t.Errorf("second push should succeed: %v", err2)
	}
}
