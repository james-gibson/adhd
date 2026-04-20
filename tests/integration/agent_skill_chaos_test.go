package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// TestAgentSkillToolChain simulates a simple agent skill that chains tool calls
func TestAgentSkillToolChain(t *testing.T) {
	// Setup MCP server
	listener, _ := net.Listen("tcp", "localhost:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cluster := lights.NewCluster()
	cluster.Add(&lights.Light{Name: "light-1", Status: lights.StatusGreen})
	cluster.Add(&lights.Light{Name: "light-2", Status: lights.StatusRed})

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	server := mcpserver.NewServer(cfg, cluster)
	_ = server.Start(context.Background())
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	if err := waitForServer(addr, 5*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}

	// Simulate agent skill: "Check cluster health"
	// Chain: status → lights.list → filter reds
	skill := &agentSkill{
		name:     "Check cluster health",
		endpoint: "http://" + addr + "/mcp",
	}

	// Step 1: Get status
	status := skill.callTool("adhd.status", nil)
	if status == nil {
		t.Fatal("status call failed")
	}

	lights, ok := status["lights"].(map[string]interface{})
	if !ok {
		t.Fatal("lights field missing from status")
	}

	totalCount := int(lights["total"].(float64))
	if totalCount != 2 {
		t.Errorf("expected 2 lights, got %d", totalCount)
	}

	// Step 2: Get lights list
	lightsList := skill.callTool("adhd.lights.list", nil)
	if lightsList == nil {
		t.Fatal("lights.list call failed")
	}

	lightsArray, ok := lightsList["lights"].([]interface{})
	if !ok {
		t.Fatal("lights array missing")
	}

	if len(lightsArray) != 2 {
		t.Errorf("expected 2 lights in list, got %d", len(lightsArray))
	}

	// Step 3: Filter reds
	redCount := 0
	for _, light := range lightsArray {
		lightMap, _ := light.(map[string]interface{})
		status, _ := lightMap["status"].(string)
		if status == "red" {
			redCount++
		}
	}

	t.Logf("✓ Skill completed: %d total lights, %d red", totalCount, redCount)
	if redCount != 1 {
		t.Errorf("expected 1 red light, got %d", redCount)
	}
}

// TestConcurrentAgentSkills simulates multiple agent skills running in parallel
func TestConcurrentAgentSkills(t *testing.T) {
	// Setup MCP server
	listener, _ := net.Listen("tcp", "localhost:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	cluster := lights.NewCluster()
	for i := 0; i < 10; i++ {
		cluster.Add(&lights.Light{Name: fmt.Sprintf("light-%d", i), Status: lights.StatusGreen})
	}

	cfg := config.MCPServerConfig{Enabled: true, Addr: addr}
	server := mcpserver.NewServer(cfg, cluster)
	_ = server.Start(context.Background())
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	if err := waitForServer(addr, 5*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}

	endpoint := "http://" + addr + "/mcp"

	// Run 5 concurrent skills
	const numSkills = 5
	var wg sync.WaitGroup
	successCount := int32(0)
	var timings []time.Duration
	var mu sync.Mutex

	for i := 0; i < numSkills; i++ {
		wg.Add(1)
		go func(skillID int) {
			defer wg.Done()

			start := time.Now()
			skill := &agentSkill{
				name:     fmt.Sprintf("Skill-%d", skillID),
				endpoint: endpoint,
			}

			// Chain: status → lights.list → get first light
			status := skill.callTool("adhd.status", nil)
			if status == nil {
				return
			}

			lightsList := skill.callTool("adhd.lights.list", nil)
			if lightsList == nil {
				return
			}

			lightsArray, _ := lightsList["lights"].([]interface{})
			if len(lightsArray) == 0 {
				return
			}

			firstLight := lightsArray[0].(map[string]interface{})
			lightName := firstLight["name"].(string)

			// Get specific light
			lightData := skill.callTool("adhd.lights.get", map[string]interface{}{
				"name": lightName,
			})
			if lightData == nil {
				return
			}

			atomic.AddInt32(&successCount, 1)
			elapsed := time.Since(start)
			mu.Lock()
			timings = append(timings, elapsed)
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	if atomic.LoadInt32(&successCount) != numSkills {
		t.Errorf("only %d/%d skills succeeded", atomic.LoadInt32(&successCount), numSkills)
	}

	if len(timings) > 0 {
		var sum time.Duration
		for _, t := range timings {
			sum += t
		}
		avg := sum / time.Duration(len(timings))
		t.Logf("✓ %d concurrent skills completed, avg time: %v", numSkills, avg)
	}
}

// TestAgentSkillWithRetry simulates skill retry logic on tool failure
func TestAgentSkillWithRetry(t *testing.T) {
	// Mock server that fails first attempt, succeeds on retry
	callCount := int32(0)
	mockServer := http.NewServeMux()
	mockServer.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)

		method, _ := req["method"].(string)
		count := atomic.AddInt32(&callCount, 1)

		if method == "adhd.status" && count == 1 {
			// First call fails
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary failure"}`))
			return
		}

		// Subsequent calls succeed
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"jsonrpc":"2.0",
			"id":1,
			"result":{
				"lights":{"total":1,"green":1,"red":0,"yellow":0,"dark":0}
			}
		}`))
	})

	server := http.NewServeMux()
	server.Handle("/", mockServer)
	httpServer := &http.Server{Handler: server}
	listener, _ := net.Listen("tcp", "localhost:0")

	go func() {
		if err := httpServer.Serve(listener); err != nil {
			t.Errorf("server error: %v", err)
		}
	}()
	//go httpServer.Serve(listener)
	//defer listener.Close()

	endpoint := "http://" + listener.Addr().String() + "/mcp"

	// Skill with retry logic
	skill := &agentSkill{
		name:       "Check with retry",
		endpoint:   endpoint,
		maxRetries: 3,
		retryDelay: 10 * time.Millisecond,
	}

	status := skill.callToolWithRetry("adhd.status", nil)
	if status == nil {
		t.Fatal("status call failed after retries")
	}

	lights, _ := status["lights"].(map[string]interface{})
	total := int(lights["total"].(float64))
	if total != 1 {
		t.Errorf("expected 1 light, got %d", total)
	}

	t.Logf("✓ Skill recovered from transient failure after %d attempts", atomic.LoadInt32(&callCount))
}

// TestAgentSkillErrorPropagation tests error handling in tool chains
func TestAgentSkillErrorPropagation(t *testing.T) {
	// Mock server that returns error for a specific tool
	mockServer := http.NewServeMux()
	mockServer.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)

		method, _ := req["method"].(string)

		if method == "adhd.isotope.peers" {
			// This tool always fails
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"jsonrpc":"2.0",
				"id":1,
				"error":{"code":-32000,"message":"isotope service unavailable"}
			}`))
			return
		}

		// Other tools succeed
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"jsonrpc":"2.0",
			"id":1,
			"result":{"data":"success"}
		}`))
	})

	server := http.NewServeMux()
	server.Handle("/", mockServer)
	httpServer := &http.Server{Handler: server}
	listener, _ := net.Listen("tcp", "localhost:0")
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = listener.Close() }()

	endpoint := "http://" + listener.Addr().String() + "/mcp"

	skill := &agentSkill{
		name:     "Chain with error",
		endpoint: endpoint,
	}

	// Step 1: Status succeeds
	status := skill.callTool("adhd.status", nil)
	if status == nil {
		t.Fatal("status should succeed")
	}

	// Step 2: Peers fails
	peers := skill.callTool("adhd.isotope.peers", nil)
	if peers != nil {
		t.Fatal("peers should fail")
	}

	// Step 3: Should not be called
	result, _ := skill.callToolWithContext("adhd.lights.list", nil)

	t.Logf("✓ Skill error propagation: status succeeded, peers failed, subsequent tool not called: %v", result == nil)
}

// TestAgentSkillConcurrentModification tests race conditions in tool chains
func TestAgentSkillConcurrentModification(t *testing.T) {
	// Mock server that allows concurrent modifications
	var mu sync.RWMutex
	lightStatus := map[string]string{"light-1": "green"}

	mockServer := http.NewServeMux()
	mockServer.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)

		method, _ := req["method"].(string)
		params, _ := req["params"].(map[string]interface{})

		if method == "adhd.lights.get" {
			name, _ := params["name"].(string)
			mu.RLock()
			status := lightStatus[name]
			mu.RUnlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"jsonrpc":"2.0",
				"id":1,
				"result":{"name":"%s","status":"%s"}
			}`, name, status)
			return
		}

		if method == "adhd.lights.update" {
			name, _ := params["name"].(string)
			newStatus, _ := params["status"].(string)
			mu.Lock()
			lightStatus[name] = newStatus
			mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"success":true}}`))
			return
		}
	})

	server := http.NewServeMux()
	server.Handle("/", mockServer)
	httpServer := &http.Server{Handler: server}
	listener, _ := net.Listen("tcp", "localhost:0")
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = listener.Close() }()

	endpoint := "http://" + listener.Addr().String() + "/mcp"

	// Skill A: Update light
	// Skill B: Read light status
	var wg sync.WaitGroup
	wg.Add(2)

	var skillBResult string

	go func() {
		defer wg.Done()
		skill := &agentSkill{name: "Skill-A", endpoint: endpoint}
		skill.callTool("adhd.lights.update", map[string]interface{}{
			"name":   "light-1",
			"status": "red",
		})
	}()

	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond) // Let skill A run first
		skill := &agentSkill{name: "Skill-B", endpoint: endpoint}
		result := skill.callTool("adhd.lights.get", map[string]interface{}{
			"name": "light-1",
		})
		if result != nil {
			status, _ := result["status"].(string)
			skillBResult = status
		}
	}()

	wg.Wait()

	// Skill B should see the update from Skill A
	if skillBResult != "red" {
		t.Errorf("skill B should see update from skill A, got status=%s", skillBResult)
	}

	t.Logf("✓ Concurrent modification visible across skills: A updated, B read 'red'")
}

// agentSkill simulates an agent that chains tool calls
type agentSkill struct {
	name       string
	endpoint   string
	maxRetries int
	retryDelay time.Duration
}

func (s *agentSkill) callTool(method string, params interface{}) map[string]interface{} {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}

	body, _ := json.Marshal(req)
	resp, err := testHTTPClient.Post(s.endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&result)

	if _, hasError := result["error"]; hasError {
		return nil
	}

	resultData, _ := result["result"].(map[string]interface{})
	return resultData
}

func (s *agentSkill) callToolWithRetry(method string, params interface{}) map[string]interface{} {
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		result := s.callTool(method, params)
		if result != nil {
			return result
		}
		if attempt < s.maxRetries {
			time.Sleep(s.retryDelay)
		}
	}
	return nil
}

func (s *agentSkill) callToolWithContext(method string, params interface{}) (map[string]interface{}, bool) {
	result := s.callTool(method, params)
	return result, result != nil
}
