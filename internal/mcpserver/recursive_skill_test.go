package mcpserver

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/james-gibson/adhd/internal/navtrace"
)

// This is the "complex skill" fixture: a skill that recurses itself and counts
// the letter r in "strawberry" (three of them), with the shards executed on
// different nodes.
//
// The counting is trivial on purpose. What is being tested is the topology: can
// a skill hand a shard to another node, get a number back, and have the sum be
// the right one? The sum is also the integrity check — 4 means a shard was
// counted twice, 2 means one was dropped.

const countRSkill = "adhd.skill.count-r.call"

// countRDynamicTool is the recursive skill's local body: it counts its shard and
// reports the 32-bit skill id so a trace can attribute the partial result.
func countRDynamicTool() *dynamicTool {
	return &dynamicTool{
		name:        countRSkill,
		description: "Count the letter r in one shard. The caller owns the reduction.",
		inputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"shard_id": map[string]interface{}{"type": "string"},
				"shard":    map[string]interface{}{"type": "string"},
			},
			"required": []string{"shard_id", "shard"},
		},
		handler: func(_ context.Context, params interface{}) (interface{}, *jsonrpcError) {
			m, _ := params.(map[string]interface{})
			shard, _ := m["shard"].(string)
			id, _ := m["shard_id"].(string)
			if id == "" {
				return nil, &jsonrpcError{Code: -32602, Message: "shard_id is required"}
			}
			return map[string]interface{}{
				"shard_id": id,
				"count":    strings.Count(shard, "r"),
				"skill":    navtrace.ID(countRSkill),
			}, nil
		},
	}
}

func registerCountR(t *testing.T, node *chainNode) {
	t.Helper()
	node.srv.mu.Lock()
	node.srv.dynamicTools = append(node.srv.dynamicTools, countRDynamicTool())
	node.srv.mu.Unlock()
}

// TestRecursiveSkillFansOutAcrossNodes routes one shard to node b and another
// to node c through single-use tear-sheet hops, then adds the local shard. The
// three partials must sum to three.
func TestRecursiveSkillFansOutAcrossNodes(t *testing.T) {
	a := startChainNode(t, "node-a")
	b := startChainNode(t, "node-b")
	c := startChainNode(t, "node-c")
	for _, node := range []*chainNode{a, b, c} {
		registerCountR(t, node)
	}
	a.srv.SetTickSource(func() uint64 { return 100 })
	a.srv.RegisterProxyHopTearSkill()

	shards := []struct{ id, text string }{
		{"s0", "straw"},
		{"s1", "ber"},
		{"s2", "ry"},
	}

	sheet := map[string]interface{}{
		"id": "sheet-r", "tick": 100,
		"hops": []interface{}{
			map[string]interface{}{"to": "b", "endpoint": b.url, "nonce": "r1"},
			map[string]interface{}{"to": "c", "endpoint": c.url, "nonce": "r2"},
		},
	}

	tear := func(s interface{}, shardIndex int) (float64, map[string]interface{}) {
		shard := shards[shardIndex]
		out := postChain(t, a.url, map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName,
			"params": map[string]interface{}{
				"sheet": s,
				"call": map[string]interface{}{
					"jsonrpc": "2.0", "id": 1, "method": countRSkill,
					"params": map[string]interface{}{"shard_id": shard.id, "shard": shard.text},
				},
			},
		})
		if out.Err != nil {
			t.Fatalf("tear for %s: %d %s", shard.id, out.Err.Code, out.Err.Message)
		}
		envelope, _ := out.Result["result"].(map[string]interface{})
		body, _ := envelope["result"].(map[string]interface{})
		count, _ := body["count"].(float64)
		if body["shard_id"] != shard.id {
			t.Fatalf("shard %s was answered as %v", shard.id, body["shard_id"])
		}
		return count, out.Result
	}

	total := 0.0
	count, first := tear(sheet, 0)
	total += count
	if count != 1 {
		t.Fatalf("shard s0 counted %v, want 1", count)
	}

	rest, _ := first["sheet"].(map[string]interface{})
	count, _ = tear(rest, 1)
	total += count
	if count != 1 {
		t.Fatalf("shard s1 counted %v, want 1", count)
	}

	// The third shard is local; the recursion does not have to use a hop.
	local := postChain(t, a.url, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": countRSkill,
		"params": map[string]interface{}{"shard_id": shards[2].id, "shard": shards[2].text},
	})
	if local.Err != nil {
		t.Fatalf("local shard: %d %s", local.Err.Code, local.Err.Message)
	}
	// A direct call is not wrapped in a proxy envelope.
	localCount, _ := local.Result["count"].(float64)
	total += localCount

	if total != 3 {
		t.Fatalf("r count = %v, want 3; the reduction or the routing lost a shard", total)
	}

	// The same ticket cannot be spent twice, so a retried hop is not a second
	// shard by accident.
	replay := postChain(t, a.url, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName,
		"params": map[string]interface{}{
			"sheet": sheet,
			"call": map[string]interface{}{
				"jsonrpc": "2.0", "id": 1, "method": countRSkill,
				"params": map[string]interface{}{"shard_id": "s0", "shard": "straw"},
			},
		},
	})
	if replay.Err == nil || replay.Err.Code != -32000 {
		t.Fatalf("replayed hop should be refused: %+v", replay)
	}
}

