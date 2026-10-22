//go:build soak

package soak

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSoakSteadyState runs a prime + N prime-plus topology as real processes
// for a configurable wall-clock duration, drives steady MCP traffic through
// every node, samples resource use, and optionally kills/restarts nodes.
//
// Defaults are short enough to run on a laptop (2m). The point of the test is
// the shape of the run, not the default: ADHD_SOAK_DURATION=8h is the soak.
func TestSoakSteadyState(t *testing.T) {
	if testing.Short() {
		t.Skip("soak test skipped in -short mode")
	}

	duration := envDuration("ADHD_SOAK_DURATION", 2*time.Minute)
	plusCount := envInt("ADHD_SOAK_NODES", 3)
	rate := max(envInt("ADHD_SOAK_RATE", 2), 1)
	sampleEvery := max(envDuration("ADHD_SOAK_SAMPLE", 10*time.Second), time.Second)
	chaos := envBool("ADHD_SOAK_CHAOS")
	chaosEvery := envDuration("ADHD_SOAK_CHAOS_EVERY", 30*time.Second)

	failureBudget := 0.01
	if chaos {
		failureBudget = 0.10 // restarts genuinely drop in-flight calls
	}

	root := t.TempDir()
	prime := newLoopbackNode(t, root, "prime", false, "")
	prime.start(t)
	t.Cleanup(prime.stop)

	plus := make([]*node, plusCount)
	for i := range plus {
		n := newLoopbackNode(t, root, fmt.Sprintf("plus-%02d", i), true, prime.endpoint())
		n.start(t)
		t.Cleanup(n.stop)
		plus[i] = n
	}
	all := append([]*node{prime}, plus...)

	t.Logf("topology: prime=%s plus=%d rate=%d/s duration=%v chaos=%v",
		prime.addr, plusCount, rate, duration, chaos)

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var calls, failures atomic.Int64
	var wg sync.WaitGroup
	for _, n := range all {
		wg.Add(1)
		go func(n *node) {
			defer wg.Done()
			tick := time.NewTicker(time.Second / time.Duration(rate))
			defer tick.Stop()
			i := 0
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					method := soakMethods[i%len(soakMethods)]
					i++
					calls.Add(1)
					if err := callMCP(n.endpoint(), method); err != nil {
						failures.Add(1)
					}
				}
			}
		}(n)
	}

	start := time.Now()
	samples := make([]sample, 0, int(duration/sampleEvery)+1)
	restarts := 0
	lastChaos := start
	tick := time.NewTicker(sampleEvery)
	defer tick.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			s := sample{At: time.Now(), Nodes: make(map[string]nodeSample, len(all))}
			for _, n := range all {
				ns, err := n.sample()
				if err != nil {
					t.Errorf("sample %s: %v", n.name, err)
					continue
				}
				s.Nodes[n.name] = ns
			}
			samples = append(samples, s)

			if chaos && time.Since(lastChaos) >= chaosEvery {
				victim := all[restarts%len(all)]
				t.Logf("chaos: restarting %s", victim.name)
				victim.restart(t)
				restarts++
				lastChaos = time.Now()
				t.Logf("chaos: %s back on %s", victim.name, victim.addr)
			}
		}
	}

	cancel()
	wg.Wait()

	// ---- assertions --------------------------------------------------------

	for _, n := range all {
		if !n.alive() {
			t.Errorf("%s exited during the run (starts=%d)", n.name, n.startCount())
		}
	}

	if len(samples) < 4 {
		t.Fatalf("only %d samples collected; run too short to judge a trend", len(samples))
	}

	warmup := len(samples) / 10 // ignore heap warm-up in a long run
	for _, n := range all {
		name := n.name
		rss := make([]float64, 0, len(samples))
		fds := make([]float64, 0, len(samples))
		logBytes := make([]float64, 0, len(samples))
		for _, s := range samples {
			ns, ok := s.Nodes[name]
			if !ok {
				continue
			}
			rss = append(rss, float64(ns.RSSKB)/1024) // MiB
			fds = append(fds, float64(ns.FDs))
			logBytes = append(logBytes, float64(ns.LogBytes))
		}

		early, late := thirds(rss, warmup)
		if late > early*(1+0.5) && late-early > 50 {
			t.Errorf("%s RSS grew %.1f MiB -> %.1f MiB (%.0f%%) over %v",
				name, early, late, (late/early-1)*100, duration)
		}

		early, late = thirds(fds, warmup)
		if late-early > 32 {
			t.Errorf("%s open FDs grew %.0f -> %.0f over %v", name, early, late, duration)
		}

		if len(logBytes) >= 2 && logBytes[len(logBytes)-1] <= logBytes[0] {
			t.Errorf("%s JSONL did not grow (%d -> %d bytes); traffic stopped reaching it",
				name, int64(logBytes[0]), int64(logBytes[len(logBytes)-1]))
		}
	}

	for _, n := range all {
		total, bad, methods, err := logCheck(n.log)
		if err != nil {
			t.Errorf("%s log: %v", n.name, err)
			continue
		}
		if total == 0 {
			t.Errorf("%s log is empty", n.name)
		}
		if bad > 0 {
			t.Errorf("%s log: %d/%d malformed JSON lines", n.name, bad, total)
		}
		seen := 0
		for _, m := range soakMethods {
			if methods[m] > 0 {
				seen++
			}
		}
		if seen < 2 {
			t.Errorf("%s log shows only %d/%d call methods; traffic did not exercise the surface",
				n.name, seen, len(soakMethods))
		}
	}

	failRate := 0.0
	if c := calls.Load(); c > 0 {
		failRate = float64(failures.Load()) / float64(c)
	}
	if failRate > failureBudget {
		t.Errorf("MCP failure rate %.2f%% over %d calls (budget %.0f%%)",
			failRate*100, calls.Load(), failureBudget*100)
	}

	t.Logf("calls=%d failures=%d (%.2f%%) restarts=%d samples=%d elapsed=%v",
		calls.Load(), failures.Load(), failRate*100, restarts, len(samples), time.Since(start))

	writeReport(t, report{
		Started:  start,
		Duration: duration.String(),
		Nodes:    names(all),
		Calls:    calls.Load(),
		Failures: failures.Load(),
		Restarts: restarts,
		Samples:  samples,
		Passed:   !t.Failed(),
	})
}

func names(nodes []*node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.name
	}
	return out
}
