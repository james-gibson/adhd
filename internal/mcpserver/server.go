package mcpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/demo"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/metricsapi"
	"github.com/james-gibson/adhd/internal/mirror"
	"github.com/james-gibson/adhd/internal/proxy"
	"github.com/james-gibson/adhd/internal/smoketest"
	"github.com/james-gibson/adhd/internal/tearsheet"
)

// TopologyProvider is a function that returns current topology info
type TopologyProvider func() map[string]interface{}

// PushLogsHandler receives log entries pushed by prime-plus isotopes
type PushLogsHandler func(logs []map[string]interface{}) error

// ReceiptComputer computes a challenge-response receipt for a given feature ID and nonce.
type ReceiptComputer func(featureID, nonce string) string

// ReceiptVerifier validates a receipt against the instance's own secret.
type ReceiptVerifier func(receipt, featureID, nonce string) bool

// ClusterJoinHandler is called when adhd.cluster.join is received, wiring
// the new cluster's smoke-alarm endpoints into the running watcher.
type ClusterJoinHandler func(name, alarmA, alarmB string) error

// SecurityAlertHandler is called when a security event occurs (honeypot trigger,
// rung challenge failure). level is "warn" or "critical", event is a short tag,
// details carries context for the smoke-alarm federation report.
type SecurityAlertHandler func(level, event string, details map[string]interface{})

// callerIPKey is a context key for the HTTP caller's remote address.
type callerIPKey struct{}

// PublicTickSeconds is the width of the public tick. The tick is
// floor(unix_seconds / PublicTickSeconds): every host with a synced clock
// agrees on it without a coordinator.
const PublicTickSeconds = 5

// PathGraceTicks is the acceptance window for a negotiated route: the tick it
// was negotiated for, plus one. A negotiation that starts late in a tick can
// finish in the next and still be used. The bet is that a route one tick late
// is probably still good; two ticks late is refused.
const PathGraceTicks = 1

// tickWithinGrace reports whether a route negotiated on negotiatedTick may be
// used at now. Future ticks are not accepted (clock skew is not lateness), and
// lateness is bounded by PathGraceTicks.
func tickWithinGrace(negotiatedTick, now uint64) bool {
	return negotiatedTick <= now && now-negotiatedTick <= PathGraceTicks
}

// PublicTick returns the public tick for t. Unix seconds are UTC by
// definition, so no timezone conversion is needed or wanted.
func PublicTick(t time.Time) uint64 {
	return uint64(t.Unix() / PublicTickSeconds)
}

// dynamicTool is a runtime-registered MCP tool added when a cluster joins.
// honeypot tools return plausible-looking fake results but log the caller's identity.
type dynamicTool struct {
	name        string
	description string
	inputSchema map[string]interface{}
	honeypot    bool
	// sensitivity is the 42i distance at which this decoy's contacts become an
	// alert. Zero means "any contact" — a silent honeypot, the default and the
	// behaviour every decoy had before the threshold existed.
	sensitivity int
	fakeResult  interface{}
	handler     func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError)
	cluster     string // owning cluster name if any
	// origin is what backs the tool: the project's mcp_url, the repo slug, or
	// the decoy's description. It is how a repeat registration is told apart
	// from a different thing claiming the same name.
	origin string
}

// clusterPeerInfo tracks a peer ADHD instance discovered via adhd.cluster.join.
type clusterPeerInfo struct {
	Name     string                   `json:"name"`
	Endpoint string                   `json:"endpoint"` // adhd.mcp URL
	AlarmA   string                   `json:"alarm-a"`
	AlarmB   string                   `json:"alarm-b"`
	Projects []map[string]interface{} `json:"projects,omitempty"`
	JoinedAt time.Time                `json:"joined_at"`
}

// PathHop records a single verified hop in a negotiated trust path.
type PathHop struct {
	Isotope  string `json:"isotope"`
	Endpoint string `json:"endpoint"`
	Rung     int    `json:"rung"`
	Nonce    string `json:"nonce"`
	Receipt  string `json:"receipt"`
}

// pathDeniedMethods are the calls a route does not grant. A segment authorises
// execution along a map; it does not authorise enumerating the surface, because
// the surface (skills, proxies, honeypots) changes every tick and a single
// tools/list inside the grace window would leak the map the route only samples.
var pathDeniedMethods = map[string]bool{
	"tools/list":     true,
	"resources/list": true,
	"prompts/list":   true,
	"initialize":     true,
}

// PathSegment is one hop's capability. It is the unit a caller must know: a
// segment names the endpoint, the feature and the tick it was negotiated for.
// It is accepted on that tick and the next (one-tick grace), is reusable by any
// holder in that window, and composes freely — knowing more segments routes
// further.
type PathSegment struct {
	ID        string `json:"id"`
	From      string `json:"from,omitempty"`
	To        string `json:"to"`
	FeatureID string `json:"feature_id"`
	Tick      uint64 `json:"tick"`
	Rung      int    `json:"rung"`
	Nonce     string `json:"nonce,omitempty"`
	Receipt   string `json:"receipt,omitempty"`
}

// NegotiatedPath is a pre-verified path to a target isotope. Segments is one
// token per hop: a multi-hop caller must hold every segment, and each hop is
// validated where it is used.
type NegotiatedPath struct {
	Target       string        `json:"target"`
	Hops         []PathHop     `json:"hops"`
	Segments     []PathSegment `json:"segments,omitempty"`
	MinRung      int           `json:"min_rung"`
	FeatureID    string        `json:"feature_id"`
	Tick         uint64        `json:"tick"`
	NegotiatedAt time.Time     `json:"negotiated_at"`
}

// Server hosts an MCP-compatible endpoint exposing dashboard lights
type Server struct {
	config             config.MCPServerConfig
	cluster            *lights.Cluster
	topologyProvider   TopologyProvider
	specFidelity       func() interface{}
	pushLogsHandler    PushLogsHandler
	clusterJoinHandler ClusterJoinHandler
	middleware         func(http.Handler) http.Handler
	server             *http.Server
	httpClient         *http.Client
	proxyExecutor      *proxy.Executor

	// tearLedger remembers redeemed tear-sheet nonces so a hop cannot be
	// replayed within its tick.
	tearLedger *tearsheet.Ledger

	// Rung validation identity fields — set via SetInstanceIdentity.
	instanceIsotope string
	receiptComputer ReceiptComputer
	receiptVerifier ReceiptVerifier

	securityAlertHandler SecurityAlertHandler

	// streamObserver receives the cost record of each closed edge stream
	// (GET /mcp). Set via SetStreamObserver; see sse_edge.go.
	streamObserver StreamObserver

	// trustLevel is the rung assigned when this instance registered with
	// smoke-alarm. Peers read it during path negotiation.
	trustLevel int

	// tickSource is the public linear-time tick (default: PublicTick). Exactly
	// one tick is the lifetime of a path segment.
	tickSource func() uint64
	pathSeq    uint64

	// mirrorSink receives metadata-only frames for the backup node. The mirror
	// never carries request or response bodies.
	mirrorSink func(mirror.Frame)

	// Metrics API for smoke test scheduler endpoints
	metricsAPI *metricsapi.API
	scheduler  *smoketest.Scheduler

	// Dynamic per-repo tools registered on cluster join.
	mu           sync.RWMutex
	dynamicTools []*dynamicTool

	// honeypotDistance is the 42i distance each caller has accumulated against
	// the decoys on this host, keyed by caller address. A caller carries the
	// distance: it is the caller's departure, not the decoy's, so it survives
	// across decoys and across contacts with the same one.
	honeypotDistance map[string]int

	// Cluster peers discovered via adhd.cluster.join — used for path negotiation.
	clusterPeers map[string]*clusterPeerInfo

	// Pre-negotiated trust paths, keyed by target isotope name.
	negotiatedPaths map[string][]*NegotiatedPath

	// routePlans caches the resolved chain per target+feature for a tick, so a
	// planner asking twice does not re-run the challenge walk.
	routePlans map[string]routePlan
}

// NewServer creates an MCP server
func NewServer(cfg config.MCPServerConfig, cluster *lights.Cluster) *Server {
	return &Server{
		config:  cfg,
		cluster: cluster,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		proxyExecutor:   proxy.NewExecutor(),
		tearLedger:      tearsheet.NewLedger(),
		clusterPeers:    make(map[string]*clusterPeerInfo),
		negotiatedPaths: make(map[string][]*NegotiatedPath),
		routePlans:      make(map[string]routePlan),
		// The public tick needs no coordinator: it is derived from UTC.
		tickSource: func() uint64 { return PublicTick(time.Now()) },
	}
}

// SetInstanceIdentity wires the rung validation identity for this server.
// isotope is the public identifier to expose via adhd.isotope.instance.
// computer computes receipts; verifier validates them.
func (s *Server) SetInstanceIdentity(isotope string, computer ReceiptComputer, verifier ReceiptVerifier) {
	s.instanceIsotope = isotope
	s.receiptComputer = computer
	s.receiptVerifier = verifier
}

// SetTopologyProvider sets the callback for topology queries
func (s *Server) SetTopologyProvider(provider TopologyProvider) {
	s.topologyProvider = provider
}

// SetSpecFidelityProvider sets the callback that summarizes per-caller request
// conformance. It is reported under adhd.status.spec_fidelity.
func (s *Server) SetSpecFidelityProvider(provider func() interface{}) {
	s.specFidelity = provider
}

// SetTrustLevel records the rung assigned at isotope registration so peers can
// read it during path negotiation.
func (s *Server) SetTrustLevel(rung int) {
	s.mu.Lock()
	s.trustLevel = rung
	s.mu.Unlock()
}

// TrustLevel returns the rung assigned at isotope registration.
func (s *Server) TrustLevel() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.trustLevel
}

// SetMirrorSink installs the traffic-mirror feed for the backup node. Frames are
// metadata only: tick, path identity, endpoints and caller.
func (s *Server) SetMirrorSink(sink func(mirror.Frame)) {
	s.mu.Lock()
	s.mirrorSink = sink
	s.mu.Unlock()
}

// SetTickSource overrides the public tick source. Passing nil disables tick
// enforcement (used only by tests that need a timeless path).
// EnableTearLedger replaces the in-memory tear-sheet ledger with one backed by
// path. The child of a rotation opens the same path the parent wrote, so a
// nonce spent before the handoff is still refused after it.
//
// A corrupt or unknown-version file is an error rather than a silent empty
// ledger: replay protection that starts empty looks like protection while
// providing none.
func (s *Server) EnableTearLedger(path string) error {
	ledger, err := tearsheet.OpenLedger(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.tearLedger = ledger
	s.mu.Unlock()
	return nil
}

// tearLedgerFor returns the current ledger under the server lock.
func (s *Server) tearLedgerFor() *tearsheet.Ledger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tearLedger
}

func (s *Server) SetTickSource(source func() uint64) {
	s.mu.Lock()
	s.tickSource = source
	s.mu.Unlock()
}

func (s *Server) mirrorSinkFor() func(mirror.Frame) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mirrorSink
}

func (s *Server) nowTick() (uint64, bool) {
	s.mu.RLock()
	source := s.tickSource
	s.mu.RUnlock()
	if source == nil {
		return 0, false
	}
	return source(), true
}

// nextPathID assigns the short identifier a caller quotes ("path 88").
func (s *Server) nextPathID() string {
	return strconv.FormatUint(atomic.AddUint64(&s.pathSeq, 1), 10)
}

