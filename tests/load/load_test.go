// Package load contains system-level load tests for the full ADHD topology.
//
// Each test creates an isolated temp directory tree, writes real YAML configs,
// starts all nodes (prime + N prime-plus instances), drives concurrent MCP
// traffic, and validates correctness of the prime's JSONL log.
//
// Run with:
//
//	go test ./tests/load/... -v -timeout 120s
//	go test ./tests/load/... -v -timeout 120s -run TestLoadTopology/wide
//	go test ./tests/load/... -v -timeout 120s -count=1   # skip test cache
package load

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/headless"
)

// ---- topology helpers -------------------------------------------------------

// node represents a running headless ADHD instance in the test topology.
type node struct {
	srv     *headless.Server
	addr    string // "127.0.0.1:PORT"
	logPath string
	dir     string // per-node sub-dir under the test root
}

// endpoint returns the MCP HTTP endpoint for this node.
func (n *node) endpoint() string { return "http://" + n.addr + "/mcp" }

// freeAddr returns an available TCP address (binds then closes immediately).
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startNode creates a subdirectory under root, writes a YAML config, starts a
// headless server on a free port, and registers cleanup with t.
func startNode(t *testing.T, root, name string) *node {
	t.Helper()

	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	addr := freeAddr(t)
	logPath := filepath.Join(dir, name+".jsonl")

	// Write the config file for this node (useful for post-mortem inspection)
	cfgPath := filepath.Join(dir, "adhd.yaml")
	cfgText := fmt.Sprintf(`mcp_server:
  enabled: true
  addr: "%s"
`, addr)
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o644); err != nil {
		t.Fatalf("write config %s: %v", cfgPath, err)
	}

	cfg := &config.Config{
		MCPServer: config.MCPServerConfig{
			Enabled: true,
			Addr:    addr,
		},
	}
	srv := headless.New(cfg)
	if err := srv.Start(logPath); err != nil {
		t.Fatalf("start node %s: %v", name, err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	// Brief pause so the listener is fully bound before callers try to connect
	time.Sleep(25 * time.Millisecond)

	return &node{srv: srv, addr: addr, logPath: logPath, dir: dir}
}

// wirePrimePlus configures node as a prime-plus that forwards to primeURL.
func wirePrimePlus(n *node, primeURL string, bufferSize int) {
	n.srv.SetupMessageQueue(true, primeURL, bufferSize)
}

// ---- MCP call helpers -------------------------------------------------------

var allMethods = []string{
	"initialize",
	"tools/list",
	"adhd.status",
	"adhd.lights.list",
	"adhd.isotope.status",
	"adhd.isotope.peers",
	"ping",
}

func doMCPCall(endpoint, method string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	})
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// ---- log validation ---------------------------------------------------------

type logStats struct {
	totalLines   int
	validJSON    int
	invalidJSON  int
	byMethod     map[string]int
	byType       map[string]int
	parseErrors  []string
}

func parseLog(path string) (logStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return logStats{}, err
	}
	defer func() { _ = f.Close() }()

	stats := logStats{
		byMethod: make(map[string]int),
		byType:   make(map[string]int),
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		stats.totalLines++

		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			stats.invalidJSON++
			stats.parseErrors = append(stats.parseErrors, fmt.Sprintf("line %d: %v", stats.totalLines, err))
			continue
		}
		stats.validJSON++

		if m, ok := entry["method"].(string); ok && m != "" {
			stats.byMethod[m]++
		}
		if typ, ok := entry["type"].(string); ok {
			stats.byType[typ]++
		}
	}
	return stats, scanner.Err()
}

// ---- test cases -------------------------------------------------------------

// topologyCase describes one load scenario.
type topologyCase struct {
	name        string
	numPlusNodes int // prime-plus instances
	workersPerNode int // concurrent goroutines per plus node
	callsPerWorker int // MCP calls per worker goroutine
	bufferSize  int
}

// TestLoadTopology is the main load test. It is parameterized so you can run
// a quick smoke sweep or a heavier soak by adjusting the sub-cases.
func TestLoadTopology(t *testing.T) {
	cases := []topologyCase{
		{
			name:           "minimal",
			numPlusNodes:   1,
			workersPerNode: 2,
			callsPerWorker: 10,
			bufferSize:     500,
		},
		{
			name:           "standard",
			numPlusNodes:   3,
			workersPerNode: 5,
			callsPerWorker: 20,
			bufferSize:     1000,
		},
		{
			name:           "wide",
			numPlusNodes:   5,
			workersPerNode: 10,
			callsPerWorker: 50,
			bufferSize:     5000,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTopologyLoad(t, tc)
		})
	}
}

