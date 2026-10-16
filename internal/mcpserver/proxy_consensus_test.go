package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// fakeUpstreamMCP returns a JSON-RPC server that answers every method with a
// deterministic result carrying `answer`. Two of them with different answers
// stand in for one underlying implementation and a tampered one.
func fakeUpstreamMCP(t *testing.T, answer string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     interface{} `json:"id"`
			Method string      `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]interface{}{"answer": answer, "method": req.Method},
		})
	}))
}

func newProxyTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.MCPServer.Enabled = true
	return NewServer(cfg.MCPServer, lights.NewCluster())
}

// mcpRPC drives the real POST /mcp entry point and returns the result map.
func mcpRPC(t *testing.T, s *Server, method string, params map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleMCPRPC(rec, req)

	var resp struct {
		Result map[string]interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("%s: rpc error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result
}

// TestProxyConsensus captures the client-side consensus pattern: adhd *renames*
// proxied tools, so the same underlying capability wears two names. An agent can
// task the same job through two proxies and read the difference.
//
//	agree  → corroboration (the two names resolve to one implementation)
//	disagree → one node is wrong (the names resolve to different ones)
func TestProxyConsensus(t *testing.T) {
	s := newProxyTestServer(t)

	// One underlying implementation, reachable under two proxy names.
	agree := fakeUpstreamMCP(t, "42")
	defer agree.Close()
	s.registerProjectTool("cluster", "alpha", "", agree.URL)
	s.registerProjectTool("cluster", "beta", "", agree.URL)

	// The same task, through each name.
	a := mcpRPC(t, s, "adhd.project.alpha.call", map[string]interface{}{"method": "tools/list"})
	b := mcpRPC(t, s, "adhd.project.beta.call", map[string]interface{}{"method": "tools/list"})

	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same underlying tool behind two names disagreed: %v vs %v", a, b)
	}
	if a["answer"] != "42" {
		t.Fatalf("unexpected proxied result: %v", a)
	}

	// A second, divergent implementation: the task through a different proxy must
	// be *detectably* different, so the client can see one node is wrong.
	tampered := fakeUpstreamMCP(t, "41")
	defer tampered.Close()
	s.registerProjectTool("cluster", "gamma", "", tampered.URL)

	c := mcpRPC(t, s, "adhd.project.gamma.call", map[string]interface{}{"method": "tools/list"})
	if reflect.DeepEqual(a, c) {
		t.Fatalf("expected a detectable disagreement, got identical %v", c)
	}
	if c["answer"] != "41" {
		t.Fatalf("unexpected tampered result: %v", c)
	}

	// The names are distinct — that is the renaming the pattern relies on.
	s.mu.RLock()
	names := map[string]bool{}
	for _, dt := range s.dynamicTools {
		names[dt.name] = true
	}
	s.mu.RUnlock()
	for _, want := range []string{"adhd.project.alpha.call", "adhd.project.beta.call", "adhd.project.gamma.call"} {
		if !names[want] {
			t.Fatalf("proxy tool %q was not registered (names: %v)", want, names)
		}
	}
}
