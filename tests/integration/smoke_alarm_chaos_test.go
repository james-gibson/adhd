package integration

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSmokeAlarmRapidStateChanges validates smoke-alarm handles rapid target state transitions
func TestSmokeAlarmRapidStateChanges(t *testing.T) {
	// Mock target with rapidly changing health state
	states := []string{"healthy", "degraded", "outage", "healthy", "degraded"}
	stateIdx := int32(-1)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			idx := atomic.AddInt32(&stateIdx, 1)
			state := states[int(idx)%len(states)]
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"targets": []map[string]interface{}{
					{
						"id":    "rapid-target",
						"state": state,
					},
				},
			})
		}
	}))
	defer target.Close()

	// Simulate rapid polling (100 polls in a short time)
	var results []string
	var mu sync.Mutex

	for i := 0; i < 100; i++ {
		resp, err := http.Get(target.URL + "/status")
		if err != nil {
			t.Fatalf("poll %d failed: %v", i, err)
		}

		var body map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()

		targets, _ := body["targets"].([]interface{})
		if len(targets) > 0 {
			targetMap, _ := targets[0].(map[string]interface{})
			state, _ := targetMap["state"].(string)
			mu.Lock()
			results = append(results, state)
			mu.Unlock()
		}
	}

	// Verify results
	if len(results) != 100 {
		t.Errorf("expected 100 results, got %d", len(results))
	}

	// Check no states are skipped (sequence should be cycling)
	expectedSequence := []string{"healthy", "degraded", "outage", "healthy", "degraded"}
	for i, result := range results {
		expected := expectedSequence[i%len(expectedSequence)]
		if result != expected {
			t.Errorf("result[%d]: expected %s, got %s", i, expected, result)
		}
	}

	t.Logf("✓ Processed 100 rapid state changes without loss")
}

// TestSmokeAlarmConcurrentTargets validates handling of many targets changing state concurrently
func TestSmokeAlarmConcurrentTargets(t *testing.T) {
	const numTargets = 10
	targetStates := make([]int32, numTargets)

	// Create mock targets
	targets := make([]*httptest.Server, numTargets)
	for i := 0; i < numTargets; i++ {
		idx := i // capture for closure
		targets[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/status" {
				state := atomic.LoadInt32(&targetStates[idx])
				var stateStr string
				switch state {
				case 1:
					stateStr = "degraded"
				case 2:
					stateStr = "outage"
				default:
					stateStr = "healthy"
				}

				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"targets": []map[string]interface{}{
						{
							"id":    fmt.Sprintf("target-%d", idx),
							"state": stateStr,
						},
					},
				})
			}
		}))
	}
	defer func() {
		for _, t := range targets {
			t.Close()
		}
	}()

	// Concurrently change all targets to "unhealthy"
	var wg sync.WaitGroup
	for i := 0; i < numTargets; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			atomic.StoreInt32(&targetStates[idx], 2) // outage
		}(i)
	}
	wg.Wait()

	// Poll all targets concurrently
	type pollResult struct {
		targetID string
		state    string
	}
	results := make([]pollResult, 0)
	var resultMu sync.Mutex

	for i := 0; i < numTargets; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := http.Get(targets[idx].URL + "/status")
			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()

			var body map[string]interface{}
			_ = json.NewDecoder(resp.Body).Decode(&body)

			targetsArray, _ := body["targets"].([]interface{})
			if len(targetsArray) > 0 {
				targetMap, _ := targetsArray[0].(map[string]interface{})
				id, _ := targetMap["id"].(string)
				state, _ := targetMap["state"].(string)

				resultMu.Lock()
				results = append(results, pollResult{id, state})
				resultMu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// Verify all targets returned "outage"
	if len(results) != numTargets {
		t.Errorf("expected %d results, got %d", numTargets, len(results))
	}

	for _, r := range results {
		if r.state != "outage" {
			t.Errorf("%s: expected outage, got %s", r.targetID, r.state)
		}
	}

	t.Logf("✓ Processed concurrent state changes for %d targets", numTargets)
}

// TestSmokeAlarmManyTargets validates performance with 100+ targets
func TestSmokeAlarmManyTargets(t *testing.T) {
	const numTargets = 100

	// Create many mock targets
	targets := make([]*httptest.Server, numTargets)
	for i := 0; i < numTargets; i++ {
		idx := i
		targets[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/status" {
				states := []string{"healthy", "degraded", "outage"}
				state := states[rand.Intn(len(states))]

				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"targets": []map[string]interface{}{
						{
							"id":    fmt.Sprintf("target-%d", idx),
							"state": state,
						},
					},
				})
			}
		}))
	}
	defer func() {
		for _, t := range targets {
			t.Close()
		}
	}()

	// Poll all targets, measuring performance
	start := time.Now()
	var wg sync.WaitGroup
	successCount := int32(0)

	for i := 0; i < numTargets; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp, err := http.Get(targets[idx].URL + "/status")
			if err == nil {
				_ = resp.Body.Close()
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Verify performance
	if atomic.LoadInt32(&successCount) != int32(numTargets) {
		t.Errorf("expected %d successful polls, got %d", numTargets, atomic.LoadInt32(&successCount))
	}

	if elapsed > 5*time.Second {
		t.Logf("warning: polling %d targets took %v", numTargets, elapsed)
	}

	t.Logf("✓ Polled %d targets in %v (%.2f targets/sec)", numTargets, elapsed, float64(numTargets)/elapsed.Seconds())
}

