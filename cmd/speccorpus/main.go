// Command speccorpus exports a labelled observation corpus for the isotope
// plane.
//
// It emits four kinds, all deterministic and stratified:
//
//	request  every combination of the structural checks ScoreRequest grades
//	path     route depth, lateness and outcome (served / stale / scope / incomplete)
//	skill    tear-off queue depth and call index (served / exhausted)
//	knock    a catalogue draw (honeypot vs real), proxied or direct, at depth
//
// Each line carries the text the plane encoder consumes, a numeric label.target,
// and the named features as planes for later embeddings.
//
// The corpus is the frozen evidence of one scorer version. It is generated
// here and checked in on the isotope side (plane/testdata), where the baseline
// gate compares the last run against the prior values.
//
//	go run ./cmd/speccorpus -out /path/to/spec-corpus.jsonl
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/james-gibson/adhd/internal/mcpserver"
)

// observation is one line of the corpus.
type observation struct {
	ID     string             `json:"id"`
	Kind   string             `json:"kind"`
	Source string             `json:"source"`
	Text   string             `json:"text"`
	Label  map[string]float64 `json:"label"`
	Planes map[string]float64 `json:"planes,omitempty"`
}

func main() {
	out := flag.String("out", "spec-corpus.jsonl", "output JSONL path (- for stdout)")
	flag.Parse()

	file := io.Writer(os.Stdout)
	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "speccorpus:", err)
			os.Exit(1)
		}
		defer func() { _ = f.Close() }()
		file = f
	}

	written, err := generate(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "speccorpus:", err)
		os.Exit(1)
	}
	if *out != "-" {
		fmt.Fprintf(os.Stderr, "wrote %d observations to %s (source %s)\n", written, *out, scorerSource())
	}
}

// generate writes the corpus and returns the number of observations. It is the
// whole command minus flag handling, so a test can assert determinism.
func generate(out io.Writer) (int, error) {
	enc := json.NewEncoder(out)
	source := scorerSource()
	written := 0

	// Eleven binary structural checks, then a size axis. 2^11 combinations
	// plus two size samples keeps the corpus balanced across rungs.
	for mask := 0; mask < 1<<11; mask++ {
		bit := func(i int) bool { return mask&(1<<i) != 0 }

		method, path := http.MethodPost, "/mcp"
		contentType, accept := "application/json", "application/json, text/event-stream"
		session, version := "s", "2025-06-18"

		if bit(0) {
			method = http.MethodGet
		}
		if bit(1) {
			path = "/other"
		}
		if bit(2) {
			contentType = "text/plain"
		}
		if bit(3) {
			accept = ""
		}
		if bit(4) {
			session = ""
		}
		if bit(5) {
			version = ""
		}

		body := map[string]any{}
		if !bit(6) {
			body["jsonrpc"] = "2.0"
		}
		if !bit(7) {
			body["id"] = 1
		}
		if !bit(8) {
			body["method"] = "ping"
		}
		if bit(9) {
			body["params"] = 7
		}
		if !bit(10) {
			body["x"] = 1
		}
		raw, _ := json.Marshal(body)

		header := http.Header{}
		if contentType != "" {
			header.Set("Content-Type", contentType)
		}
		if accept != "" {
			header.Set("Accept", accept)
		}
		if session != "" {
			header.Set("Mcp-Session-Id", session)
		}
		if version != "" {
			header.Set("MCP-Protocol-Version", version)
		}
		fidelity := mcpserver.ScoreRequest(method, path, header, raw)

		obs := observation{
			ID:     fmt.Sprintf("strat-%04d", mask),
			Kind:   "request",
			Source: source,
			// The text is what the generic plane encoder consumes. Keep the
			// format stable: it is the baseline's input, and changing it changes
			// every anchor.
			Text: fmt.Sprintf("%s %s | %s | %s | %s | %s | %s",
				method, path, contentType, accept, session, version, raw),
			Label: map[string]float64{"score": float64(fidelity.Score), "level": float64(fidelity.Level), "target": float64(fidelity.Level)},
			Planes: map[string]float64{
				"post_mcp":          boolFloat(method == http.MethodPost && path == "/mcp"),
				"content_type_json": boolFloat(contentType == "application/json"),
				"accept_both":       boolFloat(accept != ""),
				"session":           boolFloat(session != ""),
				"protocol":          boolFloat(version != ""),
				"jsonrpc":           boolFloat(body["jsonrpc"] != nil),
				"id":                boolFloat(body["id"] != nil),
				"method":            boolFloat(body["method"] != nil),
				"params_object":     boolFloat(bit(9) == false),
				"no_unknown":        boolFloat(body["x"] == nil),
				"size_ok":           1,
			},
		}
		if err := enc.Encode(obs); err != nil {
			return written, err
		}
		written++
	}

	written += emitPaths(enc, source)
	written += emitSkills(enc, source)
	written += emitKnocks(enc, source)
	return written, nil
}

