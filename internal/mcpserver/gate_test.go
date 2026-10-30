package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/entitle"
	"github.com/james-gibson/adhd/internal/lights"
)

// gatedServer is a running MCP server with a generated keyring and a recorder
// for the security events it files.
type gatedServer struct {
	addr   string
	server *Server
	ring   *entitle.Keyring
	gate   *entitle.Gate

	mu     sync.Mutex
	events []securityEvent
}

type securityEvent struct {
	level   string
	event   string
	details map[string]interface{}
}

func startGatedServer(t *testing.T) *gatedServer {
	t.Helper()
	ring, err := entitle.GenerateKeyring(t.TempDir() + "/gate.key")
	if err != nil {
		t.Fatalf("GenerateKeyring: %v", err)
	}
	gate, err := entitle.NewGate(ring, time.Minute)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}

	cluster := lights.NewCluster()
	cluster.Add(&lights.Light{Name: "api", Type: "mcp", Status: lights.StatusGreen})

	addr := freeAddr(t)
	server := NewServer(config.MCPServerConfig{Enabled: true, Addr: addr}, cluster)
	server.SetGate(gate)

	g := &gatedServer{addr: addr, server: server, ring: ring, gate: gate}
	server.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		g.mu.Lock()
		g.events = append(g.events, securityEvent{level: level, event: event, details: details})
		g.mu.Unlock()
	})
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond)
	return g
}

// call posts one JSON-RPC request, with an optional Authorization header.
func (g *gatedServer) call(t *testing.T, method string, params interface{}, authorization string) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+g.addr+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var decoded map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// signed wraps params with a _shield signed by key.
func (g *gatedServer) signed(t *testing.T, key entitle.Key, method string, params map[string]interface{}) map[string]interface{} {
	t.Helper()
	if params == nil {
		params = map[string]interface{}{}
	}
	clean := make(map[string]interface{}, len(params))
	for k, v := range params {
		clean[k] = v
	}
	canonical, err := entitle.Canonical(clean)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	nonce := "n-" + time.Now().Format("150405.000000")
	epoch := entitle.CurrentEpoch(now, time.Minute)
	sig := entitle.Signature(key.Secret, method, canonical, now.Unix(), nonce, key.Rung, key.ID, &epoch)
	clean[entitle.ShieldParam] = map[string]interface{}{
		"kid":   key.ID,
		"ts":    now.Unix(),
		"nonce": nonce,
		"rung":  key.Rung,
		"epoch": epoch,
		"sig":   sig,
	}
	return clean
}

func mustKey(t *testing.T, ring *entitle.Keyring, id string) entitle.Key {
	t.Helper()
	key, ok := ring.Key(id)
	if !ok {
		t.Fatalf("keyring has no %q key", id)
	}
	return key
}

func (g *gatedServer) filed() []securityEvent {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]securityEvent, len(g.events))
	copy(out, g.events)
	return out
}

func toolNames(t *testing.T, result map[string]interface{}) []string {
	t.Helper()
	res, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("no result in %v", result)
	}
	tools, _ := res["tools"].([]interface{})
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		if m, ok := tool.(map[string]interface{}); ok {
			name, _ := m["name"].(string)
			names = append(names, name)
		}
	}
	return names
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestAnonymousCallerSeesTheEmptyFacade(t *testing.T) {
	g := startGatedServer(t)

	result := g.call(t, "tools/list", nil, "")
	if names := toolNames(t, result); len(names) != 0 {
		t.Fatalf("anonymous tools/list = %v, want empty", names)
	}

	status := g.call(t, "adhd.status", nil, "")
	res := status["result"].(map[string]interface{})
	if res["isotope"] != "" {
		t.Fatalf("facade leaked isotope: %v", res["isotope"])
	}
	if peers, _ := res["cluster_peers"].([]interface{}); len(peers) != 0 {
		t.Fatalf("facade leaked peers: %v", peers)
	}
	summary := res["lights"].(map[string]interface{})
	if summary["total"].(float64) != 0 {
		t.Fatalf("facade leaked lights: %v", summary)
	}

	if _, hasErr := g.call(t, "initialize", nil, "")["error"]; hasErr {
		t.Fatal("a client must be able to connect, even to an empty facade")
	}
}

func TestAnonymousDrivingARealToolFilesAnAlarm(t *testing.T) {
	g := startGatedServer(t)

	result := g.call(t, "adhd.gh.run", map[string]interface{}{"args": []string{"--version"}}, "")
	if result["error"] == nil {
		t.Fatalf("anonymous adhd.gh.run executed: %v", result)
	}

	result = g.call(t, "tools/call", map[string]interface{}{
		"name":      "adhd.gh.run",
		"arguments": map[string]interface{}{"args": []string{"--version"}},
	}, "")
	if errObj, ok := result["error"].(map[string]interface{}); !ok || errObj["code"].(float64) != -32601 {
		t.Fatalf("anonymous tools/call = %v, want -32601", result)
	}

	events := g.filed()
	if len(events) == 0 {
		t.Fatal("driving the real surface filed no security event")
	}
	for _, event := range events {
		if event.event != "unentitled_tool_call" {
			t.Fatalf("event = %q, want unentitled_tool_call", event.event)
		}
		if event.details["tool"] != "adhd.gh.run" {
			t.Fatalf("event tool = %v, want adhd.gh.run", event.details["tool"])
		}
	}
}

