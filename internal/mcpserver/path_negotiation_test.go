package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mirror"

	"github.com/james-gibson/isotope"
)

func rpcParams(method string, params map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
}

// TestPathNegotiationAgreesOnHowToRequest is the pre-negotiation contract: two
// cluster members agree on a path before any request, and the path names the
// endpoint and feature the request must use. The single-use skill is then
// consumed at the hop the negotiation named.
func TestPathNegotiationAgreesOnHowToRequest(t *testing.T) {
	a := startChainNode(t, "node-a")
	b := startChainNode(t, "node-b")

	// Each member needs an identity: the challenge is answered with a receipt
	// only that identity can produce.
	for _, node := range []*chainNode{a, b} {
		identity := isotope.NewInstanceIdentity()
		node.srv.SetInstanceIdentity(identity.Isotope, identity.ComputeReceipt, identity.VerifyReceipt)
	}
	a.srv.SetTrustLevel(3)
	b.srv.SetTrustLevel(4)

	// Pin the public tick so the negotiation cannot straddle a bucket.
	a.srv.SetTickSource(func() uint64 { return 88 })
	b.srv.SetTickSource(func() uint64 { return 88 })

	// A knows B as a cluster peer; negotiation resolves the direct 1-hop path.
	a.srv.mu.Lock()
	a.srv.clusterPeers["node-b"] = &clusterPeerInfo{Name: "node-b", Endpoint: b.url}
	a.srv.mu.Unlock()

	const feature = "skill.check-working"

	negotiated := postChain(t, a.url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-b",
		"feature_id":     feature,
	}))
	if negotiated.Err != nil {
		t.Fatalf("negotiate: %d %s", negotiated.Err.Code, negotiated.Err.Message)
	}
	paths, _ := negotiated.Result["paths"].([]interface{})
	if len(paths) == 0 {
		t.Fatalf("no path was agreed: %v", negotiated.Result)
	}
	best, _ := paths[0].(map[string]interface{})
	token, _ := best["token"].(string)
	if token == "" {
		t.Fatalf("agreed path has no token: %v", best)
	}

	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("decode path token: %v", err)
	}
	var path NegotiatedPath
	if err := json.Unmarshal(raw, &path); err != nil {
		t.Fatalf("parse path token: %v", err)
	}
	if path.Target != "node-b" || path.FeatureID != feature {
		t.Fatalf("path names %q/%q, want node-b/%q", path.Target, path.FeatureID, feature)
	}
	if len(path.Hops) != 1 || path.Hops[0].Endpoint != b.url {
		t.Fatalf("agreed hops = %+v, want the direct hop to %s", path.Hops, b.url)
	}
	if path.MinRung != 4 {
		t.Fatalf("agreed min_rung = %d, want the target's rung 4", path.MinRung)
	}

	// The agreement can be re-verified with fresh nonces.
	verified := postChain(t, a.url, rpcParams("adhd.path.verify", map[string]interface{}{
		"target.isotope": "node-b",
		"feature_id":     feature,
	}))
	if verified.Err != nil {
		t.Fatalf("verify: %d %s", verified.Err.Code, verified.Err.Message)
	}
	verifiedPaths, _ := verified.Result["paths"].([]interface{})
	if len(verifiedPaths) == 0 {
		t.Fatalf("verify returned nothing: %v", verified.Result)
	}
	firstVerified, _ := verifiedPaths[0].(map[string]interface{})
	if valid, _ := firstVerified["valid"].(bool); !valid {
		t.Fatalf("agreed path did not re-verify: %v", firstVerified)
	}

	// The skill is reached at the endpoint the negotiation named, and is
	// single-use within that agreement.
	var consumed atomic.Bool
	skillName := "adhd.skill.negotiated.call"
	skill := &dynamicTool{
		name:        skillName,
		description: "single-use skill on the negotiated hop",
		inputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		handler: func(_ context.Context, _ interface{}) (interface{}, *jsonrpcError) {
			if !consumed.CompareAndSwap(false, true) {
				return nil, &jsonrpcError{Code: -32000, Message: "skill already used"}
			}
			return map[string]interface{}{"ok": true}, nil
		},
	}
	b.srv.mu.Lock()
	b.srv.dynamicTools = append(b.srv.dynamicTools, skill)
	b.srv.mu.Unlock()

	first := postChain(t, path.Hops[0].Endpoint, terminalCall(skillName))
	if first.Err != nil {
		t.Fatalf("skill via agreed hop: %d %s", first.Err.Code, first.Err.Message)
	}
	if first.Result["ok"] != true {
		t.Fatalf("skill result = %v", first.Result)
	}
	reused := postChain(t, path.Hops[0].Endpoint, terminalCall(skillName))
	if reused.Err == nil || reused.Err.Code != -32000 {
		t.Fatalf("reuse should fail at the agreed hop: %+v", reused)
	}

	// Known gap: the token is data, not enforcement. adhd.proxy still accepts a
	// raw target_endpoint, so a caller can reach the same hop without agreeing
	// on the path first.
	direct := postChain(t, b.url, terminalCall("adhd.status"))
	if direct.Err != nil {
		t.Fatalf("direct call failed unexpectedly: %d %s", direct.Err.Code, direct.Err.Message)
	}
	t.Logf("agreed %s/%s over %d hop(s), min_rung=%d; direct target_endpoint still bypasses the token",
		path.Target, path.FeatureID, len(path.Hops), path.MinRung)
}

