package smokelink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// TestWatcherPollsSmokeAlarm verifies the watcher polls a smoke-alarm endpoint
func TestWatcherPollsSmokeAlarm(t *testing.T) {
	// Create mock smoke-alarm server
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.URL.Path == "/status" {
			resp := StatusResponse{
				Service: "test",
				Live:    true,
				Ready:   true,
				Targets: []TargetStatus{
					{
						ID:        "target-1",
						State:     "healthy",
						CheckedAt: time.Now().Format(time.RFC3339),
						LatencyMs: 100,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	// Create watcher
	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "test",
			Endpoint: server.URL,
			Interval: 100 * time.Millisecond,
			UseSSE:   false,
		},
	}
	watcher := NewWatcher(endpoints)

	// Start watcher and collect updates
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Wait for at least one update
	select {
	case update := <-updates:
		if update.SourceName != "test" {
			t.Errorf("expected source 'test', got %q", update.SourceName)
		}
		if update.TargetID != "target-1" {
			t.Errorf("expected target ID 'target-1', got %q", update.TargetID)
		}
		if update.Status != lights.StatusGreen {
			t.Errorf("expected status green, got %v", update.Status)
		}
	case <-ctx.Done():
		t.Error("timeout waiting for update")
	}

	// Verify polling happened
	if callCount == 0 {
		t.Error("watcher did not poll the endpoint")
	}
}

// TestWatcherMapsHealthState verifies status mapping
func TestWatcherMapsHealthState(t *testing.T) {
	tests := map[string]lights.Status{
		"healthy":    lights.StatusGreen,
		"degraded":   lights.StatusYellow,
		"unhealthy":  lights.StatusRed,
		"outage":     lights.StatusRed,
		"regression": lights.StatusRed,
		"unknown":    lights.StatusDark,
	}

	for state, expected := range tests {
		got := mapHealthState(state)
		if got != expected {
			t.Errorf("mapHealthState(%q) = %v, want %v", state, got, expected)
		}
	}
}

// TestWatcherDeduplicatesUpdates verifies target-level updates are deduplicated
// for unchanged state, while instance-level heartbeat updates are still emitted
// every poll cycle to keep the Bubble Tea command pipeline alive.
func TestWatcherDeduplicatesUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		resp := StatusResponse{
			Service: "test",
			Targets: []TargetStatus{
				{
					ID:        "target-1",
					State:     "healthy",
					CheckedAt: time.Now().Format(time.RFC3339),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "test",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
		},
	}
	watcher := NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	updates := make(chan LightUpdate, 32)
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
	// Target-level updates must be deduplicated: only the first discovery emits one.
	if targetUpdates != 1 {
		t.Errorf("target updates: got %d, want 1 (deduplicated)", targetUpdates)
	}
	// Heartbeat updates must arrive on every poll to keep the Bubble Tea pipeline alive.
	if heartbeats < 2 {
		t.Errorf("heartbeat updates: got %d, want ≥2 (one per poll cycle)", heartbeats)
	}
}

// TestWatcherHandlesStateChange verifies updates are sent on state changes
func TestWatcherHandlesStateChange(t *testing.T) {
	var currentState atomic.Value
	currentState.Store("healthy")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			resp := StatusResponse{
				Service: "test",
				Targets: []TargetStatus{
					{
						ID:        "target-1",
						State:     currentState.Load().(string),
						CheckedAt: time.Now().Format(time.RFC3339),
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server.Close()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "test",
			Endpoint: server.URL,
			Interval: 50 * time.Millisecond,
		},
	}
	watcher := NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	updates := make(chan LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Collect first update (healthy)
	var first LightUpdate
	select {
	case first = <-updates:
		if first.Status != lights.StatusGreen {
			t.Errorf("first update status = %v, want green", first.Status)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for first update")
	}

	// Change state
	currentState.Store("outage")

	// Wait for state change update
	found := false
	for !found {
		select {
		case update := <-updates:
			if update.Status == lights.StatusRed {
				found = true
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout waiting for state change update")
		}
	}
}

// TestWatcherHandlesUnreachableEndpoint emits a red instance-level update
func TestWatcherHandlesUnreachableEndpoint(t *testing.T) {
	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "unreachable",
			Endpoint: "http://localhost:1", // Port 1 is unlikely to be open
			Interval: 50 * time.Millisecond,
		},
	}
	watcher := NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan LightUpdate, 10)
	watcher.Start(ctx, updates)

	// An unreachable endpoint must emit an instance-level red update so the
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

// TestWatcherMultipleEndpoints verifies parallel polling
func TestWatcherMultipleEndpoints(t *testing.T) {
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			resp := StatusResponse{
				Targets: []TargetStatus{
					{
						ID:        "ep1-target",
						State:     "healthy",
						CheckedAt: time.Now().Format(time.RFC3339),
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			resp := StatusResponse{
				Targets: []TargetStatus{
					{
						ID:        "ep2-target",
						State:     "degraded",
						CheckedAt: time.Now().Format(time.RFC3339),
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer server2.Close()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "endpoint1",
			Endpoint: server1.URL,
			Interval: 50 * time.Millisecond,
		},
		{
			Name:     "endpoint2",
			Endpoint: server2.URL,
			Interval: 50 * time.Millisecond,
		},
	}
	watcher := NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Collect updates
	seen := make(map[string]bool)
	for {
		select {
		case update := <-updates:
			seen[update.TargetID] = true
		case <-ctx.Done():
			goto done
		}
	}
done:

	if !seen["ep1-target"] {
		t.Error("did not receive update from endpoint1")
	}
	if !seen["ep2-target"] {
		t.Error("did not receive update from endpoint2")
	}
}

// TestWatcherRefusesARepeatedAdoption is the poller guard behind adhd#49: a
// rejoin or a registry poll that re-adds the same name for the same endpoint
// must not start a second poller for a light that already has one.
func TestWatcherRefusesARepeatedAdoption(t *testing.T) {
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		polls.Add(1)
		resp := StatusResponse{
			Targets: []TargetStatus{
				{ID: "peer", State: "healthy", CheckedAt: time.Now().Format(time.RFC3339)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	watcher := NewWatcher([]config.SmokeAlarmEndpoint{
		{Name: "alarm-a", Endpoint: server.URL, Interval: 100 * time.Millisecond},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	updates := make(chan LightUpdate, 32)
	watcher.Start(ctx, updates)

	// The same claim, spelled with a trailing slash: one name, one endpoint,
	// so it is the same poll and must be refused.
	if watcher.WatchEndpoint(ctx, config.SmokeAlarmEndpoint{
		Name:     "alarm-a",
		Endpoint: server.URL + "/",
		Interval: 100 * time.Millisecond,
	}, updates) {
		t.Fatal("a repeated adoption was accepted; it would double the poll")
	}

	// Drain: the refused adoption must never appear as a second source.
	for {
		select {
		case update := <-updates:
			if update.SourceName != "alarm-a" {
				t.Fatalf("a refused adoption emitted a light: %+v", update)
			}
		case <-ctx.Done():
			if polls.Load() == 0 {
				t.Fatal("the first adoption never polled")
			}
			return
		}
	}
}

// TestWatcherKeepsReplicasUnderSeparateNames is the boundary adhd#49 must not
// cross: alarm-a and alarm-b are a counter-rotating replica set — two nodes of
// one route, each pointed at the other — and a registry may name one address
// twice on purpose. Names are the adopter's claims; endpoint equality is not
// route equality, so both are watched and both stay visible.
func TestWatcherKeepsReplicasUnderSeparateNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		resp := StatusResponse{
			Targets: []TargetStatus{
				{ID: "peer", State: "healthy", CheckedAt: time.Now().Format(time.RFC3339)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	watcher := NewWatcher([]config.SmokeAlarmEndpoint{
		{Name: "alarm-a", Endpoint: server.URL, Interval: 50 * time.Millisecond},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	updates := make(chan LightUpdate, 64)
	watcher.Start(ctx, updates)

	if !watcher.WatchEndpoint(ctx, config.SmokeAlarmEndpoint{
		Name:     "alarm-b",
		Endpoint: server.URL,
		Interval: 50 * time.Millisecond,
	}, updates) {
		t.Fatal("a second name for one address must still be watched: it is a replica, not a duplicate")
	}

	seen := make(map[string]bool)
	for {
		select {
		case update := <-updates:
			if update.TargetID == "peer" {
				seen[update.SourceName] = true
			}
		case <-ctx.Done():
			if !seen["alarm-a"] || !seen["alarm-b"] {
				t.Fatalf("sources = %v, want both replica names", seen)
			}
			return
		}
	}
}
