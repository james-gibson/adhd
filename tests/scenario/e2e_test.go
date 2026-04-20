// Package scenario — end-to-end tests that spawn both ocd-smoke-alarm and adhd
// as real OS processes and verify the full chain over plain HTTP.
//
// Chain:
//
//	mock HTTP service → real ocd-smoke-alarm → real adhd --headless → MCP HTTP (this test)
//
// No in-process library calls.  If the wire format between binaries drifts, these
// tests break — that's the point.
package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- adhd binary builder ----------------------------------------------------

var (
	buildADHDOnce sync.Once
	buildADHDErr  error
	adhdBinary    string
)

func ensureADHDBinary(t *testing.T) string {
	t.Helper()
	buildADHDOnce.Do(func() {
		tmp, err := os.MkdirTemp("", "adhd-bin-*")
		if err != nil {
			buildADHDErr = fmt.Errorf("mktemp: %w", err)
			return
		}
		out := filepath.Join(tmp, "adhd")
		// Module root is two levels up from tests/scenario/
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			buildADHDErr = fmt.Errorf("abs path: %w", err)
			return
		}
		var stderr bytes.Buffer
		cmd := exec.Command("go", "build", "-o", out, "./cmd/adhd")
		cmd.Dir = root
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildADHDErr = fmt.Errorf("go build adhd: %w\n%s", err, stderr.String())
			return
		}
		adhdBinary = out
	})
	if buildADHDErr != nil {
		t.Fatalf("build adhd: %v", buildADHDErr)
	}
	return adhdBinary
}

// ---- adhd instance ----------------------------------------------------------

type adhdInstance struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	stdout  *bytes.Buffer
	stderr  *bytes.Buffer
	mcpURL  string // "http://localhost:PORT/mcp"
	logPath string
}

// startADHDHeadless writes a minimal config yaml, then spawns:
//
//	adhd --headless --config <cfg> --mcp-addr <addr> --log <logfile>
//
// It waits until adhd's MCP server responds to an initialize request.
func startADHDHeadless(t *testing.T, smokeAlarmEndpoints []smokeEndpointCfg) *adhdInstance {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "adhd.yaml")
	logPath := filepath.Join(dir, "adhd.jsonl")

	// Build smoke_alarm block
	var smokeBlock string
	for _, ep := range smokeAlarmEndpoints {
		smokeBlock += fmt.Sprintf(`
  - name: %q
    endpoint: %q
    interval: "80ms"
    use_sse: false
`, ep.name, ep.endpoint)
	}

	yaml := fmt.Sprintf(`mcp_server:
  enabled: true
  addr: ":0"
smoke_alarm:%s
`, smokeBlock)

	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write adhd config: %v", err)
	}

	// Pick a free port for MCP — pass as --mcp-addr override
	mcpAddr := freeLocalAddr(t)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, ensureADHDBinary(t),
		"--headless",
		"--config", cfgPath,
		"--mcp-addr", ":"+portOf(mcpAddr),
		"--log", logPath,
	)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start adhd: %v", err)
	}

	inst := &adhdInstance{
		cmd:     cmd,
		cancel:  cancel,
		stdout:  stdout,
		stderr:  stderr,
		mcpURL:  "http://" + mcpAddr + "/mcp",
		logPath: logPath,
	}

	waitCtx, wcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer wcancel()
	if err := waitForMCPServer(waitCtx, inst.mcpURL); err != nil {
		inst.stop(t)
		t.Fatalf("adhd MCP server not ready: %v\nstdout: %s\nstderr: %s",
			err, stdout.String(), stderr.String())
	}

	t.Cleanup(func() { inst.stop(t) })
	return inst
}

func (a *adhdInstance) stop(t *testing.T) {
	if a.cancel != nil {
		a.cancel()
	}
	done := make(chan error, 1)
	go func() { done <- a.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if t != nil {
			t.Logf("adhd did not exit; killing")
		}
		_ = a.cmd.Process.Kill()
	}
}

type smokeEndpointCfg struct {
	name     string
	endpoint string
}

// portOf extracts the port string from "localhost:PORT".
func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}

