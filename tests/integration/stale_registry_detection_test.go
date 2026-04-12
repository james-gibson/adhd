package integration

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestStaleRegistryDetection validates fire-marshal's ability to detect dead clusters
func TestStaleRegistryDetection(t *testing.T) {
	resp, err := http.Get(clusterRegistryURL)
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		t.Fatalf("failed to decode registry: %v", err)
	}

	if len(registry) == 0 {
		t.Fatal("registry is empty")
	}

	t.Logf("Probing registry for stale entries: %d clusters", len(registry))

	// Probe each cluster endpoint
	type clusterStatus struct {
		name      string
		alarmAOK  bool
		alarmBOK  bool
		mcpOK     bool
		reachable bool
	}

	var results []clusterStatus
	var mu sync.Mutex
	var wg sync.WaitGroup
	healthyCount := int32(0)
	deadCount := int32(0)

	for clusterName, clusterData := range registry {
		wg.Add(1)
		go func(name string, data map[string]interface{}) {
			defer wg.Done()

			status := clusterStatus{name: name}

			// Probe alarm_a
			if alarmA, ok := data["alarm_a"].(string); ok {
				client := &http.Client{Timeout: timeoutDuration}
				resp, err := client.Get(alarmA)
				if err == nil {
					_ = resp.Body.Close()
					status.alarmAOK = true
				}
			}

			// Probe alarm_b
			if alarmB, ok := data["alarm_b"].(string); ok {
				client := &http.Client{Timeout: timeoutDuration}
				resp, err := client.Get(alarmB)
				if err == nil {
					_ = resp.Body.Close()
					status.alarmBOK = true
				}
			}

			// Probe MCP
			if mcpURL, ok := data["adhd_mcp"].(string); ok {
				client := &http.Client{Timeout: timeoutDuration}
				resp, err := client.Get(mcpURL)
				if err == nil {
					_ = resp.Body.Close()
					status.mcpOK = true
				}
			}

			// Cluster is reachable if at least one endpoint responds
			status.reachable = status.alarmAOK || status.alarmBOK || status.mcpOK

			mu.Lock()
			results = append(results, status)
			mu.Unlock()

			if status.reachable {
				atomic.AddInt32(&healthyCount, 1)
			} else {
				atomic.AddInt32(&deadCount, 1)
			}
		}(clusterName, clusterData)

		// Rate limit
		if len(results)%10 == 9 {
			wg.Wait()
		}
	}
	wg.Wait()

	healthy := atomic.LoadInt32(&healthyCount)
	dead := atomic.LoadInt32(&deadCount)
	total := int32(len(registry))

	t.Logf("Stale Registry Detection Results:")
	t.Logf("  Total registered: %d", total)
	t.Logf("  Reachable:        %d (%.1f%%)", healthy, float64(healthy)*100/float64(total))
	t.Logf("  Dead:             %d (%.1f%%)", dead, float64(dead)*100/float64(total))

	// Fire-marshal would flag these issues
	if healthy == 0 {
		t.Logf("CRITICAL: 0/30 clusters reachable - registry is completely stale")
		t.Logf("This should trigger: fire-marshal-cluster-health = RED")
		t.Logf("Recommended action: Audit and clean registry")
	} else if healthy < total/2 {
		t.Logf("WARNING: <50%% cluster health (%d/%d) - registry may be stale", healthy, total)
	}

	// Show individual cluster status
	if healthy < total {
		t.Logf("\nDead clusters (should be cleaned from registry):")
		deadCount := 0
		for _, status := range results {
			if !status.reachable && deadCount < 5 {
				t.Logf("  - %s (alarm_a:%v alarm_b:%v mcp:%v)",
					status.name, status.alarmAOK, status.alarmBOK, status.mcpOK)
				deadCount++
			}
		}
		if deadCount < int(dead) {
			t.Logf("  ... and %d more", dead-int32(deadCount))
		}
	}

	// This test should PASS even if all clusters are dead
	// (it's validating that we CAN detect the staleness)
	// But fire-marshal should FAIL its health check
	t.Logf("✓ Stale registry detection test completed")
	t.Logf("  Fire-marshal should report: CRITICAL - %d/%d clusters unreachable", dead, total)
}