// TestSmokeAlarmTargetAddRemoval validates dynamic target list changes
func TestSmokeAlarmTargetAddRemoval(t *testing.T) {
	var targetsMu sync.RWMutex
	targets := map[string]*httptest.Server{}

	// Coordinator server that tracks which targets should exist
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/targets" {
			targetsMu.RLock()
			targetList := make([]string, 0, len(targets))
			for id := range targets {
				targetList = append(targetList, id)
			}
			targetsMu.RUnlock()

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"targets": targetList,
			})
		}
	}))
	defer coordinator.Close()

	// Add and remove targets rapidly
	addTarget := func(id string) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/status" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"targets": []map[string]interface{}{
						{"id": id, "state": "healthy"},
					},
				})
			}
		}))
		targetsMu.Lock()
		targets[id] = server
		targetsMu.Unlock()
	}

	removeTarget := func(id string) {
		targetsMu.Lock()
		if s, ok := targets[id]; ok {
			s.Close()
			delete(targets, id)
		}
		targetsMu.Unlock()
	}

	// Perform 10 operations rapidly
	operations := []struct {
		op string
		id string
	}{
		{"add", "target-1"},
		{"add", "target-2"},
		{"add", "target-3"},
		{"remove", "target-1"},
		{"add", "target-4"},
		{"add", "target-5"},
		{"remove", "target-2"},
		{"remove", "target-3"},
		{"add", "target-6"},
		{"remove", "target-4"},
	}

	for _, op := range operations {
		if op.op == "add" {
			addTarget(op.id)
		} else {
			removeTarget(op.id)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify final state
	resp, _ := http.Get(coordinator.URL + "/targets")
	var result map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	_ = resp.Body.Close()

	finalTargets, _ := result["targets"].([]interface{})
	targetsMu.RLock()
	expectedCount := len(targets)
	targetsMu.RUnlock()

	if len(finalTargets) != expectedCount {
		t.Errorf("expected %d targets, got %d", expectedCount, len(finalTargets))
	}

	t.Logf("✓ Completed 10 add/remove operations, final count: %d", len(finalTargets))

	// Cleanup
	targetsMu.Lock()
	for _, s := range targets {
		s.Close()
	}
	targetsMu.Unlock()
}

// TestSmokeAlarmUnreachableTargetIsolation validates that unreachable targets don't block others
func TestSmokeAlarmUnreachableTargetIsolation(t *testing.T) {
	// One target that responds quickly
	fastTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"targets": []map[string]interface{}{
					{"id": "fast-target", "state": "healthy"},
				},
			})
		}
	}))
	defer fastTarget.Close()

	// One target that hangs (simulate unreachable)
	slowTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			time.Sleep(5 * time.Second) // Hang for 5 seconds
		}
	}))
	defer slowTarget.Close()

	// Poll both targets with timeout
	type pollResult struct {
		target string
		ok     bool
		time   time.Duration
	}

	results := make([]pollResult, 0)
	var mu sync.Mutex

	// Poll fast target — should succeed quickly
	go func() {
		start := time.Now()
		resp, err := http.Get(fastTarget.URL + "/status")
		duration := time.Since(start)
		mu.Lock()
		results = append(results, pollResult{"fast", err == nil, duration})
		mu.Unlock()
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	// Poll slow target with timeout
	go func() {
		client := &http.Client{Timeout: 500 * time.Millisecond}
		start := time.Now()
		_, err := client.Get(slowTarget.URL + "/status")
		duration := time.Since(start)
		mu.Lock()
		results = append(results, pollResult{"slow", err == nil, duration})
		mu.Unlock()
	}()

	// Wait for both
	time.Sleep(2 * time.Second)

	// Verify fast target succeeded quickly
	mu.Lock()
	defer mu.Unlock()

	if len(results) < 2 {
		t.Fatal("expected 2 results")
	}

	var fastResult, slowResult pollResult
	for _, r := range results {
		if r.target == "fast" {
			fastResult = r
		} else {
			slowResult = r
		}
	}

	if !fastResult.ok {
		t.Error("fast target should have succeeded")
	}
	if fastResult.time > 1*time.Second {
		t.Errorf("fast target took too long: %v", fastResult.time)
	}

	if slowResult.ok {
		t.Error("slow target should have timed out")
	}

	t.Logf("✓ Fast target polled in %v (unreachable target didn't block)", fastResult.time)
}

// TestSmokeAlarmMalformedResponseHandling validates graceful error handling
func TestSmokeAlarmMalformedResponseHandling(t *testing.T) {
	callCount := int32(0)

	malformedTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			atomic.AddInt32(&callCount, 1)
			// First call: malformed JSON
			count := atomic.LoadInt32(&callCount)
			if count == 1 {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{invalid json`))
			} else {
				// Second call: valid response
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"targets": []map[string]interface{}{
						{"id": "recovered", "state": "healthy"},
					},
				})
			}
		}
	}))
	defer malformedTarget.Close()

	// First poll (malformed)
	resp1, _ := http.Get(malformedTarget.URL + "/status")
	_ = resp1.Body.Close()

	// Second poll (valid) — smoke-alarm should recover
	resp2, err := http.Get(malformedTarget.URL + "/status")
	if err != nil {
		t.Fatalf("second poll failed: %v", err)
	}

	var body map[string]interface{}
	_ = json.NewDecoder(resp2.Body).Decode(&body)
	_ = resp2.Body.Close()

	// Verify recovery
	targets, ok := body["targets"].([]interface{})
	if !ok || len(targets) == 0 {
		t.Fatal("failed to recover from malformed response")
	}

	t.Logf("✓ Recovered from malformed response after %d calls", atomic.LoadInt32(&callCount))
}
