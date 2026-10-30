//go:build soak

package soak

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- binary ----------------------------------------------------------------

var (
	buildOnce sync.Once
	buildErr  error
	builtBin  string
)

// adhdBinary builds cmd/adhd once per test binary and returns its path.
func adhdBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "adhd-soak-bin-*")
		if err != nil {
			buildErr = fmt.Errorf("mktemp: %w", err)
			return
		}
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			buildErr = fmt.Errorf("module root: %w", err)
			return
		}
		out := filepath.Join(dir, "adhd")
		cmd := exec.Command("go", "build", "-o", out, "./cmd/adhd")
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("go build adhd: %w\n%s", err, stderr.String())
			return
		}
		builtBin = out
	})
	if buildErr != nil {
		t.Fatalf("build adhd: %v", buildErr)
	}
	return builtBin
}

// ---- addressing ------------------------------------------------------------

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free addr: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// lanIPv4 returns the first non-loopback IPv4 address, or "".
func lanIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		return ipnet.IP.String()
	}
	return ""
}

// ---- node ------------------------------------------------------------------

// node is one adhd process. The binary, not a library, so the soak exercises
// signal handling, config parsing and the real HTTP listener.
type node struct {
	name  string
	dir   string
	cfg   string
	log   string
	addr  string // host:port the MCP server binds
	plus  bool
	prime string // prime MCP URL, when plus is true
	demo  bool   // launch with --demo instead of --config

	// readyTimeout bounds the wait for the MCP listener. --demo browses for
	// up to 10s, so that path needs a longer bound.
	readyTimeout time.Duration

	mu     sync.Mutex
	cmd    *exec.Cmd
	done   chan error
	stderr *bytes.Buffer
	starts int
}

func newLoopbackNode(t *testing.T, root, name string, plus bool, primeURL string) *node {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	addr := freeLoopbackAddr(t)
	cfg := filepath.Join(dir, "adhd.yaml")
	cfgText := fmt.Sprintf("mcp_server:\n  enabled: true\n  addr: %q\n", addr)
	if err := os.WriteFile(cfg, []byte(cfgText), 0o600); err != nil {
		t.Fatalf("write config %s: %v", cfg, err)
	}
	return &node{
		name:  name,
		dir:   dir,
		cfg:   cfg,
		log:   filepath.Join(dir, name+".jsonl"),
		addr:  addr,
		plus:  plus,
		prime: primeURL,

		readyTimeout: 20 * time.Second,
	}
}

func (n *node) endpoint() string { return "http://" + n.addr + "/mcp" }

// start launches the process and waits for initialize to answer.
func (n *node) start(t *testing.T) {
	t.Helper()
	args := []string{
		"--headless",
		"--log", n.log,
		"--mcp-addr", n.addr,
	}
	if n.demo {
		args = append(args, "--demo")
	} else {
		args = append(args, "--config", n.cfg)
	}
	if n.plus && n.prime != "" {
		args = append(args, "--prime-plus", "--prime-addr", n.prime, "--buffer-size", "5000")
	}

	cmd := exec.Command(adhdBinary(t), args...)
	cmd.Dir = n.dir
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", n.name, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	n.mu.Lock()
	n.cmd = cmd
	n.done = done
	n.stderr = stderr
	n.starts++
	n.mu.Unlock()

	if err := waitReady(n.endpoint(), n.readyTimeout); err != nil {
		n.stop()
		t.Fatalf("%s not ready: %v\nstderr:\n%s", n.name, err, stderr.String())
	}
}

// stop kills the process and waits for it to be reaped.
func (n *node) stop() {
	n.mu.Lock()
	cmd, done := n.cmd, n.done
	n.cmd, n.done = nil, nil
	n.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// restart kills and relaunches on the same address.
func (n *node) restart(t *testing.T) {
	t.Helper()
	n.stop()
	n.start(t)
}

func (n *node) alive() bool {
	n.mu.Lock()
	cmd := n.cmd
	n.mu.Unlock()
	return cmd != nil && cmd.Process != nil
}

func (n *node) pid() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cmd == nil || n.cmd.Process == nil {
		return 0
	}
	return n.cmd.Process.Pid
}

func (n *node) startCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.starts
}

// ---- MCP -------------------------------------------------------------------

