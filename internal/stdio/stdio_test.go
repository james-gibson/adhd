package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestStdioHelperProcess is not a test: with GO_WANT_STDIO_HELPER=1 the test
// binary serves MCP over stdio, which is the cheapest spec-compliant child a Go
// test can spawn.
func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_STDIO_HELPER") != "1" {
		return
	}
	if os.Getenv("HELPER_STUBBORN") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if path := os.Getenv("HELPER_CHILD_PID_FILE"); path != "" {
		// A wrapper's grandchild: same process group, so teardown must reach it.
		child := childProcess()
		_ = child.Start()
		_ = os.WriteFile(path, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	}

	server := &helperServer{
		in:  bufio.NewReaderSize(os.Stdin, 64<<10),
		out: bufio.NewWriter(os.Stdout),
	}
	defer func() { _ = server.out.Flush() }()
	server.serve()
	os.Exit(0)
}

type helperServer struct {
	in  *bufio.Reader
	out *bufio.Writer
	mu  sync.Mutex

	bridgeAnswered bool
}

func (h *helperServer) serve() {
	for {
		line, err := h.in.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method,omitempty"`
			Params  json.RawMessage `json:"params,omitempty"`
			Error   *struct {
				Code int `json:"code"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		if msg.Method == "" {
			if msg.Error != nil && string(msg.ID) == "999" {
				h.mu.Lock()
				h.bridgeAnswered = true
				h.mu.Unlock()
			}
			continue
		}

		switch msg.Method {
		case "initialize":
			h.reply(msg.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]string{"name": "helper", "version": "1"},
			})
			if os.Getenv("HELPER_SEND_REQUEST") == "1" {
				h.write(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      999,
					"method":  "roots/list",
				})
				h.write(map[string]interface{}{
					"jsonrpc": "2.0",
					"method":  "notifications/message",
					"params":  map[string]string{"level": "info", "data": "banner"},
				})
			}
		case "notifications/initialized":
			// A notification has no response.
		case "tools/list":
			tools := []map[string]interface{}{
				{"name": "echo", "description": "echo a value", "inputSchema": map[string]interface{}{"type": "object"}},
				{"name": "upper", "description": "uppercase a value", "inputSchema": map[string]interface{}{"type": "object"}},
				{"name": "fail", "description": "always fails", "inputSchema": map[string]interface{}{"type": "object"}},
			}
			h.mu.Lock()
			if h.bridgeAnswered {
				tools = append(tools, map[string]interface{}{"name": "bridge-answered", "inputSchema": map[string]interface{}{"type": "object"}})
			}
			h.mu.Unlock()
			h.reply(msg.ID, map[string]interface{}{"tools": tools})
		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			value, _ := params.Arguments["value"].(string)
			switch params.Name {
			case "echo":
				h.reply(msg.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": value}},
				})
			case "upper":
				h.reply(msg.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": strings.ToUpper(value)}},
				})
			case "slow":
				time.Sleep(5 * time.Second)
				h.reply(msg.ID, map[string]interface{}{"content": []map[string]string{{"type": "text", "text": "late"}}})
			default:
				h.write(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      json.RawMessage(msg.ID),
					"error":   map[string]interface{}{"code": -32000, "message": "tool failed: " + params.Name},
				})
			}
		default:
			h.write(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(msg.ID),
				"error":   map[string]interface{}{"code": -32601, "message": "method not found"},
			})
		}
	}
}

func (h *helperServer) reply(id json.RawMessage, result interface{}) {
	h.write(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	})
}

func (h *helperServer) write(payload interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = h.out.Write(append(data, '\n'))
	_ = h.out.Flush()
}

// childProcess is the grandchild a wrapper would spawn: a long sleep in the
// same process group.
func childProcess() *exec.Cmd {
	return exec.Command("/bin/sh", "-c", "sleep 300")
}

func helperTarget(t *testing.T, extraEnv map[string]string) Target {
	t.Helper()
	env := map[string]string{"GO_WANT_STDIO_HELPER": "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return Target{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestStdioHelperProcess"},
		Env:     env,
		Timeout: 10 * time.Second,
	}
}

func TestListDoesNotStartATarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	states := m.List()
	if len(states) != 1 || states[0].Running {
		t.Fatalf("List = %+v, want one stopped target (listing must not spawn)", states)
	}
}

func TestListIsNamespacedByTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	result, err := m.Call(ctx, "fake", "tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"echo"`) {
		t.Fatalf("tools/list = %s", raw)
	}

	states := m.List()
	if len(states) != 1 || !states[0].Running {
		t.Fatalf("List = %+v, want a running target after a call", states)
	}
	if len(states[0].Tools) != 3 {
		t.Fatalf("cached tools = %v, want 3", states[0].Tools)
	}
}

func TestCallForwardsToolsCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	result, err := m.Call(ctx, "fake", "tools/call", map[string]interface{}{
		"name":      "upper",
		"arguments": map[string]interface{}{"value": "quiet"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "QUIET") {
		t.Fatalf("tools/call = %s, want QUIET", raw)
	}
}

func TestConcurrentCallsAreDemultiplexed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := fmt.Sprintf("v%d", i)
			result, err := m.Call(ctx, "fake", "tools/call", map[string]interface{}{
				"name":      "echo",
				"arguments": map[string]interface{}{"value": value},
			})
			if err != nil {
				errs <- err
				return
			}
			raw, _ := json.Marshal(result)
			if !strings.Contains(string(raw), value) {
				errs <- fmt.Errorf("call %d got %s, want %s", i, raw, value)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestChildErrorIsReturnedAsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	_, err := m.Call(ctx, "fake", "tools/call", map[string]interface{}{"name": "fail"})
	if err == nil || !strings.Contains(err.Error(), "tool failed") {
		t.Fatalf("err = %v, want the child's error", err)
	}
}

func TestCallTimeoutIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := helperTarget(t, nil)
	target.Timeout = 2 * time.Second
	m := NewManager(ctx, []Target{target})
	defer func() { _ = m.Close() }()

	start := time.Now()
	_, err := m.Call(ctx, "fake", "tools/call", map[string]interface{}{"name": "slow"})
	if err == nil {
		t.Fatal("a child that never answered returned success")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("call took %s; the timeout was not applied", elapsed)
	}
}

func TestInitializeIsReserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	if _, err := m.Call(ctx, "fake", "initialize", nil); err == nil {
		t.Fatal("a caller was allowed to re-initialize the child session")
	}
}

func TestUnknownTargetIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, []Target{helperTarget(t, nil)})
	defer func() { _ = m.Close() }()

	_, err := m.Call(ctx, "not-configured", "tools/list", nil)
	if !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("err = %v, want ErrUnknownTarget", err)
	}
}

func TestAllowedToolsIsEnforced(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := helperTarget(t, nil)
	target.AllowedTools = []string{"echo"}
	m := NewManager(ctx, []Target{target})
	defer func() { _ = m.Close() }()

	if _, err := m.Call(ctx, "fake", "tools/call", map[string]interface{}{"name": "upper"}); err == nil {
		t.Fatal("a tool outside allowed_tools was forwarded")
	}
	if _, err := m.Call(ctx, "fake", "tools/call", map[string]interface{}{"name": "echo"}); err != nil {
		t.Fatalf("an allowed tool was refused: %v", err)
	}
}

func TestServerToClientRequestIsAnswered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	target := helperTarget(t, map[string]string{"HELPER_SEND_REQUEST": "1"})
	m := NewManager(ctx, []Target{target})
	defer func() { _ = m.Close() }()

	result, err := m.Call(ctx, "fake", "tools/list", nil)
	if err != nil {
		t.Fatalf("tools/list after a server request: %v", err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "bridge-answered") {
		t.Fatalf("tools/list = %s; the child did not see an error answer to its roots/list", raw)
	}
}

func TestEnvIsNotInherited(t *testing.T) {
	t.Setenv("ADHD_TEST_SECRET", "do-not-leak")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := helperTarget(t, nil)
	m := NewManager(ctx, []Target{target})
	env := buildEnv(target)
	for _, entry := range env {
		if strings.Contains(entry, "ADHD_TEST_SECRET") {
			t.Fatalf("buildEnv inherited %q; a child must not see the parent environment", entry)
		}
	}
	_ = m.Close()
}

func TestInheritEnvIsExplicit(t *testing.T) {
	t.Setenv("ADHD_TEST_PASS", "by-request")
	target := Target{Name: "x", Command: "true", InheritEnv: []string{"ADHD_TEST_PASS"}}
	env := buildEnv(target)
	found := false
	for _, entry := range env {
		if entry == "ADHD_TEST_PASS=by-request" {
			found = true
		}
	}
	if !found {
		t.Fatalf("env = %v, want the named variable passed through", env)
	}
}

func TestCloseKillsTheProcessGroup(t *testing.T) {
	pidFile := t.TempDir() + "/grandchild.pid"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := helperTarget(t, map[string]string{"HELPER_CHILD_PID_FILE": pidFile})
	m := NewManager(ctx, []Target{target})

	if _, err := m.Call(ctx, "fake", "tools/list", nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived the session teardown; the group was not killed", pid)
}

func TestCloseEscalatesToSigkill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := helperTarget(t, map[string]string{"HELPER_STUBBORN": "1"})
	m := NewManager(ctx, []Target{target})
	if _, err := m.Call(ctx, "fake", "tools/list", nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	start := time.Now()
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Close took %s; SIGKILL escalation is not bounded", elapsed)
	}
}

func TestCrashedChildIsCooledDownNotRespawning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A command that exits immediately: the session starts, the handshake
	// fails, and the next call must be refused by the cooldown rather than
	// spawning again.
	target := Target{Name: "dead", Command: "/bin/sh", Args: []string{"-c", "exit 1"}, Timeout: 2 * time.Second}
	m := NewManager(ctx, []Target{target})
	defer func() { _ = m.Close() }()

	if _, err := m.Call(ctx, "dead", "tools/list", nil); err == nil {
		t.Fatal("a dead target answered")
	}
	_, err := m.Call(ctx, "dead", "tools/list", nil)
	if err == nil || !strings.Contains(err.Error(), "cooldown") {
		t.Fatalf("second call err = %v, want a cooldown refusal", err)
	}
}
