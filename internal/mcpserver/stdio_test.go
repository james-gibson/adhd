package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/stdio"
)

// TestStdioHelperProcess is not a test: with GO_WANT_STDIO_HELPER=1 the test
// binary serves MCP over stdio, so the bridge tests drive a real child.
func TestStdioHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_STDIO_HELPER") != "1" {
		return
	}
	in := bufio.NewReaderSize(os.Stdin, 64<<10)
	out := bufio.NewWriter(os.Stdout)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id,omitempty"`
			Method string          `json:"method,omitempty"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		write := func(payload map[string]interface{}) {
			data, _ := json.Marshal(payload)
			_, _ = out.Write(append(data, '\n'))
			_ = out.Flush()
		}
		switch msg.Method {
		case "initialize":
			write(map[string]interface{}{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
				"serverInfo":      map[string]string{"name": "helper", "version": "1"},
			}})
		case "notifications/initialized":
		case "tools/list":
			write(map[string]interface{}{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]interface{}{
				"tools": []map[string]interface{}{
					{"name": "echo", "description": "echo a value", "inputSchema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{"value": map[string]interface{}{"type": "string"}},
					}},
					{"name": "search", "inputSchema": map[string]interface{}{"type": "object"}},
				},
			}})
		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			value, _ := params.Arguments["value"].(string)
			if params.Name != "echo" {
				write(map[string]interface{}{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]interface{}{"code": -32601, "message": "no such tool"}})
				continue
			}
			write(map[string]interface{}{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]interface{}{
				"content": []map[string]string{{"type": "text", "text": "echo:" + value}},
			}})
		default:
			write(map[string]interface{}{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]interface{}{"code": -32601, "message": "method not found"}})
		}
	}
}

func fakeStdioManager(t *testing.T) *stdio.Manager {
	t.Helper()
	m := stdio.NewManager(context.Background(), []stdio.Target{{
		Name:    "fake",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestStdioHelperProcess"},
		Env:     map[string]string{"GO_WANT_STDIO_HELPER": "1"},
		Timeout: 10 * time.Second,
	}})
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// stdioServer is a server with the stdio bridge installed but the gate off.
type stdioServer struct {
	*gatedServer
}

func startStdioServer(t *testing.T) *stdioServer {
	t.Helper()
	cluster := lights.NewCluster()
	addr := freeAddr(t)
	server := NewServer(config.MCPServerConfig{Enabled: true, Addr: addr}, cluster)

	g := &gatedServer{addr: addr, server: server}
	server.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		g.mu.Lock()
		g.events = append(g.events, securityEvent{level: level, event: event, details: details})
		g.mu.Unlock()
	})
	server.SetStdioManager(fakeStdioManager(t))
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	time.Sleep(50 * time.Millisecond)
	return &stdioServer{gatedServer: g}
}

// toolText unwraps MCP's tools/call envelope to the JSON text it carries.
func toolText(t *testing.T, result map[string]interface{}) string {
	t.Helper()
	res, ok := result["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("no result in %v", result)
	}
	content, _ := res["content"].([]interface{})
	if len(content) == 0 {
		t.Fatalf("empty content in %v", res)
	}
	first, _ := content[0].(map[string]interface{})
	text, _ := first["text"].(string)
	if text == "" {
		t.Fatalf("no text in %v", first)
	}
	return text
}

func TestStdioBridgeIsRegisteredPerTarget(t *testing.T) {
	g := startStdioServer(t)

	names := toolNames(t, g.call(t, "tools/list", nil, ""))
	for _, want := range []string{"adhd.stdio.fake.call", "adhd.stdio.list"} {
		if !contains(names, want) {
			t.Fatalf("tools/list missing %q: %v", want, names)
		}
	}
	// Registration is lazy: the child has not started, so its tools are not
	// part of the surface yet, and no process was spawned by enumerating.
	for _, name := range names {
		if strings.HasPrefix(name, "adhd.stdio.fake.echo") {
			t.Fatalf("child tool %q registered before the session started: %v", name, names)
		}
	}
}

func TestStdioBridgeListsAndCallsThroughTheChild(t *testing.T) {
	g := startStdioServer(t)

	listed := g.call(t, "tools/call", map[string]interface{}{
		"name":      "adhd.stdio.fake.call",
		"arguments": map[string]interface{}{"method": "tools/list"},
	}, "")
	text := toolText(t, listed)
	if !strings.Contains(text, `"echo"`) || !strings.Contains(text, `"search"`) {
		t.Fatalf("bridged tools/list = %s", text)
	}

	echoed := g.call(t, "tools/call", map[string]interface{}{
		"name": "adhd.stdio.fake.call",
		"arguments": map[string]interface{}{
			"tool":      "echo",
			"arguments": map[string]interface{}{"value": "hello"},
		},
	}, "")
	if text := toolText(t, echoed); !strings.Contains(text, "echo:hello") {
		t.Fatalf("bridged tools/call = %s, want echo:hello", text)
	}

	// Once the session started, the child's tools are namespaced surface
	// entries: <target>.<tool>, not a bare name that could shadow another.
	names := toolNames(t, g.call(t, "tools/list", nil, ""))
	for _, want := range []string{"adhd.stdio.fake.echo", "adhd.stdio.fake.search"} {
		if !contains(names, want) {
			t.Fatalf("namespaced tool %q missing: %v", want, names)
		}
	}

	// The child's input schema travels with the namespaced entry, so a client
	// can actually fill the call in.
	listedTools, _ := g.call(t, "tools/list", nil, "")["result"].(map[string]interface{})
	tools, _ := listedTools["tools"].([]interface{})
	for _, entry := range tools {
		tool, _ := entry.(map[string]interface{})
		if tool["name"] != "adhd.stdio.fake.echo" {
			continue
		}
		schema, _ := tool["inputSchema"].(map[string]interface{})
		props, _ := schema["properties"].(map[string]interface{})
		if _, ok := props["value"]; !ok {
			t.Fatalf("namespaced tool lost the child's schema: %v", schema)
		}
	}

	direct := g.call(t, "tools/call", map[string]interface{}{
		"name":      "adhd.stdio.fake.echo",
		"arguments": map[string]interface{}{"value": "direct"},
	}, "")
	if text := toolText(t, direct); !strings.Contains(text, "echo:direct") {
		t.Fatalf("namespaced tool call = %s", text)
	}
}

func TestAdhdProxyRefusesAStdioEndpoint(t *testing.T) {
	g := startStdioServer(t)

	result := g.call(t, "tools/call", map[string]interface{}{
		"name": "adhd.proxy",
		"arguments": map[string]interface{}{
			"target_endpoint": "stdio:uvx mcp-server-qdrant",
			"call":            map[string]interface{}{"method": "tools/list"},
		},
	}, "")
	errObj, ok := result["error"].(map[string]interface{})
	if !ok || errObj["code"].(float64) != -32602 {
		t.Fatalf("adhd.proxy stdio target = %v, want -32602", result)
	}

	events := g.filed()
	if len(events) != 1 || events[0].event != "stdio_knock" {
		t.Fatalf("events = %+v, want one stdio_knock", events)
	}
	if events[0].details["target_endpoint"] != "stdio:uvx mcp-server-qdrant" {
		t.Fatalf("knock details = %+v", events[0].details)
	}
}

func TestAdhdProxyRefusesACallerSuppliedCommand(t *testing.T) {
	g := startStdioServer(t)

	result := g.call(t, "adhd.proxy", map[string]interface{}{
		"command": "sh",
		"args":    []string{"-c", "id"},
		"call":    map[string]interface{}{"method": "tools/list"},
	}, "")
	if result["error"] == nil {
		t.Fatalf("a command-shaped proxy request was accepted: %v", result)
	}
	events := g.filed()
	if len(events) != 1 || events[0].event != "stdio_knock" {
		t.Fatalf("events = %+v, want one stdio_knock", events)
	}
}

func TestProjectEndpointResolvesAStdioTarget(t *testing.T) {
	g := startStdioServer(t)

	g.server.registerProjectTool("cluster", "qdrant", "qdrant", "stdio:fake")

	result := g.call(t, "tools/call", map[string]interface{}{
		"name":      "adhd.project.qdrant.call",
		"arguments": map[string]interface{}{"method": "tools/list"},
	}, "")
	if text := toolText(t, result); !strings.Contains(text, `"echo"`) {
		t.Fatalf("project->stdio tools/list = %s", text)
	}
}

func TestStdioCollisionIsReportedNotDropped(t *testing.T) {
	g := startStdioServer(t)

	// Something else already holds the namespaced name.
	if outcome := g.server.registerDynamic(&dynamicTool{
		name:   "adhd.stdio.fake.search",
		origin: "someone-else",
	}); outcome != registrationAdded {
		t.Fatalf("seed registration = %v", outcome)
	}

	g.server.registerStdioTools("fake", []stdio.Tool{
		{Name: "echo", InputSchema: []byte(`{"type":"object","properties":{"value":{"type":"string"}}}`)},
		{Name: "search"},
	})

	list := g.call(t, "tools/call", map[string]interface{}{
		"name":      "adhd.stdio.list",
		"arguments": map[string]interface{}{},
	}, "")
	text := toolText(t, list)
	if !strings.Contains(text, "adhd.stdio.fake.search") {
		t.Fatalf("collision not reported: %s", text)
	}
}

func TestStdioToolsAreAtTheExecTier(t *testing.T) {
	g := startGatedServer(t)
	manager := fakeStdioManager(t)
	g.server.SetStdioManager(manager)

	read := mustKey(t, g.ring, "read")
	names := toolNames(t, g.call(t, "tools/list", g.signed(t, read, "tools/list", nil), ""))
	for _, name := range names {
		if strings.HasPrefix(name, "adhd.stdio") {
			t.Fatalf("read capability saw %q", name)
		}
	}

	write := mustKey(t, g.ring, "write")
	names = toolNames(t, g.call(t, "tools/list", g.signed(t, write, "tools/list", nil), ""))
	for _, name := range names {
		if strings.HasPrefix(name, "adhd.stdio") {
			t.Fatalf("write capability saw %q", name)
		}
	}

	exec := mustKey(t, g.ring, "exec")
	names = toolNames(t, g.call(t, "tools/list", g.signed(t, exec, "tools/list", nil), ""))
	if !contains(names, "adhd.stdio.fake.call") {
		t.Fatalf("exec capability did not see the bridge: %v", names)
	}
}
