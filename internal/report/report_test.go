package report_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/report"
)

// featureDir returns the absolute path to the top-level features/ directory.
func featureDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine caller path")
	}
	// internal/report/report_test.go -> project root is 3 levels up
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "features")
}

// writeJSONLLog writes a pre-built slice of log entries to a temp JSONL file.
func writeJSONLLog(t *testing.T, entries []map[string]interface{}) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "traffic-*.jsonl")
	if err != nil {
		t.Fatalf("create temp log: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatalf("encode entry: %v", err)
		}
	}
	return f.Name()
}

func entry(typ, method string, isError bool) map[string]interface{} {
	e := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"type":      typ,
		"method":    method,
	}
	if isError {
		e["error"] = map[string]interface{}{"code": -32601, "message": "not found"}
	}
	return e
}

// ---- scanner ----------------------------------------------------------------

func TestScannerCountsRequestsAndResponses(t *testing.T) {
	path := writeJSONLLog(t, []map[string]interface{}{
		entry("request", "initialize", false),
		entry("response", "initialize", false),
		entry("request", "tools/list", false),
		entry("response", "tools/list", false),
		entry("request", "adhd.lights.get", false),
		entry("response", "adhd.lights.get", true), // error response
	})

	cl, err := report.ScanLog(path)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}

	if cl.ValidLines != 6 {
		t.Errorf("valid=%d want 6", cl.ValidLines)
	}
	if !cl.Seen("initialize") {
		t.Error("initialize not seen")
	}
	if !cl.Succeeded("tools/list") {
		t.Error("tools/list should have succeeded")
	}
	if !cl.Errored("adhd.lights.get") {
		t.Error("adhd.lights.get should have errored")
	}
	if cl.Succeeded("adhd.lights.get") {
		t.Error("adhd.lights.get all responses were errors; should not count as succeeded")
	}
}

func TestScannerHandlesCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte(
		"{\"type\":\"request\",\"method\":\"ping\"}\nnot valid json\n{\"type\":\"response\",\"method\":\"ping\"}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	cl, err := report.ScanLog(path)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}
	if cl.InvalidLines != 1 {
		t.Errorf("invalid=%d want 1", cl.InvalidLines)
	}
	if cl.ValidLines != 2 {
		t.Errorf("valid=%d want 2", cl.ValidLines)
	}
}

// ---- matcher ----------------------------------------------------------------

func TestMatchFeaturesPassWhenMethodsSeen(t *testing.T) {
	path := writeJSONLLog(t, []map[string]interface{}{
		entry("request", "initialize", false),
		entry("response", "initialize", false),
		entry("request", "tools/list", false),
		entry("response", "tools/list", false),
		entry("request", "adhd.status", false),
		entry("response", "adhd.status", false),
		entry("request", "adhd.lights.list", false),
		entry("response", "adhd.lights.list", false),
		entry("request", "adhd.lights.get", false),
		entry("response", "adhd.lights.get", false),   // success (single scenario)
		entry("request", "adhd.lights.get", false),
		entry("response", "adhd.lights.get", true),    // error response (missing scenario)
	})

	cl, err := report.ScanLog(path)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}

	results, err := report.MatchFeatures(cl, []string{featureDir(t)})
	if err != nil {
		t.Fatalf("MatchFeatures: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results — check features/ directory path")
	}

	var mcpFeat *report.FeatureResult
	for i := range results {
		if results[i].Domain == "mcp-server" {
			mcpFeat = &results[i]
		}
	}
	if mcpFeat == nil {
		t.Fatal("mcp-server feature not found")
	}
	if mcpFeat.PassCount == 0 {
		t.Errorf("mcp-server: 0 passing scenarios; scenarios=%v", summarize(mcpFeat.Scenarios))
	}
	t.Logf("mcp-server: pass=%d fail=%d nocov=%d unver=%d", mcpFeat.PassCount, mcpFeat.FailCount, mcpFeat.NoCoverage, mcpFeat.Unverifiable)
}

func TestMatchFeaturesNoCoverageFromEmptyLog(t *testing.T) {
	path := writeJSONLLog(t, nil)
	cl, err := report.ScanLog(path)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}

	results, err := report.MatchFeatures(cl, []string{featureDir(t)})
	if err != nil {
		t.Fatalf("MatchFeatures: %v", err)
	}
	for _, fr := range results {
		if fr.PassCount > 0 {
			t.Errorf("feature %q: %d passes from empty log", fr.Name, fr.PassCount)
		}
		if fr.FailCount > 0 {
			t.Errorf("feature %q: %d fails from empty log", fr.Name, fr.FailCount)
		}
	}
}

// ---- formatter --------------------------------------------------------------

func TestFormatContainsExpectedSections(t *testing.T) {
	path := writeJSONLLog(t, []map[string]interface{}{
		entry("request", "initialize", false),
		entry("response", "initialize", false),
		entry("request", "tools/list", false),
		entry("response", "tools/list", false),
		entry("request", "adhd.status", false),
		entry("response", "adhd.status", false),
		entry("request", "adhd.lights.list", false),
		entry("response", "adhd.lights.list", false),
		entry("request", "adhd.lights.get", false),
		entry("response", "adhd.lights.get", false),
		entry("request", "adhd.lights.get", false),
		entry("response", "adhd.lights.get", true),
		entry("request", "ping", false),
		entry("response", "ping", false),
	})

	cl, _ := report.ScanLog(path)
	results, _ := report.MatchFeatures(cl, []string{featureDir(t)})
	output := report.Format(results, cl)

	for _, want := range []string{
		"ADHD MCP Traffic Report",
		"Observed Methods",
		"initialize",
		"Feature:",
		"PASS",
		"NOT VERIFIABLE",
		"Summary",
		"Coverage:",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("report missing %q", want)
		}
	}

	t.Log("\n" + output)
}

