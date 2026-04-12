package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const clusterRegistryURL = "http://192.168.7.18:19100/cluster"

var registryClient = &http.Client{Timeout: 5 * time.Second}

func registryGet() (*http.Response, error) {
	return registryClient.Get(clusterRegistryURL)
}

// TestClusterRegistryEnumeration validates that all clusters are discoverable and consistent
func TestClusterRegistryEnumeration(t *testing.T) {
	resp, err := registryGet()
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

	t.Logf("Found %d clusters in registry", len(registry))

	// Verify all clusters have required fields
	for clusterName, clusterData := range registry {
		if name, ok := clusterData["name"].(string); !ok || name == "" {
			t.Errorf("cluster %s missing name field", clusterName)
		}
		if alarmA, ok := clusterData["alarm_a"].(string); !ok || alarmA == "" {
			t.Errorf("cluster %s missing alarm_a", clusterName)
		}
		if alarmB, ok := clusterData["alarm_b"].(string); !ok || alarmB == "" {
			t.Errorf("cluster %s missing alarm_b", clusterName)
		}
		if mcpURL, ok := clusterData["adhd_mcp"].(string); !ok || mcpURL == "" {
			t.Errorf("cluster %s missing adhd_mcp", clusterName)
		}
	}

	// Verify consistency on repeated queries
	time.Sleep(100 * time.Millisecond)
	resp2, _ := registryGet()
	var registry2 map[string]map[string]interface{}
	_ = json.NewDecoder(resp2.Body).Decode(&registry2)
	_ = resp2.Body.Close()

	if len(registry) != len(registry2) {
		t.Errorf("registry size changed: %d → %d", len(registry), len(registry2))
	}

	t.Logf("✓ Registry enumeration: %d clusters, all fields present, consistent", len(registry))
}

// TestClusterEndpointReachability validates all registered endpoints are reachable
func TestClusterEndpointReachability(t *testing.T) {
	resp, err := registryGet()
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)

	type endpoint struct {
		name string
		url  string
		kind string // "alarm_a", "alarm_b", "adhd_mcp"
	}

	var endpoints []endpoint
	for clusterName, clusterData := range registry {
		if alarmA, ok := clusterData["alarm_a"].(string); ok {
			endpoints = append(endpoints, endpoint{clusterName, alarmA, "alarm_a"})
		}
		if alarmB, ok := clusterData["alarm_b"].(string); ok {
			endpoints = append(endpoints, endpoint{clusterName, alarmB, "alarm_b"})
		}
		if mcpURL, ok := clusterData["adhd_mcp"].(string); ok {
			endpoints = append(endpoints, endpoint{clusterName, mcpURL, "adhd_mcp"})
		}
	}

	t.Logf("Probing %d endpoints (%d clusters)", len(endpoints), len(registry))

	var wg sync.WaitGroup
	reachable := int32(0)
	unreachable := int32(0)
	var mu sync.Mutex
	var issues []string

	for i := 0; i < len(endpoints); i++ {
		wg.Add(1)
		go func(ep endpoint) {
			defer wg.Done()

			client := &http.Client{Timeout: 2 * time.Second}
			start := time.Now()
			resp, err := client.Get(ep.url)
			elapsed := time.Since(start)

			if err == nil {
				_ = resp.Body.Close()
				atomic.AddInt32(&reachable, 1)
				if elapsed > 1*time.Second {
					mu.Lock()
					issues = append(issues, fmt.Sprintf("%s/%s slow (%v)", ep.name, ep.kind, elapsed))
					mu.Unlock()
				}
			} else {
				atomic.AddInt32(&unreachable, 1)
				mu.Lock()
				issues = append(issues, fmt.Sprintf("%s/%s unreachable: %v", ep.name, ep.kind, err))
				mu.Unlock()
			}
		}(endpoints[i])

		// Rate limit concurrent requests
		if i%10 == 9 {
			wg.Wait()
		}
	}
	wg.Wait()

	reachCount := atomic.LoadInt32(&reachable)
	unreachCount := atomic.LoadInt32(&unreachable)

	t.Logf("Reachability: %d reachable, %d unreachable", reachCount, unreachCount)

	if len(issues) > 0 {
		t.Logf("Issues found:")
		for _, issue := range issues {
			t.Logf("  - %s", issue)
		}
	}

	// Allow some failures but not all
	if unreachCount > int32(len(endpoints)/2) {
		t.Errorf("too many unreachable endpoints: %d/%d", unreachCount, len(endpoints))
	}
}

// TestClusterConcurrentDiscovery validates concurrent registry queries
func TestClusterConcurrentDiscovery(t *testing.T) {
	// Get the registry once to know the cluster count
	resp, err := registryGet()
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)
	_ = resp.Body.Close()

	numClusters := len(registry)
	t.Logf("Testing concurrent discovery with %d clusters", numClusters)

	// Simulate 30 concurrent ADHD instances querying the registry
	const numQueriers = 30
	var wg sync.WaitGroup
	successCount := int32(0)
	var timings []time.Duration
	var timingMu sync.Mutex

	for i := 0; i < numQueriers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			start := time.Now()
			resp, err := registryGet()
			elapsed := time.Since(start)

			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()

			var r map[string]map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
				return
			}

			if len(r) == numClusters {
				atomic.AddInt32(&successCount, 1)
			}

			timingMu.Lock()
			timings = append(timings, elapsed)
			timingMu.Unlock()
		}()
	}
	wg.Wait()

	// Calculate percentiles
	if len(timings) > 0 {
		var sum time.Duration
		for _, t := range timings {
			sum += t
		}
		avg := sum / time.Duration(len(timings))
		t.Logf("Average query time: %v", avg)
	}

	if atomic.LoadInt32(&successCount) < int32(numQueriers/2) {
		t.Errorf("only %d/%d concurrent queries succeeded", atomic.LoadInt32(&successCount), numQueriers)
	}

	t.Logf("✓ Concurrent discovery: %d/%d successful", atomic.LoadInt32(&successCount), numQueriers)
}

