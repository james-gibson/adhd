package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProjectIDNamespacesDynamicTool(t *testing.T) {
	s := &Server{}
	s.registerProjectToolID("cluster", "01JABCDEF", "pretty-name", "owner/repo", "http://127.0.0.1:1/mcp")
	data, err := json.Marshal(s.handleToolsList())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"adhd.project.01JABCDEF.call"`) {
		t.Fatalf("tool not namespaced by id: %s", data)
	}
	if !strings.Contains(string(data), "pretty-name") {
		t.Fatalf("display name missing from description: %s", data)
	}
}

func TestProjectIDCollisionUsesID(t *testing.T) {
	s := &Server{}
	s.registerProjectToolID("alpha", "01JA", "grep", "", "http://127.0.0.1:1/mcp")
	s.registerProjectToolID("beta", "01JB", "grep", "", "http://127.0.0.1:2/mcp")
	data, _ := json.Marshal(s.handleToolsList())
	for _, want := range []string{"adhd.project.01JA.call", "adhd.project.01JB.call"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s in %s", want, data)
		}
	}
}

func TestProjectIDFallsBackToName(t *testing.T) {
	if got := sanitizeProjectID("bad id!!", "pretty"); got != "bad-id" {
		t.Fatalf("sanitizeProjectID = %q", got)
	}
	if got := sanitizeProjectID("", "pretty"); got != "pretty" {
		t.Fatalf("fallback = %q", got)
	}
	if got := sanitizeProjectID("", ""); got != "project" {
		t.Fatalf("empty fallback = %q", got)
	}
}
