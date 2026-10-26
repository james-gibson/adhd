package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
)

// SpecBodyLimit is the body size at which a request stops scoring as
// well-formed. It is a scoring threshold, not a rejection limit: the cluster
// deliberately accepts the JSON minimum, and dispatch stays permissive.
const SpecBodyLimit = 1 << 20

// SpecFidelity scores how close an inbound request is to JSON-RPC 2.0 over
// MCP's HTTP transport. It is a fitness signal, not a gate: the lab's clients
// intentionally send the JSON minimum, and a bare {"method":...} still
// dispatches. The score exists so a caller that is closer to the specification
// ranks higher, the same way the isotope rung ranks departure from nominal.
//
// Score is 0..100, Level maps it onto the seven-rung scale.
type SpecFidelity struct {
	Score      int      `json:"score"`
	Level      int      `json:"level"`
	Violations []string `json:"violations,omitempty"`
}

// ScoreRequest grades one HTTP request. It never mutates or consumes the body
// beyond reading the provided bytes, and it is deterministic: the same inputs
// always produce the same score and the same violation order.
//
// Weights: HTTP shape 20, JSON-RPC envelope 50, hygiene 10, MCP transport 20.
func ScoreRequest(method, path string, header http.Header, body []byte) SpecFidelity {
	f := SpecFidelity{}
	miss := func(why string) { f.Violations = append(f.Violations, why) }

	// --- HTTP shape (20) --------------------------------------------------
	if method == http.MethodPost && path == "/mcp" {
		f.Score += 10
	} else {
		miss("expected POST /mcp")
	}

	contentType := strings.ToLower(strings.TrimSpace(header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "application/json") {
		f.Score += 5
	} else {
		miss("content-type must be application/json")
	}

	accept := strings.ToLower(header.Get("Accept"))
	acceptJSON := strings.Contains(accept, "application/json") || strings.Contains(accept, "*/*")
	acceptSSE := strings.Contains(accept, "text/event-stream") || strings.Contains(accept, "*/*")
	switch {
	case acceptJSON && acceptSSE:
		f.Score += 5
	case acceptJSON || acceptSSE:
		f.Score += 2
		miss("accept should allow both application/json and text/event-stream")
	default:
		miss("accept should allow application/json and text/event-stream")
	}

	// --- JSON-RPC envelope (50) -------------------------------------------
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		miss("body must be a single JSON object")
		// Nothing below can be graded; HTTP shape still counts.
		f.Level = levelFor(f.Score)
		return f
	}
	f.Score += 5

	var version string
	if raw, ok := payload["jsonrpc"]; ok && json.Unmarshal(raw, &version) == nil && version == "2.0" {
		f.Score += 15
	} else {
		miss(`jsonrpc must be "2.0"`)
	}

	if raw, ok := payload["id"]; ok && validRequestID(raw) {
		f.Score += 15
	} else {
		miss("id must be a string or number")
	}

	var name string
	if raw, ok := payload["method"]; ok && json.Unmarshal(raw, &name) == nil && name != "" {
		f.Score += 10
	} else {
		miss("method must be a non-empty string")
	}

	if raw, ok := payload["params"]; ok {
		var probe any
		if json.Unmarshal(raw, &probe) != nil {
			miss("params is not valid JSON")
		} else {
			switch probe.(type) {
			case map[string]any, []any:
				f.Score += 5
			default:
				miss("params must be an object or array")
			}
		}
	} else {
		// params is optional in JSON-RPC 2.0.
		f.Score += 5
	}

	// --- Hygiene (10) ------------------------------------------------------
	unknown := false
	for key := range payload {
		switch key {
		case "jsonrpc", "id", "method", "params":
		default:
			unknown = true
		}
	}
	if unknown {
		miss("unknown top-level fields")
	} else {
		f.Score += 5
	}

	if len(body) <= SpecBodyLimit {
		f.Score += 5
	} else {
		miss("body exceeds 1 MiB")
	}

	// --- MCP transport (20) ------------------------------------------------
	if header.Get("Mcp-Session-Id") != "" {
		f.Score += 10
	} else {
		miss("missing Mcp-Session-Id")
	}
	if header.Get("MCP-Protocol-Version") != "" {
		f.Score += 10
	} else {
		miss("missing MCP-Protocol-Version")
	}

	if f.Score > 100 {
		f.Score = 100
	}
	f.Level = levelFor(f.Score)
	return f
}

// levelFor maps a score onto the seven-rung scale.
//
//	0      no HTTP shape worth grading
//	1      some HTTP shape, no JSON object
//	2      JSON object, envelope unusable
//	3      partial envelope (the JSON minimum lives here)
//	4      complete JSON-RPC 2.0 request
//	5      complete request + one transport header
//	6      complete request + session + protocol version
func levelFor(score int) int {
	switch {
	case score >= 95:
		return 6
	case score >= 85:
		return 5
	case score >= 65:
		return 4
	case score >= 45:
		return 3
	case score >= 25:
		return 2
	case score >= 10:
		return 1
	default:
		return 0
	}
}

// validRequestID reports whether raw is a JSON-RPC request id: a string or a
// number. null is not a request id (it is reserved for responses), and
// booleans, objects and arrays are invalid.
func validRequestID(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return false
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return true
	}
	var num json.Number
	return json.Unmarshal(raw, &num) == nil
}
