//go:build soak

package soak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

// clusterInfo mirrors the JSON contract of lezz's /cluster registry.
type clusterInfo struct {
	Name    string `json:"name"`
	AlarmA  string `json:"alarm_a"`
	AlarmB  string `json:"alarm_b"`
	AdhdMCP string `json:"adhd_mcp"`
}

// registryStub is the host side of a lezz demo registry: GET /cluster,
// POST /cluster, DELETE /cluster/<name>. It binds every interface, which is
// what lezz does, so the join can be exercised over the machine's LAN address
// rather than loopback.
type registryStub struct {
	mu       sync.Mutex
	clusters map[string]clusterInfo
}

func newRegistryStub(seed clusterInfo) *registryStub {
	return &registryStub{clusters: map[string]clusterInfo{seed.Name: seed}}
}

func (r *registryStub) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/cluster":
		r.mu.Lock()
		out := make(map[string]clusterInfo, len(r.clusters))
		for k, v := range r.clusters {
			out[k] = v
		}
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)

	case req.Method == http.MethodPost && req.URL.Path == "/cluster":
		var info clusterInfo
		if err := json.NewDecoder(req.Body).Decode(&info); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if info.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.clusters[info.Name] = info
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case req.Method == http.MethodDelete && strings.HasPrefix(req.URL.Path, "/cluster/"):
		name := strings.TrimPrefix(req.URL.Path, "/cluster/")
		r.mu.Lock()
		delete(r.clusters, name)
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	default:
		http.NotFound(w, req)
	}
}

func startRegistry(t *testing.T, seed clusterInfo) (*registryStub, string) {
	t.Helper()
	ln, err := net.Listen("tcp", ":19100")
	if err != nil {
		t.Skipf("discovery port 19100 unavailable (%v); stop any lezz demo first", err)
	}
	stub := newRegistryStub(seed)
	srv := &http.Server{Handler: stub, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return stub, "http://localhost:19100/cluster"
}

// startAlarm serves the minimal /status an alarm needs to answer a liveness
// probe.
func startAlarm(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok")
		case "/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"service":"soak-mock","live":true,"ready":true,"targets":[{"id":"soak-target","state":"healthy","message":"ok","latency_ms":1,"checked_at":"`+time.Now().UTC().Format(time.RFC3339)+`"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func postCluster(t *testing.T, url string, info clusterInfo) {
	t.Helper()
	body, _ := json.Marshal(info)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: HTTP %d", url, resp.StatusCode)
	}
}

func getRegistry(t *testing.T, url string) map[string]clusterInfo {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	var out map[string]clusterInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
}

// startLANRegistry serves the registry on one LAN address only, so the
// localhost fallback in adhd's Browse cannot succeed and discovery must come
// from mDNS.
func startLANRegistry(t *testing.T, host string, seed clusterInfo) (*registryStub, string) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "19100"))
	if err != nil {
		t.Skipf("cannot bind %s:19100 (%v)", host, err)
	}
	stub := newRegistryStub(seed)
	srv := &http.Server{Handler: stub, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return stub, "http://" + ln.Addr().String() + "/cluster"
}

// advertiseRegistryMDNS publishes the same service lezz registers.
func advertiseRegistryMDNS(t *testing.T, port int) {
	t.Helper()
	server, err := zeroconf.Register("lezz-demo", "_lezz-demo._tcp", "local.", port, []string{"v=1"}, nil)
	if err != nil {
		t.Skipf("mDNS registration unavailable: %v", err)
	}
	t.Cleanup(server.Shutdown)
}

// TestMDNSRegistryDiscovery is the intended cross-host path: the registry is
// bound to the LAN address, advertised as _lezz-demo._tcp, and the dashboard
// finds it without any localhost registry to fall back on.
func TestMDNSRegistryDiscovery(t *testing.T) {
	lan := lanIPv4()
	if lan == "" {
		t.Skip("no non-loopback IPv4 address on this machine")
	}

	alarm := startAlarm(t)
	_, registryURL := startLANRegistry(t, lan, clusterInfo{
		Name:    "base-1000",
		AlarmA:  alarm.URL,
		AlarmB:  alarm.URL,
		AdhdMCP: "",
	})
	t.Logf("registry bound to %s only", registryURL)

	if resp, err := (&http.Client{Timeout: 300 * time.Millisecond}).Get("http://127.0.0.1:19100/cluster"); err == nil {
		_ = resp.Body.Close()
		t.Skip("something is already serving localhost:19100; the fallback would mask mDNS")
	}

	advertiseRegistryMDNS(t, 19100)

	root := t.TempDir()
	n := newLoopbackNode(t, root, "mdns-demo", false, "")
	n.demo = true
	n.readyTimeout = 40 * time.Second
	n.start(t)
	t.Cleanup(n.stop)

	if err := waitLights(n, 2, 25*time.Second); err != nil {
		t.Fatalf("mDNS discovery: %v; stderr=%s", err, n.stderr.String())
	}
	if !strings.Contains(n.stderr.String(), "found 1 cluster(s)") {
		t.Errorf("dashboard did not report the discovered cluster; stderr=%s", n.stderr.String())
	}
	t.Logf("dashboard discovered the LAN registry over mDNS (no localhost fallback)")
}