// TestPathTickLifetimeAndSegments is the path lifetime model:
//
//   - a path is bound to one tick and reusable by any holder during that tick
//     (unlike a single-use skill);
//   - a multi-hop route is one segment per hop, and holding only the first
//     segment gets you to the midpoint and no further;
//   - a segment from an earlier tick is refused and alerted.
func TestPathTickLifetimeAndSegments(t *testing.T) {
	a := startChainNode(t, "node-a")
	b := startChainNode(t, "node-b")
	c := startChainNode(t, "node-c")

	for _, node := range []*chainNode{a, b, c} {
		identity := isotope.NewInstanceIdentity()
		node.srv.SetInstanceIdentity(identity.Isotope, identity.ComputeReceipt, identity.VerifyReceipt)
	}
	a.srv.SetTrustLevel(3)
	b.srv.SetTrustLevel(4)
	c.srv.SetTrustLevel(5)

	var tick atomic.Uint64
	tick.Store(88)
	for _, node := range []*chainNode{a, b, c} {
		node.srv.SetTickSource(func() uint64 { return tick.Load() })
	}

	// A -> B -> C. B knows C, so A can negotiate a two-segment route.
	a.srv.mu.Lock()
	a.srv.clusterPeers["node-b"] = &clusterPeerInfo{Name: "node-b", Endpoint: b.url}
	a.srv.mu.Unlock()
	b.srv.mu.Lock()
	b.srv.clusterPeers["node-c"] = &clusterPeerInfo{Name: "node-c", Endpoint: c.url}
	b.srv.mu.Unlock()

	var staleAlerts atomic.Int64
	a.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		if event == "path_stale" {
			staleAlerts.Add(1)
		}
	})

	const feature = "gh"
	negotiated := postChain(t, a.url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-c",
		"feature_id":     feature,
	}))
	if negotiated.Err != nil {
		t.Fatalf("negotiate: %d %s", negotiated.Err.Code, negotiated.Err.Message)
	}
	paths, _ := negotiated.Result["paths"].([]interface{})
	if len(paths) == 0 {
		t.Fatalf("no route to node-c: %v", negotiated.Result)
	}
	path, _ := paths[0].(map[string]interface{})
	if hops, _ := path["hops"].(float64); hops != 2 {
		t.Fatalf("route has %v hops, want 2: %v", path["hops"], path)
	}
	if got, _ := path["tick"].(float64); got != 88 {
		t.Fatalf("path tick = %v, want 88", path["tick"])
	}
	rawSegments, _ := path["segments"].([]interface{})
	if len(rawSegments) != 2 {
		t.Fatalf("route has %d segments, want 2", len(rawSegments))
	}
	segments := make([]interface{}, 0, len(rawSegments))
	for _, segment := range rawSegments {
		segments = append(segments, segment)
	}

	call := func(segs []interface{}) rpcOutcome {
		return postChain(t, a.url, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "adhd.path.call",
			"params": map[string]interface{}{
				"segments": segs,
				"call":     map[string]interface{}{"method": "adhd.status"},
			},
		})
	}

	// Tick 88: both segments traverse to C, and the path is reusable.
	for attempt := 0; attempt < 2; attempt++ {
		out := call(segments)
		if out.Err != nil {
			t.Fatalf("attempt %d at tick 88: %d %s", attempt, out.Err.Code, out.Err.Message)
		}
		terminal, err := unwrap(out.Result, 2)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if got := landedOn(terminal, "adhd.status"); got != "node-c" {
			t.Fatalf("attempt %d landed on %q, want node-c", attempt, got)
		}
	}

	// Knowing only the first segment routes one hop: the caller lands at the
	// midpoint and can continue from there. Depth is the caller's choice; the
	// server does not require the whole route up front.
	partial := call(segments[:1])
	if partial.Err != nil {
		t.Fatalf("single-hop traversal: %d %s", partial.Err.Code, partial.Err.Message)
	}
	midpoint, err := unwrap(partial.Result, 1)
	if err != nil {
		t.Fatalf("single-hop unwrap: %v", err)
	}
	if got := landedOn(midpoint, "adhd.status"); got != "node-b" {
		t.Fatalf("single segment landed on %q, want the midpoint node-b", got)
	}

	// Knowing only the second segment is also enough to reach C: the map names
	// the endpoint, so the hop does not have to be taken in sequence.
	jump := call(segments[1:])
	if jump.Err != nil {
		t.Fatalf("second-segment traversal: %d %s", jump.Err.Code, jump.Err.Message)
	}
	direct, err := unwrap(jump.Result, 1)
	if err != nil {
		t.Fatalf("second-segment unwrap: %v", err)
	}
	if got := landedOn(direct, "adhd.status"); got != "node-c" {
		t.Fatalf("second segment landed on %q, want node-c", got)
	}

	// One tick late: the grace window still accepts it. The route was valid
	// when negotiated; a boundary crossing should not strand the caller.
	tick.Store(89)
	late := call(segments)
	if late.Err != nil {
		t.Fatalf("one-tick-late path refused: %d %s", late.Err.Code, late.Err.Message)
	}
	lateTerminal, err := unwrap(late.Result, 2)
	if err != nil {
		t.Fatalf("one-tick-late unwrap: %v", err)
	}
	if got := landedOn(lateTerminal, "adhd.status"); got != "node-c" {
		t.Fatalf("one-tick-late route landed on %q, want node-c", got)
	}

	// Two ticks late is stale. Verification does not renew it.
	tick.Store(90)
	stale := call(segments)
	if stale.Err == nil || stale.Err.Code != -32010 {
		t.Fatalf("stale path should be refused with -32010: %+v", stale)
	}
	if !strings.Contains(stale.Err.Message, "expired") {
		t.Fatalf("stale error did not name expiry: %s", stale.Err.Message)
	}
	if staleAlerts.Load() != 1 {
		t.Fatalf("stale alerts = %d, want 1", staleAlerts.Load())
	}

	verified := postChain(t, a.url, rpcParams("adhd.path.verify", map[string]interface{}{
		"target.isotope": "node-c",
		"feature_id":     feature,
	}))
	if verified.Err != nil {
		t.Fatalf("verify: %d %s", verified.Err.Code, verified.Err.Message)
	}
	verifiedPaths, _ := verified.Result["paths"].([]interface{})
	first, _ := verifiedPaths[0].(map[string]interface{})
	if valid, _ := first["valid"].(bool); valid {
		t.Fatalf("stale path verified as valid: %v", first)
	}
	if reason, _ := first["reason"].(string); reason != "stale_tick" {
		t.Fatalf("verify reason = %q, want stale_tick", reason)
	}
	if staleAlerts.Load() != 2 {
		t.Fatalf("stale alerts after verify = %d, want 2", staleAlerts.Load())
	}

	light := a.srv.cluster.GetByName("security/path_stale")
	if light == nil || light.GetStatus() != lights.StatusRed {
		t.Fatalf("no red security/path_stale light: %+v", light)
	}
}

