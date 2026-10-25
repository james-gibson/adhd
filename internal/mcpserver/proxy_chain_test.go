package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// This file exercises the proxy chain: `adhd.proxy` forwards a nested call to
// another node's MCP endpoint, so an agent can hop A -> B -> C by wrapping the
// terminal call in one proxy per hop. The run knows the chain it built, and
// every agent must land on the node it declared — including when it turns back
// on itself or revisits a node.

type chainNode struct {
	name string
	url  string
	srv  *Server
}

func newChainNode(name string) (*chainNode, func()) {
	cfg := config.DefaultConfig()
	cfg.MCPServer.Enabled = true
	s := NewServer(cfg.MCPServer, lights.NewCluster())
	s.SetTopologyProvider(func() map[string]interface{} {
		return map[string]interface{}{"local_name": name, "local_role": "test"}
	})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", s.handleMCPRPC)
	ts := httptest.NewServer(mux)
	return &chainNode{name: name, url: ts.URL + "/mcp", srv: s}, ts.Close
}

func startChainNode(t *testing.T, name string) *chainNode {
	t.Helper()
	n, closeFn := newChainNode(name)
	t.Cleanup(closeFn)
	return n
}

var chainClient = &http.Client{Timeout: 5 * time.Second}

type rpcOutcome struct {
	Result map[string]interface{}
	Err    *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
}

func postRaw(url string, call map[string]interface{}) (rpcOutcome, error) {
	body, err := json.Marshal(call)
	if err != nil {
		return rpcOutcome{}, fmt.Errorf("marshal chain call: %w", err)
	}
	resp, err := chainClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return rpcOutcome{}, fmt.Errorf("post to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Result map[string]interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return rpcOutcome{}, fmt.Errorf("decode chain response: %w", err)
	}
	return rpcOutcome{Result: out.Result, Err: out.Error}, nil
}

func postChain(t *testing.T, url string, call map[string]interface{}) rpcOutcome {
	t.Helper()
	out, err := postRaw(url, call)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return out
}

func terminalCall(method string) map[string]interface{} {
	return map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method}
}

func wrapProxy(targetURL string, inner map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "adhd.proxy",
		"params": map[string]interface{}{
			"target_endpoint": targetURL,
			"call":            inner,
		},
	}
}

// buildChain nests one proxy per path entry after the first. Posting the
// result to path[0] makes path[last] the node that answers.
func buildChain(nodes []*chainNode, path []int, method string) map[string]interface{} {
	call := terminalCall(method)
	for i := len(path) - 1; i >= 1; i-- {
		call = wrapProxy(nodes[path[i]].url, call)
	}
	return call
}

// unwrap peels the reply through the proxy hops. Each hop returns the target's
// whole JSON-RPC response as its result, so the chain's depth is visible in the
// nesting: hops == len(path)-1 must yield the terminal payload.
func unwrap(result map[string]interface{}, hops int) (map[string]interface{}, error) {
	current := result
	for level := 0; level < hops; level++ {
		inner, ok := current["result"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("hop %d: reply is not a nested JSON-RPC response: %v", level+1, current)
		}
		current = inner
	}
	return current, nil
}

// landedOn reads the node identity out of a terminal reply. Both methods carry
// the topology provider's local_name, so a misrouted hop is visible.
func landedOn(result map[string]interface{}, method string) string {
	field := "instance"
	if method == "adhd.isotope.status" {
		field = "name"
	}
	name, _ := result[field].(string)
	return name
}

// TestAgentRandomTurnsThroughProxyChain runs seeded agents that choose a start
// node, a number of turns, and a next node per turn (including themselves and
// nodes already visited). The run records the path it asked for and requires
// the terminal reply to come from exactly that node.
func TestAgentRandomTurnsThroughProxyChain(t *testing.T) {
	nodes := []*chainNode{
		startChainNode(t, "node-a"),
		startChainNode(t, "node-b"),
		startChainNode(t, "node-c"),
	}
	methods := []string{"adhd.status", "adhd.isotope.status"}
	rng := rand.New(rand.NewSource(42))

	const agents = 200
	turns := 0
	for agent := 0; agent < agents; agent++ {
		path := []int{rng.Intn(len(nodes))}
		hops := rng.Intn(4) // 0..3 proxy hops
		for h := 0; h < hops; h++ {
			path = append(path, rng.Intn(len(nodes)))
		}
		method := methods[rng.Intn(len(methods))]

		out := postChain(t, nodes[path[0]].url, buildChain(nodes, path, method))
		if out.Err != nil {
			t.Fatalf("agent %d, path %v: rpc error %d: %s", agent, path, out.Err.Code, out.Err.Message)
		}
		terminal, err := unwrap(out.Result, len(path)-1)
		if err != nil {
			t.Fatalf("agent %d, path %v: %v", agent, path, err)
		}
		want := nodes[path[len(path)-1]].name
		if got := landedOn(terminal, method); got != want {
			t.Fatalf("agent %d, path %v: landed on %q, want %q (result %v)",
				agent, path, got, want, terminal)
		}
		turns += len(path) - 1
	}
	t.Logf("%d agents, %d proxy turns, 0 misroutes", agents, turns)
}

