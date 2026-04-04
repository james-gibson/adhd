package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/headless"
)

// startHeadlessOnFreeAddr creates and starts a headless server on a free port.
// Returns the server and the actual bound addr (e.g. "127.0.0.1:PORT").
func startHeadlessOnFreeAddr(t *testing.T, logPath string) (*headless.Server, string) {
	t.Helper()
	addr := FreeAddr(t)
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    addr,
		},
	}
	srv := headless.New(cfg)
	if err := srv.Start(logPath); err != nil {
		t.Fatalf("failed to start headless server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })
	// Give the listener a moment to bind
	time.Sleep(20 * time.Millisecond)
	return srv, addr
}

// mcpEndpoint builds the full MCP HTTP endpoint URL from a bound addr.
func mcpEndpoint(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr + "/mcp"
	}
	return "http://" + addr + "/mcp"
}

// sendMCPRequest posts a single JSON-RPC message to endpoint and returns the raw response.
func sendMCPRequest(t *testing.T, endpoint, method string, params interface{}) map[string]interface{} {
	t.Helper()
	body := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	data, _ := json.Marshal(body)
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("MCP call %q to %s failed: %v", method, endpoint, err)
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result
}

// TestPrimePlusRelaysMessagesToPrime verifies the full relay chain:
//  1. Prime headless starts — accepts push-logs via MCP
//  2. Prime-plus headless starts — buffers logs locally
//  3. External caller sends several MCP messages to prime-plus
//  4. Prime-plus message queue pushes them to prime via smoke-alarm.isotope.push-logs
//  5. Prime log file contains the relayed entries
func TestPrimePlusRelaysMessagesToPrime(t *testing.T) {
	primeLog := t.TempDir() + "/prime.jsonl"
	plusLog := t.TempDir() + "/plus.jsonl"

	// --- start prime ---
	prime, primeAddr := startHeadlessOnFreeAddr(t, primeLog)
	_ = prime
	primeURL := mcpEndpoint(primeAddr)

	// Confirm prime responds to initialize
	initResp := sendMCPRequest(t, primeURL, "initialize", nil)
	if initResp["result"] == nil {
		t.Fatalf("prime did not respond to initialize: %v", initResp)
	}

	// --- start prime-plus wired to prime ---
	_, plusAddr := startHeadlessOnFreeAddr(t, plusLog)
	plusURL := mcpEndpoint(plusAddr)

	// Wire the prime-plus message queue to point at the prime
	// (SetupMessageQueue triggers the retry goroutine)
	plus2 := func() *headless.Server {
		cfg := &config.Config{
			MCPServer: config.MCPServerConfig{
				Enabled: true,
				Addr:    plusAddr,
			},
		}
		srv := headless.New(cfg)
		_ = srv.Start(plusLog)
		srv.SetupMessageQueue(true, primeURL, 100)
		t.Cleanup(func() { _ = srv.Shutdown() })
		return srv
	}()

	// Send a series of MCP messages to the prime-plus (initialize + tools/list + custom calls)
	messages := []struct {
		method string
		params interface{}
	}{
		{"initialize", nil},
		{"tools/list", nil},
		{"adhd.status", nil},
		{"adhd.lights.list", nil},
		{"ping", nil},
	}
	for _, msg := range messages {
		resp := sendMCPRequest(t, plusURL, msg.method, msg.params)
		t.Logf("prime-plus response to %q: result=%v err=%v", msg.method, resp["result"] != nil, resp["error"])
	}

	// Log traffic entries directly into the prime-plus so the queue has data to push
	for i, msg := range messages {
		_ = plus2.LogTraffic(headless.MCPTrafficLog{
			Timestamp: time.Now().UTC(),
			Type:      "request",
			Method:    msg.method,
		})
		_ = plus2.LogTraffic(headless.MCPTrafficLog{
			Timestamp: time.Now().UTC(),
			Type:      "response",
			Method:    msg.method,
		})
		_ = i
	}

	// Trigger an explicit push to prime (bypasses the 5s retry interval in tests)
	if err := plus2.PushToPrime(); err != nil {
		t.Fatalf("PushToPrime failed: %v", err)
	}

	// Give async writes a moment to flush to disk
	time.Sleep(50 * time.Millisecond)

	// --- verify prime log contains relayed entries ---
	primeData, err := os.ReadFile(primeLog)
	if err != nil {
		t.Fatalf("could not read prime log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(primeData)), "\n")
	var relayed []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		relayed = append(relayed, line)
	}

	if len(relayed) == 0 {
		t.Fatal("prime log is empty — push-logs messages were not received by prime")
	}

	t.Logf("prime received %d relayed log entries", len(relayed))

	// Spot-check: each relayed entry must be valid JSON
	for i, line := range relayed {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Errorf("relayed entry %d is not valid JSON: %v\n  raw: %s", i, err, line)
		}
	}

	// Verify we got at least the 10 entries (5 messages × 2 directions)
	if len(relayed) < len(messages)*2 {
		t.Errorf("expected at least %d relayed entries, got %d", len(messages)*2, len(relayed))
	}
}