// TestEightHopRouteFromCallerSuppliedMap is the Dewey/phone-number case: a
// caller that knows the map for the tick supplies eight segments and the
// cluster routes all eight. There is no server-side depth cap.
func TestEightHopRouteFromCallerSuppliedMap(t *testing.T) {
	const hops = 8
	const tick = uint64(88)

	nodes := make([]*chainNode, hops+1)
	for i := range nodes {
		nodes[i] = startChainNode(t, fmt.Sprintf("node-%d", i))
		nodes[i].srv.SetTickSource(func() uint64 { return tick })
	}

	segments := make([]interface{}, 0, hops)
	for i := 0; i < hops; i++ {
		segment := PathSegment{
			ID:        strconv.Itoa(i + 1),
			From:      nodes[i].name,
			To:        nodes[i+1].url,
			FeatureID: "gh",
			Tick:      tick,
		}
		raw, err := json.Marshal(segment)
		if err != nil {
			t.Fatalf("marshal segment %d: %v", i, err)
		}
		segments = append(segments, base64.StdEncoding.EncodeToString(raw))
	}

	out := postChain(t, nodes[0].url, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "adhd.path.call",
		"params": map[string]interface{}{
			"segments": segments,
			"call":     map[string]interface{}{"method": "adhd.status"},
		},
	})
	if out.Err != nil {
		t.Fatalf("eight-hop route: %d %s", out.Err.Code, out.Err.Message)
	}
	terminal, err := unwrap(out.Result, hops)
	if err != nil {
		t.Fatalf("unwrap %d hops: %v", hops, err)
	}
	if got := landedOn(terminal, "adhd.status"); got != nodes[hops].name {
		t.Fatalf("eight hops landed on %q, want %q", got, nodes[hops].name)
	}
}