func runTopologyLoad(t *testing.T, tc topologyCase) {
	t.Helper()

	// Single isolated root for this entire run
	root := t.TempDir()
	t.Logf("topology root: %s", root)

	// --- start prime ---
	prime := startNode(t, root, "prime")
	t.Logf("prime  addr=%s log=%s", prime.addr, prime.logPath)

	// Verify prime is alive
	if err := doMCPCall(prime.endpoint(), "initialize"); err != nil {
		t.Fatalf("prime not responsive: %v", err)
	}

	// --- start prime-plus nodes ---
	plusNodes := make([]*node, tc.numPlusNodes)
	for i := 0; i < tc.numPlusNodes; i++ {
		n := startNode(t, root, fmt.Sprintf("plus-%02d", i))
		wirePrimePlus(n, prime.endpoint(), tc.bufferSize)
		plusNodes[i] = n
		t.Logf("plus-%02d addr=%s log=%s", i, n.addr, n.logPath)
	}

	// Write topology manifest to root so it can be inspected after the test
	writeTopologyManifest(t, root, prime, plusNodes)

	// --- drive concurrent load ---
	start := time.Now()
	var totalSent atomic.Int64

	var wg sync.WaitGroup
	for _, n := range plusNodes {
		n := n
		for w := 0; w < tc.workersPerNode; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				for c := 0; c < tc.callsPerWorker; c++ {
					method := allMethods[c%len(allMethods)]
					// Send via HTTP (tests traffic middleware logging)
					_ = doMCPCall(n.endpoint(), method)
					// Also log directly into the queue so it gets pushed
					_ = n.srv.LogTraffic(headless.MCPTrafficLog{
						Timestamp: time.Now().UTC(),
						Type:      "request",
						Method:    method,
					})
					totalSent.Add(1)
				}
			}(w)
		}
	}
	wg.Wait()
	loadDuration := time.Since(start)

	t.Logf("load phase done: sent=%d in %v (%.0f/s)",
		totalSent.Load(), loadDuration,
		float64(totalSent.Load())/loadDuration.Seconds())

	// --- flush all prime-plus queues to prime ---
	pushStart := time.Now()
	for i, n := range plusNodes {
		if err := n.srv.PushToPrime(); err != nil {
			t.Errorf("plus-%02d PushToPrime: %v", i, err)
		}
	}
	time.Sleep(60 * time.Millisecond) // let file writes flush
	t.Logf("push phase done in %v", time.Since(pushStart))

	// --- validate per-node logs ---
	for i, n := range plusNodes {
		stats, err := parseLog(n.logPath)
		if err != nil {
			t.Errorf("plus-%02d log parse: %v", i, err)
			continue
		}
		if stats.invalidJSON > 0 {
			t.Errorf("plus-%02d: %d invalid JSON lines: %v", i, stats.invalidJSON, stats.parseErrors)
		}
		// Each worker logs 1 request per call + 2 from the HTTP middleware (req+resp)
		// so the lower bound is callsPerWorker × workers direct logs
		minExpected := tc.callsPerWorker * tc.workersPerNode
		if stats.validJSON < minExpected {
			t.Errorf("plus-%02d: got %d valid log lines, want >= %d", i, stats.validJSON, minExpected)
		}
		t.Logf("plus-%02d: lines=%d valid=%d methods=%v", i, stats.totalLines, stats.validJSON, stats.byMethod)
	}

	// --- validate prime log ---
	primeStats, err := parseLog(prime.logPath)
	if err != nil {
		t.Fatalf("prime log parse: %v", err)
	}
	if primeStats.invalidJSON > 0 {
		t.Errorf("prime: %d invalid JSON lines: %v", primeStats.invalidJSON, primeStats.parseErrors)
	}

	// Prime should have received at least the direct-logged entries from all nodes
	// Each node logged callsPerWorker × workersPerNode entries
	minPrimeEntries := tc.callsPerWorker * tc.workersPerNode * tc.numPlusNodes
	if primeStats.totalLines < minPrimeEntries {
		t.Errorf("prime: got %d log lines, want >= %d (sum of all push-logs payloads)",
			primeStats.totalLines, minPrimeEntries)
	}

	t.Logf("prime: lines=%d valid=%d methods=%v types=%v",
		primeStats.totalLines, primeStats.validJSON,
		primeStats.byMethod, primeStats.byType)

	// Every relayed method should be a known one
	for method := range primeStats.byMethod {
		known := false
		for _, m := range allMethods {
			if m == method {
				known = true
				break
			}
		}
		// push-logs itself appears in the prime log as a traffic entry
		if method == "smoke-alarm.isotope.push-logs" {
			known = true
		}
		if !known {
			t.Errorf("prime log contains unexpected method %q", method)
		}
	}

	// Print final summary
	t.Logf("=== %s summary ===", tc.name)
	t.Logf("  nodes:   1 prime + %d prime-plus", tc.numPlusNodes)
	t.Logf("  sent:    %d messages total (%.0f/s)", totalSent.Load(),
		float64(totalSent.Load())/loadDuration.Seconds())
	t.Logf("  prime:   %d log entries received", primeStats.totalLines)
	t.Logf("  root:    %s", root)
}

