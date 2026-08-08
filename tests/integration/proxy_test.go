package integration

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

// startMCPOnAddr starts an MCP server on an explicit address (used so a proxy
// caller can reach the target deterministically).
func startMCPOnAddr(t *testing.T, addr string, cluster *lights.Cluster) {
	cfg := config.MCPServerConfig{
		Enabled: true,
		Addr:    addr,
	}
	server := mcpserver.NewServer(cfg, cluster)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("failed to start MCP server: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
}

// TestProxyDirectMethod verifies adhd.proxy forwards a call via the direct
// JSON-RPC method dispatch switch.
func TestProxyDirectMethod(t *testing.T) {
	// Target server that will receive the proxied adhd.status call.
	targetAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	startMCPOnAddr(t, targetAddr, lights.NewCluster())

	// Source server issuing the proxy request.
	srcAddr := startMCPServer(t, lights.NewCluster())
	targetURL := fmt.Sprintf("http://%s/mcp", targetAddr)

	result := doMCPCall(t, srcAddr, "adhd.proxy", map[string]interface{}{
		"target_endpoint": targetURL,
		"call": map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "adhd.status",
		},
	})

	if result["error"] != nil {
		t.Fatalf("adhd.proxy returned error: %v", result["error"])
	}
	if result["result"] == nil {
		t.Fatalf("adhd.proxy returned no result: %v", result)
	}
}

// TestProxyToolsCallNested verifies adhd.proxy is callable through tools/call
// with the MCP-spec "arguments" key and a nested call object.
func TestProxyToolsCallNested(t *testing.T) {
	targetAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	startMCPOnAddr(t, targetAddr, lights.NewCluster())

	srcAddr := startMCPServer(t, lights.NewCluster())
	targetURL := fmt.Sprintf("http://%s/mcp", targetAddr)

	result := doMCPCall(t, srcAddr, "tools/call", map[string]interface{}{
		"name": "adhd.proxy",
		"arguments": map[string]interface{}{
			"target_endpoint": targetURL,
			"call": map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "adhd.status",
			},
		},
	})

	if result["error"] != nil {
		t.Fatalf("tools/call adhd.proxy returned error: %v", result["error"])
	}
	// tools/call wraps the result in a content[] text block.
	if result["result"] == nil {
		t.Fatalf("tools/call adhd.proxy returned no result: %v", result)
	}
}

// TestProxyMissingEndpoint verifies validation still rejects a missing
// target_endpoint.
func TestProxyMissingEndpoint(t *testing.T) {
	srcAddr := startMCPServer(t, lights.NewCluster())

	result := doMCPCall(t, srcAddr, "adhd.proxy", map[string]interface{}{
		"call": map[string]interface{}{"method": "adhd.status"},
	})

	if result["error"] == nil {
		t.Fatalf("expected error for missing target_endpoint, got: %v", result)
	}
	msg := fmt.Sprintf("%v", result["error"])
	if !containsStr(msg, "target_endpoint") {
		t.Fatalf("expected target_endpoint error, got: %v", msg)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