// TestNegotiateDiscoversFourHops is the default discovery depth: the negotiator
// walks the peer graph four hops without the caller assembling the map, and the
// route is one segment per hop.
func TestNegotiateDiscoversFourHops(t *testing.T) {
	names := []string{"node-a", "node-b", "node-c", "node-d", "node-e"}
	nodes := make([]*chainNode, len(names))
	for i, name := range names {
		nodes[i] = startChainNode(t, name)
		identity := isotope.NewInstanceIdentity()
		nodes[i].srv.SetInstanceIdentity(identity.Isotope, identity.ComputeReceipt, identity.VerifyReceipt)
		nodes[i].srv.SetTrustLevel(3 + i)
		nodes[i].srv.SetTickSource(func() uint64 { return 88 })
	}
	// A chain: each node knows only its successor.
	for i := 0; i < len(nodes)-1; i++ {
		next := nodes[i+1]
		nodes[i].srv.mu.Lock()
		nodes[i].srv.clusterPeers[next.name] = &clusterPeerInfo{Name: next.name, Endpoint: next.url}
		nodes[i].srv.mu.Unlock()
	}

	negotiated := postChain(t, nodes[0].url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-e",
		"feature_id":     "gh",
	}))
	if negotiated.Err != nil {
		t.Fatalf("negotiate: %d %s", negotiated.Err.Code, negotiated.Err.Message)
	}
	paths, _ := negotiated.Result["paths"].([]interface{})
	if len(paths) == 0 {
		t.Fatalf("no four-hop route discovered: %v", negotiated.Result)
	}
	path, _ := paths[0].(map[string]interface{})
	if hops, _ := path["hops"].(float64); hops != 4 {
		t.Fatalf("route has %v hops, want 4: %v", path["hops"], path)
	}
	segments, _ := path["segments"].([]interface{})
	if len(segments) != 4 {
		t.Fatalf("route has %d segments, want 4", len(segments))
	}

	out := postChain(t, nodes[0].url, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "adhd.path.call",
		"params": map[string]interface{}{
			"segments": segments,
			"call":     map[string]interface{}{"method": "adhd.status"},
		},
	})
	if out.Err != nil {
		t.Fatalf("four-hop call: %d %s", out.Err.Code, out.Err.Message)
	}
	terminal, err := unwrap(out.Result, 4)
	if err != nil {
		t.Fatalf("unwrap four hops: %v", err)
	}
	if got := landedOn(terminal, "adhd.status"); got != "node-e" {
		t.Fatalf("four hops landed on %q, want node-e", got)
	}

	// A shallower budget stops at the depth it was given.
	shallow := postChain(t, nodes[0].url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-e",
		"feature_id":     "gh",
		"max_hops":       float64(2),
	}))
	if shallow.Err != nil {
		t.Fatalf("shallow negotiate: %d %s", shallow.Err.Code, shallow.Err.Message)
	}
	if found, _ := shallow.Result["paths_found"].(float64); found != 0 {
		t.Fatalf("max_hops=2 found %v routes to a 4-hop target: %v", found, shallow.Result)
	}
}

