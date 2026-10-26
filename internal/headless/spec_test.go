package headless

import (
	"fmt"
	"math"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/mcpserver"
)

func TestSpecFidelityTracksCallers(t *testing.T) {
	s := New(config.DefaultConfig())

	// Same host, different ports -> one caller, exponentially weighted.
	s.recordSpecFidelity("10.0.0.1:5555", mcpserver.SpecFidelity{Score: 100, Level: 6})
	s.recordSpecFidelity("10.0.0.1:5556", mcpserver.SpecFidelity{Score: 50, Level: 3})
	s.recordSpecFidelity("10.0.0.2:1234", mcpserver.SpecFidelity{Score: 20, Level: 1})

	got := s.GetSpecFidelity().(map[string]interface{})
	callers := got["callers"].([]specCallerScore)
	if len(callers) != 2 {
		t.Fatalf("got %d callers, want 2", len(callers))
	}
	if callers[0].Caller != "10.0.0.1" {
		t.Errorf("leading caller = %q, want the higher-scoring 10.0.0.1", callers[0].Caller)
	}
	if math.Abs(callers[0].Score-90) > 0.001 { // 0.8*100 + 0.2*50
		t.Errorf("weighted score = %.2f, want 90", callers[0].Score)
	}
	if callers[0].Samples != 2 {
		t.Errorf("samples = %d, want 2", callers[0].Samples)
	}
	if math.Abs(got["average"].(float64)-55) > 0.001 {
		t.Errorf("average = %.2f, want 55", got["average"])
	}
}

func TestSpecFidelityCallerCap(t *testing.T) {
	s := New(config.DefaultConfig())
	for i := 0; i < 300; i++ {
		s.recordSpecFidelity(fmt.Sprintf("10.0.%d.%d:1", i/250, i%250), mcpserver.SpecFidelity{Score: 60, Level: 3})
	}
	callers := s.GetSpecFidelity().(map[string]interface{})["callers"].([]specCallerScore)
	if len(callers) != 256 {
		t.Fatalf("tracked %d callers, want the cap of 256", len(callers))
	}
}

// The next two tests characterise the current trust binding. They pass today
// and are meant to be replaced when the score is keyed by identity rather than
// address: a passing test here means the gap still exists.

func TestSpecFidelityFollowsTheAddressNotTheIdentity(t *testing.T) {
	s := New(config.DefaultConfig())

	// A trusted caller at one address.
	s.recordSpecFidelity("10.0.0.1:5555", mcpserver.SpecFidelity{Score: 100, Level: 6})

	// A different client occupies the same address. It inherits the history:
	// the score is an exponential average over the address, not the identity.
	s.recordSpecFidelity("10.0.0.1:6666", mcpserver.SpecFidelity{Score: 20, Level: 1})
	callers := s.GetSpecFidelity().(map[string]interface{})["callers"].([]specCallerScore)
	if len(callers) != 1 || callers[0].Caller != "10.0.0.1" {
		t.Fatalf("callers = %+v, want one entry for the shared address", callers)
	}
	if math.Abs(callers[0].Score-84) > 0.001 { // 0.8*100 + 0.2*20
		t.Fatalf("score = %.2f, want 84: a new client inherited the address's history", callers[0].Score)
	}
	if callers[0].Samples != 2 {
		t.Fatalf("samples = %d, want 2", callers[0].Samples)
	}

	// The same client moving to a new address starts from nothing. There is no
	// argument to recordSpecFidelity that could have carried its identity.
	s.recordSpecFidelity("10.0.0.2:5555", mcpserver.SpecFidelity{Score: 20, Level: 1})
	callers = s.GetSpecFidelity().(map[string]interface{})["callers"].([]specCallerScore)
	for _, c := range callers {
		if c.Caller == "10.0.0.2" && c.Samples != 1 {
			t.Fatalf("new address has %d samples, want a fresh 1: rotation reset the record", c.Samples)
		}
	}
}

func TestSpecFidelityFloodEvictsTheUnverifiedFirst(t *testing.T) {
	s := New(config.DefaultConfig())

	// A chain-verified caller with a poor request score: the chain is what
	// ranks it, not its manners.
	s.recordChainedFidelity("iso-trusted", 4, mcpserver.SpecFidelity{Score: 0, Level: 0})
	for i := 0; i < 255; i++ {
		s.recordSpecFidelity(fmt.Sprintf("10.1.%d.%d:1", i/250, i%250), mcpserver.SpecFidelity{Score: 100, Level: 6})
	}
	// One more unverified address crosses the cap. It must evict an unverified
	// peer, not the chain.
	s.recordSpecFidelity("10.2.0.1:1", mcpserver.SpecFidelity{Score: 100, Level: 6})

	callers := s.GetSpecFidelity().(map[string]interface{})["callers"].([]specCallerScore)
	if len(callers) != 256 {
		t.Fatalf("tracked %d callers, want 256", len(callers))
	}
	found := false
	for _, c := range callers {
		if c.Caller == "iso-trusted" {
			found = true
		}
	}
	if !found {
		t.Fatal("a perfect-scoring flood evicted the chain-verified caller")
	}
}

func TestSpecFidelityFlatFloodEvictsByAge(t *testing.T) {
	s := New(config.DefaultConfig())
	for i := 0; i < 256; i++ {
		s.recordSpecFidelity(fmt.Sprintf("10.3.%d.%d:1", i/250, i%250), mcpserver.SpecFidelity{Score: 60, Level: 3})
	}
	// Equal trust: the oldest is the only fair victim.
	s.recordSpecFidelity("10.4.0.1:1", mcpserver.SpecFidelity{Score: 60, Level: 3})
	callers := s.GetSpecFidelity().(map[string]interface{})["callers"].([]specCallerScore)
	if len(callers) != 256 {
		t.Fatalf("tracked %d callers, want 256", len(callers))
	}
	for _, c := range callers {
		if c.Caller == "10.3.0.0" {
			t.Fatal("a flat flood should evict the oldest equal-trust entry")
		}
	}
}

func TestSpecFidelityScoreIsNotTrust(t *testing.T) {
	s := New(config.DefaultConfig())
	// The score is reported, never ranked.
	s.recordChainedFidelity("iso-low-score", 2, mcpserver.SpecFidelity{Score: 0, Level: 0})
	s.recordChainedFidelity("iso-high-score", 0, mcpserver.SpecFidelity{Score: 100, Level: 6})

	callers := s.GetSpecFidelity().(map[string]interface{})["callers"].([]specCallerScore)
	if len(callers) != 2 {
		t.Fatalf("got %d callers, want 2", len(callers))
	}
	// The summary may sort by score, but eviction order is chain-first.
	for _, c := range callers {
		switch c.Caller {
		case "iso-low-score":
			if c.ChainRung != 2 {
				t.Fatalf("chain rung = %d, want 2", c.ChainRung)
			}
		case "iso-high-score":
			if c.ChainRung != 0 {
				t.Fatalf("unverified caller has rung %d, want 0", c.ChainRung)
			}
		}
	}
	if !lessTrusted(&specCallerScore{Caller: "iso-high-score", ChainRung: 0},
		&specCallerScore{Caller: "iso-low-score", ChainRung: 2}) {
		t.Fatal("an unverified caller with a perfect score outranked a chained one")
	}
}