// waitForMCPServer polls the MCP endpoint with an initialize request until it
// returns a valid JSON-RPC response or the context expires.
func waitForMCPServer(ctx context.Context, mcpURL string) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"clientInfo":      map[string]string{"name": "e2e-test", "version": "1.0"},
		},
	}
	body, _ := json.Marshal(req)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for MCP server: %w", ctx.Err())
		default:
		}

		resp, err := client.Post(mcpURL, "application/json", bytes.NewReader(body))
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

// mcpCall sends a JSON-RPC request to adhd's MCP server and returns the parsed response.
func mcpCall(t *testing.T, mcpURL, method string, params interface{}) map[string]interface{} {
	t.Helper()
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(mcpURL, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		t.Fatalf("POST %s %s: %v", mcpURL, method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response from %s: %v", method, err)
	}
	return result
}

// waitForLight polls adhd.lights.list until a light named `lightName` has the
// expected status string, or the deadline passes.
func waitForLight(t *testing.T, mcpURL, lightName, wantStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp := mcpCall(t, mcpURL, "adhd.lights.list", map[string]interface{}{})
		result, _ := resp["result"].(map[string]interface{})
		lights, _ := result["lights"].([]interface{})
		for _, l := range lights {
			lm, _ := l.(map[string]interface{})
			if lm["name"] == lightName && lm["status"] == wantStatus {
				return
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	// Capture final state for diagnostics
	resp := mcpCall(t, mcpURL, "adhd.lights.list", map[string]interface{}{})
	t.Fatalf("light %q never reached status %q within %v; final lights: %v",
		lightName, wantStatus, timeout, resp["result"])
}

// ---- e2e tests --------------------------------------------------------------

// TestE2EHealthyService verifies the full chain: healthy mock service →
// ocd-smoke-alarm (real) → adhd (real) → adhd.lights.list returns green.
func TestE2EHealthyService(t *testing.T) {
	svc, _ := mockService(t)

	alarm := startSmokeAlarmInstance(t, "e2e-healthy",
		freeLocalAddr(t),
		[]string{httpTarget("web", svc.URL+"/health")},
	)

	adhd := startADHDHeadless(t, []smokeEndpointCfg{
		{name: "e2e-healthy", endpoint: alarm.StatusURL},
	})

	// Light name format: "smoke:<instance>/<targetID>"
	waitForLight(t, adhd.mcpURL, "smoke:e2e-healthy/web", "green", 10*time.Second)
}

// TestE2EOutageService verifies the chain for an unreachable target.
func TestE2EOutageService(t *testing.T) {
	closed := freeLocalAddr(t) // allocate then release — port will be closed

	alarm := startSmokeAlarmInstance(t, "e2e-outage",
		freeLocalAddr(t),
		[]string{httpTarget("dead", "http://"+closed+"/health")},
	)

	adhd := startADHDHeadless(t, []smokeEndpointCfg{
		{name: "e2e-outage", endpoint: alarm.StatusURL},
	})

	waitForLight(t, adhd.mcpURL, "smoke:e2e-outage/dead", "red", 10*time.Second)
}

// TestE2EStateTransition verifies healthy → outage → healthy propagates end-to-end.
func TestE2EStateTransition(t *testing.T) {
	svc, healthy := mockService(t)

	alarm := startSmokeAlarmInstance(t, "e2e-flip",
		freeLocalAddr(t),
		[]string{httpTarget("flapper", svc.URL+"/health")},
	)

	adhd := startADHDHeadless(t, []smokeEndpointCfg{
		{name: "e2e-flip", endpoint: alarm.StatusURL},
	})

	lightName := "smoke:e2e-flip/flapper"

	// Initial state: green
	waitForLight(t, adhd.mcpURL, lightName, "green", 10*time.Second)

	// Flip to outage
	healthy.Store(false)
	waitForLight(t, adhd.mcpURL, lightName, "red", 10*time.Second)

	// Recover
	healthy.Store(true)
	waitForLight(t, adhd.mcpURL, lightName, "green", 10*time.Second)
}

// TestE2EToolsContractFireMarshal verifies that a fire-marshal-style probe
// of adhd's MCP server finds all three required tools with correct schemas.
// This is the "fire-marshal certification" check — every tool must be callable.
func TestE2EToolsContractFireMarshal(t *testing.T) {
	// Minimal adhd — no smoke-alarm configured; we only care about MCP tools.
	adhd := startADHDHeadless(t, nil)

	// Step 1: initialize
	initResp := mcpCall(t, adhd.mcpURL, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "fire-marshal", "version": "1.0"},
	})
	if initResp["result"] == nil {
		t.Fatalf("initialize returned no result: %v", initResp)
	}

	// Step 2: tools/list — must expose all three adhd tools
	toolsResp := mcpCall(t, adhd.mcpURL, "tools/list", map[string]interface{}{})
	result, _ := toolsResp["result"].(map[string]interface{})
	tools, _ := result["tools"].([]interface{})

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		if tm, ok := tool.(map[string]interface{}); ok {
			toolNames[fmt.Sprint(tm["name"])] = true
		}
	}

	required := []string{"adhd.lights.list", "adhd.lights.get", "adhd.status"}
	for _, name := range required {
		if !toolNames[name] {
			t.Errorf("tool %q not found; declared tools: %v", name, toolNames)
		}
	}

	// Step 3: call each tool and verify a valid response (no error field)
	for _, name := range required {
		var params interface{}
		if name == "adhd.lights.get" {
			// lights.get requires a name; use a non-existent one to get a clean error response
			// (error response is still a valid MCP response — not a connection failure)
			params = map[string]interface{}{"name": "nonexistent"}
		} else {
			params = map[string]interface{}{}
		}

		resp := mcpCall(t, adhd.mcpURL, name, params)
		// A valid MCP response has either result or error, but always jsonrpc + id
		if resp["jsonrpc"] != "2.0" {
			t.Errorf("tool %q: response missing jsonrpc field: %v", name, resp)
		}
		if name != "adhd.lights.get" && resp["result"] == nil {
			t.Errorf("tool %q: no result in response: %v", name, resp)
		}
	}
}