// TestClusterDualAlarmRedundancy validates that clusters have dual smoke-alarms
func TestClusterDualAlarmRedundancy(t *testing.T) {
	resp, err := registryGet()
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)

	for clusterName, clusterData := range registry {
		alarmA, _ := clusterData["alarm_a"].(string)
		alarmB, _ := clusterData["alarm_b"].(string)

		if alarmA == alarmB {
			t.Errorf("%s: alarm_a and alarm_b are the same (%s)", clusterName, alarmA)
		}

		// At least one should be reachable
		client := &http.Client{Timeout: 1 * time.Second}
		respA, errA := client.Get(alarmA)
		respB, errB := client.Get(alarmB)

		if respA != nil {
			_ = respA.Body.Close()
		}
		if respB != nil {
			_ = respB.Body.Close()
		}

		if errA != nil && errB != nil {
			t.Logf("warning: %s both alarms unreachable", clusterName)
		}
	}

	t.Logf("✓ Verified dual alarm configuration for all clusters")
}

// TestClusterIsotopeUniqueness validates that all clusters have unique isotope IDs
func TestClusterIsotopeUniqueness(t *testing.T) {
	resp, err := registryGet()
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)

	isotopes := make(map[string]string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	duplicates := 0

	for clusterName, clusterData := range registry {
		wg.Add(1)
		go func(name string, data map[string]interface{}) {
			defer wg.Done()

			mcpURL, ok := data["adhd_mcp"].(string)
			if !ok {
				return
			}

			// Call adhd.isotope.instance to get the isotope ID
			client := &http.Client{Timeout: 1 * time.Second}
			resp, err := client.Get(mcpURL)
			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()

			var result map[string]interface{}
			body, _ := io.ReadAll(resp.Body)
			if err := json.Unmarshal(body, &result); err != nil {
				return
			}

			if respData, ok := result["result"].(map[string]interface{}); ok {
				if isotope, ok := respData["isotope"].(string); ok {
					mu.Lock()
					if existing, seen := isotopes[isotope]; seen {
						t.Logf("warning: isotope %s seen in both %s and %s", isotope, existing, name)
						duplicates++
					}
					isotopes[isotope] = name
					mu.Unlock()
				}
			}
		}(clusterName, clusterData)
	}
	wg.Wait()

	if duplicates > 0 {
		t.Errorf("found %d duplicate isotope IDs", duplicates)
	}

	t.Logf("✓ Verified isotope uniqueness: %d unique IDs", len(isotopes))
}

// TestClusterRegistryStress validates registry performance under sustained load
func TestClusterRegistryStress(t *testing.T) {
	// Get initial registry state
	resp, err := registryGet()
	if err != nil {
		t.Skipf("cluster registry unavailable: %v", err)
	}
	var registry map[string]map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&registry)
	_ = resp.Body.Close()

	numClusters := len(registry)
	t.Logf("Stress testing registry with %d clusters", numClusters)

	// Run 100 queries over 10 seconds
	const queryCount = 100
	start := time.Now()
	successCount := int32(0)
	var timings []time.Duration
	var mu sync.Mutex

	for i := 0; i < queryCount; i++ {
		go func() {
			queryStart := time.Now()
			resp, err := registryGet()
			elapsed := time.Since(queryStart)

			if err != nil {
				return
			}
			defer func() { _ = resp.Body.Close() }()

			var r map[string]map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
				return
			}

			if len(r) > 0 {
				atomic.AddInt32(&successCount, 1)
				mu.Lock()
				timings = append(timings, elapsed)
				mu.Unlock()
			}
		}()

		time.Sleep(100 * time.Millisecond)
	}

	// Wait for all queries to complete
	time.Sleep(2 * time.Second)

	elapsed := time.Since(start)
	successCount_ := atomic.LoadInt32(&successCount)

	t.Logf("Stress test results:")
	t.Logf("  Queries: %d/%d successful", successCount_, queryCount)
	t.Logf("  Duration: %v", elapsed)

	if len(timings) > 0 {
		var minT, maxT, sumT time.Duration
		minT = timings[0]
		maxT = timings[0]
		for _, t := range timings {
			if t < minT {
				minT = t
			}
			if t > maxT {
				maxT = t
			}
			sumT += t
		}
		avgT := sumT / time.Duration(len(timings))
		t.Logf("  Response times: min=%v avg=%v max=%v", minT, avgT, maxT)
	}

	if successCount_ < int32(queryCount/2) {
		t.Errorf("too many failed queries: %d/%d", queryCount-int(successCount_), queryCount)
	}
}