// TestPathHopsAreMirroredPerHop is the backup feed: every hop emits one
// metadata frame for the tick, so the backup can reconstruct the map traversal
// without receiving the traffic itself.
func TestPathHopsAreMirroredPerHop(t *testing.T) {
	names := []string{"node-a", "node-b", "node-c", "node-d", "node-e"}
	nodes := make([]*chainNode, len(names))
	for i, name := range names {
		nodes[i] = startChainNode(t, name)
		identity := isotope.NewInstanceIdentity()
		nodes[i].srv.SetInstanceIdentity(identity.Isotope, identity.ComputeReceipt, identity.VerifyReceipt)
		nodes[i].srv.SetTrustLevel(3 + i)
		nodes[i].srv.SetTickSource(func() uint64 { return 88 })
	}
	for i := 0; i < len(nodes)-1; i++ {
		next := nodes[i+1]
		nodes[i].srv.mu.Lock()
		nodes[i].srv.clusterPeers[next.name] = &clusterPeerInfo{Name: next.name, Endpoint: next.url}
		nodes[i].srv.mu.Unlock()
	}

	var mu sync.Mutex
	var frames []mirror.Frame
	for _, node := range nodes {
		node.srv.SetMirrorSink(func(frame mirror.Frame) {
			mu.Lock()
			frames = append(frames, frame)
			mu.Unlock()
		})
	}

	negotiated := postChain(t, nodes[0].url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-e",
		"feature_id":     "gh",
	}))
	paths, _ := negotiated.Result["paths"].([]interface{})
	if len(paths) == 0 {
		t.Fatalf("no route: %v", negotiated.Result)
	}
	path, _ := paths[0].(map[string]interface{})
	segments, _ := path["segments"].([]interface{})

	out := postChain(t, nodes[0].url, map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "adhd.path.call",
		"params": map[string]interface{}{
			"segments": segments,
			"call":     map[string]interface{}{"method": "adhd.status"},
		},
	})
	if out.Err != nil {
		t.Fatalf("call: %d %s", out.Err.Code, out.Err.Message)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 4 {
		t.Fatalf("mirrored %d hops, want 4: %+v", len(frames), frames)
	}
	seen := map[string]bool{}
	for i, frame := range frames {
		if frame.Tick != 88 {
			t.Errorf("frame %d negotiated tick = %d, want 88", i, frame.Tick)
		}
		if frame.UsedTick != 88 {
			t.Errorf("frame %d used tick = %d, want 88", i, frame.UsedTick)
		}
		if frame.Kind != "path.hop" || frame.Feature != "gh" || frame.Path == "" || frame.To == "" {
			t.Errorf("frame %d incomplete: %+v", i, frame)
		}
		if seen[frame.Path] {
			t.Errorf("frame %d repeats path %s", i, frame.Path)
		}
		seen[frame.Path] = true
	}
}