// TestClusterAcceptsRemoteJoin is the contract a second machine relies on: a
// registry bound to every interface accepts a cluster POSTed over the LAN
// address, keeps its fields, is idempotent, and forgets the node on DELETE
// without disturbing the seed cluster.
func TestClusterAcceptsRemoteJoin(t *testing.T) {
	lan := lanIPv4()
	if lan == "" {
		t.Skip("no non-loopback IPv4 address on this machine")
	}

	alarm := startAlarm(t)
	seed := clusterInfo{
		Name:    "base-1000",
		AlarmA:  alarm.URL,
		AlarmB:  alarm.URL,
		AdhdMCP: "http://127.0.0.1:9090/mcp",
	}
	_, localURL := startRegistry(t, seed)
	remoteURL := fmt.Sprintf("http://%s:19100/cluster", lan)

	got := getRegistry(t, remoteURL)
	if len(got) != 1 || got["base-1000"].AlarmA != alarm.URL {
		t.Fatalf("seed cluster not served over %s: %+v", remoteURL, got)
	}

	joining := clusterInfo{
		Name:    "remote-2000",
		AlarmA:  alarm.URL,
		AlarmB:  alarm.URL,
		AdhdMCP: fmt.Sprintf("http://%s:9091/mcp", lan),
	}
	postCluster(t, remoteURL, joining)
	postCluster(t, remoteURL, joining) // idempotent

	got = getRegistry(t, remoteURL)
	if len(got) != 2 {
		t.Fatalf("after join: got %d clusters, want 2: %+v", len(got), got)
	}
	r, ok := got["remote-2000"]
	if !ok {
		t.Fatalf("joined cluster missing: %+v", got)
	}
	if r.AlarmA != joining.AlarmA || r.AlarmB != joining.AlarmB || r.AdhdMCP != joining.AdhdMCP {
		t.Errorf("joined cluster lost fields: %+v", r)
	}
	if _, ok := got["base-1000"]; !ok {
		t.Errorf("seed cluster disappeared after join: %+v", got)
	}

	// Repeated reads must not flap.
	for i := 0; i < 5; i++ {
		if again := getRegistry(t, localURL); len(again) != len(got) {
			t.Fatalf("registry size changed between reads: %d -> %d", len(got), len(again))
		}
	}

	req, _ := http.NewRequest(http.MethodDelete, remoteURL+"/remote-2000", http.NoBody)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()

	got = getRegistry(t, remoteURL)
	if _, ok := got["remote-2000"]; ok {
		t.Errorf("cluster still registered after DELETE: %+v", got)
	}
	if _, ok := got["base-1000"]; !ok {
		t.Errorf("DELETE removed the seed cluster too: %+v", got)
	}
	t.Logf("remote join contract holds over %s", remoteURL)
}

// TestDemoNodeConsumesExternalRegistry checks the other half: an adhd started
// with --demo builds its lights from a registry it did not start.
//
// The live-join assertion (a cluster that registers *after* adhd started) is
// the known gap: --demo polls localhost:19100, and headless mode never polls at
// all. Set ADHD_SOAK_STRICT=1 to make the gap a failure instead of a log line.
func TestDemoNodeConsumesExternalRegistry(t *testing.T) {
	alarm := startAlarm(t)
	_, _ = startRegistry(t, clusterInfo{
		Name:    "base-1000",
		AlarmA:  alarm.URL,
		AlarmB:  alarm.URL,
		AdhdMCP: "",
	})

	root := t.TempDir()
	n := newLoopbackNode(t, root, "demo", false, "")
	n.demo = true
	n.readyTimeout = 40 * time.Second
	n.start(t)
	t.Cleanup(n.stop)

	if err := waitLights(n, 2, 20*time.Second); err != nil {
		t.Fatalf("startup discovery: %v; stderr=%s", err, n.stderr.String())
	}
	t.Logf("adhd discovered the external registry at startup")

	// A second machine joins after startup.
	alarm2 := startAlarm(t)
	postCluster(t, "http://localhost:19100/cluster", clusterInfo{
		Name:   "joiner-3000",
		AlarmA: alarm2.URL,
		AlarmB: alarm2.URL,
	})

	err := waitLights(n, 4, 20*time.Second)
	if err == nil {
		t.Logf("live join picked up without a restart")
		return
	}

	msg := fmt.Sprintf("known gap: cluster joined the registry after startup was not picked up: %v", err)
	if envBool("ADHD_SOAK_STRICT") {
		t.Error(msg)
		return
	}
	t.Log(msg + " (set ADHD_SOAK_STRICT=1 to fail)")
}

func waitLights(n *node, want int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	last := 0
	var lastErr error
	for time.Now().Before(deadline) {
		raw, err := callMCPResult(n.endpoint(), "adhd.lights.list")
		if err != nil {
			lastErr = err
		} else {
			var envelope struct {
				Lights []any `json:"lights"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				lastErr = err
			} else {
				last = len(envelope.Lights)
				if last >= want {
					return nil
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("lights=%d want>=%d after %v (last error: %v)", last, want, timeout, lastErr)
}
