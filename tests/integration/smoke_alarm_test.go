package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// TestSmokeAlarmPolling verifies that ADHD watcher correctly polls smoke-alarm endpoint
func TestSmokeAlarmPolling(t *testing.T) {
	// Create mock smoke-alarm server
	server, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "service-1",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
			LatencyMs: 50,
		},
	})

	// Create watcher
	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "test-alarm",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
			UseSSE:   false,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	// Start watcher
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Wait for at least one update
	select {
	case update := <-updates:
		if update.SourceName != "test-alarm" {
			t.Errorf("expected source 'test-alarm', got %q", update.SourceName)
		}
		if update.TargetID != "service-1" {
			t.Errorf("expected targetID 'service-1', got %q", update.TargetID)
		}
		if update.Status != lights.StatusGreen {
			t.Errorf("expected status green, got %v", update.Status)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for update")
	}
}

// TestSmokeAlarmStateChange verifies updates on state changes
func TestSmokeAlarmStateChange(t *testing.T) {
	// Create mock with initial healthy state
	currentState := "healthy"
	server, updateTargets := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "service-1",
			State:     currentState,
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "test-alarm",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Wait for initial healthy status
	select {
	case update := <-updates:
		if update.Status != lights.StatusGreen {
			t.Fatalf("expected initial status green, got %v", update.Status)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for first update")
	}

	// Change state to unhealthy
	updateTargets([]mockTargetStatus{
		{
			ID:        "service-1",
			State:     "outage",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	// Wait for state change update
	found := false
	deadline := time.Now().Add(1 * time.Second)
	for !found && time.Now().Before(deadline) {
		select {
		case update := <-updates:
			if update.Status == lights.StatusRed {
				found = true
			}
		case <-time.After(100 * time.Millisecond):
		}
	}

	if !found {
		t.Fatal("timeout waiting for state change to red")
	}
}

// TestSmokeAlarmMultipleEndpoints verifies parallel polling of multiple endpoints
func TestSmokeAlarmMultipleEndpoints(t *testing.T) {
	server1, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "service-a",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	server2, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "service-b",
			State:     "degraded",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "alarm-1",
			Endpoint: server1.URL,
			Interval: 50 * time.Millisecond,
		},
		{
			Name:     "alarm-2",
			Endpoint: server2.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Collect all updates
	seen := make(map[string]lights.Status)
	for {
		select {
		case update := <-updates:
			seen[update.TargetID] = update.Status
		case <-ctx.Done():
			goto done
		}
	}
done:

	if status, ok := seen["service-a"]; !ok || status != lights.StatusGreen {
		t.Error("service-a not received with green status")
	}

	if status, ok := seen["service-b"]; !ok || status != lights.StatusYellow {
		t.Error("service-b not received with yellow status")
	}
}

// TestSmokeAlarmDeduplication verifies target-level updates are deduplicated for
// unchanged state. Instance-level heartbeat updates are still emitted every poll
// cycle to keep the Bubble Tea waitForLightUpdate command pipeline alive.
func TestSmokeAlarmDeduplication(t *testing.T) {
	server, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "service-1",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "test-alarm",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 32)
	watcher.Start(ctx, updates)

	var targetUpdates, heartbeats int
	for {
		select {
		case u := <-updates:
			if u.IsInstance {
				heartbeats++
			} else if len(u.RemoteFeatures) == 0 {
				targetUpdates++
			}
		case <-ctx.Done():
			goto done
		}
	}
done:
	// Target-level updates must be deduplicated.
	if targetUpdates != 1 {
		t.Errorf("target updates: got %d, want 1 (deduplicated)", targetUpdates)
	}
	// Heartbeats must arrive on every poll to keep the Bubble Tea pipeline alive.
	if heartbeats < 2 {
		t.Errorf("heartbeat updates: got %d, want ≥2 (one per poll cycle)", heartbeats)
	}
}

// TestSmokeAlarmMultipleTargets verifies handling of multiple targets from one endpoint
func TestSmokeAlarmMultipleTargets(t *testing.T) {
	server, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "target-1",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
		{
			ID:        "target-2",
			State:     "degraded",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
		{
			ID:        "target-3",
			State:     "unhealthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "multi-target-alarm",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Collect all target IDs
	seen := make(map[string]bool)
	statuses := make(map[string]lights.Status)

	for {
		select {
		case update := <-updates:
			seen[update.TargetID] = true
			statuses[update.TargetID] = update.Status
		case <-ctx.Done():
			goto done
		}
	}
done:

	if !seen["target-1"] || statuses["target-1"] != lights.StatusGreen {
		t.Error("target-1 not received correctly")
	}
	if !seen["target-2"] || statuses["target-2"] != lights.StatusYellow {
		t.Error("target-2 not received correctly")
	}
	if !seen["target-3"] || statuses["target-3"] != lights.StatusRed {
		t.Error("target-3 not received correctly")
	}
}

// TestSmokeAlarmUnreachableEndpoint verifies graceful handling of unreachable endpoints
func TestSmokeAlarmUnreachableEndpoint(t *testing.T) {
	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "unreachable",
			Endpoint: "http://127.0.0.1:1",
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)

	// Should not panic
	watcher.Start(ctx, updates)

	// An unreachable endpoint must emit a red instance-level update so the
	// boot sequence can show a real failure rather than leaving the light dark.
	select {
	case update := <-updates:
		if !update.IsInstance {
			t.Error("expected instance-level update from unreachable endpoint")
		}
		if update.Status != lights.StatusRed {
			t.Errorf("expected red status from unreachable endpoint, got %v", update.Status)
		}
		if update.SourceName != "unreachable" {
			t.Errorf("expected source 'unreachable', got %q", update.SourceName)
		}
	case <-ctx.Done():
		t.Error("timeout: no update received from unreachable endpoint")
	}
}

// TestSmokeAlarmStatusMapping verifies correct status mapping from all state types
func TestSmokeAlarmStatusMapping(t *testing.T) {
	tests := map[string]lights.Status{
		"healthy":    lights.StatusGreen,
		"degraded":   lights.StatusYellow,
		"unhealthy":  lights.StatusRed,
		"outage":     lights.StatusRed,
		"regression": lights.StatusRed,
		"unknown":    lights.StatusDark,
	}

	for state, expectedStatus := range tests {
		server, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
			{
				ID:        "test-target",
				State:     state,
				CheckedAt: time.Now().Format(time.RFC3339),
			},
		})

		endpoints := []config.SmokeAlarmEndpoint{
			{
				Name:     "test",
				Endpoint: server.URL,
				Interval: 50 * time.Millisecond,
			},
		}

		watcher := smokelink.NewWatcher(endpoints)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)

		updates := make(chan smokelink.LightUpdate, 1)
		watcher.Start(ctx, updates)

		select {
		case update := <-updates:
			if update.Status != expectedStatus {
				t.Errorf("state %q: expected status %v, got %v", state, expectedStatus, update.Status)
			}
		case <-ctx.Done():
			t.Errorf("state %q: timeout waiting for update", state)
		}

		cancel()
	}
}

// TestSmokeAlarmWithCluster verifies integration with lights cluster
func TestSmokeAlarmWithCluster(t *testing.T) {
	server, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "api-service",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "production",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)
	cluster := lights.NewCluster()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Consume updates and add to cluster
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case update := <-updates:
				cluster.Add(&lights.Light{
					Name:       update.TargetID,
					Type:       "smoke-alarm",
					Source:     update.SourceName,
					Status:     update.Status,
					Details:    update.Details,
					LastUpdated: time.Now(),
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait a bit then check cluster
	time.Sleep(200 * time.Millisecond)
	cancel()
	wg.Wait()

	allLights := cluster.All()
	if len(allLights) == 0 {
		t.Fatal("no lights in cluster")
	}

	light := cluster.GetByName("api-service")
	if light == nil {
		t.Fatal("api-service light not found")
	}

	if light.GetStatus() != lights.StatusGreen {
		t.Errorf("expected green status, got %v", light.GetStatus())
	}
}
