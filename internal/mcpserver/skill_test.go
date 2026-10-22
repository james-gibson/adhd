package mcpserver

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/proxy"
)

// TestDynamicSkillEmbedsWorkingProxiesOnce is the bounded counterpart to the
// honeypot field. The ten working upstreams are not registered as individual
// proxies at all: they are closed over by a single dynamic skill that uses them
// explicitly, and the skill is single-use. The catalogue an agent can discover
// is the skill plus decoys, so random turns are noise and there is no reusable
// handle on the working set.
func TestDynamicSkillIsATearOffTicketQueue(t *testing.T) {
	node := startChainNode(t, "node-a")

	const (
		working       = 10
		decoyTools    = 10000
		randomTurns   = 500
		tickets       = 4
		concurrentUse = 8
	)
	skillName := "adhd.skill.check-working.call"

	// The working set: ten real upstreams, reachable only through the skill's
	// closure. None of them gets a callable tool name.
	upstreams := make([]string, 0, working)
	for i := 0; i < working; i++ {
		upstream := fakeUpstreamMCP(t, fmt.Sprintf("working-%02d", i))
		t.Cleanup(upstream.Close)
		upstreams = append(upstreams, upstream.URL)
	}

	// Tear-off tickets: each use pulls one ticket off the queue. The queue can
	// hold several, not just one; exhaustion is what refuses the call.
	queue := make(chan struct{}, tickets)
	for i := 0; i < tickets; i++ {
		queue <- struct{}{}
	}
	var checkedTotal atomic.Int64

	skill := &dynamicTool{
		name:        skillName,
		description: "tear-off queue of checks across the ten embedded upstreams",
		inputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		handler: func(ctx context.Context, _ interface{}) (interface{}, *jsonrpcError) {
			select {
			case <-queue:
				// one tear-off ticket consumed
			default:
				node.srv.fireSecurity("warn", "skill_reuse", map[string]interface{}{
					"tool": skillName,
				})
				return nil, &jsonrpcError{Code: -32000, Message: "skill queue exhausted: every ticket was used"}
			}

			answers := make([]interface{}, 0, len(upstreams))
			for _, target := range upstreams {
				resp, err := node.srv.proxyExecutor.ExecuteProxy(ctx, proxy.ProxyRequest{
					TargetEndpoint: target,
					Call: map[string]interface{}{
						"jsonrpc": "2.0", "id": 1, "method": "tools/list",
					},
				})
				if err != nil {
					return nil, &jsonrpcError{Code: -32603, Message: err.Error()}
				}
				if resp.Error != nil {
					return nil, &jsonrpcError{Code: resp.Error.Code, Message: resp.Error.Message}
				}
				answers = append(answers, resp.Result)
			}
			checkedTotal.Add(int64(len(answers)))
			return map[string]interface{}{"checked": len(answers), "answers": answers}, nil
		},
	}
	node.srv.mu.Lock()
	node.srv.dynamicTools = append(node.srv.dynamicTools, skill)
	node.srv.mu.Unlock()

	for i := 0; i < decoyTools; i++ {
		node.srv.RegisterHoneypotTool(
			fmt.Sprintf("adhd.project.decoy-%05d.call", i),
			"decoy", map[string]interface{}{"ok": true},
		)
	}

	var honeypotAlerts, reuseAlerts atomic.Int64
	node.srv.SetSecurityAlertHandler(func(level, event string, details map[string]interface{}) {
		switch event {
		case "honeypot_triggered":
			honeypotAlerts.Add(1)
		case "skill_reuse":
			reuseAlerts.Add(1)
		}
	})

	// The working set is not individually addressable.
	for i := 0; i < working; i++ {
		name := fmt.Sprintf("adhd.project.working-%02d.call", i)
		out := postChain(t, node.url, terminalCall(name))
		if out.Err == nil || out.Err.Code != -32601 {
			t.Fatalf("%s should not be callable: %+v", name, out)
		}
	}

	listed := postChain(t, node.url, terminalCall("tools/list"))
	tools, _ := listed.Result["tools"].([]interface{})
	if len(tools) == 0 {
		t.Fatalf("tools/list returned nothing: %v", listed.Result)
	}
	foundSkill := false
	for _, entry := range tools {
		tool, _ := entry.(map[string]interface{})
		name, _ := tool["name"].(string)
		if name == skillName {
			foundSkill = true
		}
		if strings.Contains(name, "working-") {
			t.Fatalf("working upstream leaked into the catalogue as %q", name)
		}
	}
	if !foundSkill {
		t.Fatalf("skill %q is not listed", skillName)
	}

	// Concurrent first use: exactly one caller may consume the skill.
	var wg sync.WaitGroup
	var successes, reused atomic.Int64
	var checked atomic.Int64
	for i := 0; i < concurrentUse; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := postRaw(node.url, terminalCall(skillName))
			if err != nil {
				t.Errorf("skill call: %v", err)
				return
			}
			if out.Err != nil {
				if strings.Contains(out.Err.Message, "exhausted") {
					reused.Add(1)
					return
				}
				t.Errorf("unexpected skill error: %d %s", out.Err.Code, out.Err.Message)
				return
			}
			successes.Add(1)
			if n, ok := out.Result["checked"].(float64); ok {
				checked.Store(int64(n))
			}
		}()
	}
	wg.Wait()

	if successes.Load() != tickets {
		t.Fatalf("skill served %d calls, want one per ticket (%d)", successes.Load(), tickets)
	}
	if reused.Load() != concurrentUse-tickets {
		t.Fatalf("exhaustion refusals = %d, want %d", reused.Load(), concurrentUse-tickets)
	}
	if checkedTotal.Load() != int64(tickets*working) {
		t.Fatalf("skill checked %d upstreams, want %d", checkedTotal.Load(), tickets*working)
	}
	if reuseAlerts.Load() != concurrentUse-tickets {
		t.Fatalf("reuse alerts = %d, want %d: exhaustion was not surfaced", reuseAlerts.Load(), concurrentUse-tickets)
	}

	reuseLight := node.srv.cluster.GetByName("security/skill_reuse")
	if reuseLight == nil || reuseLight.GetStatus() != lights.StatusRed {
		t.Fatalf("no red security/skill_reuse light: %+v", reuseLight)
	}

	// Random turns stay in the discoverable catalogue: all noise.
	rng := rand.New(rand.NewSource(11))
	for turn := 0; turn < randomTurns; turn++ {
		name := fmt.Sprintf("adhd.project.decoy-%05d.call", rng.Intn(decoyTools))
		out := postChain(t, node.url, terminalCall(name))
		if out.Err != nil {
			t.Fatalf("turn %d: %d %s", turn, out.Err.Code, out.Err.Message)
		}
		if out.Result["ok"] != true {
			t.Fatalf("turn %d: decoy returned %v", turn, out.Result)
		}
	}
	if honeypotAlerts.Load() != randomTurns {
		t.Fatalf("honeypot alerts = %d, want %d", honeypotAlerts.Load(), randomTurns)
	}
	if remaining := len(queue); remaining != 0 {
		t.Fatalf("%d tickets left after %d calls; the queue was not consumed exactly once per call", remaining, successes.Load())
	}
}
