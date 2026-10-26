package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGenerateIsDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	n, err := generate(&first)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if n != 2356 {
		t.Fatalf("wrote %d observations, want 2356 (2048 request + 64 path + 52 skill + 192 knock)", n)
	}
	if _, err := generate(&second); err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if first.String() != second.String() {
		t.Fatal("corpus is not deterministic: two runs differ")
	}

	ids := map[string]bool{}
	levels := map[int]int{}
	kinds := map[string]int{}
	lines := strings.Split(strings.TrimSpace(first.String()), "\n")
	if len(lines) != n {
		t.Fatalf("%d lines for %d observations", len(lines), n)
	}
	for i, line := range lines {
		var obs observation
		if err := json.Unmarshal([]byte(line), &obs); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		switch obs.Kind {
		case "request", "path", "skill", "knock":
		default:
			t.Fatalf("line %d: unknown kind %q", i, obs.Kind)
		}
		if obs.ID == "" || obs.Text == "" {
			t.Fatalf("line %d: incomplete observation %+v", i, obs)
		}
		if ids[obs.ID] {
			t.Fatalf("duplicate id %q", obs.ID)
		}
		ids[obs.ID] = true
		kinds[obs.Kind]++
		if _, ok := obs.Label["target"]; !ok {
			t.Fatalf("line %d (%s): no label.target", i, obs.Kind)
		}
		if obs.Kind == "request" {
			if obs.Label["level"] < 0 || obs.Label["level"] > 6 {
				t.Fatalf("line %d: level out of range: %.0f", i, obs.Label["level"])
			}
			if obs.Label["score"] < 0 || obs.Label["score"] > 100 {
				t.Fatalf("line %d: score out of range: %.0f", i, obs.Label["score"])
			}
			levels[int(obs.Label["level"])]++
		}
		if len(obs.Planes) == 0 {
			t.Fatalf("line %d: no planes recorded", i)
		}
	}
	for level := 1; level <= 6; level++ {
		if levels[level] == 0 {
			t.Fatalf("corpus has no observations at level %d: %v", level, levels)
		}
	}
	for _, want := range []struct {
		kind string
		n    int
	}{{"request", 2048}, {"path", 64}, {"skill", 52}, {"knock", 192}} {
		if kinds[want.kind] != want.n {
			t.Fatalf("kind %s has %d observations, want %d", want.kind, kinds[want.kind], want.n)
		}
	}
	t.Logf("kinds: %v levels: %s", kinds, levelsString(levels))
}

func levelsString(levels map[int]int) string {
	var parts []string
	for level := 0; level <= 6; level++ {
		if levels[level] > 0 {
			parts = append(parts, fmt.Sprintf("%d:%d", level, levels[level]))
		}
	}
	return strings.Join(parts, " ")
}