// SetPushLogsHandler registers the handler for incoming push-logs from prime-plus isotopes
func (s *Server) SetPushLogsHandler(h PushLogsHandler) {
	s.pushLogsHandler = h
}

// SetClusterJoinHandler registers the handler called when adhd.cluster.join is received.
func (s *Server) SetClusterJoinHandler(h ClusterJoinHandler) {
	s.clusterJoinHandler = h
}

// SetSecurityAlertHandler registers a callback invoked on honeypot triggers and
// rung challenge failures. Use it to forward events to the smoke-alarm.
func (s *Server) SetSecurityAlertHandler(h SecurityAlertHandler) {
	s.securityAlertHandler = h
}

// SetScheduler registers the smoke test scheduler for metrics API access
func (s *Server) SetScheduler(scheduler *smoketest.Scheduler) {
	s.scheduler = scheduler
	s.metricsAPI = metricsapi.NewAPI(scheduler)
}

// Contact weights, in 42i distance. A decoy is not hit one way: the same decoy
// answers a direct method call and a tools/call, and the two say different
// things about the caller. The scale is smoke-alarm's — a tool-proxy grades a
// wrong or missing capability prefix at +16 and an unknown tool at +32
// (smoke-alarm/internal/mcp_faults/tool_proxy.go) — read here for how the
// contact was *made*:
//
//   - tools/call at the decoy: the advertised route, aimed at bait — the caller
//     has no stamp for it, which is the +16 wrong-prefix case;
//   - a direct method call at the decoy's name: a name invoked as a method the
//     surface never offered as one — the +32 boundary case.
//
// The two weights are what make a threshold meaningful: set it above a single
// tools/call and the caller is tolerated until it shows a second, worse signal.
const (
	contactViaToolsCall = 16
	contactViaMethod    = 32
)

// callerHost is the address without the ephemeral port. A caller is an address,
// not a connection: keying the accumulated distance on RemoteAddr would give
// each new connection its own ledger, and a threshold could never be crossed by
// a caller that reconnects (which is what a curl-shaped probe does).
func callerHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// honeypotContact accounts for one contact with a decoy and reports whether it
// crosses that decoy's sensitivity. Every contact is logged; only a crossing
// fires the security alert. The distance accumulates per caller, so a caller
// that knocks once and leaves stays below a threshold set above its knock, and
// one that keeps probing crosses it.
//
// The threshold is a policy, not a measurement: 0 keeps the decoy silent on any
// contact (the pre-threshold behaviour), and a value set from outside the cycle
// (the join payload) is what the honeypotted node's operator decided in advance.
func (s *Server) honeypotContact(decoy *dynamicTool, callerIP, via string, weight int) (distance, threshold int, fired bool) {
	caller := callerHost(callerIP)
	s.mu.Lock()
	if s.honeypotDistance == nil {
		s.honeypotDistance = map[string]int{}
	}
	s.honeypotDistance[caller] += weight
	distance = s.honeypotDistance[caller]
	threshold = decoy.sensitivity
	s.mu.Unlock()

	fired = threshold <= 0 || distance >= threshold
	return distance, threshold, fired
}

// fireSecurity adds a red security light to the cluster and calls the handler.
func (s *Server) fireSecurity(level, event string, details map[string]interface{}) {
	// Surface as a red light so the local cluster/TUI shows it immediately.
	s.cluster.Upsert(&lights.Light{
		Name:    "security/" + event,
		Type:    "security",
		Source:  "honeypot",
		Status:  lights.StatusRed,
		Details: fmt.Sprintf("%v", details),
	})
	if s.securityAlertHandler != nil {
		s.securityAlertHandler(level, event, details)
	}
}

// SetMiddleware sets an HTTP middleware to wrap all requests (e.g., for traffic logging)
func (s *Server) SetMiddleware(mw func(http.Handler) http.Handler) {
	s.middleware = mw
}