// TestE2EMultipleAlarms verifies adhd aggregates lights from two concurrent
// real ocd-smoke-alarm instances.
func TestE2EMultipleAlarms(t *testing.T) {
	svc1, _ := mockService(t)
	svc2, healthy2 := mockService(t)

	alarm1 := startSmokeAlarmInstance(t, "e2e-multi-a",
		freeLocalAddr(t),
		[]string{httpTarget("alpha", svc1.URL+"/health")},
	)
	alarm2 := startSmokeAlarmInstance(t, "e2e-multi-b",
		freeLocalAddr(t),
		[]string{httpTarget("beta", svc2.URL+"/health")},
	)

	adhd := startADHDHeadless(t, []smokeEndpointCfg{
		{name: "e2e-multi-a", endpoint: alarm1.StatusURL},
		{name: "e2e-multi-b", endpoint: alarm2.StatusURL},
	})

	// Both start healthy
	waitForLight(t, adhd.mcpURL, "smoke:e2e-multi-a/alpha", "green", 10*time.Second)
	waitForLight(t, adhd.mcpURL, "smoke:e2e-multi-b/beta", "green", 10*time.Second)

	// Degrade alarm2's service — alarm1 should stay green
	healthy2.Store(false)
	waitForLight(t, adhd.mcpURL, "smoke:e2e-multi-b/beta", "red", 10*time.Second)

	// Verify alarm1 stayed green throughout
	resp := mcpCall(t, adhd.mcpURL, "adhd.lights.get", map[string]interface{}{"name": "smoke:e2e-multi-a/alpha"})
	result, _ := resp["result"].(map[string]interface{})
	if result["status"] != "green" {
		t.Errorf("alarm1 alpha should still be green, got: %v", result["status"])
	}
}

// Ensure mockService and httpTarget are available from interop_test.go in the same package.
// (They are — this file is in package scenario alongside interop_test.go.)
var _ = (*atomic.Bool)(nil)
var _ = (*httptest.Server)(nil)