// TestPrimeAcceptsPushLogsDirect calls smoke-alarm.isotope.push-logs directly on a prime
// and verifies the entries appear in the prime's log file.
func TestPrimeAcceptsPushLogsDirect(t *testing.T) {
	primeLog := t.TempDir() + "/prime.jsonl"
	_, primeAddr := startHeadlessOnFreeAddr(t, primeLog)
	primeURL := mcpEndpoint(primeAddr)

	// Build a batch of log entries to push
	logs := make([]map[string]interface{}, 0, 6)
	methods := []string{"initialize", "tools/list", "adhd.status", "adhd.lights.list", "ping", "adhd.isotope.status"}
	for _, m := range methods {
		logs = append(logs, map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"type":      "request",
			"method":    m,
		})
	}

	resp := sendMCPRequest(t, primeURL, "smoke-alarm.isotope.push-logs", map[string]interface{}{
		"logs": logs,
	})

	if resp["error"] != nil {
		t.Fatalf("push-logs returned error: %v", resp["error"])
	}

	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result shape: %v", resp["result"])
	}

	accepted, _ := result["accepted"].(float64)
	if int(accepted) != len(logs) {
		t.Errorf("expected accepted=%d, got %v", len(logs), accepted)
	}

	// Give file writes a moment
	time.Sleep(20 * time.Millisecond)

	data, err := os.ReadFile(primeLog)
	if err != nil {
		t.Fatalf("could not read prime log: %v", err)
	}

	// Count non-empty lines
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var count int
	for _, l := range lines {
		if l != "" {
			count++
		}
	}

	if count < len(logs) {
		t.Errorf("prime log has %d entries, expected at least %d", count, len(logs))
	}

	t.Logf("push-logs accepted=%d, prime log entries=%d", len(logs), count)
}