// Start begins listening for MCP requests
func (s *Server) Start(ctx context.Context) error {
	if !s.config.Enabled {
		slog.Debug("MCP server disabled")
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", s.handleMCPRPC)
	mux.HandleFunc("GET /mcp", s.handleMCPSSE)

	// Register metrics API endpoints if scheduler is available
	if s.metricsAPI != nil {
		mux.HandleFunc("GET /api/endpoints/{id}/metrics", s.metricsAPI.HandleMetrics)
		mux.HandleFunc("GET /api/metrics/snapshot", s.metricsAPI.HandleSnapshot)
		mux.HandleFunc("GET /api/events", s.metricsAPI.HandleEvents)
	}

	var handler http.Handler = mux
	if s.middleware != nil {
		handler = s.middleware(mux)
	}

	s.server = &http.Server{
		Addr:    s.config.Addr,
		Handler: handler,
	}

	slog.Info("starting MCP server", "addr", s.config.Addr)

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("MCP server error", "error", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// handleMCPRPC processes JSON-RPC requests
func (s *Server) handleMCPRPC(w http.ResponseWriter, r *http.Request) {
	// Inject caller IP into context for honeypot and audit logging.
	//
	// RemoteAddr is a stopgap, not attribution: it is defeated by a local caller,
	// and — with no Host/Origin validation (adhd#29) — by DNS rebinding, where a
	// rebound caller logs as 127.0.0.1. Golden attribution is the entry id of the
	// session established at the cluster's ingress: assigned, not supplied. This
	// tool surface holds no session, so it should cite that entry id (carried on
	// an OTel trace and sealed by the isotope) rather than derive an identity from
	// an address. internal/telemetry is a stub today, so none of that exists yet.
	ctxWithIP := context.WithValue(r.Context(), callerIPKey{}, r.RemoteAddr)
	r = r.WithContext(ctxWithIP)

	var req jsonrpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.respondError(w, -32700, "Parse error")
		return
	}

	var result interface{}
	var respErr *jsonrpcError

	switch req.Method {
	case "initialize":
		result = s.handleInitialize()

	case "adhd.status":
		result = s.handleStatus()

	case "adhd.lights.list":
		result = s.handleLightsList()

	case "adhd.lights.get":
		result, respErr = s.handleLightsGet(req.Params)

	case "adhd.isotope.status":
		result = s.handleIsotopeStatus()

	case "adhd.isotope.peers":
		result = s.handleIsotopePeers()

	case "tools/list":
		result = s.handleToolsList()

	case "tools/call":
		result, respErr = s.handleToolsCall(r.Context(), req.Params)

	case "ping":
		result = map[string]interface{}{"pong": true}

	case "smoke-alarm.isotope.push-logs":
		result, respErr = s.handlePushLogs(req.Params) //nolint:govet

	case "adhd.isotope.instance":
		result = s.handleIsotopeInstance()

	case "adhd.rung.respond":
		result, respErr = s.handleRungRespond(req.Params)

	case "adhd.rung.verify":
		result, respErr = s.handleRungVerify(req.Params)

	case "adhd.rung.challenge":
		result, respErr = s.handleRungChallenge(r.Context(), req.Params)

	case "adhd.cluster.join":
		result, respErr = s.handleClusterJoin(req.Params)

	case "adhd.cluster.leave":
		result, respErr = s.handleClusterLeave(req.Params)

	case "adhd.hurl.run":
		result, respErr = s.handleHurlRun(r.Context(), req.Params)

	case "adhd.gh.run":
		result, respErr = s.handleGHRun(r.Context(), req.Params)

	case "adhd.pr":
		result, respErr = s.handlePRCache(r.Context(), req.Params)

	case "adhd.dependabot.alerts":
		result, respErr = s.handleDependabotAlerts(r.Context(), req.Params)

	case "adhd.path.negotiate":
		result, respErr = s.handlePathNegotiate(r.Context(), req.Params)

	case "adhd.path.verify":
		result, respErr = s.handlePathVerify(r.Context(), req.Params)

	case "adhd.path.list":
		result, respErr = s.handlePathList(req.Params)

	case "adhd.path.call":
		result, respErr = s.handlePathCall(r.Context(), req.Params)

	case "adhd.proxy":
		result, respErr = s.handleProxyCall(r.Context(), req.Params)

	default:
		// Check runtime-registered per-repo and per-project tools.
		s.mu.RLock()
		var dynTool *dynamicTool
		for _, t := range s.dynamicTools {
			if t.name == req.Method {
				dynTool = t
				break
			}
		}
		s.mu.RUnlock()
		if dynTool != nil {
			if dynTool.honeypot {
				callerIP, _ := r.Context().Value(callerIPKey{}).(string)
				distance, threshold, fired := s.honeypotContact(dynTool, callerIP, "method", contactViaMethod)
				slog.Warn("honeypot tool triggered",
					"tool", req.Method,
					"caller_ip", callerIP,
					"via", "method",
					"distance_42i", distance,
					"threshold_42i", threshold,
					"fired", fired,
					"params", req.Params,
				)
				if fired {
					s.fireSecurity("warn", "honeypot_triggered", map[string]interface{}{
						"tool":          req.Method,
						"caller_ip":     callerIP,
						"via":           "method",
						"distance_42i":  distance,
						"threshold_42i": threshold,
					})
				}
				result = dynTool.fakeResult
			} else {
				result, respErr = dynTool.handler(r.Context(), req.Params)
			}
		} else {
			respErr = &jsonrpcError{
				Code:    -32601,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
	}

	if respErr != nil {
		resp.Error = respErr
	} else {
		resp.Result = result
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Warn("encode response", "error", err)
	}
}

// handleMCPSSE serves the server→client SSE stream on GET /mcp.
//
// It carries server-initiated messages only. The response to a POST /mcp is
// delivered on that POST's own response — whether as application/json or as a
// stream — so nothing written here is attributable to a caller's request id,
// and nothing here is billed to one. The edge record this emits is the cost of
// the channel, not of any call made through it.
//
// The stream is unbuffered and counted; see sse_edge.go for why, and for what
// the record is for.
func (s *Server) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Ask intermediaries not to reintroduce the buffer the edge just removed.
	h.Set("X-Accel-Buffering", "no")

	edge := newSSEEdge(w, flusher)

	closedBy := ClosedByServerClose
	defer func() {
		s.reportStream(edge.record(closedBy, r))
	}()

	// One frame, so a client that has connected knows it is connected. The
	// stream then carries nothing until a server-initiated message exists: an
	// empty idle stream is the correct steady state, not a fault.
	if err := edge.send("endpoint", map[string]string{"status": "listening"}); err != nil {
		slog.Warn("write SSE handshake", "error", err)
		closedBy = ClosedByError
		return
	}

	<-r.Context().Done()

	// The request context is done either because the client hung up or because
	// the server is shutting down. From here the two are indistinguishable, so
	// the reading is conservative: call it a client abort, which understates
	// delivered bytes rather than overstating them. When the shutdown path is
	// wired to this handler it can say so explicitly.
	closedBy = ClosedByClientAbort
}

// handleInitialize answers the MCP handshake: it returns the initialize response.
//
// The protocol revision is pinned to 2024-11-05, but the version is NOT the
// gate. Protocol drift is real: a peer speaking an older revision should be
// accepted with a request to upgrade *when the trust chain is present*
// (isotope/rung), and refused when it is not. Access is decided by the trust
// chain, not by the protocol string — see adhd#27. The surface negotiated here
// is the fire marshal's: smoke-alarm's, reached by proxy through adhd with the
// isotopes' help.
func (s *Server) handleInitialize() interface{} {
	return map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"serverInfo": map[string]string{
			"name":    "adhd",
			"version": "0.1.0",
		},
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		// The first message a workspace user — or their agent — gets from this
		// surface. It is the lab's MOTD, and it is itself a signed test vector
		// (isotope/testdata/burned, key: zaphod-beeblebrox), so the greeting
		// demonstrates the model it describes: check it, don't believe it.
		"instructions": Motd,
	}
}

// Motd is the greeting a workspace user gets when they connect. Kept identical to
// lezz's `lezz motd` so the human path and the agent path say the same thing.
const Motd = "Don't Panic. 42 is a coordinate, not a credential; trust is what you pinned.\n" +
	"This greeting is a signed test vector (isotope/testdata/burned, key: zaphod-beeblebrox), " +
	"so anyone can check it and nobody has to trust it. Verify it; don't believe it."

// handleToolsList returns available tools (static + runtime-registered per-repo tools)
func (s *Server) handleToolsList() interface{} {
	static := []map[string]interface{}{
		{
			"name":        "adhd.status",
			"description": "Get dashboard status summary (lights count by status)",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "adhd.lights.list",
			"description": "List all lights with their current status",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "adhd.lights.get",
			"description": "Get a specific light by name",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Light name",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "adhd.isotope.status",
			"description": "Get this isotope's role and topology status",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "adhd.isotope.peers",
			"description": "Get list of discovered peer ADHD isotopes with trust rungs",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "adhd.isotope.instance",
			"description": "Get this instance's public isotope identifier for rung validation",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		{
			"name":        "adhd.rung.respond",
			"description": "Respond to a rung validation challenge by computing a receipt",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID being validated",
					},
					"nonce": map[string]interface{}{
						"type":        "string",
						"description": "Fresh nonce from the challenger",
					},
				},
				"required": []string{"feature_id", "nonce"},
			},
		},
		{
			"name":        "adhd.rung.verify",
			"description": "Verify a receipt submitted in response to a rung challenge",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"receipt": map[string]interface{}{
						"type":        "string",
						"description": "Receipt to verify",
					},
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID from the original challenge",
					},
					"nonce": map[string]interface{}{
						"type":        "string",
						"description": "Nonce from the original challenge",
					},
				},
				"required": []string{"receipt", "feature_id", "nonce"},
			},
		},
		{
			"name":        "adhd.rung.challenge",
			"description": "Issue a rung validation challenge to a peer ADHD instance",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target.url": map[string]interface{}{
						"type":        "string",
						"description": "Base URL of the peer MCP server",
					},
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID to challenge the peer on",
					},
					"nonce": map[string]interface{}{
						"type":        "string",
						"description": "Fresh nonce for this challenge",
					},
				},
				"required": []string{"target.url", "feature_id", "nonce"},
			},
		},
		{
			"name":        "adhd.cluster.join",
			"description": "Notify this ADHD instance that a new lezz demo cluster has joined the registry",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Cluster name",
					},
					"alarm_a": map[string]interface{}{
						"type":        "string",
						"description": "HTTP URL of the cluster primary smoke-alarm",
					},
					"alarm_b": map[string]interface{}{
						"type":        "string",
						"description": "HTTP URL of the cluster secondary smoke-alarm",
					},
					"adhd_mcp": map[string]interface{}{
						"type":        "string",
						"description": "HTTP URL of the cluster's own ADHD MCP server",
					},
					"github_repos": map[string]interface{}{
						"type":        "array",
						"description": "GitHub repo slugs owned by this cluster",
						"items":       map[string]interface{}{"type": "string"},
					},
					"projects": map[string]interface{}{
						"type":        "array",
						"description": "Projects exposing an MCP server; each registers an adhd.project.<id>.call proxy tool, where <id> defaults to the project name",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id": map[string]interface{}{
									"type":        "string",
									"description": "Stable seed id that namespaces the tool; defaults to name. A route is not an id — it rotates",
								},
								"name":    map[string]interface{}{"type": "string"},
								"repo":    map[string]interface{}{"type": "string"},
								"mcp_url": map[string]interface{}{"type": "string"},
							},
						},
					},
					"honeypot-tools": map[string]interface{}{
						"type":        "array",
						"description": "Decoy tool descriptors to register. Callers who invoke these are logged as untrusted; the alert fires when a caller's accumulated 42i distance reaches the decoy's sensitivity.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name":        map[string]interface{}{"type": "string"},
								"description": map[string]interface{}{"type": "string"},
								"sensitivity": map[string]interface{}{
									"type":        "integer",
									"minimum":     0,
									"description": "42i distance at which contacts on this decoy become an alert: a tools/call costs 16, a direct method call costs 32, per caller. 0 or absent fires on any contact (a silent honeypot).",
								},
							},
						},
					},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "adhd.cluster.leave",
			"description": "Notify this ADHD instance that a cluster has left the registry (removes peer tracking and dynamic tools)",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Cluster name to remove",
					},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "adhd.hurl.run",
			"description": "Run a hurl HTTP test script and return pass/fail with output. hurl must be installed.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"script": map[string]interface{}{
						"type":        "string",
						"description": "Hurl script content (HTTP requests with optional assertions)",
					},
					"certify_domain": map[string]interface{}{
						"type":        "string",
						"description": "If set and the script passes, emit CapabilityVerifiedMsg for this domain",
					},
				},
				"required": []string{"script"},
			},
		},
		{
			"name":        "adhd.gh.run",
			"description": "Run a gh CLI command and return its output. GitHub CLI must be installed and authenticated.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"args": map[string]interface{}{
						"type":        "array",
						"description": "Arguments to pass to gh",
						"items":       map[string]interface{}{"type": "string"},
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "Timeout in seconds (default: 30)",
					},
				},
				"required": []string{"args"},
			},
		},
		{
			"name":        "adhd.pr",
			"description": "Read a pull request through the local lezz PR cache (view metadata/files/comments or diff). Read-only; the cache endpoint is local config.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo": map[string]interface{}{
						"type":        "string",
						"description": "Repository slug (owner/name)",
					},
					"number": map[string]interface{}{
						"type":        "integer",
						"description": "Pull request number",
						"minimum":     1,
					},
					"kind": map[string]interface{}{
						"type":        "string",
						"description": "view (metadata, files, comments) or diff (patch)",
						"enum":        []string{"view", "diff"},
					},
					"refresh": map[string]interface{}{
						"type":        "boolean",
						"description": "Bypass the cache TTL",
					},
				},
				"required": []string{"repo", "number"},
			},
		},
		{
			"name":        "adhd.dependabot.alerts",
			"description": "Fetch Dependabot security alerts for a GitHub repository via the gh CLI.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo": map[string]interface{}{
						"type":        "string",
						"description": "Repository slug (e.g. owner/name)",
					},
					"state": map[string]interface{}{
						"type":        "string",
						"description": "Alert state filter: open, dismissed, auto_dismissed, fixed (default: open)",
					},
				},
				"required": []string{"repo"},
			},
		},
		{
			"name":        "adhd.path.negotiate",
			"description": "Pre-negotiate verified multi-hop trust paths to a target isotope with honeypot tool exchange.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target.isotope": map[string]interface{}{
						"type":        "string",
						"description": "Name of the target isotope to negotiate paths to",
					},
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID to verify at each hop",
					},
				},
				"required": []string{"target.isotope", "feature_id"},
			},
		},
		{
			"name":        "adhd.path.verify",
			"description": "Re-verify all hops in a previously negotiated path by re-issuing fresh rung challenges.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target.isotope": map[string]interface{}{
						"type":        "string",
						"description": "Target isotope whose paths to re-verify",
					},
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID to use for re-verification challenges",
					},
				},
				"required": []string{"target.isotope", "feature_id"},
			},
		},
		{
			"name":        "adhd.path.list",
			"description": "List all pre-negotiated trust paths, optionally filtered by target isotope.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target.isotope": map[string]interface{}{
						"type":        "string",
						"description": "Filter to a specific target (optional)",
					},
				},
			},
		},
		{
			"name":        "adhd.path.call",
			"description": "Call along a negotiated path segment. The caller must present the segment for the hop it is about to use; a multi-hop route forwards the remaining segments so each hop validates its own token against its own tick. A route executes; it does not grant enumeration (tools/list, resources/list, prompts/list, initialize).",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"segments": map[string]interface{}{
						"type":        "array",
						"description": "Base64 path segments, first hop first",
						"items":       map[string]interface{}{"type": "string"},
					},
					"call": map[string]interface{}{
						"type":        "object",
						"description": "JSON-RPC call to execute at the final hop",
					},
				},
				"required": []string{"segments", "call"},
			},
		},
		{
			"name":        "adhd.proxy",
			"description": "Proxy an MCP call to a target endpoint with optional authentication (bearer token, API key, OAuth2).",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target_endpoint": map[string]interface{}{
						"type":        "string",
						"description": "Target MCP endpoint URL (e.g., https://api.example.com/mcp)",
					},
					"auth": map[string]interface{}{
						"type":        "object",
						"description": "Optional authentication configuration",
						"properties": map[string]interface{}{
							"type": map[string]interface{}{
								"type":        "string",
								"description": "Auth type: bearer, api-key, or oauth2",
								"enum":        []string{"bearer", "api-key", "oauth2"},
							},
							"token": map[string]interface{}{
								"type":        "string",
								"description": "Bearer token, API key, or OAuth2 token",
							},
							"header": map[string]interface{}{
								"type":        "string",
								"description": "Custom header name for api-key type (defaults to X-API-Key)",
							},
						},
						"required": []string{"type", "token"},
					},
					"call": map[string]interface{}{
						"type":        "object",
						"description": "The MCP JSON-RPC call to forward to the target endpoint",
						"properties": map[string]interface{}{
							"jsonrpc": map[string]interface{}{
								"type":        "string",
								"description": "JSON-RPC version (defaults to 2.0)",
							},
							"method": map[string]interface{}{
								"type":        "string",
								"description": "Method name (e.g., tools/list, adhd.status)",
							},
							"params": map[string]interface{}{
								"type":        "object",
								"description": "Method parameters (optional)",
							},
						},
						"required": []string{"method"},
					},
				},
				"required": []string{"target_endpoint", "call"},
			},
		},
	}

	s.mu.RLock()
	dynTools := make([]map[string]interface{}, 0, len(s.dynamicTools))
	for _, t := range s.dynamicTools {
		dynTools = append(dynTools, map[string]interface{}{
			"name":        t.name,
			"description": t.description,
			"inputSchema": t.inputSchema,
		})
	}
	s.mu.RUnlock()

	return map[string]interface{}{
		"tools": append(static, dynTools...),
	}
}

