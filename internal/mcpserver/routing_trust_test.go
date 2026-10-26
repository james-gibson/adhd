package mcpserver

import (
	"sync/atomic"
	"testing"

	"github.com/james-gibson/isotope"
)

// TestRoutingDoesNotConsultSpecFidelity characterises the missing gate: the
// router has no behavioural input, so a peer whose recorded request shape is
// below any floor is still negotiated to. It passes today; it should fail once
// eligibility consults the score.
func TestRoutingDoesNotConsultSpecFidelity(t *testing.T) {
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

	// A provider that reports nothing worth trusting. If routing reads it at
	// all, the flag flips.
	var consulted atomic.Bool
	a.srv.SetSpecFidelityProvider(func() interface{} {
		consulted.Store(true)
		return map[string]interface{}{"average": 0.0, "callers": []interface{}{}}
	})

	out := postChain(t, a.url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-b",
		"feature_id":     "gh",
	}))
	if out.Err != nil {
		t.Fatalf("negotiate: %d %s", out.Err.Code, out.Err.Message)
	}
	paths, _ := out.Result["paths"].([]interface{})
	if len(paths) == 0 {
		t.Fatalf("no path was negotiated: %v", out.Result)
	}
	if consulted.Load() {
		t.Fatal("routing read spec_fidelity; the gate exists after all")
	}
}

// TestTearTicketBoundsOneHop is the complement: the single-use ticket bounds
// what an eligible hop can be asked to do, independently of how trusted it is.
func TestTearTicketBoundsOneHop(t *testing.T) {
	node := startChainNode(t, "node-a")
	node.srv.SetTickSource(func() uint64 { return 100 })
	node.srv.RegisterProxyHopTearSkill()

	upstream := fakeUpstreamMCP(t, "one")
	t.Cleanup(upstream.Close)

	sheet := map[string]interface{}{
		"id": "sheet-b", "tick": 100,
		"hops": []interface{}{
			map[string]interface{}{"to": "b", "endpoint": upstream.URL, "nonce": "b1"},
		},
	}
	call := func() rpcOutcome {
		return postChain(t, node.url, map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "method": ProxyHopTearSkillName,
			"params": map[string]interface{}{
				"sheet": sheet,
				"call":  map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
			},
		})
	}
	if out := call(); out.Err != nil {
		t.Fatalf("first use: %+v", out.Err)
	}
	if out := call(); out.Err == nil || out.Err.Code != -32000 {
		t.Fatalf("second use should be refused as reuse: %+v", out)
	}
}