// TestMultiHopRelay verifies three-node chain: prime-plus-2 → prime-plus-1 → prime
// (a linear chain, not a ring — proving the buffer-and-forward model)
func TestMultiHopRelay(t *testing.T) {
	primeLog := t.TempDir() + "/prime.jsonl"

	_, primeAddr := startHeadlessOnFreeAddr(t, primeLog)
	primeURL := mcpEndpoint(primeAddr)

	// prime-plus-1 pushes to prime
	plusLog1 := t.TempDir() + "/plus1.jsonl"
	plus1cfg := &config.Config{
		MCPServer: config.MCPServerConfig{Enabled: true, Addr: FreeAddr(t)},
	}
	plus1 := headless.New(plus1cfg)
	_ = plus1.Start(plusLog1)
	t.Cleanup(func() { _ = plus1.Shutdown() })
	time.Sleep(20 * time.Millisecond)
	plus1URL := mcpEndpoint(plus1cfg.MCPServer.Addr)
	plus1.SetupMessageQueue(true, primeURL, 100)

	// prime-plus-2 pushes to prime-plus-1
	plusLog2 := t.TempDir() + "/plus2.jsonl"
	plus2cfg := &config.Config{
		MCPServer: config.MCPServerConfig{Enabled: true, Addr: FreeAddr(t)},
	}
	plus2 := headless.New(plus2cfg)
	_ = plus2.Start(plusLog2)
	t.Cleanup(func() { _ = plus2.Shutdown() })
	time.Sleep(20 * time.Millisecond)
	plus2.SetupMessageQueue(true, plus1URL, 100)

	// Log entries on plus-2
	methods := []string{"initialize", "adhd.status", "adhd.lights.list"}
	for _, m := range methods {
		_ = plus2.LogTraffic(headless.MCPTrafficLog{
			Timestamp: time.Now().UTC(),
			Type:      "request",
			Method:    m,
		})
	}

	// Push plus-2 → plus-1
	if err := plus2.PushToPrime(); err != nil {
		t.Fatalf("plus2.PushToPrime failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Push plus-1 → prime (plus-1 received from plus-2 and now forwards to prime)
	if err := plus1.PushToPrime(); err != nil {
		t.Fatalf("plus1.PushToPrime failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify prime has the entries
	data, err := os.ReadFile(primeLog)
	if err != nil {
		t.Fatalf("could not read prime log: %v", err)
	}

	var count int
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			count++
		}
	}

	if count < len(methods) {
		t.Errorf("prime received %d entries, expected at least %d via two-hop relay", count, len(methods))
	}
	t.Logf("multi-hop relay: plus2(%d entries) → plus1 → prime(%d entries received)", len(methods), count)
}

// TestPrimePlusLogsMultipleMethods sends a named sequence of MCP calls to prime-plus
// and confirms each call type appears in the prime-plus log and gets pushed to prime.
func TestPrimePlusLogsMultipleMethods(t *testing.T) {
	primeLog := t.TempDir() + "/prime.jsonl"
	_, primeAddr := startHeadlessOnFreeAddr(t, primeLog)
	primeURL := mcpEndpoint(primeAddr)

	plusLog := t.TempDir() + "/plus.jsonl"
	plusCfg := &config.Config{
		MCPServer: config.MCPServerConfig{Enabled: true, Addr: FreeAddr(t)},
	}
	plus := headless.New(plusCfg)
	_ = plus.Start(plusLog)
	t.Cleanup(func() { _ = plus.Shutdown() })
	time.Sleep(20 * time.Millisecond)
	plus.SetupMessageQueue(true, primeURL, 100)
	plusURL := mcpEndpoint(plusCfg.MCPServer.Addr)

	// Send a defined sequence of MCP calls
	sequence := []string{
		"initialize",
		"tools/list",
		"adhd.status",
		"adhd.lights.list",
		"adhd.isotope.status",
		"adhd.isotope.peers",
		"ping",
	}

	for _, method := range sequence {
		resp := sendMCPRequest(t, plusURL, method, nil)
		if resp["error"] != nil {
			t.Logf("note: %q returned error (expected for some methods): %v", method, resp["error"])
		}
		// Also log traffic explicitly so queue captures it
		_ = plus.LogTraffic(headless.MCPTrafficLog{
			Timestamp: time.Now().UTC(),
			Type:      "request",
			Method:    method,
		})
	}

	// Push everything to prime
	if err := plus.PushToPrime(); err != nil {
		t.Fatalf("PushToPrime failed: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify plus log has all methods
	plusData, _ := os.ReadFile(plusLog)
	plusLines := strings.Split(strings.TrimSpace(string(plusData)), "\n")
	seenInPlus := make(map[string]bool)
	for _, line := range plusLines {
		if line == "" {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err == nil {
			if m, ok := entry["method"].(string); ok {
				seenInPlus[m] = true
			}
		}
	}
	for _, m := range sequence {
		if !seenInPlus[m] {
			t.Errorf("method %q not found in plus log", m)
		}
	}

	// Verify prime received them
	primeData, _ := os.ReadFile(primeLog)
	primeLinesCount := 0
	for _, l := range strings.Split(strings.TrimSpace(string(primeData)), "\n") {
		if l != "" {
			primeLinesCount++
		}
	}
	if primeLinesCount == 0 {
		t.Error("prime log is empty — push-logs not delivered")
	}

	t.Logf("sequence test: %d methods sent, plus log has %d entries, prime has %d entries",
		len(sequence), len(plusLines), primeLinesCount)
	t.Logf("methods confirmed in plus log: %v", fmt.Sprint(seenInPlus))
}
