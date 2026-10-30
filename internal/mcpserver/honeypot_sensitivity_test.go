package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// A decoy's sensitivity is the 42i distance at which contacts on it become an
// alert. The distance is the caller's, not the decoy's, and it accumulates
// across contacts on the same host: a tools/call costs 16 (the advertised route
// with no stamp for it) and a direct method call costs 32 (a name invoked as a
// method the surface never offered). The tests below pin all three ends of that
// dial — silent on any contact, tolerant up to a threshold, and alerting exactly
// at the crossing — because a trap that fires at the wrong distance is either
// noise or a disarmed surface.

const sensitivityDecoy = "adhd.project.canary.call"

// joinWithSensitivity joins a cluster that installs the canary at the given
// sensitivity. It goes through the wire method rather than the register call so
// the payload's parsing and its echo are covered too.
func joinWithSensitivity(t *testing.T, node *chainNode, sensitivity interface{}) map[string]interface{} {
	t.Helper()
	// A join is only answered when the instance is wired to a watcher; the
	// decoy registration itself does not need one.
	node.srv.SetClusterJoinHandler(func(ClusterJoin) error { return nil })
	decoy := map[string]interface{}{
		"name":        sensitivityDecoy,
		"description": "internal project proxy (do not call)",
	}
	if sensitivity != nil {
		decoy["sensitivity"] = sensitivity
	}
	out := postChain(t, node.url, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "adhd.cluster.join",
		"params": map[string]interface{}{
			"name":           "probe-cluster",
			"honeypot-tools": []interface{}{decoy},
		},
	})
	if out.Err != nil {
		t.Fatalf("join refused: %v", out.Err)
	}
	return out.Result
}

// alerts records what the honeypot host fired, so a test can assert on both the
// count and the details the alert carries.
func alerts(node *chainNode) func() []map[string]interface{} {
	var mu sync.Mutex
	var seen []map[string]interface{}
	node.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		if event != "honeypot_triggered" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		copied := map[string]interface{}{"level": level}
		for k, v := range details {
			copied[k] = v
		}
		seen = append(seen, copied)
	})
	return func() []map[string]interface{} {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]interface{}{}, seen...)
	}
}

// postFresh posts one call on a connection of its own, the way a separate
// client invocation arrives. It is not a detail: a caller is an address, not a
// connection, so the tests must not let keep-alive hand them a stable port.
func postFresh(t *testing.T, url string, call map[string]interface{}) rpcOutcome {
	t.Helper()
	body, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("marshal call: %v", err)
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post to %s: %v", url, err)
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
		t.Fatalf("decode response: %v", err)
	}
	return rpcOutcome{Result: out.Result, Err: out.Error}
}

func knockViaToolsCall(t *testing.T, node *chainNode) {
	t.Helper()
	out := postFresh(t, node.url, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]interface{}{"name": sensitivityDecoy},
	})
	if out.Err != nil {
		t.Fatalf("tools/call on the decoy: %v", out.Err)
	}
}

func knockViaMethod(t *testing.T, node *chainNode) {
	t.Helper()
	out := postFresh(t, node.url, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  sensitivityDecoy,
	})
	if out.Err != nil {
		t.Fatalf("direct method call on the decoy: %v", out.Err)
	}
}

// TestHoneypotSilentByDefault keeps the pre-threshold behaviour: a decoy whose
// payload names no sensitivity fires on the first contact, however it was made.
func TestHoneypotSilentByDefault(t *testing.T) {
	node := startChainNode(t, "hp-default")
	seen := alerts(node)

	joinWithSensitivity(t, node, nil)
	knockViaToolsCall(t, node)

	got := seen()
	if len(got) != 1 {
		t.Fatalf("silent decoy: want 1 alert on the first contact, got %d", len(got))
	}
	if d := got[0]["distance_42i"]; d != 16 {
		t.Fatalf("tools/call should cost 16, alert carried distance %v", d)
	}
}

// TestHoneypotThresholdToleratesThenFires is the dial: at 48 the caller is
// tolerated at 32 and alerted at 48, and the alert says which side it crossed.
func TestHoneypotThresholdToleratesThenFires(t *testing.T) {
	node := startChainNode(t, "hp-48")
	seen := alerts(node)

	result := joinWithSensitivity(t, node, 48)
	armed, ok := result["honeypots"].([]interface{})
	if !ok || len(armed) != 1 {
		t.Fatalf("join should state the armed decoy back, got %v", result["honeypots"])
	}
	if sens := armed[0].(map[string]interface{})["sensitivity"]; sens != float64(48) {
		t.Fatalf("join echoed sensitivity %v, want 48", sens)
	}

	knockViaMethod(t, node) // 32: below the threshold
	if got := seen(); len(got) != 0 {
		t.Fatalf("32 of 48 should be tolerated, got %d alerts: %v", len(got), got)
	}

	knockViaToolsCall(t, node) // 32+16 = 48: the crossing
	got := seen()
	if len(got) != 1 {
		t.Fatalf("want 1 alert at the crossing, got %d", len(got))
	}
	if d, want := got[0]["distance_42i"], 48; d != want {
		t.Fatalf("alert carried distance %v, want %v", d, want)
	}
	if th, want := got[0]["threshold_42i"], 48; th != want {
		t.Fatalf("alert carried threshold %v, want %v", th, want)
	}
	if via := got[0]["via"]; via != "tools/call" {
		t.Fatalf("alert should name the contact that crossed, got via=%v", via)
	}
}

// TestHoneypotKeepsAccumulatingAfterItFires: a caller that keeps knocking keeps
// costing, so a later alert shows the distance it had actually reached.
func TestHoneypotKeepsAccumulatingAfterItFires(t *testing.T) {
	node := startChainNode(t, "hp-accumulate")
	seen := alerts(node)

	joinWithSensitivity(t, node, 16)
	knockViaToolsCall(t, node) // 16 — fires
	knockViaToolsCall(t, node) // 32 — fires again, carrying the new distance

	got := seen()
	if len(got) != 2 {
		t.Fatalf("want an alert per contact above threshold, got %d", len(got))
	}
	if d, want := got[1]["distance_42i"], 32; d != want {
		t.Fatalf("second alert carried distance %v, want %v", d, want)
	}
}

// TestHoneypotRefusesBadSensitivity: a threshold that is not a distance is
// refused rather than coerced, and the refusal is visible to the caller.
func TestHoneypotRefusesBadSensitivity(t *testing.T) {
	node := startChainNode(t, "hp-bad")
	seen := alerts(node)

	result := joinWithSensitivity(t, node, "very")
	if _, ok := result["conflicts"]; !ok {
		t.Fatalf("a non-numeric sensitivity should be reported as a conflict, got %v", result)
	}

	out := postChain(t, node.url, map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params":  map[string]interface{}{"name": sensitivityDecoy},
	})
	if out.Err == nil || out.Err.Code != -32601 {
		t.Fatalf("a refused decoy must not be registered, got %v", out)
	}
	if got := seen(); len(got) != 0 {
		t.Fatalf("a refused decoy must not be armed, got %d alerts", len(got))
	}
}