// emitPaths covers route depth, the one-tick grace and the ways a hop can be
// refused. target is the depth, so the embedding is graded on recovering how
// far along the map the observation went.
func emitPaths(enc *json.Encoder, source string) int {
	written := 0
	outcomes := []string{"served", "stale", "scope", "incomplete"}
	for depth := 1; depth <= 8; depth++ {
		for _, late := range []int{0, 1} {
			for oi, outcome := range outcomes {
				proxied := 0
				if depth%2 == 0 {
					proxied = 1
				}
				obs := observation{
					ID:     fmt.Sprintf("path-d%d-late%d-%s", depth, late, outcome),
					Kind:   "path",
					Source: source,
					Text: fmt.Sprintf("path depth=%d late=%d outcome=%s proxied=%d from=node-0 to=node-%d method=adhd.path.call",
						depth, late, outcome, proxied, depth),
					Label: map[string]float64{"target": float64(depth), "late": float64(late), "outcome": float64(oi)},
					Planes: map[string]float64{
						"depth":     float64(depth),
						"late":      float64(late),
						"served":    boolFloat(outcome == "served"),
						"stale":     boolFloat(outcome == "stale"),
						"scope":     boolFloat(outcome == "scope"),
						"proxied":   float64(proxied),
						"grace_set": boolFloat(late == 1),
					},
				}
				if err := enc.Encode(obs); err != nil {
					return written
				}
				written++
			}
		}
	}
	return written
}

// emitSkills covers a tear-off queue: queue depth q, calls in order, served
// while tickets remain and exhausted after. target is the queue depth.
func emitSkills(enc *json.Encoder, source string) int {
	written := 0
	for queue := 1; queue <= 8; queue++ {
		for call := 1; call <= queue+2; call++ {
			served := call <= queue
			left := queue - call
			if left < 0 {
				left = 0
			}
			obs := observation{
				ID:     fmt.Sprintf("skill-q%d-c%d", queue, call),
				Kind:   "skill",
				Source: source,
				Text: fmt.Sprintf("skill queue=%d call=%d served=%t tickets_left=%d embedded=10",
					queue, call, served, left),
				Label: map[string]float64{"target": float64(queue), "served": boolFloat(served), "call": float64(call)},
				Planes: map[string]float64{
					"queue":        float64(queue),
					"call":         float64(call),
					"served":       boolFloat(served),
					"exhausted":    boolFloat(!served),
					"tickets_left": float64(left),
				},
			}
			if err := enc.Encode(obs); err != nil {
				return written
			}
			written++
		}
	}
	return written
}

// emitKnocks covers catalogue contacts. The field is 10 real tools in 10,010
// names per node, so drawing at the natural rate would hand the gate 99.9%
// honeypots and a metric that only measures the base rate — the same defect the
// request kind had before it was stratified across rungs. The draws here are
// stratified 50/50 real/decoy so the target is actually learned; the true
// rarity survives as planes.base_rate rather than as the sampling mix.
//
// The text is what a caller observes: the node, whether the call was proxied,
// the path depth, and the name it dialled. Whether that name was real is the
// label, not part of the text, so a name it has not seen cannot be classified
// by a leaked field.
func emitKnocks(enc *json.Encoder, source string) int {
	const (
		catalogue = 10010 // 10 real + 10,000 decoys per node
		realCount = 10
		perNode   = 4 // draws per node per contact class
		depthMax  = 4
	)
	written := 0
	for ni, letter := range []string{"a", "b", "c"} {
		for draw := 0; draw < perNode; draw++ {
			contacts := []struct {
				kind     string
				name     string
				honeypot bool
			}{
				{"real", fmt.Sprintf("adhd.project.real-%s-%02d.call", letter, draw), false},
				{"decoy", fmt.Sprintf("adhd.project.decoy-%s-%05d.call", letter, draw*7919%10000), true},
			}
			for _, contact := range contacts {
				for _, proxied := range []int{0, 1} {
					for depth := 1; depth <= depthMax; depth++ {
						target := 0.0
						if contact.honeypot {
							target = 1
						}
						obs := observation{
							ID:     fmt.Sprintf("knock-%s-%s-%02d-p%d-d%d", letter, contact.kind, draw, proxied, depth),
							Kind:   "knock",
							Source: source,
							Text: fmt.Sprintf("knock node=%s proxied=%d depth=%d name=%s catalogue=%d real_count=%d",
								letter, proxied, depth, contact.name, catalogue, realCount),
							Label: map[string]float64{
								"target": target,
								"real":   boolFloat(!contact.honeypot),
								"depth":  float64(depth),
							},
							Planes: map[string]float64{
								"catalogue":  float64(catalogue),
								"real_count": float64(realCount),
								"base_rate":  float64(realCount) / float64(catalogue),
								"real":       boolFloat(!contact.honeypot),
								"honeypot":   target,
								"proxied":    float64(proxied),
								"depth":      float64(depth),
								"node":       float64(ni),
							},
						}
						if err := enc.Encode(obs); err != nil {
							return written
						}
						written++
					}
				}
			}
		}
	}
	return written
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// scorerSource records which scorer produced the labels. Best effort: the
// corpus is checked in, so provenance should survive without a git checkout.
func scorerSource() string {
	rev, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "adhd@unknown"
	}
	source := "adhd@" + strings.TrimSpace(string(rev))
	if status, err := exec.Command("git", "status", "--porcelain").Output(); err == nil && len(bytes.TrimSpace(status)) > 0 {
		source += "+dirty"
	}
	return source
}
