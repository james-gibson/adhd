package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlePRCache(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("X-Cache", "cache")
		w.Header().Set("X-Fetched-At", "2026-10-19T18:00:00Z")
		_, _ = w.Write([]byte(`{"number":139,"title":"cached"}`))
	}))
	defer srv.Close()
	t.Setenv("ADHD_PR_CACHE_URL", srv.URL)

	s := &Server{}
	res, err := s.handlePRCache(context.Background(), map[string]interface{}{
		"repo":   "james-gibson/local-time",
		"number": float64(139),
		"kind":   "view",
	})
	if err != nil {
		t.Fatalf("handlePRCache: %+v", err)
	}
	m := res.(map[string]interface{})
	if m["source"] != "cache" || m["kind"] != "view" {
		t.Fatalf("result: %+v", m)
	}
	if !strings.Contains(m["body"].(string), `"number":139`) {
		t.Fatalf("body: %v", m["body"])
	}
	for _, want := range []string{"repo=james-gibson%2Flocal-time", "number=139", "kind=view"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q missing %q", gotQuery, want)
		}
	}
}

func TestHandlePRCacheRefresh(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("X-Cache", "gh")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	t.Setenv("ADHD_PR_CACHE_URL", srv.URL)

	s := &Server{}
	if _, err := s.handlePRCache(context.Background(), map[string]interface{}{
		"repo": "o/r", "number": float64(1), "refresh": true,
	}); err != nil {
		t.Fatalf("handlePRCache: %+v", err)
	}
	if !strings.Contains(gotQuery, "refresh=1") {
		t.Fatalf("refresh not forwarded: %q", gotQuery)
	}
}

func TestHandlePRCacheValidation(t *testing.T) {
	s := &Server{}
	cases := []map[string]interface{}{
		{"repo": "../../etc/passwd", "number": float64(1)},
		{"repo": "o/r", "number": float64(0)},
		{"repo": "o/r", "number": float64(1), "kind": "patch"},
	}
	for _, in := range cases {
		if _, err := s.handlePRCache(context.Background(), in); err == nil {
			t.Fatalf("expected validation error for %+v", in)
		}
	}
}

func TestHandlePRCacheUnconfigured(t *testing.T) {
	t.Setenv("ADHD_PR_CACHE_URL", "")
	s := &Server{}
	_, err := s.handlePRCache(context.Background(), map[string]interface{}{
		"repo": "o/r", "number": float64(1),
	})
	if err == nil || !strings.Contains(err.Message, "not configured") {
		t.Fatalf("expected not-configured error, got %+v", err)
	}
}

func TestToolsListIncludesPR(t *testing.T) {
	s := &Server{}
	data, err := json.Marshal(s.handleToolsList(capDanger))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"adhd.pr"`) {
		t.Fatalf("adhd.pr missing from tools/list")
	}
}