// TestLoadConcurrentPushToSamePrime runs many prime-plus nodes all pushing to a
// single prime at the same time to verify there are no data races or dropped writes.
func TestLoadConcurrentPushToSamePrime(t *testing.T) {
	const numNodes = 8
	const entriesPerNode = 50

	root := t.TempDir()
	prime := startNode(t, root, "prime")

	nodes := make([]*node, numNodes)
	for i := 0; i < numNodes; i++ {
		n := startNode(t, root, fmt.Sprintf("node-%02d", i))
		wirePrimePlus(n, prime.endpoint(), 1000)
		nodes[i] = n

		for j := 0; j < entriesPerNode; j++ {
			_ = n.srv.LogTraffic(headless.MCPTrafficLog{
				Timestamp: time.Now().UTC(),
				Type:      "request",
				Method:    allMethods[j%len(allMethods)],
			})
		}
	}

	// All nodes push concurrently
	var pushWG sync.WaitGroup
	var pushErrs atomic.Int64
	for _, n := range nodes {
		n := n
		pushWG.Add(1)
		go func() {
			defer pushWG.Done()
			if err := n.srv.PushToPrime(); err != nil {
				pushErrs.Add(1)
				t.Logf("push error: %v", err)
			}
		}()
	}
	pushWG.Wait()
	time.Sleep(50 * time.Millisecond)

	if pushErrs.Load() > 0 {
		t.Errorf("%d push operations failed", pushErrs.Load())
	}

	stats, err := parseLog(prime.logPath)
	if err != nil {
		t.Fatalf("prime log: %v", err)
	}
	if stats.invalidJSON > 0 {
		t.Errorf("prime log: %d corrupt lines", stats.invalidJSON)
	}

	minExpected := numNodes * entriesPerNode
	if stats.totalLines < minExpected {
		t.Errorf("prime: got %d lines, want >= %d", stats.totalLines, minExpected)
	}

	t.Logf("concurrent push: %d nodes × %d entries → prime received %d lines (0 corrupt)",
		numNodes, entriesPerNode, stats.totalLines)
}

// TestLoadSelfProbe mimics the adhd-demo-self.yaml scenario under load:
// a single node probes its own MCP server continuously, everything logs to JSONL.
func TestLoadSelfProbe(t *testing.T) {
	const probeDuration = 500 * time.Millisecond
	const probeInterval = 20 * time.Millisecond

	root := t.TempDir()
	n := startNode(t, root, "self")

	// Drive probes at the node's own MCP endpoint
	deadline := time.Now().Add(probeDuration)
	var sent atomic.Int64
	var wg sync.WaitGroup

	for time.Now().Before(deadline) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, m := range allMethods {
				_ = doMCPCall(n.endpoint(), m)
				sent.Add(1)
			}
		}()
		time.Sleep(probeInterval)
	}
	wg.Wait()
	time.Sleep(30 * time.Millisecond) // flush

	stats, err := parseLog(n.logPath)
	if err != nil {
		t.Fatalf("log parse: %v", err)
	}
	if stats.invalidJSON > 0 {
		t.Errorf("%d invalid JSON lines in self-probe log", stats.invalidJSON)
	}

	// Each HTTP probe generates a request+response entry via the middleware
	// Minimum: at least 1 request logged per call × all methods
	if stats.totalLines == 0 {
		t.Error("self-probe log is empty — traffic middleware not working")
	}

	t.Logf("self-probe: HTTP calls=%d log lines=%d (%.0f lines/s) methods=%v",
		sent.Load(), stats.totalLines,
		float64(stats.totalLines)/probeDuration.Seconds(),
		stats.byMethod)
}

// ---- manifest helper --------------------------------------------------------

func writeTopologyManifest(t *testing.T, root string, prime *node, plusNodes []*node) {
	t.Helper()

	var sb strings.Builder
	sb.WriteString("# ADHD load test topology\n")
	fmt.Fprintf(&sb, "prime:\n  addr: %s\n  log: %s\n\n", prime.addr, prime.logPath)
	sb.WriteString("plus_nodes:\n")
	for i, n := range plusNodes {
		fmt.Fprintf(&sb, "  - id: plus-%02d\n    addr: %s\n    log: %s\n    prime: %s\n",
			i, n.addr, n.logPath, prime.endpoint())
	}

	path := filepath.Join(root, "topology.yaml")
	_ = os.WriteFile(path, []byte(sb.String()), 0o644)
	t.Logf("topology manifest: %s", path)
}
