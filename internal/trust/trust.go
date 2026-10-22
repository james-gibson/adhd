// Package trust shares rung attestations between nodes out of band and measures
// the tension between their views.
//
// The link is deliberately partial: a node shares with a few peers, not all of
// them. That is not a limitation, it is the sensor. A fully connected graph
// converges before disagreement can be observed, while a sparse one exposes
// partitions, propagation delay and a node that has stopped vouching as
// measurable tension.
//
// Trust is not reconciled away. A same-epoch disagreement is recorded as a
// contradiction and reported, the same rule the plane applies to holonomy: a
// contradiction is evidence, not noise to be smoothed.
//
// Nothing here opens a socket. A gRPC (or any other) transport carries
// Attestations between peers; the rules for accepting them and the reading of
// the resulting disagreement live here so they can be tested without a wire.
package trust

import "sort"

// Attestation is one node vouching for another. Receipt is the issuer's proof
// over the claim; the transport is assumed hostile.
type Attestation struct {
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
	Rung    int    `json:"rung"`
	Epoch   uint64 `json:"epoch"`
	Receipt string `json:"receipt"`
}

// Verifier checks a receipt. A nil verifier accepts nothing: a trust channel
// with no verification is a channel for whoever can reach the port.
type Verifier func(Attestation) bool

// Contradiction is two claims for one subject at the same epoch that disagree.
// It is kept, not resolved: one of the issuers is wrong or lying, and the
// evidence is the disagreement itself.
type Contradiction struct {
	Subject  string      `json:"subject"`
	Held     Attestation `json:"held"`
	Incoming Attestation `json:"incoming"`
}

// View is one node's picture of who vouches for whom.
type View struct {
	Node           string                 `json:"node"`
	Claims         map[string]Attestation `json:"claims"`
	Contradictions []Contradiction        `json:"contradictions,omitempty"`
}

// NewView returns an empty view for node.
func NewView(node string) *View {
	return &View{Node: node, Claims: make(map[string]Attestation)}
}

// Merge accepts a claim if the verifier accepts its receipt and it is not older
// than what is held. It reports whether the view changed. A same-epoch
// disagreement is recorded and rejected.
func (v *View) Merge(a Attestation, verify Verifier) bool {
	if a.Subject == "" || a.Issuer == "" {
		return false
	}
	if verify == nil || !verify(a) {
		return false
	}
	held, ok := v.Claims[a.Subject]
	if !ok {
		v.Claims[a.Subject] = a
		return true
	}
	if a.Epoch > held.Epoch {
		v.Claims[a.Subject] = a
		return true
	}
	if a.Epoch < held.Epoch {
		return false
	}
	if a.Rung == held.Rung && a.Issuer == held.Issuer {
		return false
	}
	v.Contradictions = append(v.Contradictions, Contradiction{
		Subject: a.Subject, Held: held, Incoming: a,
	})
	return false
}

// Reading is the disagreement between two views.
type Reading struct {
	Subjects      int     `json:"subjects"`
	Disagreements int     `json:"disagreements"`
	EpochSkew     uint64  `json:"epoch_skew"`
	Value         float64 `json:"value"` // disagreements / subjects, 0..1
}

// Compare measures the tension between two views over the union of their
// subjects. A subject one side has never heard of counts as a disagreement: a
// missing claim is not agreement.
func Compare(a, b *View) Reading {
	subjects := make(map[string]struct{}, len(a.Claims)+len(b.Claims))
	for s := range a.Claims {
		subjects[s] = struct{}{}
	}
	for s := range b.Claims {
		subjects[s] = struct{}{}
	}
	names := make([]string, 0, len(subjects))
	for s := range subjects {
		names = append(names, s)
	}
	sort.Strings(names)

	var r Reading
	r.Subjects = len(names)
	for _, s := range names {
		ca, oka := a.Claims[s]
		cb, okb := b.Claims[s]
		if !oka || !okb {
			r.Disagreements++
			continue
		}
		if ca.Rung != cb.Rung {
			r.Disagreements++
		}
		skew := ca.Epoch
		if cb.Epoch > skew {
			skew = cb.Epoch
		}
		low := ca.Epoch
		if cb.Epoch < low {
			low = cb.Epoch
		}
		if skew-low > r.EpochSkew {
			r.EpochSkew = skew - low
		}
	}
	if r.Subjects > 0 {
		r.Value = float64(r.Disagreements) / float64(r.Subjects)
	}
	return r
}

// Holonomy compares each adjacent pair and the closing edge of a cycle. A cycle
// that agrees reads zero on every edge. A single divergent node shows up on
// exactly the two edges that touch it, which is why a partial graph with cycles
// can localise a liar and a tree cannot.
func Holonomy(views []*View) []Reading {
	if len(views) < 2 {
		return nil
	}
	out := make([]Reading, 0, len(views))
	for i := range views {
		out = append(out, Compare(views[i], views[(i+1)%len(views)]))
	}
	return out
}
