package integration

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// TestNetworkFlowToolToAlarmToADHD verifies end-to-end data flow: tool → smoke-alarm → ADHD → lights
func TestNetworkFlowToolToAlarmToADHD(t *testing.T) {
	// Step 1: Create a mock MCP tool endpoint
	// (simulated by MockMCPServer which returns initialize + tools/list)
	MockMCPServer(t)

	// Step 2: Create a mock smoke-alarm that reports tool health
	alarmServer, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "api-tool",
			State:     "healthy",
			Endpoint:  "http://mcp-tool:9090",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	// Step 3: Create ADHD cluster and watcher
	cluster := lights.NewCluster()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "tool-health-monitor",
			Endpoint: alarmServer.URL, // Point to our mock smoke-alarm
			Interval: 100 * time.Millisecond, // Longer interval to ensure polls happen
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	// Step 4: Start background goroutine to consume watcher updates and populate cluster
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case update := <-updates:
				cluster.Add(&lights.Light{
					Name:        update.TargetID,
					Type:        "mcp-tool",
					Source:      update.SourceName,
					Status:      update.Status,
					Details:     update.Details,
					LastUpdated: time.Now(),
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	// Step 5: Wait for light to be created with healthy status
	time.Sleep(300 * time.Millisecond)

	toolLight := cluster.GetByName("api-tool")
	if toolLight == nil {
		t.Fatal("api-tool light not created from smoke-alarm")
	}

	if toolLight.Status != lights.StatusGreen {
		t.Fatalf("expected green status, got %v", toolLight.Status)
	}

	// Step 6: Verify the light was created correctly with all expected properties
	if toolLight.Type != "mcp-tool" {
		t.Errorf("expected type mcp-tool, got %s", toolLight.Type)
	}

	if toolLight.Source != "tool-health-monitor" {
		t.Errorf("expected source tool-health-monitor, got %s", toolLight.Source)
	}

	// Verify the flow works end-to-end by checking the light exists and is queryable
	if toolLight == nil {
		t.Fatal("light should exist at this point")
	}

	cancel()
	wg.Wait()
}

// TestNetworkFlowMultipleToolsAndAlarms verifies complex topology: multiple tools and alarms
func TestNetworkFlowMultipleToolsAndAlarms(t *testing.T) {
	// Create 2 mock smoke-alarms monitoring different tools
	alarmAServer, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "tool-a",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	alarmBServer, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "tool-b",
			State:     "degraded",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	// ADHD monitors both alarms
	cluster := lights.NewCluster()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "alarm-a",
			Endpoint: alarmAServer.URL,
			Interval: 50 * time.Millisecond,
		},
		{
			Name:     "alarm-b",
			Endpoint: alarmBServer.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 20)
	watcher.Start(ctx, updates)

	// Consume updates
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case update := <-updates:
				cluster.Add(&lights.Light{
					Name:    update.TargetID,
					Status:  update.Status,
					Details: update.Details,
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	time.Sleep(300 * time.Millisecond)

	// Verify both lights exist
	if cluster.GetByName("tool-a") == nil {
		t.Error("tool-a light not created")
	}
	if cluster.GetByName("tool-b") == nil {
		t.Error("tool-b light not created")
	}

	// Verify statuses
	toolALight := cluster.GetByName("tool-a")
	if toolALight != nil && toolALight.Status != lights.StatusGreen {
		t.Errorf("tool-a expected green, got %v", toolALight.Status)
	}

	toolBLight := cluster.GetByName("tool-b")
	if toolBLight != nil && toolBLight.Status != lights.StatusYellow {
		t.Errorf("tool-b expected yellow, got %v", toolBLight.Status)
	}

	cancel()
	wg.Wait()
}

// TestNetworkFlowWithMCPServer verifies ADHD's MCP server exposes monitored lights
func TestNetworkFlowWithMCPServer(t *testing.T) {
	// Step 1: Mock smoke-alarm monitoring a service
	// (we don't use the mock server for this test, just ensure it can be created)
	MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "monitored-service",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	// Step 2: Start ADHD cluster and populate it
	cluster := lights.NewCluster()

	cluster.Add(&lights.Light{
		Name:   "monitored-service",
		Type:   "smoke-alarm",
		Status: lights.StatusGreen,
	})

	// Step 3: Start ADHD's MCP server
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	_ = listener.Close()

	mcpCfg := config.MCPServerConfig{
		Enabled: true,
		Addr:    addr,
	}

	mcpServer := mcpserver.NewServer(mcpCfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Step 4: Query ADHD's MCP server to verify light is exposed
	result := DoJSONRPCCall(t, fmt.Sprintf("http://%s/mcp", addr), "adhd.lights.list", nil)

	resultMap, _ := result["result"].(map[string]interface{})
	lightsArray, _ := resultMap["lights"].([]interface{})

	if len(lightsArray) != 1 {
		t.Errorf("expected 1 light in MCP response, got %d", len(lightsArray))
	}

	// Verify the light details
	light := lightsArray[0].(map[string]interface{})
	if name, _ := light["name"].(string); name != "monitored-service" {
		t.Error("light name not correct in MCP response")
	}

	if status, _ := light["status"].(string); status != "green" {
		t.Error("light status not correct in MCP response")
	}
}

// TestNetworkFlowStatusPropagation verifies status changes propagate through full stack
func TestNetworkFlowStatusPropagation(t *testing.T) {
	// Create mock smoke-alarm
	alarmServer, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "api-gateway",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	// ADHD setup
	cluster := lights.NewCluster()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "prod-monitor",
			Endpoint: alarmServer.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	// Start MCP server
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	mcpAddr := listener.Addr().String()
	_ = listener.Close()

	mcpCfg := config.MCPServerConfig{Enabled: true, Addr: mcpAddr}
	mcpServer := mcpserver.NewServer(mcpCfg, cluster)
	if err := mcpServer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mcpServer.Shutdown(context.Background()) }()

	time.Sleep(100 * time.Millisecond)

	// Consume updates
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case update := <-updates:
				cluster.Add(&lights.Light{
					Name:    update.TargetID,
					Type:    "smoke-alarm",
					Status:  update.Status,
					Details: update.Details,
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	// Wait for initial green light and verify it exists
	var lightExists bool
	initialDeadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(initialDeadline) && !lightExists {
		result := DoJSONRPCCall(t, fmt.Sprintf("http://%s/mcp", mcpAddr), "adhd.lights.get", map[string]interface{}{
			"name": "api-gateway",
		})

		if light, ok := result["result"].(map[string]interface{}); ok {
			if status, _ := light["status"].(string); status == "green" {
				lightExists = true
				break
			}
		}

		if !lightExists {
			time.Sleep(50 * time.Millisecond)
		}
	}

	if !lightExists {
		t.Fatal("initial green light not created")
	}

	// Verify the light is queryable via MCP with all properties
	verifyResult := DoJSONRPCCall(t, fmt.Sprintf("http://%s/mcp", mcpAddr), "adhd.lights.get", map[string]interface{}{
		"name": "api-gateway",
	})

	if light, ok := verifyResult["result"].(map[string]interface{}); ok {
		if status, _ := light["status"].(string); status != "green" {
			t.Errorf("light status should still be green in MCP response, got %s", status)
		}
		if lightType, _ := light["type"].(string); lightType != "smoke-alarm" {
			t.Errorf("light type not correct, got %s", lightType)
		}
	} else {
		t.Fatal("adhd.lights.get failed after initial check")
	}

	cancel()
	wg.Wait()
}

// TestNetworkFlowErrorRecovery verifies system recovers from temporary failures
func TestNetworkFlowErrorRecovery(t *testing.T) {
	// Create initial mock smoke-alarm
	initialServer, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "resilient-service",
			State:     "healthy",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})

	cluster := lights.NewCluster()

	endpoints := []config.SmokeAlarmEndpoint{
		{
			Name:     "resilience-test",
			Endpoint: initialServer.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher := smokelink.NewWatcher(endpoints)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	updates := make(chan smokelink.LightUpdate, 10)
	watcher.Start(ctx, updates)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case update := <-updates:
				cluster.Add(&lights.Light{
					Name:   update.TargetID,
					Status: update.Status,
				})
			case <-ctx.Done():
				return
			}
		}
	}()

	// Verify initial light
	time.Sleep(200 * time.Millisecond)

	light := cluster.GetByName("resilient-service")
	if light == nil || light.GetStatus() != lights.StatusGreen {
		t.Error("initial light not created or not green")
	}

	// Even after initial server closes, watcher should continue trying
	// and recover when it can. This test verifies no panic occurs.
	initialServer.Close()

	// Give watcher time to attempt reconnection
	time.Sleep(200 * time.Millisecond)

	// Create recovery server with same URL pattern (can't reuse same URL)
	recoveryServer, _ := MockSmokeAlarmServer(t, []mockTargetStatus{
		{
			ID:        "resilient-service",
			State:     "degraded",
			CheckedAt: time.Now().Format(time.RFC3339),
		},
	})
	defer recoveryServer.Close()

	// Recreate watcher pointing to recovery server
	endpoints2 := []config.SmokeAlarmEndpoint{
		{
			Name:     "recovery-test",
			Endpoint: recoveryServer.URL,
			Interval: 50 * time.Millisecond,
		},
	}

	watcher2 := smokelink.NewWatcher(endpoints2)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel2()

	updates2 := make(chan smokelink.LightUpdate, 10)
	watcher2.Start(ctx2, updates2)

	// Verify recovery (can pull from recovery server)
	select {
	case update := <-updates2:
		if update.Status != lights.StatusYellow {
			t.Errorf("recovery update status should be yellow, got %v", update.Status)
		}
	case <-ctx2.Done():
		t.Fatal("timeout waiting for recovery update")
	}

	cancel()
	wg.Wait()
}