// handleToolsCall invokes a tool by name and returns the result
func (s *Server) handleToolsCall(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	paramsMap, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}

	toolName, ok := paramsMap["name"].(string)
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "name is required and must be a string"}
	}

	toolInput := paramsMap["arguments"]
	if toolInput == nil {
		// Fall back to the legacy "input" key for backward compatibility.
		toolInput = paramsMap["input"]
	}
	if toolInput == nil {
		toolInput = map[string]interface{}{}
	}

	// Route to the appropriate handler based on tool name
	var result interface{}
	var errResult *jsonrpcError

	switch toolName {
	case "adhd.status":
		result = s.handleStatus()
	case "adhd.lights.list":
		result = s.handleLightsList()
	case "adhd.lights.get":
		result, errResult = s.handleLightsGet(toolInput)
	case "adhd.isotope.status":
		result = s.handleIsotopeStatus()
	case "adhd.isotope.peers":
		result = s.handleIsotopePeers()
	case "adhd.isotope.instance":
		result = s.handleIsotopeInstance()
	case "adhd.rung.respond":
		result, errResult = s.handleRungRespond(toolInput)
	case "adhd.rung.verify":
		result, errResult = s.handleRungVerify(toolInput)
	case "adhd.rung.challenge":
		result, errResult = s.handleRungChallenge(ctx, toolInput)
	case "adhd.cluster.join":
		result, errResult = s.handleClusterJoin(toolInput)
	case "adhd.cluster.leave":
		result, errResult = s.handleClusterLeave(toolInput)
	case "adhd.hurl.run":
		result, errResult = s.handleHurlRun(ctx, toolInput)
	case "adhd.gh.run":
		result, errResult = s.handleGHRun(ctx, toolInput)
	case "adhd.pr":
		result, errResult = s.handlePRCache(ctx, toolInput)
	case "adhd.dependabot.alerts":
		result, errResult = s.handleDependabotAlerts(ctx, toolInput)
	case "adhd.path.negotiate":
		result, errResult = s.handlePathNegotiate(ctx, toolInput)
	case "adhd.path.verify":
		result, errResult = s.handlePathVerify(ctx, toolInput)
	case "adhd.path.list":
		result, errResult = s.handlePathList(toolInput)
	case "adhd.path.call":
		result, errResult = s.handlePathCall(ctx, toolInput)
	case "adhd.proxy":
		result, errResult = s.handleProxyCall(ctx, toolInput)
	default:
		// Check dynamic tools
		s.mu.RLock()
		var dynTool *dynamicTool
		for _, t := range s.dynamicTools {
			if t.name == toolName {
				dynTool = t
				break
			}
		}
		s.mu.RUnlock()
		if dynTool != nil {
			if dynTool.honeypot {
				callerIP, _ := ctx.Value(callerIPKey{}).(string)
				distance, threshold, fired := s.honeypotContact(dynTool, callerIP, "tools/call", contactViaToolsCall)
				slog.Warn("honeypot tool triggered via tools/call",
					"tool", toolName,
					"caller_ip", callerIP,
					"via", "tools/call",
					"distance_42i", distance,
					"threshold_42i", threshold,
					"fired", fired,
				)
				if fired {
					s.fireSecurity("warn", "honeypot_triggered", map[string]interface{}{
						"tool":          toolName,
						"caller_ip":     callerIP,
						"via":           "tools/call",
						"distance_42i":  distance,
						"threshold_42i": threshold,
					})
				}
				result = dynTool.fakeResult
			} else {
				result, errResult = dynTool.handler(ctx, toolInput)
			}
		} else {
			return nil, &jsonrpcError{Code: -32601, Message: "Tool not found: " + toolName}
		}
	}

	if errResult != nil {
		return nil, errResult
	}

	// Wrap result in MCP tools/call response format
	var text string
	switch r := result.(type) {
	case string:
		text = r
	default:
		data, err := json.Marshal(r)
		if err != nil {
			text = fmt.Sprintf("%v", r)
		} else {
			text = string(data)
		}
	}

	return map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": text,
			},
		},
	}, nil
}

// handleStatus returns dashboard summary
func (s *Server) handleStatus() interface{} {
	lightSummary := map[string]int{
		"total":  s.cluster.Count(),
		"green":  s.cluster.CountByStatus(lights.StatusGreen),
		"red":    s.cluster.CountByStatus(lights.StatusRed),
		"yellow": s.cluster.CountByStatus(lights.StatusYellow),
		"dark":   s.cluster.CountByStatus(lights.StatusDark),
	}

	s.mu.RLock()
	dynamicCount := len(s.dynamicTools)
	peerNames := make([]string, 0, len(s.clusterPeers))
	for name := range s.clusterPeers {
		peerNames = append(peerNames, name)
	}
	pathCount := 0
	for _, paths := range s.negotiatedPaths {
		pathCount += len(paths)
	}
	s.mu.RUnlock()

	out := map[string]interface{}{
		"lights":        lightSummary,
		"cluster_peers": peerNames,
		"paths":         pathCount,
		"dynamic_tools": dynamicCount,
	}

	if s.topologyProvider != nil {
		topo := s.topologyProvider()
		if v, ok := topo["trust_rung"]; ok {
			out["trust_rung"] = v
		}
		if v, ok := topo["local_name"]; ok {
			out["instance"] = v
		}
	}
	if s.instanceIsotope != "" {
		out["isotope"] = s.instanceIsotope
	}
	if s.specFidelity != nil {
		out["spec_fidelity"] = s.specFidelity()
	}
	out["tick"] = PublicTick(time.Now())

	return out
}

// handleLightsList returns all lights
func (s *Server) handleLightsList() interface{} {
	result := []interface{}{}

	for _, light := range s.cluster.All() {
		result = append(result, map[string]interface{}{
			"name":        light.Name,
			"type":        light.Type,
			"source":      light.Source,
			"status":      light.GetStatus(),
			"details":     light.GetDetails(),
			"lastUpdated": light.GetLastUpdated(),
		})
	}

	return map[string]interface{}{
		"lights": result,
	}
}

// handleLightsGet returns a single light
func (s *Server) handleLightsGet(params interface{}) (interface{}, *jsonrpcError) {
	paramsMap, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "Invalid params"}
	}

	nameVal, ok := paramsMap["name"]
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "Missing 'name' parameter"}
	}

	name, ok := nameVal.(string)
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "Name must be a string"}
	}

	light := s.cluster.GetByName(name)
	if light == nil {
		return nil, &jsonrpcError{Code: -32602, Message: "Light not found"}
	}

	return map[string]interface{}{
		"name":        light.Name,
		"type":        light.Type,
		"source":      light.Source,
		"status":      light.GetStatus(),
		"details":     light.GetDetails(),
		"lastUpdated": light.GetLastUpdated(),
	}, nil
}

// handleIsotopeStatus returns this isotope's topology role and status
func (s *Server) handleIsotopeStatus() interface{} {
	if s.topologyProvider == nil {
		return map[string]interface{}{
			"name":       "adhd",
			"role":       "standalone",
			"status":     "ready",
			"trust_rung": s.TrustLevel(),
			"tick":       PublicTick(time.Now()),
		}
	}

	topology := s.topologyProvider()
	return map[string]interface{}{
		"name":       topology["local_name"],
		"role":       topology["local_role"],
		"status":     "ready",
		"trust_rung": s.TrustLevel(),
		"tick":       PublicTick(time.Now()),
	}
}

// handleIsotopePeers returns discovered peer ADHD isotopes, merging topology
// provider peers with cluster peers discovered via adhd.cluster.join.
func (s *Server) handleIsotopePeers() interface{} {
	var peers []interface{}

	if s.topologyProvider != nil {
		topology := s.topologyProvider()
		if tp, ok := topology["peers"].([]interface{}); ok {
			peers = append(peers, tp...)
		}
	}

	s.mu.RLock()
	for _, cp := range s.clusterPeers {
		peers = append(peers, map[string]interface{}{
			"name":      cp.Name,
			"role":      "cluster",
			"endpoint":  cp.Endpoint,
			"status":    "active",
			"joined_at": cp.JoinedAt,
		})
	}
	s.mu.RUnlock()

	if peers == nil {
		peers = []interface{}{}
	}
	return map[string]interface{}{
		"peers": peers,
	}
}

// handleIsotopeInstance returns this instance's public isotope identifier.
func (s *Server) handleIsotopeInstance() interface{} {
	return map[string]interface{}{
		"isotope": s.instanceIsotope,
	}
}

// handleRungRespond computes a challenge-response receipt for the caller.
func (s *Server) handleRungRespond(params interface{}) (interface{}, *jsonrpcError) {
	if s.receiptComputer == nil {
		return nil, &jsonrpcError{Code: -32000, Message: "rung validation not configured"}
	}
	p, err := parseStringParams(params, "feature_id", "nonce")
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}
	receipt := s.receiptComputer(p["feature_id"], p["nonce"])
	return map[string]interface{}{
		"receipt":    receipt,
		"feature_id": p["feature_id"],
		"nonce":      p["nonce"],
	}, nil
}

// handleRungVerify verifies a receipt submitted by a challenger.
func (s *Server) handleRungVerify(params interface{}) (interface{}, *jsonrpcError) {
	if s.receiptVerifier == nil {
		return nil, &jsonrpcError{Code: -32000, Message: "rung validation not configured"}
	}
	p, err := parseStringParams(params, "receipt", "feature_id", "nonce")
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}
	valid := s.receiptVerifier(p["receipt"], p["feature_id"], p["nonce"])
	return map[string]interface{}{
		"valid":      valid,
		"feature_id": p["feature_id"],
		"nonce":      p["nonce"],
	}, nil
}

// handleRungChallenge issues a rung validation challenge to a peer instance.
// It sends adhd.rung.respond to the peer, then adhd.rung.verify, and reports
// whether the peer successfully proved it has a real implementation.
func (s *Server) handleRungChallenge(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	p, err := parseStringParams(params, "target.url", "feature_id", "nonce")
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}
	targetURL := p["target.url"]
	featureID := p["feature_id"]
	nonce := p["nonce"]

	// Step 1: Ask the peer to produce a receipt for this challenge.
	respondResult, callErr := s.callPeerMCP(ctx, targetURL, "adhd.rung.respond", map[string]interface{}{
		"feature_id": featureID,
		"nonce":      nonce,
	})
	if callErr != nil {
		return map[string]interface{}{
			"verified": false,
			"error":    callErr.Error(),
		}, nil
	}

	receipt, _ := respondResult["receipt"].(string)
	if receipt == "" {
		return map[string]interface{}{
			"verified": false,
			"error":    "peer returned empty receipt",
		}, nil
	}

	// Step 2: Ask the peer to verify its own receipt — only a correct
	// implementation can return valid=true for the original (feature_id, nonce).
	verifyResult, callErr := s.callPeerMCP(ctx, targetURL, "adhd.rung.verify", map[string]interface{}{
		"receipt":    receipt,
		"feature_id": featureID,
		"nonce":      nonce,
	})
	if callErr != nil {
		return map[string]interface{}{
			"verified": false,
			"receipt":  receipt,
			"error":    callErr.Error(),
		}, nil
	}

	verified, _ := verifyResult["valid"].(bool)
	peerIsotope := ""
	if isoResult, iErr := s.callPeerMCP(ctx, targetURL, "adhd.isotope.instance", nil); iErr == nil {
		peerIsotope, _ = isoResult["isotope"].(string)
	}

	// The challenged instance's own trust rung. Without it a negotiated path
	// always carries min_rung 0, and path ranking is a no-op.
	peerRung := 0
	if status, sErr := s.callPeerMCP(ctx, targetURL, "adhd.isotope.status", nil); sErr == nil {
		if r, ok := status["trust_rung"].(float64); ok {
			peerRung = int(r)
		}
	}

	return map[string]interface{}{
		"verified":     verified,
		"receipt":      receipt,
		"feature_id":   featureID,
		"nonce":        nonce,
		"peer_isotope": peerIsotope,
		"rung":         peerRung,
	}, nil
}