// TestProxyChainBreaksAtDeadLink checks that a link that disappears mid-chain
// surfaces as an error at the caller, rather than a silent fallback to a
// neighbouring node.
func TestProxyChainBreaksAtDeadLink(t *testing.T) {
	a := startChainNode(t, "node-a")

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + "/mcp"
	dead.Close()

	call := wrapProxy(deadURL, terminalCall("adhd.status"))
	out := postChain(t, a.url, call)
	if out.Err == nil {
		t.Fatalf("expected a dead-link error, got result %v", out.Result)
	}
	if !strings.Contains(out.Err.Message, "Failed to reach target endpoint") {
		t.Fatalf("error did not name the proxy failure: %d %s", out.Err.Code, out.Err.Message)
	}
}

// FuzzProxyChain fuzzes the turn logic itself: which node each turn targets,
// how deeply the calls nest, and whether the terminal is exactly the last turn.
// It is deliberately pure — the network path is covered by
// TestAgentRandomTurnsThroughProxyChain, because a fuzz worker hammering real
// sockets exhausts ephemeral ports long before it exhausts the input space.
func FuzzProxyChain(f *testing.F) {
	for _, seed := range []string{"a", "ab", "abc", "aaaa", "cbabc", "abcabcabc"} {
		f.Add(seed)
	}
	nodes := []*chainNode{{name: "node-a", url: "http://a/mcp"}, {name: "node-b", url: "http://b/mcp"}, {name: "node-c", url: "http://c/mcp"}}

	f.Fuzz(func(t *testing.T, raw string) {
		path := sanitizeTurns(raw)
		if len(path) == 0 || len(path) > 8 {
			t.Fatalf("sanitized path length out of bounds: %v", path)
		}
		for _, turn := range path {
			if turn < 0 || turn >= len(nodes) {
				t.Fatalf("turn %d out of range in %v", turn, path)
			}
		}

		depth := len(path) - 1
		call := buildChain(nodes, path, "ping")
		current := call
		for hop := 1; hop <= depth; hop++ {
			if current["method"] != "adhd.proxy" {
				t.Fatalf("hop %d is %v, want adhd.proxy", hop, current["method"])
			}
			params, ok := current["params"].(map[string]interface{})
			if !ok {
				t.Fatalf("hop %d has no params", hop)
			}
			if got := params["target_endpoint"]; got != nodes[path[hop]].url {
				t.Fatalf("hop %d targets %v, want %v", hop, got, nodes[path[hop]].url)
			}
			next, ok := params["call"].(map[string]interface{})
			if !ok {
				t.Fatalf("hop %d does not nest a call", hop)
			}
			current = next
		}
		if current["method"] != "ping" {
			t.Fatalf("terminal method = %v, want ping", current["method"])
		}

		// The reply peels exactly `depth` times, no more and no less.
		payload := map[string]interface{}{"instance": nodes[path[len(path)-1]].name}
		for i := 0; i < depth; i++ {
			payload = map[string]interface{}{"jsonrpc": "2.0", "id": 1, "result": payload}
		}
		terminal, err := unwrap(payload, depth)
		if err != nil {
			t.Fatalf("unwrap %d: %v", depth, err)
		}
		if got := landedOn(terminal, "adhd.status"); got != nodes[path[len(path)-1]].name {
			t.Fatalf("landed on %q, want %q", got, nodes[path[len(path)-1]].name)
		}
		if _, err := unwrap(payload, depth+1); err == nil {
			t.Fatalf("depth %d unwrapped one level too far", depth)
		}
	})
}

// sanitizeTurns maps a to node 0, b to 1, c to 2; anything else is dropped. An
// empty result becomes a single direct turn. Depth is capped so one input
// cannot turn into an unbounded request chain.
func sanitizeTurns(raw string) []int {
	turns := make([]int, 0, len(raw))
	for _, r := range raw {
		if len(turns) >= 8 {
			break
		}
		switch r {
		case 'a', 'A':
			turns = append(turns, 0)
		case 'b', 'B':
			turns = append(turns, 1)
		case 'c', 'C':
			turns = append(turns, 2)
		}
	}
	if len(turns) == 0 {
		turns = []int{0}
	}
	return turns
}

