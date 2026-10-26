package mcpserver

import (
	"testing"

	"github.com/james-gibson/adhd/internal/navtrace"
	"github.com/james-gibson/isotope"
)

// TestRoutePlanResolvesATargetIntoAChain is the planner contract: one call
// names the target, and the answer is a callable chain plus the segments a
// caller presents to adhd.path.call.
func TestRoutePlanResolvesATargetIntoAChain(t *testing.T) {
	a := startChainNode(t, "node-a")
	b := startChainNode(t, "node-b")

	for _, node := range []*chainNode{a, b} {
		identity := isotope.NewInstanceIdentity()
		node.srv.SetInstanceIdentity(identity.Isotope, identity.ComputeReceipt, identity.VerifyReceipt)
	}
	a.srv.SetTrustLevel(3)
	b.srv.SetTrustLevel(4)
	a.srv.SetTickSource(func() uint64 { return 88 })
	b.srv.SetTickSource(func() uint64 { return 88 })

	a.srv.mu.Lock()
	a.srv.clusterPeers["node-b"] = &clusterPeerInfo{Name: "node-b", Endpoint: b.url}
	a.srv.mu.Unlock()

	a.srv.RegisterRoutePlanSkill()

	const feature = "skill.check-working"
	call := func() rpcOutcome {
		return postChain(t, a.url, map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "method": RoutePlanSkillName,
			"params": map[string]interface{}{
				"target.isotope": "node-b",
				"feature_id":     feature,
			},
		})
	}

	out := call()
	if out.Err != nil {
		t.Fatalf("plan: %d %s", out.Err.Code, out.Err.Message)
	}
	if out.Result["target"] != "node-b" {
		t.Fatalf("target = %v, want node-b", out.Result["target"])
	}
	if got := out.Result["hops"]; got != float64(1) {
		t.Fatalf("hops = %v, want 1", got)
	}
	if got := out.Result["min_rung"]; got != float64(4) {
		t.Fatalf("min_rung = %v, want 4", got)
	}
	if got := out.Result["tick"]; got != float64(88) {
		t.Fatalf("tick = %v, want 88", got)
	}
	if got := out.Result["skill"]; got != float64(navtrace.ID(RoutePlanSkillName)) {
		t.Fatalf("skill = %v, want %d", got, navtrace.ID(RoutePlanSkillName))
	}
	chain, _ := out.Result["chain"].([]interface{})
	if len(chain) != 1 {
		t.Fatalf("chain = %v, want one hop", chain)
	}
	hop, _ := chain[0].(map[string]interface{})
	if hop["isotope"] != "node-b" || hop["endpoint"] != b.url {
		t.Fatalf("hop = %v, want node-b at %s", hop, b.url)
	}
	segments, _ := out.Result["segments"].([]interface{})
	if len(segments) != 1 {
		t.Fatalf("segments = %v, want one", segments)
	}

	// The same tick answers from the plan cache rather than re-challenging.
	again := call()
	if again.Err != nil {
		t.Fatalf("cached plan: %d %s", again.Err.Code, again.Err.Message)
	}
	if cached, _ := again.Result["cached"].(bool); !cached {
		t.Fatalf("second plan in the same tick was not cached: %v", again.Result["cached"])
	}
}

func TestRoutePlanRefusesAnUnknownTarget(t *testing.T) {
	a := startChainNode(t, "node-a")
	a.srv.SetTickSource(func() uint64 { return 88 })
	a.srv.RegisterRoutePlanSkill()

	out := postChain(t, a.url, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": RoutePlanSkillName,
		"params": map[string]interface{}{
			"target.isotope": "nobody",
			"feature_id":     "gh",
		},
	})
	if out.Err == nil || out.Err.Code != -32000 {
		t.Fatalf("unknown target should be refused with -32000: %+v", out)
	}
}