// firstString returns the first non-empty string found under the given keys.
// The canonical underscore keys (alarm_a, adhd_mcp, github_repos) are tried
// first, matching the wire contract shared with lezz demo; legacy dotted keys
// (alarm-a, adhd.mcp) still resolve for callers that followed the earlier
// tool schema.
func firstString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// handleClusterJoin wires a newly-joined lezz demo cluster into the running watcher.
func (s *Server) handleClusterJoin(params interface{}) (interface{}, *jsonrpcError) {
	p, err := parseStringParams(params, "name")
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}
	// alarm-a and alarm-b are optional — extract without requiring them.
	m, _ := params.(map[string]interface{})
	alarmA := firstString(m, "alarm_a", "alarm-a")
	alarmB := firstString(m, "alarm_b", "alarm-b")

	if s.clusterJoinHandler == nil {
		return map[string]interface{}{"accepted": false, "reason": "not configured"}, nil
	}
	if err := s.clusterJoinHandler(p["name"], alarmA, alarmB); err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "cluster join failed: " + err.Error()}
	}

	// Register per-repo Dependabot tools for any GitHub repos in this cluster.
	// Each counter counts what tools/list will show for this join: a repeat is
	// reported as unchanged, not as a registration (adhd#40).
	var repoCount, repoUnchanged int
	var conflicts []joinConflict
	reposRaw, reposOK := m["github_repos"]
	if !reposOK {
		reposRaw, reposOK = m["github.repos"]
	}
	if reposOK {
		if reposSlice, ok := reposRaw.([]interface{}); ok {
			for _, r := range reposSlice {
				if slug, ok := r.(string); ok && slug != "" {
					// The tool name is derived from the slug, so a repeat is the
					// same tool serving the same repo; it can only be new or unchanged.
					if s.registerRepoTool(p["name"], slug) == registrationAdded {
						repoCount++
					} else {
						repoUnchanged++
					}
				}
			}
		}
	}

	// Register per-project MCP proxy tools for projects that expose an MCP server.
	var projectCount, projectUnchanged int
	if projRaw, ok := m["projects"]; ok {
		if projSlice, ok := projRaw.([]interface{}); ok {
			for _, pj := range projSlice {
				pjMap, ok := pj.(map[string]interface{})
				if !ok {
					continue
				}
				pjName, _ := pjMap["name"].(string)
				pjRepo, _ := pjMap["repo"].(string)
				pjMCP, _ := pjMap["mcp_url"].(string)
				if pjName == "" || pjMCP == "" {
					continue
				}
				route, explicit := projectRoute(pjMap)
				outcome := s.registerProjectToolID(p["name"], route, pjName, pjRepo, pjMCP, explicit)
				switch outcome {
				case registrationAdded, registrationReplaced:
					projectCount++
				case registrationUnchanged:
					projectUnchanged++
				case registrationConflict:
					conflicts = append(conflicts, joinConflict{
						Tool: "adhd.project." + sanitizeProjectRoute(route, pjName) + ".call",
						Reason: "same tool name already reaches another MCP endpoint; " +
							"send project.route so providers of one capability stay distinct",
					})
				}
			}
		}
	}

	// Track this cluster peer for path negotiation.
	adhdMCP := firstString(m, "adhd_mcp", "adhd.mcp")
	s.mu.Lock()
	s.clusterPeers[p["name"]] = &clusterPeerInfo{
		Name:     p["name"],
		Endpoint: adhdMCP,
		AlarmA:   alarmA,
		AlarmB:   alarmB,
		JoinedAt: time.Now(),
	}
	s.mu.Unlock()

	// Register honeypot tools sent by the joining cluster.
	var honeypotCount, honeypotUnchanged int
	armed := []map[string]interface{}{}
	if hpRaw, ok := m["honeypot-tools"]; ok {
		if hpSlice, ok := hpRaw.([]interface{}); ok {
			for _, h := range hpSlice {
				hpMap, ok := h.(map[string]interface{})
				if !ok {
					continue
				}
				hpName, _ := hpMap["name"].(string)
				hpDesc, _ := hpMap["description"].(string)
				if hpName == "" {
					continue
				}
				// sensitivity is the 42i distance at which this decoy's contacts
				// become an alert. Absent means 0: silent on any contact.
				sensitivity := 0
				if raw, ok := hpMap["sensitivity"]; ok {
					f, isNum := raw.(float64)
					if !isNum || f < 0 || f != float64(int(f)) {
						conflicts = append(conflicts, joinConflict{
							Tool: hpName,
							Reason: "sensitivity must be a 42i distance (a non-negative integer); " +
								"0 or absent means any contact fires",
						})
						continue
					}
					sensitivity = int(f)
				}
				switch s.registerHoneypotTool(p["name"], hpName, hpDesc, sensitivity) {
				case registrationAdded, registrationReplaced:
					honeypotCount++
					armed = append(armed, map[string]interface{}{
						"name":        hpName,
						"sensitivity": sensitivity,
					})
				case registrationUnchanged:
					honeypotUnchanged++
					armed = append(armed, map[string]interface{}{
						"name":        hpName,
						"sensitivity": sensitivity,
					})
				case registrationConflict:
					conflicts = append(conflicts, joinConflict{
						Tool:   hpName,
						Reason: "decoy name already registered by another cluster",
					})
				}
			}
		}
	}

	slog.Info("adhd.cluster.join accepted",
		"name", p["name"],
		"alarm-a", alarmA,
		"alarm-b", alarmB,
		"repos", repoCount,
		"projects", projectCount,
		"honeypots", honeypotCount,
		"conflicts", len(conflicts),
	)
	result := map[string]interface{}{
		"accepted":            true,
		"name":                p["name"],
		"repos_registered":    repoCount,
		"projects_registered": projectCount,
		"honeypots_set":       honeypotCount,
	}
	// A trap's threshold is part of the wiring, so the host states it back: a
	// caller that set a sensitivity can see what was armed, and one that set
	// nothing sees the decoy is silent on any contact.
	if len(armed) > 0 {
		result["honeypots"] = armed
	}
	// Report repeats and refusals rather than folding them into the counts: a
	// caller that pushed two providers of one capability must be able to see
	// that one is not reachable.
	if repoUnchanged > 0 {
		result["repos_unchanged"] = repoUnchanged
	}
	if projectUnchanged > 0 {
		result["projects_unchanged"] = projectUnchanged
	}
	if honeypotUnchanged > 0 {
		result["honeypots_unchanged"] = honeypotUnchanged
	}
	if len(conflicts) > 0 {
		result["conflicts"] = conflicts
	}
	return result, nil
}

// handleHurlRun executes a hurl script and returns pass/fail with output.
// hurl must be installed and on PATH. The script is written to a temp file,
// run with --test --color=false, and the combined output is returned.
// If certify_domain is set and the script passes, a CapabilityVerifiedMsg
// is noted in the result for the caller to act on.
func (s *Server) handleHurlRun(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	script, _ := m["script"].(string)
	if script == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "script is required"}
	}
	certifyDomain, _ := m["certify_domain"].(string)

	// Write script to a temp file.
	f, err := os.CreateTemp("", "adhd-hurl-*.hurl")
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "create temp file: " + err.Error()}
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		return nil, &jsonrpcError{Code: -32000, Message: "write script: " + err.Error()}
	}
	_ = f.Close()

	// Check hurl is available.
	hurlPath, err := exec.LookPath("hurl")
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "hurl not found on PATH — install hurl (https://hurl.dev)"}
	}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, hurlPath, "--test", "--color=false", f.Name()) //nolint:gosec
	out, runErr := cmd.CombinedOutput()
	passed := runErr == nil

	result := map[string]interface{}{
		"passed": passed,
		"output": string(out),
	}
	if certifyDomain != "" {
		result["certify_domain"] = certifyDomain
		result["certified"] = passed
	}

	slog.Debug("adhd.hurl.run", "passed", passed, "certify_domain", certifyDomain)
	return result, nil
}

// handleGHRun executes a gh CLI command and returns its combined output.
// The gh CLI must be installed and authenticated (gh auth login).
func (s *Server) handleGHRun(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	argsRaw, ok := m["args"]
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "args is required"}
	}
	argsSlice, ok := argsRaw.([]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "args must be an array"}
	}
	args := make([]string, 0, len(argsSlice))
	for i, a := range argsSlice {
		s, ok := a.(string)
		if !ok {
			return nil, &jsonrpcError{Code: -32602, Message: fmt.Sprintf("args[%d] must be a string", i)}
		}
		args = append(args, s)
	}

	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "gh CLI not found on PATH — install GitHub CLI (https://cli.github.com)"}
	}

	timeoutSec := 30
	if ts, ok := m["timeout_seconds"].(float64); ok && ts > 0 {
		timeoutSec = int(ts)
	}

	if q := ghQueueURL(); q != "" {
		return s.runGHViaQueue(ctx, q, args, timeoutSec)
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ghPath, args...) //nolint:gosec
	out, runErr := cmd.CombinedOutput()
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	slog.Debug("adhd.gh.run", "args", args, "exit_code", exitCode)
	return map[string]interface{}{
		"output":    string(out),
		"exit_code": exitCode,
		"success":   exitCode == 0,
	}, nil
}

// handleDependabotAlerts fetches Dependabot alerts for a repo via the gh CLI.
func (s *Server) handleDependabotAlerts(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	repo, _ := m["repo"].(string)
	if repo == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "repo is required (e.g. owner/name)"}
	}
	state := "open"
	if st, ok := m["state"].(string); ok && st != "" {
		state = st
	}
	return s.fetchDependabotAlerts(ctx, repo, state)
}

// fetchDependabotAlerts calls gh api to retrieve Dependabot alerts and returns a structured summary.
func (s *Server) fetchDependabotAlerts(ctx context.Context, repo, state string) (interface{}, *jsonrpcError) {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "gh CLI not found on PATH"}
	}

	apiPath := fmt.Sprintf("repos/%s/dependabot/alerts", repo)
	args := []string{"api", apiPath, "--paginate", "-f", "state=" + state}

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ghPath, args...) //nolint:gosec
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "gh api failed: " + strings.TrimSpace(string(out))}
	}

	var rawAlerts []map[string]interface{}
	if err := json.Unmarshal(out, &rawAlerts); err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "parse alerts: " + err.Error()}
	}

	alerts := make([]map[string]interface{}, 0, len(rawAlerts))
	for _, a := range rawAlerts {
		alert := map[string]interface{}{
			"number": a["number"],
			"state":  a["state"],
			"url":    a["html_url"],
		}
		if dep, ok := a["dependency"].(map[string]interface{}); ok {
			if pkg, ok := dep["package"].(map[string]interface{}); ok {
				alert["package"] = pkg["name"]
				alert["ecosystem"] = pkg["ecosystem"]
			}
		}
		if sa, ok := a["security_advisory"].(map[string]interface{}); ok {
			alert["severity"] = sa["severity"]
			alert["summary"] = sa["summary"]
			alert["cve"] = sa["cve_id"]
		}
		alerts = append(alerts, alert)
	}

	slog.Debug("adhd.dependabot.alerts", "repo", repo, "state", state, "count", len(alerts))
	return map[string]interface{}{
		"repo":   repo,
		"count":  len(alerts),
		"alerts": alerts,
	}, nil
}

// registerRepoTool registers a per-repo adhd.dependabot.<owner>.<repo> tool for
// a cluster that owns the given GitHub repo slug (e.g. "james-gibson/adhd").
// The name is derived from the slug, so a repeat registration is the same tool
// serving the same repo and is reported as unchanged, not as a new one.
func (s *Server) registerRepoTool(clusterName, slug string) registrationOutcome {
	safeName := strings.ReplaceAll(slug, "/", ".")
	toolName := "adhd.dependabot." + safeName

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == toolName {
			return registrationUnchanged
		}
	}
	capturedSlug := slug
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        toolName,
		description: fmt.Sprintf("Fetch Dependabot alerts for %s (cluster: %s). Requires gh CLI.", slug, clusterName),
		origin:      slug,
		inputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"state": map[string]interface{}{
					"type":        "string",
					"description": "Alert state filter: open, dismissed, auto_dismissed, fixed (default: open)",
				},
			},
		},
		handler: func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
			state := "open"
			if m, ok := params.(map[string]interface{}); ok {
				if st, ok := m["state"].(string); ok && st != "" {
					state = st
				}
			}
			return s.fetchDependabotAlerts(ctx, capturedSlug, state)
		},
		cluster: clusterName,
	})
	slog.Info("registered per-repo tool", "tool", toolName, "cluster", clusterName)
	return registrationAdded
}

