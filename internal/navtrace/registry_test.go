package navtrace

import (
	"sync"
	"testing"
)

func TestIDIsDerivedAndStable(t *testing.T) {
	const name = "adhd.skill.route-plan.call"
	first := ID(name)
	if first == 0 {
		t.Fatal("zero is reserved")
	}
	for i := 0; i < 100; i++ {
		if got := ID(name); got != first {
			t.Fatalf("id moved on call %d: %d -> %d", i, first, got)
		}
	}
	if ID("adhd.skill.other.call") == first {
		t.Fatal("different names share an id")
	}
}

func TestRegistryResolvesAndAliases(t *testing.T) {
	r := NewRegistry()
	id, err := r.Register("adhd.skill.route-plan.call")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if name, ok := r.Resolve(id); !ok || name != "adhd.skill.route-plan.call" {
		t.Fatalf("resolve: %q %v", name, ok)
	}

	// A refinement renames the capability; the lineage keeps the id.
	if err := r.Alias("adhd.skill.plan-route.call", "adhd.skill.route-plan.call"); err != nil {
		t.Fatalf("alias: %v", err)
	}
	if got := r.ID("adhd.skill.plan-route.call"); got != id {
		t.Fatalf("aliased id %d, want %d", got, id)
	}
}

func TestRegistryRefusesEmptyAliases(t *testing.T) {
	r := NewRegistry()
	if err := r.Alias("", "x"); err == nil {
		t.Fatal("empty alias should fail")
	}
	if err := r.Alias("x", ""); err == nil {
		t.Fatal("empty earlier name should fail")
	}
}

func TestRegistryIsConcurrent(t *testing.T) {
	r := NewRegistry()
	const workers = 16
	var wg sync.WaitGroup
	ids := make([]uint32, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, err := r.Register("adhd.skill.route-plan.call")
			if err != nil {
				t.Errorf("register: %v", err)
				return
			}
			ids[n] = id
		}(i)
	}
	wg.Wait()
	for i := 1; i < workers; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent registration disagreed: %d vs %d", ids[i], ids[0])
		}
	}
}
