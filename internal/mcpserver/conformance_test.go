package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

func specHeader(contentType string, extra map[string]string) http.Header {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

func TestScoreRequestLevels(t *testing.T) {
	full := map[string]string{
		"Accept":               "application/json, text/event-stream",
		"Mcp-Session-Id":       "abc",
		"MCP-Protocol-Version": "2025-06-18",
	}
	noTransport := map[string]string{
		"Accept": "application/json, text/event-stream",
	}

	cases := []struct {
		name       string
		method     string
		path       string
		header     http.Header
		body       string
		wantScore  int
		wantLevel  int
		wantViolIn string // substring that must appear in a violation, if any
	}{
		{
			name:      "true spec",
			method:    http.MethodPost,
			path:      "/mcp",
			header:    specHeader("application/json", full),
			body:      `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`,
			wantScore: 100,
			wantLevel: 6,
		},
		{
			name:      "complete envelope, no transport headers",
			method:    http.MethodPost,
			path:      "/mcp",
			header:    specHeader("application/json", noTransport),
			body:      `{"jsonrpc":"2.0","id":"a","method":"ping"}`,
			wantScore: 80,
			wantLevel: 4,
		},
		{
			name:       "the JSON minimum (bare method)",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("application/json", noTransport),
			body:       `{"method":"adhd.status"}`,
			wantScore:  50,
			wantLevel:  3,
			wantViolIn: `jsonrpc must be "2.0"`,
		},
		{
			name:       "not JSON",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("application/json", noTransport),
			body:       `hello`,
			wantScore:  20,
			wantLevel:  1,
			wantViolIn: "body must be a single JSON object",
		},
		{
			name:       "JSON array",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("application/json", noTransport),
			body:       `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
			wantScore:  20,
			wantLevel:  1,
			wantViolIn: "body must be a single JSON object",
		},
		{
			name:       "null id",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("application/json", noTransport),
			body:       `{"jsonrpc":"2.0","id":null,"method":"ping"}`,
			wantScore:  65,
			wantLevel:  4,
			wantViolIn: "id must be a string or number",
		},
		{
			name:       "params scalar",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("application/json", noTransport),
			body:       `{"jsonrpc":"2.0","id":1,"method":"ping","params":7}`,
			wantScore:  75,
			wantLevel:  4,
			wantViolIn: "params must be an object or array",
		},
		{
			name:       "unknown field",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("application/json", noTransport),
			body:       `{"jsonrpc":"2.0","id":1,"method":"ping","x":1}`,
			wantScore:  75,
			wantLevel:  4,
			wantViolIn: "unknown top-level fields",
		},
		{
			name:       "wrong path",
			method:     http.MethodPost,
			path:       "/other",
			header:     specHeader("application/json", noTransport),
			body:       `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			wantScore:  70,
			wantLevel:  4,
			wantViolIn: "expected POST /mcp",
		},
		{
			name:       "wrong content type",
			method:     http.MethodPost,
			path:       "/mcp",
			header:     specHeader("text/plain", noTransport),
			body:       `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			wantScore:  75,
			wantLevel:  4,
			wantViolIn: "content-type must be application/json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreRequest(tc.method, tc.path, tc.header, []byte(tc.body))
			if got.Score != tc.wantScore {
				t.Errorf("score = %d, want %d (violations: %v)", got.Score, tc.wantScore, got.Violations)
			}
			if got.Level != tc.wantLevel {
				t.Errorf("level = %d, want %d", got.Level, tc.wantLevel)
			}
			if tc.wantViolIn != "" {
				joined := strings.Join(got.Violations, "\n")
				if !strings.Contains(joined, tc.wantViolIn) {
					t.Errorf("violations missing %q:\n%s", tc.wantViolIn, joined)
				}
			}
		})
	}
}

func TestScoreRequestIsMonotonicTowardSpec(t *testing.T) {
	h := specHeader("application/json", map[string]string{
		"Accept": "application/json, text/event-stream",
	})

	bare := ScoreRequest(http.MethodPost, "/mcp", h, []byte(`{"method":"ping"}`))
	envelope := ScoreRequest(http.MethodPost, "/mcp", h, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if envelope.Score <= bare.Score {
		t.Errorf("a complete envelope (%d) should outscore the JSON minimum (%d)", envelope.Score, bare.Score)
	}
	if envelope.Level <= bare.Level {
		t.Errorf("levels did not rise with conformance: %d -> %d", bare.Level, envelope.Level)
	}

	withTransport := h.Clone()
	withTransport.Set("Mcp-Session-Id", "abc")
	withTransport.Set("MCP-Protocol-Version", "2025-06-18")
	transport := ScoreRequest(http.MethodPost, "/mcp", withTransport, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if transport.Score <= envelope.Score {
		t.Errorf("transport headers (%d) should outscore envelope alone (%d)", transport.Score, envelope.Score)
	}
}

// TestBareJSONStillDispatches pins the design: scoring is not a gate. A caller
// that sends the JSON minimum is graded lower and still served.
func TestBareJSONStillDispatches(t *testing.T) {
	s := NewServer(config.MCPServerConfig{}, lights.NewCluster())
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	s.handleMCPRPC(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"pong":true`) {
		t.Fatalf("bare JSON did not dispatch: %s", rec.Body.String())
	}
}

func FuzzScoreRequest(f *testing.F) {
	type seed struct {
		method, path, contentType, accept, session, version, body string
	}
	for _, s := range []seed{
		{"POST", "/mcp", "application/json", "application/json, text/event-stream", "abc", "2025-06-18", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`},
		{"POST", "/mcp", "application/json", "application/json", "", "", `{"method":"adhd.status"}`},
		{"POST", "/mcp", "text/plain", "", "", "", `hello`},
		{"POST", "/mcp", "", "", "", "", `{}`},
		{"GET", "/mcp", "", "text/event-stream", "", "", ``},
		{"POST", "/mcp", "application/json", "*/*", "s", "v", `{"jsonrpc":"2.0","id":null,"method":""}`},
		{"POST", "/", "application/json", "", "", "", `[1,2,3]`},
	} {
		f.Add(s.method, s.path, s.contentType, s.accept, s.session, s.version, s.body)
	}

	f.Fuzz(func(t *testing.T, method, path, contentType, accept, session, version, body string) {
		h := http.Header{}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		if accept != "" {
			h.Set("Accept", accept)
		}
		if session != "" {
			h.Set("Mcp-Session-Id", session)
		}
		if version != "" {
			h.Set("MCP-Protocol-Version", version)
		}

		got := ScoreRequest(method, path, h, []byte(body))
		if got.Score < 0 || got.Score > 100 {
			t.Fatalf("score out of range: %d", got.Score)
		}
		if got.Level < 0 || got.Level > 6 {
			t.Fatalf("level out of range: %d", got.Level)
		}
		if got.Level != levelFor(got.Score) {
			t.Fatalf("level %d does not match score %d", got.Level, got.Score)
		}

		again := ScoreRequest(method, path, h, []byte(body))
		if again.Score != got.Score || again.Level != got.Level || len(again.Violations) != len(got.Violations) {
			t.Fatalf("scoring is not deterministic: %+v vs %+v", got, again)
		}
	})
}