// registerHoneypotTool registers a decoy tool that looks legitimate but logs any caller
// that invokes it. The fakeResult is returned to avoid revealing the trap.
//
// This is a knock surface, in the older port-knocking sense: invoking the decoy
// *is* the knock. It differs from knocking on a closed port in that the decoy
// answers — "supports the knock" — so the caller cannot tell the knock landed
// wrong, and the sequence stays informative instead of becoming a black hole.
//
// The knock is a *detection* signal: the caller IP is logged and the event is
// filed via fireSecurity -> /federation/report. It never authorizes anything —
// a knock no more admits a caller than a wrong knock rejects one. Who may drive
// the real surface is the trust chain's decision (adhd#27); the knock only tells
// us someone knocked, and what to do about it is decided after.
//
// A simple knock is an ordered call sequence — the same tools invoked descending
// vs ascending, or addressed proxy hops called in scrambled order (Enigma-style).
// No single phase carries the security: each ordering is one bit, and it is the
// *collection* of phases that the verifier checks against the outcome. The order
// is a *phase* rather than a guess because the isotope record fixes it —
// VerifyIsotopeChain checks each link's role and requires strictly increasing
// timestamps, signed so the sequence is non-repudiable. The clock order is
// required, not incidental: an external sidecar verifies every hop against the
// shared tick, so no hop vouches for itself. Simple example: A -> B -> C, each
// with a different job.
func (s *Server) registerHoneypotTool(clusterName, name, description string, sensitivity int) registrationOutcome {
	return s.registerDecoy(clusterName, name, description, map[string]interface{}{"ok": true}, sensitivity)
}

// RegisterHoneypotTool is the exported version for external use (e.g. tests or demo setup).
// It registers a silent decoy: sensitivity 0 means any contact fires.
func (s *Server) RegisterHoneypotTool(name, description string, fakeResult interface{}) registrationOutcome {
	return s.registerDecoy("", name, description, fakeResult, 0)
}

// registerDecoy adds a honeypot. A decoy's name is authored by the joining
// cluster, so a second cluster claiming the same name is either a repeat of the
// same trap (unchanged) or a different one (conflict). It is never overwritten:
// silently swapping a decoy's identity is how a detection surface is disarmed.
func (s *Server) registerDecoy(clusterName, name, description string, fakeResult interface{}, sensitivity int) registrationOutcome {
	origin := description
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name != name {
			continue
		}
		if t.honeypot && t.origin == origin && t.sensitivity == sensitivity {
			return registrationUnchanged
		}
		slog.Warn("honeypot name collision",
			"tool", name, "cluster", clusterName, "registered_by", t.cluster,
			"registered_sensitivity", t.sensitivity, "offered_sensitivity", sensitivity)
		return registrationConflict
	}
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        name,
		description: description,
		origin:      origin,
		honeypot:    true,
		sensitivity: sensitivity,
		fakeResult:  fakeResult,
		inputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		cluster: clusterName,
	})
	slog.Info("registered honeypot tool", "tool", name, "cluster", clusterName)
	return registrationAdded
}

// generateNonce returns a cryptographically random 16-byte hex nonce.
func generateNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use time-based nonce (should not happen in practice)
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// handlePathNegotiate builds and stores pre-verified multi-hop trust paths to a target.
// It challenges each potential hop with adhd.rung.challenge and records the receipts.
// Direct paths (1-hop) and intermediary paths (2-hop via cluster peers) are both explored.
func (s *Server) handlePathNegotiate(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	targetIsotope, _ := m["target.isotope"].(string)
	if targetIsotope == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "target.isotope is required"}
	}
	featureID, _ := m["feature_id"].(string)
	if featureID == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "feature_id is required"}
	}

	// Default discovery depth is four hops. A caller that knows a deeper map can
	// ask for it; routing itself has no depth cap.
	maxHops := 4
	if v, ok := m["max_hops"].(float64); ok && int(v) > 0 {
		maxHops = int(v)
	}
	if maxHops > 16 {
		maxHops = 16
	}

	s.mu.RLock()
	peers := make(map[string]*clusterPeerInfo, len(s.clusterPeers))
	for k, v := range s.clusterPeers {
		peers[k] = v
	}
	s.mu.RUnlock()

	slog.Debug("path negotiate: resolving target",
		"target", targetIsotope,
		"peer_count", len(peers))

	targetEndpoint, targetName := s.resolveTargetViaDiscovery(ctx, targetIsotope, peers)
	slog.Debug("path negotiate: resolved",
		"target", targetIsotope,
		"resolved_endpoint", targetEndpoint,
		"resolved_name", targetName)
	if targetEndpoint != "" {
		originalTarget := targetIsotope
		targetIsotope = targetName
		peers[targetName] = &clusterPeerInfo{Name: targetName, Endpoint: targetEndpoint}
		if originalTarget != targetName {
			peers[originalTarget] = &clusterPeerInfo{Name: targetName, Endpoint: targetEndpoint}
		}
	}

	paths := s.discoverRoutes(ctx, peers, targetIsotope, featureID, maxHops)

	// Highest trust first; at equal trust, prefer the shorter route.
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].MinRung != paths[j].MinRung {
			return paths[i].MinRung > paths[j].MinRung
		}
		return len(paths[i].Hops) < len(paths[j].Hops)
	})

	// Store for later retrieval and verification.
	s.mu.Lock()
	s.negotiatedPaths[targetIsotope] = paths
	s.mu.Unlock()

	// Encode paths as base64 tokens for transport.
	result := make([]interface{}, 0, len(paths))
	for _, p := range paths {
		data, _ := json.Marshal(p)
		encodedSegments := make([]string, 0, len(p.Segments))
		for i := range p.Segments {
			segData, _ := json.Marshal(p.Segments[i])
			encodedSegments = append(encodedSegments, base64.StdEncoding.EncodeToString(segData))
		}
		result = append(result, map[string]interface{}{
			"target":        p.Target,
			"min_rung":      p.MinRung,
			"hops":          len(p.Hops),
			"feature_id":    p.FeatureID,
			"tick":          p.Tick,
			"segments":      encodedSegments,
			"negotiated_at": p.NegotiatedAt,
			"token":         base64.StdEncoding.EncodeToString(data),
		})
	}

	slog.Info("adhd.path.negotiate complete",
		"target", targetIsotope,
		"paths_found", len(paths),
		"best_rung", func() int {
			if len(paths) > 0 {
				return paths[0].MinRung
			}
			return 0
		}(),
	)
	return map[string]interface{}{
		"target":      targetIsotope,
		"paths_found": len(paths),
		"paths":       result,
	}, nil
}

// handlePathCall executes a call along one hop of a negotiated path.
//
// The caller must present the segment for the hop it is about to use. A
// multi-hop route forwards the remaining segments to the next hop, so every hop
// validates its own token against its own tick: holding only the first segment
// gets a caller to the midpoint and no further. The same segment can be used by
// any holder during its tick — paths are reusable within a tick, unlike a
// single-use skill.
func (s *Server) handlePathCall(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	call, _ := m["call"].(map[string]interface{})
	if len(call) == 0 {
		return nil, &jsonrpcError{Code: -32602, Message: "call is required and must be an object"}
	}
	segments, err := decodeSegments(m)
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}

	// Every segment is a self-contained hop. The caller supplies as much of the
	// map as it knows: one segment routes one hop, eight route eight. There is no
	// server-side route length to satisfy.
	segment := segments[0]
	now, ticked := s.nowTick()
	if ticked && !tickWithinGrace(segment.Tick, now) {
		s.fireSecurity("warn", "path_stale", map[string]interface{}{
			"path": segment.ID,
			"tick": segment.Tick,
			"now":  now,
		})
		return nil, &jsonrpcError{
			Code:    -32010,
			Message: fmt.Sprintf("path %s expired: negotiated for tick %d, now %d (grace %d tick)", segment.ID, segment.Tick, now, PathGraceTicks),
		}
	}

	if method, _ := call["method"].(string); pathDeniedMethods[method] {
		s.fireSecurity("warn", "path_scope", map[string]interface{}{
			"path":   segment.ID,
			"method": method,
		})
		return nil, &jsonrpcError{
			Code:    -32011,
			Message: fmt.Sprintf("path %s does not grant %s: routes execute, they do not enumerate", segment.ID, method),
		}
	}

	if sink := s.mirrorSinkFor(); sink != nil {
		caller, _ := ctx.Value(callerIPKey{}).(string)
		var usedTick uint64
		if ticked {
			usedTick = now
		}
		sink(mirror.Frame{
			Tick:     segment.Tick,
			UsedTick: usedTick,
			Kind:     "path.hop",
			Path:     segment.ID,
			From:     segment.From,
			To:       segment.To,
			Feature:  segment.FeatureID,
			Caller:   caller,
			At:       time.Now().UTC(),
		})
	}

	var forward map[string]interface{}
	if len(segments) > 1 {
		forward = map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "adhd.path.call",
			"params": map[string]interface{}{
				"segments": encodeSegments(segments[1:]),
				"call":     call,
			},
		}
	} else {
		if call["jsonrpc"] == nil {
			call["jsonrpc"] = "2.0"
		}
		if call["id"] == nil {
			call["id"] = 1
		}
		forward = call
	}

	resp, execErr := s.proxyExecutor.ExecuteProxy(ctx, proxy.ProxyRequest{
		TargetEndpoint: segment.To,
		Call:           forward,
	})
	if execErr != nil {
		return nil, &jsonrpcError{Code: -32603, Message: execErr.Error()}
	}
	if resp.Error != nil {
		return nil, &jsonrpcError{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	// A refusal at a later hop arrives inside the nested JSON-RPC response.
	if nested, ok := resp.Result.(map[string]interface{}); ok {
		if errObj, ok := nested["error"].(map[string]interface{}); ok {
			code, _ := errObj["code"].(float64)
			message, _ := errObj["message"].(string)
			return nil, &jsonrpcError{Code: int(code), Message: message}
		}
	}
	return resp.Result, nil
}

func decodeSegments(m map[string]interface{}) ([]PathSegment, error) {
	var encoded []string
	switch v := m["segments"].(type) {
	case []interface{}:
		for _, entry := range v {
			if str, ok := entry.(string); ok && str != "" {
				encoded = append(encoded, str)
			}
		}
	case []string:
		encoded = v
	}
	if len(encoded) == 0 {
		if single, ok := m["path"].(string); ok && single != "" {
			encoded = []string{single}
		}
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("segments is required")
	}

	out := make([]PathSegment, 0, len(encoded))
	for _, enc := range encoded {
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("segment: %w", err)
		}
		var segment PathSegment
		if err := json.Unmarshal(raw, &segment); err != nil {
			return nil, fmt.Errorf("segment: %w", err)
		}
		if segment.To == "" {
			return nil, fmt.Errorf("segment %s has no endpoint", segment.ID)
		}
		out = append(out, segment)
	}
	return out, nil
}

