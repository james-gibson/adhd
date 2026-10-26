package mcpserver

import (
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestProxyHopTearSheetIsSingleUse walks a two-hop sheet: the first tear spends
// the first ticket and forwards through the first hop, the remainder is passed
// on and spends the second, and replaying the original sheet is refused as
// reuse rather than silently retried.
func TestProxyHopTearSheetIsSingleUse(t *testing.T) {
	node := startChainNode(t, "node-a")
	var tick atomic.Uint64
	tick.Store(1000)
	node.srv.SetTickSource(func() uint64 { return tick.Load() })

	first := fakeUpstreamMCP(t, "one")
	second := fakeUpstreamMCP(t, "two")
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)

	node.srv.RegisterProxyHopTearSkill()

	var reuseAlerts, staleAlerts atomic.Int64
	node.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		switch event {
		case "skill_reuse":
			reuseAlerts.Add(1)
		case "path_stale":
			staleAlerts.Add(1)
		}
	})

	sheet := map[string]interface{}{
		"id":   "sheet-1",
		"tick": 1000,
		"hops": []interface{}{
			map[string]interface{}{"from": "a", "to": "b", "endpoint": first.URL, "nonce": "n1"},
			map[string]interface{}{"from": "b", "to": "c", "endpoint": second.URL, "nonce": "n2"},
		},
	}
	forward := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}

	call := func(s interface{}) rpcOutcome {
		return postChain(t, node.url, map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName,
			"params": map[string]interface{}{"sheet": s, "call": forward},
		})
	}

	firstOut := call(sheet)
	if firstOut.Err != nil {
		t.Fatalf("first tear: %+v", firstOut.Err)
	}
	if got := firstOut.Result["remaining"]; got != float64(1) {
		t.Fatalf("remaining %v, want 1", got)
	}
	ticket, _ := firstOut.Result["ticket"].(map[string]interface{})
	if ticket["nonce"] != "n1" || ticket["last"] != false {
		t.Fatalf("unexpected ticket: %+v", ticket)
	}
	if answer := nestedAnswer(firstOut.Result["result"]); answer != "one" {
		t.Fatalf("first hop did not reach the first upstream: %+v", firstOut.Result["result"])
	}
	rest, ok := firstOut.Result["sheet"].(map[string]interface{})
	if !ok {
		t.Fatalf("no remainder sheet: %+v", firstOut.Result)
	}

	secondOut := call(rest)
	if secondOut.Err != nil {
		t.Fatalf("second tear: %+v", secondOut.Err)
	}
	if got := secondOut.Result["remaining"]; got != float64(0) {
		t.Fatalf("remaining %v, want 0", got)
	}
	ticket, _ = secondOut.Result["ticket"].(map[string]interface{})
	if ticket["nonce"] != "n2" || ticket["last"] != true || ticket["index"] != float64(1) {
		t.Fatalf("unexpected second ticket: %+v", ticket)
	}
	if answer := nestedAnswer(secondOut.Result["result"]); answer != "two" {
		t.Fatalf("second hop did not reach the second upstream: %+v", secondOut.Result["result"])
	}

	// Replaying the original sheet re-presents the first ticket.
	replay := call(sheet)
	if replay.Err == nil || replay.Err.Code != -32000 {
		t.Fatalf("replay should be refused as reuse: %+v", replay)
	}
	if reuseAlerts.Load() != 1 {
		t.Fatalf("reuse alerts = %d, want 1", reuseAlerts.Load())
	}

	// Two ticks late is outside the grace window and never reaches the ledger.
	tick.Store(1002)
	stale := call(sheet)
	if stale.Err == nil || stale.Err.Code != -32010 {
		t.Fatalf("stale sheet should be refused: code=%v msg=%q", codeOf(stale), msgOf(stale))
	}
	if staleAlerts.Load() != 1 {
		t.Fatalf("stale alerts = %d, want 1", staleAlerts.Load())
	}

	// A tick that has not arrived is a parameter error, not an expiry.
	tick.Store(999)
	future := call(sheet)
	if future.Err == nil || future.Err.Code != -32602 {
		t.Fatalf("future sheet should be refused as invalid: %+v", future)
	}
}

// nestedAnswer unwraps the JSON-RPC envelope the upstream returned.
func nestedAnswer(v interface{}) string {
	envelope, _ := v.(map[string]interface{})
	result, _ := envelope["result"].(map[string]interface{})
	answer, _ := result["answer"].(string)
	return answer
}

func codeOf(out rpcOutcome) int {
	if out.Err == nil {
		return 0
	}
	return out.Err.Code
}

func msgOf(out rpcOutcome) string {
	if out.Err == nil {
		return ""
	}
	return out.Err.Message
}

func TestProxyHopTearSheetNeedsBothParts(t *testing.T) {
	node := startChainNode(t, "node-a")
	node.srv.SetTickSource(func() uint64 { return 1000 })
	node.srv.RegisterProxyHopTearSkill()

	for _, tc := range []struct {
		name   string
		params map[string]interface{}
	}{
		{"no sheet", map[string]interface{}{"call": map[string]interface{}{"method": "tools/list"}}},
		{"no call", map[string]interface{}{"sheet": map[string]interface{}{"id": "s"}}},
		{"empty sheet", map[string]interface{}{
			"sheet": map[string]interface{}{"id": "s", "tick": 1000, "hops": []interface{}{}},
			"call":  map[string]interface{}{"method": "tools/list"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := postChain(t, node.url, map[string]interface{}{
				"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName, "params": tc.params,
			})
			if out.Err == nil || out.Err.Code != -32602 {
				t.Fatalf("want -32602, got %+v", out)
			}
		})
	}
}

// TestTearLedgerSurvivesRestart is the rotation case: the parent spends a
// ticket, the child opens the ledger the parent wrote, and the replay is still
// refused.
func TestTearLedgerSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tear-ledger.json")
	upstream := fakeUpstreamMCP(t, "one")
	t.Cleanup(upstream.Close)

	start := func() *chainNode {
		node := startChainNode(t, "node-a")
		node.srv.SetTickSource(func() uint64 { return 1000 })
		if err := node.srv.EnableTearLedger(path); err != nil {
			t.Fatalf("enable ledger: %v", err)
		}
		node.srv.RegisterProxyHopTearSkill()
		return node
	}

	sheet := map[string]interface{}{
		"id": "sheet-1", "tick": 1000,
		"hops": []interface{}{
			map[string]interface{}{"to": "b", "endpoint": upstream.URL, "nonce": "n1"},
		},
	}
	call := func(node *chainNode) rpcOutcome {
		return postChain(t, node.url, map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName,
			"params": map[string]interface{}{
				"sheet": sheet,
				"call":  map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
			},
		})
	}

	parent := start()
	if out := call(parent); out.Err != nil {
		t.Fatalf("parent tear: %+v", out.Err)
	}

	// A fresh process, same ledger path.
	child := start()
	replay := call(child)
	if replay.Err == nil || replay.Err.Code != -32000 {
		t.Fatalf("child should refuse the parent's spent ticket: code=%v msg=%q", codeOf(replay), msgOf(replay))
	}
}