// TestHopDedupIsNotShardDedup characterises the remaining gap: two distinct
// tickets can carry the same shard, because the tear sheet makes hops
// single-use, not work units. A recursive skill needs its own work key — or an
// idempotent reducer — before the sum can be trusted.
func TestHopDedupIsNotShardDedup(t *testing.T) {
	a := startChainNode(t, "node-a")
	b := startChainNode(t, "node-b")
	registerCountR(t, b)
	a.srv.SetTickSource(func() uint64 { return 100 })
	a.srv.RegisterProxyHopTearSkill()

	var reuseAlerts atomic.Int64
	a.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		if event == "skill_reuse" {
			reuseAlerts.Add(1)
		}
	})

	// Two fresh tickets, both addressed to b, both carrying the same shard.
	sheet := map[string]interface{}{
		"id": "sheet-dup", "tick": 100,
		"hops": []interface{}{
			map[string]interface{}{"to": "b", "endpoint": b.url, "nonce": "d1"},
			map[string]interface{}{"to": "b", "endpoint": b.url, "nonce": "d2"},
		},
	}
	call := func(s interface{}) float64 {
		out := postChain(t, a.url, map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName,
			"params": map[string]interface{}{
				"sheet": s,
				"call": map[string]interface{}{
					"jsonrpc": "2.0", "id": 1, "method": countRSkill,
					"params": map[string]interface{}{"shard_id": "s0", "shard": "straw"},
				},
			},
		})
		if out.Err != nil {
			t.Fatalf("tear: %d %s", out.Err.Code, out.Err.Message)
		}
		envelope, _ := out.Result["result"].(map[string]interface{})
		body, _ := envelope["result"].(map[string]interface{})
		count, _ := body["count"].(float64)
		return count
	}

	first := call(sheet)
	// The remainder is needed for the second hop; re-read it from a fresh call
	// is not possible, so this test only needs the first shard twice through
	// two independent sheets.
	second := call(map[string]interface{}{
		"id": "sheet-dup-2", "tick": 100,
		"hops": []interface{}{
			map[string]interface{}{"to": "b", "endpoint": b.url, "nonce": "d3"},
		},
	})
	if first+second != 2 {
		t.Fatalf("same shard counted %v; the hop ledger cannot see a work duplicate", first+second)
	}
	if reuseAlerts.Load() != 0 {
		t.Fatalf("reuse alerts = %d; no ticket was reused, only the work", reuseAlerts.Load())
	}
}