func encodeSegments(segments []PathSegment) []string {
	out := make([]string, 0, len(segments))
	for i := range segments {
		data, _ := json.Marshal(segments[i])
		out = append(out, base64.StdEncoding.EncodeToString(data))
	}
	return out
}

// handlePathVerify re-challenges each hop of all stored paths to the target,
// confirming they remain alive and trusted with fresh nonces.
func (s *Server) handlePathVerify(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	targetIsotope, _ := m["target.isotope"].(string)
	if targetIsotope == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "target.isotope is required"}
	}
	featureID, _ := m["feature_id"].(string)
	if featureID == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "feature_id is required"}
	}

	s.mu.RLock()
	paths := s.negotiatedPaths[targetIsotope]
	s.mu.RUnlock()

	if len(paths) == 0 {
		return map[string]interface{}{
			"target":  targetIsotope,
			"paths":   []interface{}{},
			"message": "no pre-negotiated paths — run adhd.path.negotiate first",
		}, nil
	}

	now, ticked := s.nowTick()
	results := make([]interface{}, 0, len(paths))
	for _, path := range paths {
		allValid := true
		if ticked && !tickWithinGrace(path.Tick, now) {
			// Verification does not renew a path. A token older than the grace
			// window is stale and stays stale.
			s.fireSecurity("warn", "path_stale", map[string]interface{}{
				"target": path.Target,
				"tick":   path.Tick,
				"now":    now,
			})
			results = append(results, map[string]interface{}{
				"target":   path.Target,
				"min_rung": path.MinRung,
				"valid":    false,
				"reason":   "stale_tick",
				"tick":     path.Tick,
				"now":      now,
				"hops":     []interface{}{},
			})
			continue
		}
		hopResults := make([]interface{}, 0, len(path.Hops))
		for _, hop := range path.Hops {
			nonce := generateNonce()
			res, err := s.callPeerMCP(ctx, hop.Endpoint, "adhd.rung.challenge", map[string]interface{}{
				"target.url": hop.Endpoint,
				"feature_id": featureID,
				"nonce":      nonce,
			})
			valid := err == nil && isVerified(res)
			if !valid {
				allValid = false
			}
			hopResults = append(hopResults, map[string]interface{}{
				"isotope":  hop.Isotope,
				"endpoint": hop.Endpoint,
				"rung":     hop.Rung,
				"valid":    valid,
			})
		}
		results = append(results, map[string]interface{}{
			"target":   path.Target,
			"min_rung": path.MinRung,
			"valid":    allValid,
			"hops":     hopResults,
		})
	}

	return map[string]interface{}{
		"target": targetIsotope,
		"paths":  results,
	}, nil
}

// handlePathList returns pre-negotiated paths, optionally filtered by target.
func (s *Server) handlePathList(params interface{}) (interface{}, *jsonrpcError) {
	var filterTarget string
	if m, ok := params.(map[string]interface{}); ok {
		filterTarget, _ = m["target.isotope"].(string)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result := []interface{}{}
	for target, paths := range s.negotiatedPaths {
		if filterTarget != "" && target != filterTarget {
			continue
		}
		for _, p := range paths {
			result = append(result, map[string]interface{}{
				"target":        p.Target,
				"min_rung":      p.MinRung,
				"hops":          len(p.Hops),
				"feature_id":    p.FeatureID,
				"negotiated_at": p.NegotiatedAt,
			})
		}
	}

	return map[string]interface{}{"paths": result}, nil
}

// peerRungFromResult extracts a trust rung from a callPeerMCP result if present.
// buildSegments turns a hop list into per-hop capability tokens, each stamped
// with the tick it is valid for. A caller must hold every segment to traverse a
// multi-hop route.
func (s *Server) buildSegments(hops []PathHop, feature string, tick uint64) []PathSegment {
	segments := make([]PathSegment, 0, len(hops))
	from := "self"
	for _, hop := range hops {
		segments = append(segments, PathSegment{
			ID:        s.nextPathID(),
			From:      from,
			To:        hop.Endpoint,
			FeatureID: feature,
			Tick:      tick,
			Rung:      hop.Rung,
			Nonce:     hop.Nonce,
			Receipt:   hop.Receipt,
		})
		from = hop.Isotope
	}
	return segments
}

// routeNode is one discovered endpoint in the peer graph.
type routeNode struct {
	Name     string
	Endpoint string
}

// discoverRoutes walks the peer graph up to maxHops and verifies every route it
// finds. The default depth is four; routing itself has no cap, so a caller that
// holds a deeper map can ask for it.
func (s *Server) discoverRoutes(ctx context.Context, seeds map[string]*clusterPeerInfo, target, feature string, maxHops int) []*NegotiatedPath {
	endpoints := make(map[string]string, len(seeds))
	adj := make(map[string][]routeNode, len(seeds)+1)
	for name, peer := range seeds {
		if name == "" || peer == nil || peer.Endpoint == "" {
			continue
		}
		endpoints[name] = peer.Endpoint
		adj["self"] = append(adj["self"], routeNode{Name: name, Endpoint: peer.Endpoint})
	}

	// Breadth-first expansion. Depth 1 is the local peer list; each later depth
	// asks the previous hop for its peers, which is also the voucher for the
	// challenge at that hop.
	frontier := make([]string, 0, len(seeds))
	for _, edge := range adj["self"] {
		frontier = append(frontier, edge.Name)
	}
	visited := map[string]bool{"self": true}
	for depth := 1; depth < maxHops && len(frontier) > 0; depth++ {
		next := make([]string, 0, len(frontier))
		for _, name := range frontier {
			if visited[name] {
				continue
			}
			visited[name] = true
			endpoint := endpoints[name]
			if endpoint == "" {
				continue
			}
			topology, err := s.callPeerMCP(ctx, endpoint, "adhd.isotope.peers", nil)
			if err != nil {
				continue
			}
			for _, edge := range peersFromTopology(topology) {
				if edge.Name == "" || edge.Endpoint == "" || visited[edge.Name] {
					continue
				}
				endpoints[edge.Name] = edge.Endpoint
				adj[name] = append(adj[name], edge)
				next = append(next, edge.Name)
			}
		}
		frontier = next
	}

	var routes [][]string
	var walk func(path []string)
	walk = func(path []string) {
		if len(routes) >= 32 {
			return
		}
		last := path[len(path)-1]
		if last == target {
			routes = append(routes, append([]string(nil), path...))
			return
		}
		if len(path)-1 >= maxHops {
			return
		}
		for _, edge := range adj[last] {
			if containsName(path, edge.Name) {
				continue
			}
			walk(append(path, edge.Name))
		}
	}
	walk([]string{"self"})

	seen := map[string]bool{}
	out := make([]*NegotiatedPath, 0, len(routes))
	for _, route := range routes {
		key := strings.Join(route, ">")
		if seen[key] {
			continue
		}
		seen[key] = true
		hops, ok := s.challengeRoute(ctx, route, endpoints, feature)
		if !ok {
			continue
		}
		minRung := hops[0].Rung
		for _, hop := range hops[1:] {
			if hop.Rung < minRung {
				minRung = hop.Rung
			}
		}
		tick, _ := s.nowTick()
		out = append(out, &NegotiatedPath{
			Target:       target,
			FeatureID:    feature,
			Hops:         hops,
			Segments:     s.buildSegments(hops, feature, tick),
			MinRung:      minRung,
			Tick:         tick,
			NegotiatedAt: time.Now(),
		})
	}
	return out
}

// challengeRoute verifies each hop: the first hop challenges itself, and every
// later hop is challenged by the hop before it.
func (s *Server) challengeRoute(ctx context.Context, route []string, endpoints map[string]string, feature string) ([]PathHop, bool) {
	hops := make([]PathHop, 0, len(route)-1)
	for i, name := range route[1:] {
		endpoint := endpoints[name]
		if endpoint == "" {
			return nil, false
		}
		challenger := endpoint
		if i > 0 {
			challenger = endpoints[route[i]]
		}
		nonce := generateNonce()
		res, err := s.callPeerMCP(ctx, challenger, "adhd.rung.challenge", map[string]interface{}{
			"target.url": endpoint,
			"feature_id": feature,
			"nonce":      nonce,
		})
		if err != nil || !isVerified(res) {
			return nil, false
		}
		hops = append(hops, PathHop{
			Isotope:  name,
			Endpoint: endpoint,
			Rung:     peerRungFromResult(res),
			Nonce:    nonce,
			Receipt:  safeReceipt(res),
		})
	}
	return hops, len(hops) > 0
}

func peersFromTopology(topology map[string]interface{}) []routeNode {
	peers, _ := topology["peers"].([]interface{})
	out := make([]routeNode, 0, len(peers))
	for _, entry := range peers {
		peer, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := peer["name"].(string)
		endpoint, _ := peer["endpoint"].(string)
		out = append(out, routeNode{Name: name, Endpoint: endpoint})
	}
	return out
}

func containsName(path []string, name string) bool {
	for _, entry := range path {
		if entry == name {
			return true
		}
	}
	return false
}

func peerRungFromResult(res map[string]interface{}) int {
	if r, ok := res["rung"].(float64); ok {
		return int(r)
	}
	return 0
}

// isVerified returns true if a challenge result has verified=true.
func isVerified(res map[string]interface{}) bool {
	v, _ := res["verified"].(bool)
	return v
}

// safeReceipt extracts the receipt string from a challenge result.
func safeReceipt(res map[string]interface{}) string {
	r, _ := res["receipt"].(string)
	return r
}

// resolveTargetViaDiscovery resolves a target isotope by name, IP:port, or discovery URL.
// It queries the discovery endpoint of known cluster peers to find the target.
// Returns (endpoint, name) if found, or ("", targetIsotope) if not.
func (s *Server) resolveTargetViaDiscovery(ctx context.Context, targetIsotope string, localPeers map[string]*clusterPeerInfo) (string, string) {
	normalizedTarget := strings.TrimPrefix(targetIsotope, "http://")
	normalizedTarget = strings.TrimPrefix(normalizedTarget, "https://")
	normalizedTarget = strings.TrimSuffix(normalizedTarget, "/mcp")
	normalizedTarget = strings.TrimSuffix(normalizedTarget, "/cluster")

	slog.Debug("resolve target: checking local peers",
		"target", targetIsotope,
		"normalized", normalizedTarget)

	for _, peer := range localPeers {
		if peer.Endpoint == "" {
			continue
		}
		normalizedEndpoint := strings.TrimPrefix(peer.Endpoint, "http://")
		normalizedEndpoint = strings.TrimSuffix(normalizedEndpoint, "/mcp")
		slog.Debug("resolve target: peer endpoint",
			"peer_name", peer.Name,
			"endpoint", peer.Endpoint,
			"normalized", normalizedEndpoint,
			"match", strings.Contains(normalizedEndpoint, normalizedTarget) || strings.Contains(normalizedTarget, normalizedEndpoint))
		if strings.Contains(normalizedEndpoint, normalizedTarget) || strings.Contains(normalizedTarget, normalizedEndpoint) {
			return peer.Endpoint, peer.Name
		}
		// The peer's cluster registry lives on the well-known discovery port,
		// not on the port the peer serves MCP (or its alarm) on — replace that
		// port, never append to it. Appending produced URLs such as
		// "http://host:5100:19100/cluster", which never resolved.
		discoveryURL := ""
		if peer.AlarmA != "" {
			discoveryURL = demo.RegistryURLForAlarm(peer.AlarmA)
		} else if peer.Endpoint != "" {
			discoveryURL = demo.RegistryURLForAlarm(peer.Endpoint)
		}
		if discoveryURL == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL, nil)
		if err != nil {
			continue
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			continue
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			continue
		}
		var clusters map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&clusters); err != nil {
			continue
		}
		for name, info := range clusters {
			infoMap, _ := info.(map[string]interface{})
			if infoMap == nil {
				continue
			}
			if name == targetIsotope {
				endpoint := firstString(infoMap, "adhd_mcp", "adhd.mcp")
				return endpoint, name
			}
			if endpoint := firstString(infoMap, "adhd_mcp", "adhd.mcp"); endpoint != "" {
				if strings.Contains(endpoint, targetIsotope) || strings.Contains(targetIsotope, strings.Split(endpoint, "://")[1]) {
					return endpoint, name
				}
			}
		}
	}
	return "", targetIsotope
}