// TestRandomTurnsThroughHoneypotField is the noise case: every node publishes a
// large proxy catalogue, but only a few entries resolve to a real upstream. An
// agent taking random turns lands on decoys almost every time, so the run can
// measure how loud the wrong turns are — and that each one is surfaced rather
// than swallowed.
//
// The proportions are the point: 10 real proxies and 10,000 honeypots per node,
// so a random turn is wrong 99.9% of the time.
func TestRandomTurnsThroughHoneypotField(t *testing.T) {
	nodes := []*chainNode{
		startChainNode(t, "node-a"),
		startChainNode(t, "node-b"),
		startChainNode(t, "node-c"),
	}

	const (
		realPerNode  = 10
		decoyPerNode = 10000
		turns        = 2000
		proxiedShare = 0.2
	)

	real := make(map[string]string, realPerNode*len(nodes))
	catalogues := make([][]string, len(nodes))
	for ni := range nodes {
		letter := string(rune('a' + ni))
		catalogues[ni] = make([]string, 0, realPerNode+decoyPerNode)
		for i := 0; i < realPerNode; i++ {
			id := fmt.Sprintf("real-%s-%02d", letter, i)
			answer := fmt.Sprintf("real-%s-%02d", letter, i)
			upstream := fakeUpstreamMCP(t, answer)
			t.Cleanup(upstream.Close)
			nodes[ni].srv.registerProjectTool("cluster", id, "", upstream.URL)
			name := "adhd.project." + id + ".call"
			real[name] = answer
			catalogues[ni] = append(catalogues[ni], name)
		}
		for i := 0; i < decoyPerNode; i++ {
			name := fmt.Sprintf("adhd.project.decoy-%s-%05d.call", letter, i)
			nodes[ni].srv.RegisterHoneypotTool(name, "decoy", map[string]interface{}{"ok": true})
			catalogues[ni] = append(catalogues[ni], name)
		}
	}

	var alertMu sync.Mutex
	alerted := make(map[string]int, decoyPerNode*len(nodes))
	perNodeAlerts := make([]int, len(nodes))
	for ni, node := range nodes {
		idx := ni
		node.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
			if event != "honeypot_triggered" {
				return
			}
			alertMu.Lock()
			defer alertMu.Unlock()
			perNodeAlerts[idx]++
			if tool, _ := details["tool"].(string); tool != "" {
				alerted[tool]++
			}
		})
	}

	rng := rand.New(rand.NewSource(7))
	honeypotHits, realHits := 0, 0
	for turn := 0; turn < turns; turn++ {
		owner := rng.Intn(len(nodes))
		name := catalogues[owner][rng.Intn(len(catalogues[owner]))]
		// Project tools take {"method": ...}; honeypots ignore params.
		call := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  name,
			"params":  map[string]interface{}{"method": "tools/list"},
		}

		entry := owner
		proxied := rng.Float64() < proxiedShare
		if proxied {
			// Enter at a different node and proxy across to the owner, so the
			// wrong turn is made through the chain rather than locally.
			entry = (owner + 1 + rng.Intn(len(nodes)-1)) % len(nodes)
			call = wrapProxy(nodes[owner].url, call)
		}

		out := postChain(t, nodes[entry].url, call)
		if out.Err != nil {
			t.Fatalf("turn %d (%s, entry=%d, proxied=%v): rpc error %d: %s",
				turn, name, entry, proxied, out.Err.Code, out.Err.Message)
		}
		result := out.Result
		if proxied {
			inner, err := unwrap(result, 1)
			if err != nil {
				t.Fatalf("turn %d (%s): %v", turn, name, err)
			}
			result = inner
		}

		if answer, ok := real[name]; ok {
			realHits++
			if result["answer"] != answer {
				t.Fatalf("turn %d: real proxy %s returned %v, want answer %q", turn, name, result, answer)
			}
			continue
		}

		honeypotHits++
		if result["ok"] != true {
			t.Fatalf("turn %d: honeypot %s did not return the decoy result: %v", turn, name, result)
		}
	}

	alertMu.Lock()
	alertCount := 0
	for _, n := range alerted {
		alertCount += n
	}
	alertMu.Unlock()

	if realHits+honeypotHits != turns {
		t.Fatalf("classified %d turns, want %d", realHits+honeypotHits, turns)
	}
	if honeypotHits < turns*995/1000 {
		t.Fatalf("honeypot noise %d/%d is quieter than the catalogue predicts", honeypotHits, turns)
	}
	if alertCount != honeypotHits {
		t.Fatalf("security alerts %d != honeypot hits %d: wrong turns are not all surfaced", alertCount, honeypotHits)
	}
	for ni, node := range nodes {
		light := node.srv.cluster.GetByName("security/honeypot_triggered")
		if perNodeAlerts[ni] == 0 {
			continue
		}
		if light == nil {
			t.Fatalf("node %s saw %d honeypot contacts but raised no security light", node.name, perNodeAlerts[ni])
		}
		if light.GetStatus() != lights.StatusRed {
			t.Fatalf("node %s security light status = %s, want red", node.name, light.GetStatus())
		}
	}

	t.Logf("turn noise: %d honeypot / %d real out of %d turns (%.1f%% wrong), %d alerts across %d nodes",
		honeypotHits, realHits, turns, float64(honeypotHits)/float64(turns)*100, alertCount, len(nodes))
}