// ---- end-to-end: mock ADHD server → scan → report ---------------------------

func TestReportFromMockServerTraffic(t *testing.T) {
	mock := newMockADHDServer(t)

	// Drive all known ADHD MCP methods against the mock
	methods := []string{
		"initialize",
		"tools/list",
		"adhd.status",
		"adhd.lights.list",
		"adhd.lights.get",       // success (name="primary")
		"adhd.isotope.status",
		"adhd.isotope.peers",
		"ping",
	}

	logPath := filepath.Join(t.TempDir(), "live.jsonl")
	f, _ := os.Create(logPath)
	enc := json.NewEncoder(f)

	for _, method := range methods {
		params := map[string]interface{}{}
		if method == "adhd.lights.get" {
			params["name"] = "primary"
		}

		body, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0", "id": 1,
			"method": method, "params": params,
		})
		resp, err := http.Post(mock.URL+"/mcp", "application/json", bytes.NewReader(body))

		_ = enc.Encode(entry("request", method, false))
		if err == nil {
			var result map[string]interface{}
			_ = json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			hasError := result["error"] != nil
			_ = enc.Encode(entry("response", method, hasError))
		}
	}

	// Also call adhd.lights.get with a missing name to exercise the error scenario
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 2,
		"method": "adhd.lights.get", "params": map[string]interface{}{"name": "nonexistent"},
	})
	resp, _ := http.Post(mock.URL+"/mcp", "application/json", bytes.NewReader(body))
	_ = enc.Encode(entry("request", "adhd.lights.get", false))
	if resp != nil {
		var result map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		_ = enc.Encode(entry("response", "adhd.lights.get", result["error"] != nil))
	}

	f.Close()

	cl, err := report.ScanLog(logPath)
	if err != nil {
		t.Fatalf("ScanLog: %v", err)
	}

	results, err := report.MatchFeatures(cl, []string{featureDir(t)})
	if err != nil {
		t.Fatalf("MatchFeatures: %v", err)
	}

	output := report.Format(results, cl)
	t.Log("\n" + output)

	pass, fail, nocov, unver := 0, 0, 0, 0
	for _, fr := range results {
		pass += fr.PassCount
		fail += fr.FailCount
		nocov += fr.NoCoverage
		unver += fr.Unverifiable
	}
	t.Logf("totals: pass=%d fail=%d nocov=%d unver=%d", pass, fail, nocov, unver)

	if pass == 0 {
		t.Error("expected at least some passing scenarios")
	}
	if unver == 0 {
		t.Error("expected at least some not-verifiable scenarios (keyboard ops, smoke-alarm)")
	}
	// Failures should be 0 — mock returns proper responses
	if fail > 0 {
		t.Errorf("unexpected %d failing scenarios", fail)
	}
}

// newMockADHDServer starts a mock ADHD MCP endpoint serving all known methods.
func newMockADHDServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		method, _ := req["method"].(string)
		id := req["id"]

		var result interface{}
		var errObj interface{}

		switch method {
		case "initialize":
			result = map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "mock-adhd", "version": "0.1.0"},
			}
		case "tools/list":
			result = map[string]interface{}{
				"tools": []map[string]string{
					{"name": "adhd.status"},
					{"name": "adhd.lights.list"},
					{"name": "adhd.lights.get"},
					{"name": "adhd.isotope.status"},
					{"name": "adhd.isotope.peers"},
				},
			}
		case "adhd.status":
			result = map[string]interface{}{"total": 3, "green": 2, "red": 1, "dark": 0}
		case "adhd.lights.list":
			result = map[string]interface{}{
				"lights": []map[string]interface{}{
					{"name": "feature-a", "status": "green"},
					{"name": "feature-b", "status": "red"},
				},
			}
		case "adhd.lights.get":
			var name string
			if params, ok := req["params"].(map[string]interface{}); ok {
				name, _ = params["name"].(string)
			}
			if name == "" || name == "nonexistent" {
				errObj = map[string]interface{}{"code": -32602, "message": "Light not found"}
			} else {
				result = map[string]interface{}{"name": name, "status": "green"}
			}
		case "adhd.isotope.status":
			result = map[string]interface{}{"role": "prime"}
		case "adhd.isotope.peers":
			result = map[string]interface{}{"peers": []interface{}{}}
		case "ping":
			result = map[string]interface{}{"pong": true}
		default:
			errObj = map[string]interface{}{"code": -32601, "message": "method not found: " + method}
		}

		resp := map[string]interface{}{"jsonrpc": "2.0", "id": id}
		if errObj != nil {
			resp["error"] = errObj
		} else {
			resp["result"] = result
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func summarize(scenarios []report.ScenarioResult) string {
	var parts []string
	for _, s := range scenarios {
		parts = append(parts, s.Status.String()+":"+s.ScenarioName)
	}
	return strings.Join(parts, "; ")
}
