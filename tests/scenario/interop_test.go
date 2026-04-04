// Package scenario — interop tests that spawn real ocd-smoke-alarm instances.
//
// The binary is built once from ../ocd-smoke-alarm via go build and run as a
// child process for each test.  ADHD's smokelink watcher is then pointed at the
// smoke-alarm's /status endpoint, exercising the full integration path:
//
//   mock HTTP service → ocd-smoke-alarm probe → /status → ADHD watcher → JSONL log → report
//
// If ocd-smoke-alarm changes its /status JSON format these tests will break,
// which is intentional — they act as a contract check between the two binaries.
package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/headless"
	"github.com/james-gibson/adhd/internal/report"
)

// ---- binary builder ---------------------------------------------------------

var (
	buildSmokeAlarmOnce sync.Once
	buildSmokeAlarmErr  error
	smokeAlarmBinary    string
)

func ensureSmokeAlarmBinary(t *testing.T) string {
	t.Helper()
	buildSmokeAlarmOnce.Do(func() {
		// Prefer building from source when the sibling repo is present.
		root, err := filepath.Abs(filepath.Join("..", "..", "..", "ocd-smoke-alarm"))
		if err == nil {
			if _, statErr := os.Stat(root); statErr == nil {
				tmp, mkErr := os.MkdirTemp("", "ocd-smoke-alarm-bin-*")
				if mkErr != nil {
					buildSmokeAlarmErr = fmt.Errorf("mktemp: %w", mkErr)
					return
				}
				out := filepath.Join(tmp, "ocd-smoke-alarm")
				var stderr bytes.Buffer
				cmd := exec.Command("go", "build", "-o", out, "./cmd/ocd-smoke-alarm")
				cmd.Dir = root
				cmd.Stderr = &stderr
				if buildErr := cmd.Run(); buildErr != nil {
					buildSmokeAlarmErr = fmt.Errorf("go build: %w\n%s", buildErr, stderr.String())
					return
				}
				smokeAlarmBinary = out
				return
			}
		}
		// Source not present — check ~/.lezz/bin (managed by lezz), then PATH.
		if home, err := os.UserHomeDir(); err == nil {
			lezzBin := filepath.Join(home, ".lezz", "bin", "ocd-smoke-alarm")
			if _, statErr := os.Stat(lezzBin); statErr == nil {
				smokeAlarmBinary = lezzBin
				return
			}
		}
		p, lookErr := exec.LookPath("ocd-smoke-alarm")
		if lookErr != nil {
			buildSmokeAlarmErr = fmt.Errorf("ocd-smoke-alarm not found in source tree, ~/.lezz/bin, or PATH: %w", lookErr)
			return
		}
		smokeAlarmBinary = p
	})
	if buildSmokeAlarmErr != nil {
		t.Skipf("skipping: ocd-smoke-alarm unavailable: %v", buildSmokeAlarmErr)
	}
	return smokeAlarmBinary
}

// ---- instance launcher ------------------------------------------------------

type alarmInstance struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
	healthAddr string // "127.0.0.1:PORT"
	StatusURL  string // "http://127.0.0.1:PORT/status"
}

// startSmokeAlarmInstance builds a config YAML, starts ocd-smoke-alarm as a
// subprocess, and waits for its /readyz endpoint to return 200.
func startSmokeAlarmInstance(t *testing.T, name, healthAddr string, targetEntries []string) *alarmInstance {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// Write a minimal config — only what we need for probing + health endpoint.
	targetsBlock := strings.Join(targetEntries, "\n")
	yaml := fmt.Sprintf(`version: "1"
service:
  name: "%s"
  mode: "background"
  log_level: "warn"
  poll_interval: "80ms"
  timeout: "500ms"
  max_workers: 2
health:
  enabled: true
  listen_addr: "%s"
  endpoints:
    healthz: "/healthz"
    readyz: "/readyz"
    status: "/status"
runtime:
  lock_file: "%s"
  state_dir: "%s"
  baseline_file: "%s"
  event_history_size: 64
  graceful_shutdown_timeout: "2s"
discovery:
  enabled: false
alerts:
  aggressive: false
  sinks:
    log:
      enabled: false
    os_notification:
      enabled: false
known_state:
  enabled: false
federation:
  enabled: false
hosted:
  enabled: false
meta_config:
  enabled: false
dynamic_config:
  enabled: false
telemetry:
  enabled: false
remote_agent:
  managed_updates: false
targets:
%s
`, name,
		healthAddr,
		filepath.Join(dir, "run.lock"),
		filepath.Join(dir, "state"),
		filepath.Join(dir, "state", "known-good.json"),
		targetsBlock)

	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, ensureSmokeAlarmBinary(t), "serve", "--config", cfgPath)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start smoke-alarm %q: %v", name, err)
	}

	inst := &alarmInstance{
		cmd:        cmd,
		cancel:     cancel,
		stdout:     stdout,
		stderr:     stderr,
		healthAddr: healthAddr,
		StatusURL:  "http://" + healthAddr,
	}

	// Wait for readiness
	readyURL := "http://" + healthAddr + "/readyz"
	waitCtx, wcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer wcancel()
	if err := pollUntilOK(waitCtx, readyURL); err != nil {
		inst.stop(t)
		t.Fatalf("alarm %q not ready: %v\nstdout: %s\nstderr: %s",
			name, err, stdout.String(), stderr.String())
	}

	t.Cleanup(func() { inst.stop(t) })
	return inst
}