// TestPathHopCannotEnumerateTools closes the grace-window recon hole: a valid
// tear tag lets a caller execute along the map, not list the surface that
// changes every tick. The refusal happens at the hop that sees the real method
// and propagates to the caller.
func TestPathHopCannotEnumerateTools(t *testing.T) {
	a := startChainNode(t, "node-a")
	b := startChainNode(t, "node-b")
	c := startChainNode(t, "node-c")
	for _, node := range []*chainNode{a, b, c} {
		identity := isotope.NewInstanceIdentity()
		node.srv.SetInstanceIdentity(identity.Isotope, identity.ComputeReceipt, identity.VerifyReceipt)
		node.srv.SetTrustLevel(4)
		node.srv.SetTickSource(func() uint64 { return 88 })
	}
	a.srv.mu.Lock()
	a.srv.clusterPeers["node-b"] = &clusterPeerInfo{Name: "node-b", Endpoint: b.url}
	a.srv.mu.Unlock()
	b.srv.mu.Lock()
	b.srv.clusterPeers["node-c"] = &clusterPeerInfo{Name: "node-c", Endpoint: c.url}
	b.srv.mu.Unlock()

	var scopeAlerts atomic.Int64
	for _, node := range []*chainNode{a, b, c} {
		node.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
			if event == "path_scope" {
				scopeAlerts.Add(1)
			}
		})
	}

	negotiated := postChain(t, a.url, rpcParams("adhd.path.negotiate", map[string]interface{}{
		"target.isotope": "node-c",
		"feature_id":     "gh",
	}))
	paths, _ := negotiated.Result["paths"].([]interface{})
	if len(paths) == 0 {
		t.Fatalf("no route: %v", negotiated.Result)
	}
	path, _ := paths[0].(map[string]interface{})
	segments, _ := path["segments"].([]interface{})

	call := func(method string) rpcOutcome {
		return postChain(t, a.url, map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "adhd.path.call",
			"params": map[string]interface{}{
				"segments": segments,
				"call":     map[string]interface{}{"method": method},
			},
		})
	}

	for _, method := range []string{"tools/list", "resources/list", "prompts/list", "initialize"} {
		out := call(method)
		if out.Err == nil || out.Err.Code != -32011 {
			t.Fatalf("%s should be refused with -32011, got %+v", method, out)
		}
		if !strings.Contains(out.Err.Message, "does not grant") {
			t.Fatalf("%s refusal did not name the scope: %s", method, out.Err.Message)
		}
	}
	if scopeAlerts.Load() != 4 {
		t.Fatalf("path_scope alerts = %d, want 4 (one per enumeration attempt)", scopeAlerts.Load())
	}
	light := a.srv.cluster.GetByName("security/path_scope")
	if light == nil || light.GetStatus() != lights.StatusRed {
		t.Fatalf("no red security/path_scope light: %+v", light)
	}

	// Execution along the same route is unaffected.
	exec := call("adhd.status")
	if exec.Err != nil {
		t.Fatalf("status through the route: %d %s", exec.Err.Code, exec.Err.Message)
	}
	terminal, err := unwrap(exec.Result, 2)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if got := landedOn(terminal, "adhd.status"); got != "node-c" {
		t.Fatalf("status landed on %q, want node-c", got)
	}
}