func TestAnUnentitledReadProbeDoesNotFileTheAlarm(t *testing.T) {
	g := startGatedServer(t)

	g.call(t, "adhd.status", nil, "")
	g.call(t, "tools/call", map[string]interface{}{"name": "adhd.status", "arguments": map[string]interface{}{}}, "")
	g.call(t, "adhd.rung.respond", map[string]interface{}{"feature_id": "f", "nonce": "n"}, "")

	if events := g.filed(); len(events) != 0 {
		t.Fatalf("a monitor reading the facade filed %d alarms; the signal would be buried: %+v", len(events), events)
	}
}

func TestGuessingAnUnknownMethodDoesNotSpamTheAlarm(t *testing.T) {
	g := startGatedServer(t)
	g.call(t, "not.a.real.method", nil, "")
	if events := g.filed(); len(events) != 0 {
		t.Fatalf("an unknown method filed %d events; guessing must not spam the alarm", len(events))
	}
}

func TestReadCapabilitySeesOnlyReadTools(t *testing.T) {
	g := startGatedServer(t)
	read := mustKey(t, g.ring, "read")

	result := g.call(t, "tools/list", g.signed(t, read, "tools/list", nil), "")
	names := toolNames(t, result)

	for _, want := range []string{"adhd.status", "adhd.lights.list", "adhd.isotope.instance"} {
		if !contains(names, want) {
			t.Fatalf("read tools/list missing %q: %v", want, names)
		}
	}
	for _, denied := range []string{"adhd.gh.run", "adhd.hurl.run", "adhd.proxy", "adhd.cluster.join"} {
		if contains(names, denied) {
			t.Fatalf("read tools/list exposed %q: %v", denied, names)
		}
	}
}

func TestReadCapabilityCannotInvokeAnExecTool(t *testing.T) {
	g := startGatedServer(t)
	read := mustKey(t, g.ring, "read")

	params := g.signed(t, read, "tools/call", map[string]interface{}{
		"name":      "adhd.gh.run",
		"arguments": map[string]interface{}{"args": []string{"--version"}},
	})
	result := g.call(t, "tools/call", params, "")
	errObj, ok := result["error"].(map[string]interface{})
	if !ok || errObj["code"].(float64) != -32601 {
		t.Fatalf("read key invoking adhd.gh.run = %v, want -32601", result)
	}
	events := g.filed()
	if len(events) != 1 || events[0].details["required"].(int) != capDanger {
		t.Fatalf("events = %+v, want one with required=capDanger", events)
	}
}

func TestCapabilityTiersAreActuallyDifferent(t *testing.T) {
	g := startGatedServer(t)

	// The generated keyring matches kb's: read=0, write=3, exec=6. The 6 tier is
	// "X" and sees everything, including the shell tools; the boundary that
	// matters is write (3), which must not reach the proxy or the exec tier.
	write := mustKey(t, g.ring, "write")
	names := toolNames(t, g.call(t, "tools/list", g.signed(t, write, "tools/list", nil), ""))
	if !contains(names, "adhd.cluster.join") {
		t.Fatalf("write tools/list missing adhd.cluster.join: %v", names)
	}
	for _, denied := range []string{"adhd.proxy", "adhd.path.list", "adhd.gh.run"} {
		if contains(names, denied) {
			t.Fatalf("write tools/list exposed %q: %v", denied, names)
		}
	}

	exec := mustKey(t, g.ring, "exec")
	names = toolNames(t, g.call(t, "tools/list", g.signed(t, exec, "tools/list", nil), ""))
	if !contains(names, "adhd.proxy") || !contains(names, "adhd.gh.run") {
		t.Fatalf("exec (tier 6) tools/list = %v, want proxy and shell", names)
	}
}

func TestBearerCapabilityIsAccepted(t *testing.T) {
	g := startGatedServer(t)
	exec := mustKey(t, g.ring, "exec")
	result := g.call(t, "tools/list", nil, "Bearer "+exec.ID+"."+exec.Secret)
	if names := toolNames(t, result); !contains(names, "adhd.proxy") {
		t.Fatalf("bearer tools/list = %v, want adhd.proxy", names)
	}
}

func TestSignatureIsStrippedBeforeDispatch(t *testing.T) {
	g := startGatedServer(t)
	read := mustKey(t, g.ring, "read")

	params := g.signed(t, read, "adhd.lights.get", map[string]interface{}{"name": "api"})
	result := g.call(t, "adhd.lights.get", params, "")
	res, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("adhd.lights.get = %v", result)
	}
	if res["name"] != "api" {
		t.Fatalf("light = %v, want api; _shield must not reach the handler", res)
	}
}

func TestBusyServerStillFiltersTheSurface(t *testing.T) {
	g := startGatedServer(t)
	// A dynamic project tool is exec: a read caller must not see it, and the
	// status count must not tell them it exists.
	g.server.registerProjectTool("cluster", "kb", "james-gibson/kb", "http://127.0.0.1:1/mcp")

	read := mustKey(t, g.ring, "read")
	result := g.call(t, "tools/list", g.signed(t, read, "tools/list", nil), "")
	if names := toolNames(t, result); contains(names, "adhd.project.kb.call") {
		t.Fatalf("read caller saw an exec project tool: %v", names)
	}

	status := g.call(t, "adhd.status", g.signed(t, read, "adhd.status", nil), "")
	res := status["result"].(map[string]interface{})
	if res["dynamic_tools"].(float64) != 0 {
		t.Fatalf("status counted hidden dynamic tools: %v", res["dynamic_tools"])
	}
}
