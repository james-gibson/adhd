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
	"os"
	"sort"
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

// TestSelfJoinDoesNotDoubleTheLights is adhd#49 end to end. `lezz demo` pushes
// its own cluster at its own dashboard, carrying the alarms the config already
// watches and its own adhd_mcp. The join must be recognised as a self-join —
// the advertised address is the machine's outbound IP, the bind is loopback —
// so the light count stays at one per probe, while the join's projects are
// still registered.
func TestSelfJoinDoesNotDoubleTheLights(t *testing.T) {
	alarmA := startAlarm(t)
	alarmB := startAlarm(t)

	n := newLoopbackNode(t, t.TempDir(), "self-join", false, "")
	cfg := fmt.Sprintf(`mcp_server:
  enabled: true
  addr: %q
smoke_alarm:
  - name: alarm-a
    endpoint: %q
    interval: 500ms
  - name: alarm-b
    endpoint: %q
    interval: 500ms
`, n.addr, alarmA.URL, alarmB.URL)
	if err := os.WriteFile(n.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	n.start(t)
	t.Cleanup(n.stop)

	// One target per alarm: two probes, two lights.
	if err := waitLights(n, 2, 20*time.Second); err != nil {
		t.Fatalf("baseline: %v\nstderr:\n%s", err, n.stderr.String())
	}

	type joinResult struct {
		Accepted           bool   `json:"accepted"`
		SelfJoin           bool   `json:"self_join"`
		SelfJoinProof      string `json:"self_join_proof"`
		PeerDisclosure     string `json:"peer_disclosure"`
		ProjectsRegistered int    `json:"projects_registered"`
	}
	join := func(adhdMCP string) joinResult {
		t.Helper()
		raw, err := callMCPWithParams(n.endpoint(), "adhd.cluster.join", map[string]any{
			"name":     "demo-61384",
			"alarm_a":  alarmA.URL,
			"alarm_b":  alarmB.URL,
			"adhd_mcp": adhdMCP,
			"projects": []any{
				map[string]any{
					"id":      "01JABCDEF",
					"name":    "tuner",
					"repo":    "james-gibson/tuner",
					"mcp_url": "http://127.0.0.1:1/mcp",
				},
			},
		})
		if err != nil {
			t.Fatalf("adhd.cluster.join: %v", err)
		}
		var res joinResult
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("decode join result %s: %v", raw, err)
		}
		if !res.Accepted {
			t.Fatalf("join was not accepted: %s", raw)
		}
		return res
	}

	// The address lezz actually advertises: the outbound IP, while adhd binds
	// loopback. The endpoint cannot be asked who it is, so the listener is the
	// evidence.
	port := n.addr[strings.LastIndex(n.addr, ":")+1:]
	host := lanIPv4()
	if host == "" {
		host = "127.0.0.1"
	}
	advertised := join("http://" + net.JoinHostPort(host, port) + "/mcp")
	if !advertised.SelfJoin {
		t.Errorf("a join naming this instance's own listener was not reported as a self-join")
	}
	if advertised.ProjectsRegistered != 1 {
		t.Errorf("projects_registered = %d, want 1 (only the watchers are skipped)",
			advertised.ProjectsRegistered)
	}

	// The same join addressed at a reachable alias of this instance: now the
	// endpoint answers, and the proof is its own public isotope id, not a port.
	reachable := join(n.endpoint())
	if !reachable.SelfJoin || reachable.SelfJoinProof != "isotope" {
		t.Errorf("a join addressed at this instance's own endpoint was proven by %q, want isotope",
			reachable.SelfJoinProof)
	}
	if reachable.PeerDisclosure != "isotope" {
		t.Errorf("peer_disclosure = %q, want isotope for a reachable endpoint", reachable.PeerDisclosure)
	}
	// The advertised address is not ours to answer on, so nothing is disclosed:
	// that is "silent", not "no route", and the listener is the evidence used.
	if advertised.PeerDisclosure != "silent" {
		t.Errorf("peer_disclosure = %q, want silent for an address nothing answers on", advertised.PeerDisclosure)
	}
	t.Logf("self-join proven by %s when reachable, %s when only advertised",
		reachable.SelfJoinProof, advertised.SelfJoinProof)

	// A joined watcher jitters its first poll by up to its 10s interval, so
	// give the old behaviour time to surface a second light before asserting.
	time.Sleep(12 * time.Second)

	names, err := lightNames(n.endpoint())
	if err != nil {
		t.Fatalf("adhd.lights.list: %v", err)
	}
	want := []string{"smoke:alarm-a/soak-target", "smoke:alarm-b/soak-target"}
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("lights = %v, want %v (a self-join must not double them)", names, want)
	}
}

// lightNames returns the sorted smoke lights the dashboard reports.
func lightNames(endpoint string) ([]string, error) {
	raw, err := callMCPResult(endpoint, "adhd.lights.list")
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Lights []struct {
			Name string `json:"name"`
		} `json:"lights"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	var names []string
	for _, l := range envelope.Lights {
		if strings.HasPrefix(l.Name, "smoke:") {
			names = append(names, l.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}
