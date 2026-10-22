package trust

import "testing"

func acceptAll(Attestation) bool { return true }
func rejectAll(Attestation) bool { return false }

func TestMergeRequiresAVerifiedReceipt(t *testing.T) {
	v := NewView("a")
	claim := Attestation{Subject: "b", Issuer: "a", Rung: 3, Epoch: 1, Receipt: "r"}

	if v.Merge(claim, nil) {
		t.Fatal("a nil verifier accepted a claim")
	}
	if v.Merge(claim, rejectAll) {
		t.Fatal("a rejecting verifier accepted a claim")
	}
	if !v.Merge(claim, acceptAll) {
		t.Fatal("a verified claim was not merged")
	}
	if v.Claims["b"].Rung != 3 {
		t.Fatalf("rung = %d, want 3", v.Claims["b"].Rung)
	}
}

func TestMergeKeepsTheNewerEpoch(t *testing.T) {
	v := NewView("a")
	_ = v.Merge(Attestation{Subject: "b", Issuer: "a", Rung: 2, Epoch: 1}, acceptAll)

	if v.Merge(Attestation{Subject: "b", Issuer: "a", Rung: 4, Epoch: 2}, acceptAll) != true {
		t.Fatal("newer epoch was not accepted")
	}
	if v.Merge(Attestation{Subject: "b", Issuer: "a", Rung: 1, Epoch: 0}, acceptAll) {
		t.Fatal("older epoch replaced the view")
	}
	if v.Claims["b"].Rung != 4 {
		t.Fatalf("rung = %d, want 4", v.Claims["b"].Rung)
	}
}

func TestMergeReportsAContradictionInsteadOfResolvingIt(t *testing.T) {
	v := NewView("a")
	_ = v.Merge(Attestation{Subject: "b", Issuer: "a", Rung: 2, Epoch: 7}, acceptAll)
	_ = v.Merge(Attestation{Subject: "b", Issuer: "c", Rung: 4, Epoch: 7}, acceptAll)

	if len(v.Contradictions) != 1 {
		t.Fatalf("contradictions = %d, want 1", len(v.Contradictions))
	}
	if v.Claims["b"].Rung != 2 {
		t.Fatal("the contradiction overwrote the held claim; it must be reported, not resolved")
	}
	if v.Contradictions[0].Incoming.Rung != 4 {
		t.Fatalf("contradiction lost the incoming claim: %+v", v.Contradictions[0])
	}
}

func TestCompareCountsAMissingClaimAsDisagreement(t *testing.T) {
	a := NewView("a")
	b := NewView("b")
	_ = a.Merge(Attestation{Subject: "x", Issuer: "a", Rung: 1, Epoch: 1}, acceptAll)
	_ = a.Merge(Attestation{Subject: "y", Issuer: "a", Rung: 1, Epoch: 1}, acceptAll)
	_ = b.Merge(Attestation{Subject: "y", Issuer: "a", Rung: 1, Epoch: 1}, acceptAll)

	r := Compare(a, b)
	if r.Subjects != 2 || r.Disagreements != 1 {
		t.Fatalf("reading = %+v, want 1 disagreement over 2 subjects", r)
	}
	if r.Value != 0.5 {
		t.Fatalf("value = %v, want 0.5", r.Value)
	}
}

func TestCompareReadsEpochSkew(t *testing.T) {
	a := NewView("a")
	b := NewView("b")
	_ = a.Merge(Attestation{Subject: "x", Issuer: "a", Rung: 1, Epoch: 9}, acceptAll)
	_ = b.Merge(Attestation{Subject: "x", Issuer: "a", Rung: 1, Epoch: 4}, acceptAll)

	r := Compare(a, b)
	if r.Disagreements != 0 {
		t.Fatalf("same rung should agree: %+v", r)
	}
	if r.EpochSkew != 5 {
		t.Fatalf("epoch skew = %d, want 5", r.EpochSkew)
	}
}

func TestHolonomyLocalisesOneDivergentNode(t *testing.T) {
	views := []*View{NewView("a"), NewView("b"), NewView("c")}
	for _, v := range views {
		_ = v.Merge(Attestation{Subject: "s", Issuer: "root", Rung: 3, Epoch: 1}, acceptAll)
	}
	// c has a different rung for s (or a stale one).
	views[2].Claims["s"] = Attestation{Subject: "s", Issuer: "rogue", Rung: 0, Epoch: 1}

	edges := Holonomy(views)
	if len(edges) != 3 {
		t.Fatalf("edges = %d, want 3", len(edges))
	}
	// a-b agrees; b-c and c-a disagree, so the liar is localised to c.
	if edges[0].Disagreements != 0 {
		t.Fatalf("a-b = %+v, want agreement", edges[0])
	}
	if edges[1].Disagreements != 1 || edges[2].Disagreements != 1 {
		t.Fatalf("c should appear on both its edges: %+v", edges)
	}
}