// registrationOutcome reports what a dynamic-tool registration actually did.
// The join counters count added and replaced registrations only, so a count can
// never exceed what tools/list shows (adhd#40: the join used to count a project
// whose tool was silently dropped on a name clash).
type registrationOutcome int

const (
	// registrationAdded is a new tool name.
	registrationAdded registrationOutcome = iota
	// registrationReplaced is an existing tool pointed at a new endpoint. A
	// project's route is its identity, so the same route on a new URL is a
	// rejoin (lezz demo restarts on a new port), not a second project.
	registrationReplaced
	// registrationUnchanged is an identical repeat registration.
	registrationUnchanged
	// registrationConflict is the same tool name for a different thing. It is
	// never resolved by keeping the first one silently: the join reports it.
	registrationConflict
)

// joinConflict is a registration that could not be honoured, reported to the
// joining cluster so the caller learns the surface is not what it pushed.
type joinConflict struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

// RegisterDiscoveredClusters wires clusters discovered by `adhd --demo` into the
// hosted MCP server: each project becomes an adhd.project.<id>.call proxy tool,
// and each cluster's adhd_mcp endpoint becomes an addressable peer for path
// negotiation. It is the config-driven counterpart of adhd.cluster.join, which
// remains the push path used by lezz demo. Registration follows the same outcome
// rules as a join: an identical repeat is unchanged, and a project whose seed id
// moves to a new endpoint replaces its tool.
func (s *Server) RegisterDiscoveredClusters(clusters []config.DiscoveredCluster) {
	for _, c := range clusters {
		if c.AdhdMCP != "" {
			s.mu.Lock()
			if s.clusterPeers == nil {
				s.clusterPeers = make(map[string]*clusterPeerInfo)
			}
			if _, known := s.clusterPeers[c.Name]; !known {
				s.clusterPeers[c.Name] = &clusterPeerInfo{
					Name:     c.Name,
					Endpoint: c.AdhdMCP,
					JoinedAt: time.Now(),
				}
			}
			s.mu.Unlock()
		}
		for _, p := range c.Projects {
			if p.Name == "" || p.MCPURL == "" {
				continue
			}
			// A published seed id is the project's identity. Without one the
			// name is used as a fallback and is not explicit, so a name
			// collision is reported rather than silently replacing another
			// project's tool.
			s.registerProjectToolID(c.Name, p.ID, p.Name, p.Repo, p.MCPURL, p.ID != "")
		}
	}
}

// registerProjectTool is the legacy entry point: the display name is the route.
// It is kept for callers that predate project.route (ADR lezz.go 0003), and a
// name-only collision is reported as a conflict rather than dropped.
func (s *Server) registerProjectTool(clusterName, projectName, repo, mcpURL string) registrationOutcome {
	return s.registerProjectToolID(clusterName, projectName, projectName, repo, mcpURL, false)
}

// registerProjectToolID namespaces the dynamic tool by the project's route.
//
// explicitRoute distinguishes "this is the project's identity" from "we only
// have a display name": the first may move an existing tool to a new endpoint,
// the second must not, because it cannot tell a rejoin from a different project
// that happens to share a name.
func (s *Server) registerProjectToolID(clusterName, route, projectName, repo, mcpURL string, explicitRoute bool) registrationOutcome {
	route = sanitizeProjectRoute(route, projectName)
	if projectName == "" {
		projectName = route
	}
	toolName := "adhd.project." + route + ".call"

	newTool := s.newProjectTool(toolName, clusterName, projectName, repo, mcpURL)

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.dynamicTools {
		if t.name != toolName {
			continue
		}
		if t.origin == mcpURL {
			return registrationUnchanged
		}
		if !explicitRoute {
			slog.Warn("adhd.cluster.join project name collision",
				"tool", toolName,
				"cluster", clusterName,
				"registered_by", t.cluster,
				"reason", "no project.route in the join payload",
			)
			return registrationConflict
		}
		// The route names one project; the endpoint may change when it restarts.
		s.dynamicTools[i] = newTool
		slog.Info("replaced per-project proxy tool", "tool", toolName,
			"cluster", clusterName, "mcp_url", mcpURL, "previous_cluster", t.cluster)
		return registrationReplaced
	}
	s.dynamicTools = append(s.dynamicTools, newTool)
	slog.Info("registered per-project proxy tool", "tool", toolName, "project", projectName,
		"mcp_url", mcpURL, "cluster", clusterName)
	return registrationAdded
}

// newProjectTool builds the proxy tool for one project. It is a fresh value so
// a replacement never mutates a tool a concurrent call has already looked up.
func (s *Server) newProjectTool(toolName, clusterName, projectName, repo, mcpURL string) *dynamicTool {
	capturedURL := mcpURL
	capturedProject := projectName
	desc := fmt.Sprintf("Proxy JSON-RPC call to the %s MCP server (cluster: %s)", projectName, clusterName)
	if repo != "" {
		desc = fmt.Sprintf("Proxy JSON-RPC call to the %s MCP server — %s (cluster: %s)", projectName, repo, clusterName)
	}
	return &dynamicTool{
		name:        toolName,
		description: desc,
		origin:      mcpURL,
		inputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"method": map[string]interface{}{
					"type":        "string",
					"description": fmt.Sprintf("MCP method to call on the %s server (e.g. tools/list)", capturedProject),
				},
				"params": map[string]interface{}{
					"type":        "object",
					"description": "Optional parameters for the method",
				},
			},
			"required": []string{"method"},
		},
		handler: func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
			m, ok := params.(map[string]interface{})
			if !ok {
				return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
			}
			method, _ := m["method"].(string)
			if method == "" {
				return nil, &jsonrpcError{Code: -32602, Message: "method is required"}
			}
			var callParams interface{}
			if p, ok := m["params"]; ok {
				callParams = p
			}
			result, err := s.callPeerMCP(ctx, capturedURL, method, callParams)
			if err != nil {
				return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
			}
			return result, nil
		},
		cluster: clusterName,
	}
}

// projectRoute returns the routing id for a joining project and whether it was
// explicit. project.route is the field (ADR lezz.go 0003); the legacy seed.id is
// read as an alias for one release, and only a payload with neither falls back
// to the display name — which is exactly the collision #40 describes.
func projectRoute(pj map[string]interface{}) (route string, explicit bool) {
	for _, key := range []string{"route", "id"} {
		if v, ok := pj[key].(string); ok && v != "" {
			return v, true
		}
	}
	name, _ := pj["name"].(string)
	return name, false
}

// sanitizeProjectRoute keeps the dynamic tool name addressable. A route is
// expected to be a slug or ULID (idPattern ^[A-Za-z0-9_.-]+$); anything else is
// reduced, and an empty result falls back to the display name.
func sanitizeProjectRoute(route, name string) string {
	clean := func(v string) string {
		var b strings.Builder
		for _, r := range v {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
				b.WriteRune(r)
			default:
				b.WriteRune('-')
			}
		}
		return strings.Trim(b.String(), "-")
	}
	if v := clean(route); v != "" {
		return v
	}
	if v := clean(name); v != "" {
		return v
	}
	return "project"
}

// callPeerMCP sends a single JSON-RPC call to a peer MCP server and returns
// the result map. Returns an error if the call fails or the peer returns an error.
func (s *Server) callPeerMCP(ctx context.Context, baseURL, method string, params interface{}) (map[string]interface{}, error) {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	mcpURL := baseURL
	switch {
	case strings.HasSuffix(mcpURL, "/mcp"):
	case strings.HasSuffix(mcpURL, "/"):
		mcpURL += "mcp"
	default:
		mcpURL += "/mcp"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call peer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp struct {
		Result map[string]interface{} `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("peer error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

// parseStringParams extracts required string fields from a params interface{}.
func parseStringParams(params interface{}, keys ...string) (map[string]string, error) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("params must be an object")
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		v, exists := m[k]
		if !exists {
			return nil, fmt.Errorf("missing required param %q", k)
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("param %q must be a string", k)
		}
		out[k] = s
	}
	return out, nil
}

// handlePushLogs accepts log entries pushed by prime-plus isotopes
func (s *Server) handlePushLogs(rawParams interface{}) (interface{}, *jsonrpcError) {
	// Re-marshal then unmarshal to extract the typed struct
	data, err := json.Marshal(rawParams)
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "Invalid params: " + err.Error()}
	}
	var p struct {
		Logs []map[string]interface{} `json:"logs"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "Invalid params: " + err.Error()}
	}

	if s.pushLogsHandler != nil {
		if err := s.pushLogsHandler(p.Logs); err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: "push-logs handler error: " + err.Error()}
		}
	}

	return map[string]interface{}{
		"accepted": len(p.Logs),
	}, nil
}

// respondError sends an error-only response
func (s *Server) respondError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)

	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      0,
		Error: &jsonrpcError{
			Code:    code,
			Message: message,
		},
	}

	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Warn("encode response", "error", err)
	}
}

// handleProxyCall proxies an MCP call to a target endpoint with optional authentication
func (s *Server) handleProxyCall(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	paramsMap, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}

	// Normalize a string-encoded nested "call" object back into a map so both
	// direct-method and tools/call transports accept it.
	if callStr, isStr := paramsMap["call"].(string); isStr && callStr != "" {
		var callObj map[string]interface{}
		if err := json.Unmarshal([]byte(callStr), &callObj); err == nil {
			paramsMap["call"] = callObj
		}
	}

	// Round-trip through the typed struct so field extraction is unambiguous
	// and forward-compatible (unlike the bare-map assertions that failed for
	// nested call objects via tools/call).
	raw, err := json.Marshal(paramsMap)
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "invalid proxy params: " + err.Error()}
	}

	var proxyReq proxy.ProxyRequest
	if err := json.Unmarshal(raw, &proxyReq); err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: "invalid proxy params: " + err.Error()}
	}

	if proxyReq.TargetEndpoint == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "target_endpoint is required and must be a string"}
	}
	if len(proxyReq.Call) == 0 {
		return nil, &jsonrpcError{Code: -32602, Message: "call is required and must be an object"}
	}

	// Execute the proxy
	resp, err := s.proxyExecutor.ExecuteProxy(ctx, proxyReq)
	if err != nil {
		return nil, &jsonrpcError{Code: -32603, Message: "Proxy execution failed: " + err.Error()}
	}

	// If response has an error, return it
	if resp.Error != nil {
		msg := resp.Error.Message
		if resp.Error.Details != "" {
			msg += ": " + resp.Error.Details
		}
		return nil, &jsonrpcError{Code: resp.Error.Code, Message: msg}
	}

	return resp.Result, nil
}

// JSON-RPC types

type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *jsonrpcError `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