func (a *alarmInstance) stop(t *testing.T) {
	if a.cancel != nil {
		a.cancel()
	}
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if t != nil {
			t.Logf("smoke-alarm did not exit; killing")
		}
		_ = a.cmd.Process.Kill()
	}
}

func pollUntilOK(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout: %w", ctx.Err())
		default:
		}
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return nil
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// freeLocalAddr returns "127.0.0.1:PORT" with a free TCP port.
func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeLocalAddr: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// httpTarget returns a YAML fragment for one HTTP target.
func httpTarget(id, endpoint string) string {
	return fmt.Sprintf(`  - id: %q
    enabled: true
    protocol: "http"
    endpoint: %q
    transport: "http"
    expected:
      healthy_status_codes: [200]
    auth:
      type: "none"
    check:
      interval: "80ms"
      timeout: "300ms"
      retries: 0`, id, endpoint)
}

// mockService returns an httptest.Server whose HTTP 200/non-200 status is
// controlled by the healthy atomic flag (true = 200, false = 503).
func mockService(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"down"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &healthy
}

// ---- tests ------------------------------------------------------------------

// TestInteropHealthyAlarm starts a real ocd-smoke-alarm that probes a healthy
// mock HTTP service, then verifies ADHD receives green light-update events.
func TestInteropHealthyAlarm(t *testing.T) {
	svc, _ := mockService(t)

	alarm := startSmokeAlarmInstance(t, "itest-healthy",
		freeLocalAddr(t),
		[]string{httpTarget("healthy-svc", svc.URL+"/health")},
	)

	logPath := filepath.Join(t.TempDir(), "adhd.jsonl")
	cfg := &config.Config{
		SmokeAlarm: []config.SmokeAlarmEndpoint{
			{Name: "itest-healthy", Endpoint: alarm.StatusURL, Interval: 50 * time.Millisecond},
		},
	}
	srv := headless.New(cfg)
	if err := srv.Start(logPath); err != nil {
		t.Fatalf("headless.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// Allow smoke-alarm time to probe and ADHD time to poll /status
	time.Sleep(600 * time.Millisecond)

	cl, err := report.ScanLog(logPath)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}
	t.Logf("events=%d endpoints=%v statuses=%v", len(cl.Watcher.Events), cl.Watcher.Endpoints, cl.Watcher.SeenStatuses)

	if len(cl.Watcher.Events) == 0 {
		t.Fatal("no watcher events — smokelink watcher did not poll real alarm")
	}
	if cl.Watcher.ByNewStatus["green"] == 0 {
		t.Errorf("expected green lights; statuses=%v (smoke-alarm stderr: %s)", cl.Watcher.SeenStatuses, alarm.stderr.String())
	}
}

// TestInteropOutageAlarm starts a real smoke-alarm that probes an unreachable
// mock service (closed port) and verifies ADHD receives red light-update events.
func TestInteropOutageAlarm(t *testing.T) {
	// Use a port that is definitely not listening
	closedPort := freeLocalAddr(t) // allocate then release — will be closed

	alarm := startSmokeAlarmInstance(t, "itest-outage",
		freeLocalAddr(t),
		[]string{httpTarget("outage-svc", "http://"+closedPort+"/health")},
	)

	logPath := filepath.Join(t.TempDir(), "adhd.jsonl")
	cfg := &config.Config{
		SmokeAlarm: []config.SmokeAlarmEndpoint{
			{Name: "itest-outage", Endpoint: alarm.StatusURL, Interval: 50 * time.Millisecond},
		},
	}
	srv := headless.New(cfg)
	if err := srv.Start(logPath); err != nil {
		t.Fatalf("headless.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	time.Sleep(600 * time.Millisecond)

	cl, err := report.ScanLog(logPath)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}
	t.Logf("events=%d statuses=%v", len(cl.Watcher.Events), cl.Watcher.SeenStatuses)

	if len(cl.Watcher.Events) == 0 {
		t.Fatal("no watcher events from outage alarm")
	}
	// Closed port → outage → red light
	if cl.Watcher.ByNewStatus["red"] == 0 && cl.Watcher.ByNewStatus["dark"] == 0 {
		t.Errorf("expected red/dark lights from closed-port target; statuses=%v", cl.Watcher.SeenStatuses)
	}
}

// TestInteropStateTransition starts a real smoke-alarm probing a service that
// starts healthy, becomes unhealthy, then recovers.  Verifies ADHD logs the
// state transitions correctly.
func TestInteropStateTransition(t *testing.T) {
	svc, healthy := mockService(t)

	alarm := startSmokeAlarmInstance(t, "itest-transition",
		freeLocalAddr(t),
		[]string{httpTarget("flapping-svc", svc.URL+"/health")},
	)

	logPath := filepath.Join(t.TempDir(), "adhd.jsonl")
	cfg := &config.Config{
		SmokeAlarm: []config.SmokeAlarmEndpoint{
			{Name: "itest-transition", Endpoint: alarm.StatusURL, Interval: 50 * time.Millisecond},
		},
	}
	srv := headless.New(cfg)
	if err := srv.Start(logPath); err != nil {
		t.Fatalf("headless.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// Let healthy state settle
	time.Sleep(350 * time.Millisecond)
	// Force outage
	healthy.Store(false)
	time.Sleep(350 * time.Millisecond)
	// Recover
	healthy.Store(true)
	time.Sleep(350 * time.Millisecond)

	cl, err := report.ScanLog(logPath)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}
	t.Logf("events=%d transitions=%d statuses=%v",
		len(cl.Watcher.Events), cl.Watcher.Transitions, cl.Watcher.SeenStatuses)

	if cl.Watcher.Transitions == 0 {
		t.Error("expected at least one status transition (healthy → outage → healthy)")
	}
}

// TestInteropGherkinCoverage runs the full report pipeline against a real
// multi-alarm cluster and verifies that smoke-alarm-network Gherkin scenarios
// produce PASS results when backed by live ocd-smoke-alarm instances.
func TestInteropGherkinCoverage(t *testing.T) {
	// alarm-healthy: healthy service
	svcA, _ := mockService(t)
	alarmHealthy := startSmokeAlarmInstance(t, "itest-gh-healthy",
		freeLocalAddr(t),
		[]string{
			httpTarget("api-service", svcA.URL+"/health"),
			httpTarget("auth-service", svcA.URL+"/health"),
		},
	)

	// alarm-bad: service starts healthy, then goes down
	svcB, healthyB := mockService(t)
	alarmTransition := startSmokeAlarmInstance(t, "itest-gh-transition",
		freeLocalAddr(t),
		[]string{httpTarget("payment-gw", svcB.URL+"/health")},
	)

	// alarm-dead: unreachable port
	deadAddr := freeLocalAddr(t)
	alarmDead := startSmokeAlarmInstance(t, "itest-gh-dead",
		freeLocalAddr(t),
		[]string{httpTarget("dead-svc", "http://"+deadAddr+"/health")},
	)

	logPath := filepath.Join(t.TempDir(), "full.jsonl")
	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    "127.0.0.1:0",
		},
		SmokeAlarm: []config.SmokeAlarmEndpoint{
			{Name: "itest-gh-healthy", Endpoint: alarmHealthy.StatusURL, Interval: 50 * time.Millisecond},
			{Name: "itest-gh-transition", Endpoint: alarmTransition.StatusURL, Interval: 50 * time.Millisecond},
			{Name: "itest-gh-dead", Endpoint: alarmDead.StatusURL, Interval: 50 * time.Millisecond},
		},
	}

	adhd := headless.New(cfg)
	if err := adhd.Start(logPath); err != nil {
		t.Fatalf("headless.Start: %v", err)
	}
	t.Cleanup(func() { _ = adhd.Shutdown() })

	// Let healthy state settle, then inject MCP traffic and force a transition
	time.Sleep(400 * time.Millisecond)

	// Inject MCP method log entries for MCP-domain coverage
	for _, method := range []string{"initialize", "tools/list", "adhd.status",
		"adhd.lights.list", "adhd.isotope.status", "adhd.isotope.peers", "ping"} {
		_ = adhd.LogTraffic(headless.MCPTrafficLog{Timestamp: time.Now().UTC(), Type: "request", Method: method})
		_ = adhd.LogTraffic(headless.MCPTrafficLog{Timestamp: time.Now().UTC(), Type: "response", Method: method})
	}
	// lights.get: success + error pair
	_ = adhd.LogTraffic(headless.MCPTrafficLog{Timestamp: time.Now().UTC(), Type: "request", Method: "adhd.lights.get"})
	_ = adhd.LogTraffic(headless.MCPTrafficLog{Timestamp: time.Now().UTC(), Type: "response", Method: "adhd.lights.get"})
	_ = adhd.LogTraffic(headless.MCPTrafficLog{Timestamp: time.Now().UTC(), Type: "request", Method: "adhd.lights.get"})
	_ = adhd.LogTraffic(headless.MCPTrafficLog{
		Timestamp: time.Now().UTC(), Type: "response", Method: "adhd.lights.get",
		Error: &map[string]interface{}{"code": -32602, "message": "not found"},
	})

	// Force transition on payment-gw
	healthyB.Store(false)
	time.Sleep(400 * time.Millisecond)

	cl, err := report.ScanLog(logPath)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}
	t.Logf("events=%d endpoints=%v transitions=%d statuses=%v",
		len(cl.Watcher.Events), cl.Watcher.Endpoints,
		cl.Watcher.Transitions, cl.Watcher.SeenStatuses)

	results, err := report.MatchFeatures(cl, []string{featureDir(t)})
	if err != nil {
		t.Fatalf("MatchFeatures: %v", err)
	}
	output := report.Format(results, cl)
	t.Log("\n" + output)

	// Verify smoke-alarm-network scenarios
	var smokeResult *report.FeatureResult
	for i := range results {
		if results[i].Domain == "smoke-alarm-network" {
			smokeResult = &results[i]
		}
	}
	if smokeResult == nil {
		t.Fatal("smoke-alarm-network feature not found")
	}
	t.Logf("smoke-alarm-network: pass=%d fail=%d nocov=%d unver=%d",
		smokeResult.PassCount, smokeResult.FailCount,
		smokeResult.NoCoverage, smokeResult.Unverifiable)

	if smokeResult.PassCount == 0 {
		t.Error("expected smoke-alarm-network scenarios to pass with real alarm data")
	}
	if smokeResult.FailCount > 0 {
		for _, sc := range smokeResult.Scenarios {
			if sc.Status == report.StatusFail {
				t.Errorf("FAIL: %s — %s", sc.ScenarioName, sc.Note)
			}
		}
	}

	// Verify specific scenarios
	for _, sc := range smokeResult.Scenarios {
		lower := strings.ToLower(sc.ScenarioName)
		switch {
		case strings.Contains(lower, "poll"):
			if sc.Status != report.StatusPass {
				t.Errorf("poll scenario: %s", sc.Status)
			}
		case strings.Contains(lower, "multiple smoke-alarm"):
			if sc.Status != report.StatusPass {
				t.Errorf("multiple-endpoints scenario: %s", sc.Status)
			}
		case strings.Contains(lower, "graceful"):
			if sc.Status != report.StatusPass {
				t.Errorf("graceful-failure scenario: %s", sc.Status)
			}
		case strings.Contains(lower, "light update on status change"):
			if sc.Status != report.StatusPass {
				t.Errorf("state-change scenario: %s (note: %s)", sc.Status, sc.Note)
			}
		}
	}

	// Full report: no failures, MCP passes
	fail, mcpPass := 0, 0
	for _, fr := range results {
		fail += fr.FailCount
		if fr.Domain == "mcp-server" || fr.Domain == "lights" {
			mcpPass += fr.PassCount
		}
	}
	if fail > 0 {
		t.Errorf("%d failing scenarios across all features", fail)
	}
	if mcpPass == 0 {
		t.Error("expected MCP scenarios to pass")
	}

	// Write topology manifest for post-mortem inspection
	manifest := map[string]interface{}{
		"healthy_alarm":    alarmHealthy.StatusURL,
		"transition_alarm": alarmTransition.StatusURL,
		"dead_alarm":       alarmDead.StatusURL,
		"log":              logPath,
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(t.TempDir(), "topology.json"), data, 0o644)
}
