package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
)

// fakeUpstreamResult returns a JSON-RPC server that answers every method with a
// fixed result map — enough to stand in for an agent that contributes a partial.
func fakeUpstreamResult(t *testing.T, result map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID interface{} `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": req.ID, "result": result,
		})
	}))
}

// TestOrdinalReassemblyFromPartials captures the composable-cluster shape: tools
// (and the work) are distributed across nodes, so an agent assembles an ordinal
// response from *partials* — overlapping, duplicated, and out of order.
//
// The client's job is the merge: union the contributions, drop the duplicates,
// sort, and the ordinal falls out. Nothing owns the whole sequence; the whole
// sequence is what the cluster adds up to.
func TestOrdinalReassemblyFromPartials(t *testing.T) {
	s := newProxyTestServer(t)

	// Three contributing agents. Their pieces overlap (2,3,5 appear twice) and
	// each arrives unsorted; map iteration also randomises proxy call order.
	contributions := map[string][]interface{}{
		"alpha": {3, 1, 2},
		"beta":  {2, 3, 4, 5},
		"gamma": {5, 6, 7, 8, 9, 10},
	}
	for name, nums := range contributions {
		up := fakeUpstreamResult(t, map[string]interface{}{"ordinals": nums})
		defer up.Close()
		s.registerProjectTool("cluster", name, "", up.URL)
	}

	// The agent's merge: collect from every proxy, dedupe, sort.
	set := map[int]bool{}
	for name := range contributions {
		res := mcpRPC(t, s, "adhd.project."+name+".call", map[string]interface{}{"method": "tools/list"})
		got, ok := res["ordinals"].([]interface{})
		if !ok {
			t.Fatalf("%s: no ordinals in %v", name, res)
		}
		for _, v := range got {
			set[int(v.(float64))] = true
		}
	}
	var merged []int
	for k := range set {
		merged = append(merged, k)
	}
	sort.Ints(merged)

	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("reassembled ordinal:\n got %v\nwant %v", merged, want)
	}
}

// TestAlphabetReassemblyFromPartials is the same shape on a non-numeric ordinal:
// four agents each hold a slice of A–Z, with an overlap, assembled by the client.
func TestAlphabetReassemblyFromPartials(t *testing.T) {
	s := newProxyTestServer(t)

	contributions := map[string][]interface{}{
		"one":   {"G", "A", "B", "C", "D", "E", "F"},
		"two":   {"H", "I", "J", "K", "L", "M"}, // overlaps K, L with "three"
		"three": {"K", "L", "N", "O", "P", "Q", "R"},
		"four":  {"S", "T", "U", "V", "W", "X", "Y", "Z"},
	}
	for name, letters := range contributions {
		up := fakeUpstreamResult(t, map[string]interface{}{"letters": letters})
		defer up.Close()
		s.registerProjectTool("cluster", name, "", up.URL)
	}

	seen := map[string]bool{}
	for name := range contributions {
		res := mcpRPC(t, s, "adhd.project."+name+".call", map[string]interface{}{"method": "tools/list"})
		got, ok := res["letters"].([]interface{})
		if !ok {
			t.Fatalf("%s: no letters in %v", name, res)
		}
		for _, v := range got {
			seen[v.(string)] = true
		}
	}
	var merged []string
	for k := range seen {
		merged = append(merged, k)
	}
	sort.Strings(merged)

	want := []string{}
	for c := 'A'; c <= 'Z'; c++ {
		want = append(want, string(c))
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("reassembled alphabet:\n got %v\nwant %v", merged, want)
	}
}

// TestOrdinalGapIsDetected is the *chaos* half — the same family as the lab's
// chaos skills and windows: inject a perturbation and assert the system *sees*
// it. Here the perturbation is a missing contribution (`5`), and the assertion is
// that the reassembly surfaces the hole rather than silently producing a
// complete-looking ordinal.
//
// A cluster that cannot see its own gap is the failure the chaos probe exists to
// catch — which is why a chaos probe's red light is a signal, not a setback.
func TestOrdinalGapIsDetected(t *testing.T) {
	s := newProxyTestServer(t)

	contributions := map[string][]interface{}{
		"alpha": {1, 2, 3, 4},
		"beta":  {6, 7, 8, 9, 10}, // 5 is withheld on purpose — the injected chaos
	}
	for name, nums := range contributions {
		up := fakeUpstreamResult(t, map[string]interface{}{"ordinals": nums})
		defer up.Close()
		s.registerProjectTool("cluster", name, "", up.URL)
	}

	seen := map[int]bool{}
	for name := range contributions {
		res := mcpRPC(t, s, "adhd.project."+name+".call", map[string]interface{}{"method": "tools/list"})
		for _, v := range res["ordinals"].([]interface{}) {
			seen[int(v.(float64))] = true
		}
	}

	var missing []int
	for n := 1; n <= 10; n++ {
		if !seen[n] {
			missing = append(missing, n)
		}
	}

	if !reflect.DeepEqual(missing, []int{5}) {
		t.Fatalf("expected the gap to be surfaced as exactly {5}, got %v (seen %v)", missing, seen)
	}
	if len(seen) == 10 {
		t.Fatal("reassembly claimed a complete ordinal despite a withheld contribution")
	}
}

// TestOrdinalRequiresTheCluster frames the suite as a *membership predicate*.
// A single contributor holds only its partial, so a node outside the cluster can
// never complete the ordinal; only the union — what the cluster adds up to —
// passes. Passing the whole suite is therefore proof of belonging, not just of
// skill.
func TestOrdinalRequiresTheCluster(t *testing.T) {
	s := newProxyTestServer(t)

	contributions := map[string][]interface{}{
		"alpha": {1, 2, 3, 4},
		"beta":  {5, 6, 7, 8},
		"gamma": {9, 10},
	}
	for name, nums := range contributions {
		up := fakeUpstreamResult(t, map[string]interface{}{"ordinals": nums})
		defer up.Close()
		s.registerProjectTool("cluster", name, "", up.URL)
	}

	toSet := func(res map[string]interface{}) map[int]bool {
		out := map[int]bool{}
		for _, v := range res["ordinals"].([]interface{}) {
			out[int(v.(float64))] = true
		}
		return out
	}

	// One node, alone: a partial, and it knows it.
	outside := toSet(mcpRPC(t, s, "adhd.project.alpha.call", map[string]interface{}{"method": "tools/list"}))
	if len(outside) == 10 {
		t.Fatal("a single contributor should not hold the whole ordinal")
	}

	// The cluster: the union passes.
	whole := map[int]bool{}
	for name := range contributions {
		for n := range toSet(mcpRPC(t, s, "adhd.project."+name+".call", map[string]interface{}{"method": "tools/list"})) {
			whole[n] = true
		}
	}
	if len(whole) != 10 {
		t.Fatalf("the cluster union should complete 1–10, got %d distinct", len(whole))
	}
	if len(outside) >= len(whole) {
		t.Fatal("membership bought nothing: the lone node matched the cluster")
	}
}