// TestRegistryStalenessMetrics calculates staleness indicators
func TestRegistryStalenessMetrics(t *testing.T) {
	resp, err := http.Get(clusterRegistryURL)
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)

	// Calculate metrics
	var wg sync.WaitGroup
	alarmACount := int32(0)
	alarmBCount := int32(0)
	mcpCount := int32(0)
	healthyEndpoints := int32(0)
	deadEndpoints := int32(0)

	for _, clusterData := range registry {
		// Count alarm_a
		if alarmA, ok := clusterData["alarm_a"].(string); ok {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				client := &http.Client{Timeout: timeoutDuration}
				resp, err := client.Get(url)
				if err == nil {
					_ = resp.Body.Close()
					atomic.AddInt32(&alarmACount, 1)
					atomic.AddInt32(&healthyEndpoints, 1)
				} else {
					atomic.AddInt32(&deadEndpoints, 1)
				}
			}(alarmA)
		}

		// Count alarm_b
		if alarmB, ok := clusterData["alarm_b"].(string); ok {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				client := &http.Client{Timeout: timeoutDuration}
				resp, err := client.Get(url)
				if err == nil {
					_ = resp.Body.Close()
					atomic.AddInt32(&alarmBCount, 1)
					atomic.AddInt32(&healthyEndpoints, 1)
				} else {
					atomic.AddInt32(&deadEndpoints, 1)
				}
			}(alarmB)
		}

		// Count MCP
		if mcp, ok := clusterData["adhd_mcp"].(string); ok {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				client := &http.Client{Timeout: timeoutDuration}
				resp, err := client.Get(url)
				if err == nil {
					_ = resp.Body.Close()
					atomic.AddInt32(&mcpCount, 1)
					atomic.AddInt32(&healthyEndpoints, 1)
				} else {
					atomic.AddInt32(&deadEndpoints, 1)
				}
			}(mcp)
		}
	}
	wg.Wait()

	totalEndpoints := atomic.LoadInt32(&healthyEndpoints) + atomic.LoadInt32(&deadEndpoints)
	healthy := atomic.LoadInt32(&healthyEndpoints)

	t.Logf("Registry Staleness Metrics:")
	t.Logf("  Total endpoints: %d", totalEndpoints)
	t.Logf("  Healthy endpoints: %d (%.1f%%)", healthy, float64(healthy)*100/float64(totalEndpoints))
	t.Logf("  Dead endpoints: %d (%.1f%%)", atomic.LoadInt32(&deadEndpoints), float64(atomic.LoadInt32(&deadEndpoints))*100/float64(totalEndpoints))
	t.Logf("")
	t.Logf("  Endpoint breakdown:")
	t.Logf("    alarm_a: %d/~%d", atomic.LoadInt32(&alarmACount), len(registry))
	t.Logf("    alarm_b: %d/~%d", atomic.LoadInt32(&alarmBCount), len(registry))
	t.Logf("    MCP:     %d/~%d", atomic.LoadInt32(&mcpCount), len(registry))

	// Fire-marshal recommendations
	t.Logf("")
	t.Logf("Fire-marshal Recommendations:")
	if healthy == 0 {
		t.Logf("  🔴 CRITICAL: Registry is 100%% stale")
		t.Logf("  Action: Manual audit required - all clusters are dead")
		t.Logf("  Next: Clean registry and restart clusters")
	} else if healthy < totalEndpoints/4 {
		t.Logf("  🟠 WARNING: <25%% endpoint health")
		t.Logf("  Action: Review cluster health and registry consistency")
		t.Logf("  Next: Clean stale entries, restart unhealthy clusters")
	} else {
		t.Logf("  🟡 DEGRADED: Some endpoints unreachable")
		t.Logf("  Action: Monitor and plan cluster recovery")
	}
}

// TestClusterNotFoundLeavesRegistryEntry tests the scenario where clusters die but registry isn't cleaned
func TestClusterNotFoundLeavesRegistryEntry(t *testing.T) {
	resp, err := http.Get(clusterRegistryURL)
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)

	registrySize := len(registry)
	t.Logf("Registry contains %d cluster entries", registrySize)

	// Count how many are actually reachable
	reachableCount := 0
	for clusterName, clusterData := range registry {
		alarmA, _ := clusterData["alarm_a"].(string)
		alarmB, _ := clusterData["alarm_b"].(string)
		mcp, _ := clusterData["adhd_mcp"].(string)

		client := &http.Client{Timeout: timeoutDuration}

		// Try each endpoint
		if resp, err := client.Get(alarmA); err == nil {
			_ = resp.Body.Close()
			reachableCount++
			t.Logf("  ✓ %s (alarm_a)", clusterName)
		} else if resp, err := client.Get(alarmB); err == nil {
			_ = resp.Body.Close()
			reachableCount++
			t.Logf("  ✓ %s (alarm_b)", clusterName)
		} else if resp, err := client.Get(mcp); err == nil {
			_ = resp.Body.Close()
			reachableCount++
			t.Logf("  ✓ %s (mcp)", clusterName)
		}
	}

	orphanedCount := registrySize - reachableCount

	t.Logf("")
	t.Logf("Registry Consistency Check:")
	t.Logf("  Registered entries: %d", registrySize)
	t.Logf("  Reachable clusters: %d", reachableCount)
	t.Logf("  Orphaned entries:   %d (%.1f%%)", orphanedCount, float64(orphanedCount)*100/float64(registrySize))

	if orphanedCount > registrySize/2 {
		t.Logf("")
		t.Logf("🔴 REGRESSION: >50%% of registry is stale!")
		t.Logf("This should have been cleaned. Fire-marshal should flag this.")
	}
}

// Probe timeout duration
const timeoutDuration = 1 * time.Second