var soakMethods = []string{
	"initialize",
	"tools/list",
	"adhd.status",
	"adhd.lights.list",
	"adhd.isotope.status",
	"adhd.isotope.peers",
	"ping",
}

var httpClient = &http.Client{Timeout: 3 * time.Second}

func callMCP(endpoint, method string) error {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	})
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", method, resp.StatusCode)
	}
	return nil
}

func waitReady(endpoint string, max time.Duration) error {
	deadline := time.Now().Add(max)
	var last error
	for time.Now().Before(deadline) {
		if err := callMCP(endpoint, "initialize"); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %v: %w", max, last)
}

// callMCPResult performs a JSON-RPC call and returns the raw result field.
func callMCPResult(endpoint, method string) (json.RawMessage, error) {
	return callMCPWithParams(endpoint, method, nil)
}

// callMCPWithParams performs a JSON-RPC call with params (nil for none) and
// returns the raw result field. The cluster-join coverage needs it: a join with
// no payload is not a join.
func callMCPWithParams(endpoint, method string, params any) (json.RawMessage, error) {
	call := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		call["params"] = params
	}
	body, _ := json.Marshal(call)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, envelope.Error.Message)
	}
	return envelope.Result, nil
}

// ---- samples ---------------------------------------------------------------

type nodeSample struct {
	RSSKB    int64 `json:"rss_kb"`
	FDs      int   `json:"fds"`
	LogBytes int64 `json:"log_bytes"`
}

type sample struct {
	At    time.Time             `json:"at"`
	Nodes map[string]nodeSample `json:"nodes"`
}

func (n *node) sample() (nodeSample, error) {
	pid := n.pid()
	if pid == 0 {
		return nodeSample{}, fmt.Errorf("%s not running", n.name)
	}
	var s nodeSample

	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return s, fmt.Errorf("ps %s: %w", n.name, err)
	}
	if v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
		s.RSSKB = v
	}

	out, err = exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "f") {
				s.FDs++
			}
		}
	}

	if st, err := os.Stat(n.log); err == nil {
		s.LogBytes = st.Size()
	}
	return s, nil
}

// ---- log integrity ---------------------------------------------------------

func tailLines(path string, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if st.Size() > maxBytes {
		start = st.Size() - maxBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, sc.Err()
}

// logCheck parses the tail and reports malformed lines and the method mix.
func logCheck(path string) (total, bad int, methods map[string]int, err error) {
	lines, err := tailLines(path, 1<<20)
	if err != nil {
		return 0, 0, nil, err
	}
	methods = make(map[string]int)
	for _, line := range lines {
		total++
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			bad++
			continue
		}
		if m, ok := entry["method"].(string); ok && m != "" {
			methods[m]++
		}
	}
	return total, bad, methods, nil
}

// ---- trends ----------------------------------------------------------------

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	cp := append([]float64(nil), v...)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 0 {
		return (cp[mid-1] + cp[mid]) / 2
	}
	return cp[mid]
}

// thirds splits off the warm-up and returns (early, late) medians.
func thirds(vals []float64, warmup int) (float64, float64) {
	if warmup >= len(vals)-1 {
		warmup = len(vals) / 3
	}
	body := vals[warmup:]
	if len(body) < 6 {
		return 0, 0
	}
	third := len(body) / 3
	return median(body[:third]), median(body[len(body)-third:])
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes"
}

// ---- report ----------------------------------------------------------------

type report struct {
	Started   time.Time `json:"started"`
	Duration  string    `json:"duration"`
	Nodes     []string  `json:"nodes"`
	Calls     int64     `json:"calls"`
	Failures  int64     `json:"failures"`
	Restarts  int       `json:"restarts"`
	Samples   []sample  `json:"samples"`
	FailNotes []string  `json:"fail_notes,omitempty"`
	Passed    bool      `json:"passed"`
}

func writeReport(t *testing.T, r report) {
	t.Helper()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Logf("marshal report: %v", err)
		return
	}
	path := os.Getenv("ADHD_SOAK_REPORT")
	if path == "" {
		path = filepath.Join(t.TempDir(), "soak-report.json")
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Logf("write report: %v", err)
		return
	}
	t.Logf("report: %s", path)
}

// ---- context helper --------------------------------------------------------

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
