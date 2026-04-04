package integration

import (
	"context"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/health"
	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpclient"
)

// TestADHDProbesSmokeAlarmMCP verifies ADHD can probe smoke-alarm's MCP proxy
func TestADHDProbesSmokeAlarmMCP(t *testing.T) {
	// Create mock smoke-alarm MCP server
	server := MockMCPServer(t)

	// Create a binary config pointing to smoke-alarm's MCP proxy
	binaries := []config.Binary{
		{
			Name:     "smoke-alarm-proxy",
			Endpoint: server.URL,
			Features: []config.Feature{
				{Name: "service-monitoring"},
				{Name: "regression-detection"},
			},
			Interval: 100 * time.Millisecond,
			Timeout:  2 * time.Second,
		},
	}

	// Create light cluster
	cluster := lights.NewCluster()

	// Create health monitor
	monitor := health.New(binaries, cluster)

	// Start monitoring
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	monitor.Start(ctx)

	// Wait for probe
	time.Sleep(200 * time.Millisecond)

	// Verify lights were created
	serviceLight := cluster.GetByName("service-monitoring")
	if serviceLight == nil {
		t.Fatal("service-monitoring light not created")
	}

	// Verify status is green (endpoint reachable)
	if serviceLight.GetStatus() != lights.StatusGreen {
		t.Fatalf("expected green status, got %v", serviceLight.GetStatus())
	}

	// Verify source attribution
	if serviceLight.Source != "smoke-alarm-proxy" {
		t.Errorf("expected source smoke-alarm-proxy, got %s", serviceLight.Source)
	}
}

// TestADHDProbesSmokeAlarmMCPFailure verifies ADHD detects unreachable smoke-alarm
func TestADHDProbesSmokeAlarmMCPFailure(t *testing.T) {
	// Point to non-existent endpoint
	binaries := []config.Binary{
		{
			Name:     "smoke-alarm-down",
			Endpoint: "http://127.0.0.1:1", // unreachable port
			Features: []config.Feature{
				{Name: "service-monitoring"},
			},
			Interval: 100 * time.Millisecond,
			Timeout:  100 * time.Millisecond, // short timeout
		},
	}

	cluster := lights.NewCluster()
	monitor := health.New(binaries, cluster)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	monitor.Start(ctx)

	// Wait for probe to fail
	time.Sleep(300 * time.Millisecond)

	// Verify light was created
	serviceLight := cluster.GetByName("service-monitoring")
	if serviceLight == nil {
		t.Fatal("light not created for unreachable endpoint")
	}

	// Verify status is red (endpoint unreachable)
	if serviceLight.GetStatus() != lights.StatusRed {
		t.Fatalf("expected red status for unreachable endpoint, got %v", serviceLight.GetStatus())
	}

	// Verify details indicate unhealthy (consensus between local and remote)
	if serviceLight.GetDetails() != "unhealthy (consensus)" {
		t.Errorf("expected 'unhealthy (consensus)', got %q", serviceLight.GetDetails())
	}
}

// TestADHDMultipleEndpointsIncludingSmokeAlarm verifies ADHD monitors mixed binaries
func TestADHDMultipleEndpointsIncludingSmokeAlarm(t *testing.T) {
	// Create mock smoke-alarm
	smokeAlarmServer := MockMCPServer(t)

	// Create mock service
	serviceServer := MockMCPServer(t)

	binaries := []config.Binary{
		{
			Name:     "smoke-alarm",
			Endpoint: smokeAlarmServer.URL,
			Features: []config.Feature{
				{Name: "monitoring"},
			},
			Interval: 100 * time.Millisecond,
			Timeout:  2 * time.Second,
		},
		{
			Name:     "app-service",
			Endpoint: serviceServer.URL,
			Features: []config.Feature{
				{Name: "api"},
			},
			Interval: 100 * time.Millisecond,
			Timeout:  2 * time.Second,
		},
	}

	cluster := lights.NewCluster()
	monitor := health.New(binaries, cluster)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	monitor.Start(ctx)
	time.Sleep(300 * time.Millisecond)

	// Both lights should exist and be green
	monitoringLight := cluster.GetByName("monitoring")
	apiLight := cluster.GetByName("api")

	if monitoringLight == nil || monitoringLight.GetStatus() != lights.StatusGreen {
		t.Error("smoke-alarm light not green")
	}

	if apiLight == nil || apiLight.GetStatus() != lights.StatusGreen {
		t.Error("app-service light not green")
	}

	if monitoringLight.Source != "smoke-alarm" {
		t.Errorf("monitoring light source wrong: %s", monitoringLight.Source)
	}

	if apiLight.Source != "app-service" {
		t.Errorf("api light source wrong: %s", apiLight.Source)
	}
}

// TestDirectMCPClientProbeWorks verifies the underlying MCP client
func TestDirectMCPClientProbeWorks(t *testing.T) {
	// Create mock MCP server
	server := MockMCPServer(t)

	// Create MCP client
	client := mcpclient.NewHTTPClient(server.URL, 5*time.Second)

	// Probe it
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := client.Probe(ctx)

	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}

	if !result.Healthy {
		t.Fatalf("probe returned unhealthy: %s", result.Error)
	}

	if result.ToolCount == 0 {
		t.Error("probe returned no tools")
	}

	t.Logf("probe successful: %d tools, latency %v", result.ToolCount, result.Latency)
}

// BenchmarkADHDHealthMonitorProbes measures probe performance
func BenchmarkADHDHealthMonitorProbes(b *testing.B) {
	server := MockMCPServer(&testing.T{})
	defer server.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		// Perform one probe
		client := mcpclient.NewHTTPClient(server.URL, 2*time.Second)
		client.Probe(ctx)

		cancel()
	}
}

// TestADHDProbeLatencyMeasurement verifies latency tracking
func TestADHDProbeLatencyMeasurement(t *testing.T) {
	server := MockMCPServer(t)

	binaries := []config.Binary{
		{
			Name:     "latency-test",
			Endpoint: server.URL,
			Features: []config.Feature{
				{Name: "test-feature"},
			},
			Interval: 100 * time.Millisecond,
			Timeout:  5 * time.Second,
		},
	}

	cluster := lights.NewCluster()
	monitor := health.New(binaries, cluster)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	monitor.Start(ctx)
	time.Sleep(200 * time.Millisecond)

	light := cluster.GetByName("test-feature")
	if light == nil {
		t.Fatal("light not created")
	}

	// Light should be green (endpoint reachable)
	if light.GetStatus() != lights.StatusGreen {
		t.Errorf("expected green, got %v", light.GetStatus())
	}
}
