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
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
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

// dynamicTool is a runtime-registered MCP tool added when a cluster joins.
// honeypot tools return plausible-looking fake results but log the caller's identity.
type dynamicTool struct {
	name        string
	description string
	inputSchema map[string]interface{}
	honeypot    bool
	fakeResult  interface{}
	handler     func(ctx context.Context, params interface{}) (interface{}, *jsonrpcError)
}

// clusterPeerInfo tracks a peer ADHD instance discovered via adhd.cluster.join.
type clusterPeerInfo struct {
	Name      string                   `json:"name"`
	Endpoint  string                   `json:"endpoint"` // adhd_mcp URL
	AlarmA    string                   `json:"alarm_a"`
	AlarmB    string                   `json:"alarm_b"`
	Projects  []map[string]interface{} `json:"projects,omitempty"`
	JoinedAt  time.Time                `json:"joined_at"`
}

// PathHop records a single verified hop in a negotiated trust path.
type PathHop struct {
	Isotope  string `json:"isotope"`
	Endpoint string `json:"endpoint"`
	Rung     int    `json:"rung"`
	Nonce    string `json:"nonce"`
	Receipt  string `json:"receipt"`
}

// NegotiatedPath is a pre-verified multi-hop trust path to a target isotope.
type NegotiatedPath struct {
	Target       string    `json:"target"`
	Hops         []PathHop `json:"hops"`
	MinRung      int       `json:"min_rung"`
	FeatureID    string    `json:"feature_id"`
	NegotiatedAt time.Time `json:"negotiated_at"`
}

// Server hosts an MCP-compatible endpoint exposing dashboard lights
type Server struct {
	config             config.MCPServerConfig
	cluster            *lights.Cluster
	topologyProvider   TopologyProvider
	pushLogsHandler    PushLogsHandler
	clusterJoinHandler ClusterJoinHandler
	middleware         func(http.Handler) http.Handler
	server             *http.Server
	httpClient         *http.Client

	// Rung validation identity fields — set via SetInstanceIdentity.
	instanceIsotope string
	receiptComputer ReceiptComputer
	receiptVerifier ReceiptVerifier

	securityAlertHandler SecurityAlertHandler

	// Dynamic per-repo tools registered on cluster join.
	mu           sync.RWMutex
	dynamicTools []*dynamicTool

	// Cluster peers discovered via adhd.cluster.join — used for path negotiation.
	clusterPeers    map[string]*clusterPeerInfo

	// Pre-negotiated trust paths, keyed by target isotope name.
	negotiatedPaths map[string][]*NegotiatedPath
}

// NewServer creates an MCP server
func NewServer(cfg config.MCPServerConfig, cluster *lights.Cluster) *Server {
	return &Server{
		config:  cfg,
		cluster: cluster,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		clusterPeers:    make(map[string]*clusterPeerInfo),
		negotiatedPaths: make(map[string][]*NegotiatedPath),
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

	case "adhd.hurl.run":
		result, respErr = s.handleHurlRun(r.Context(), req.Params)

	case "adhd.gh.run":
		result, respErr = s.handleGHRun(r.Context(), req.Params)

	case "adhd.dependabot.alerts":
		result, respErr = s.handleDependabotAlerts(r.Context(), req.Params)

	case "adhd.path.negotiate":
		result, respErr = s.handlePathNegotiate(r.Context(), req.Params)

	case "adhd.path.verify":
		result, respErr = s.handlePathVerify(r.Context(), req.Params)

	case "adhd.path.list":
		result, respErr = s.handlePathList(req.Params)

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
				slog.Warn("honeypot tool triggered",
					"tool", req.Method,
					"caller_ip", callerIP,
					"params", req.Params,
				)
				s.fireSecurity("warn", "honeypot_triggered", map[string]interface{}{
					"tool":      req.Method,
					"caller_ip": callerIP,
				})
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

// handleMCPSSE handles SSE requests
func (s *Server) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	// For now, return a simple heartbeat SSE stream
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send initial heartbeat
	if _, err := fmt.Fprintf(w, "data: {\"type\":\"init\"}\n\n"); err != nil {
		slog.Warn("write SSE init", "error", err)
	}
	flusher.Flush()

	// Block until context is done
	<-r.Context().Done()
}

// handleInitialize returns the initialize response
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
	}
}

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
					"target_url": map[string]interface{}{
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
				"required": []string{"target_url", "feature_id", "nonce"},
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
					"honeypot_tools": map[string]interface{}{
						"type":        "array",
						"description": "Decoy tool descriptors to register. Callers who invoke these are logged as untrusted.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name":        map[string]interface{}{"type": "string"},
								"description": map[string]interface{}{"type": "string"},
							},
						},
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
					"target_isotope": map[string]interface{}{
						"type":        "string",
						"description": "Name of the target isotope to negotiate paths to",
					},
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID to verify at each hop",
					},
				},
				"required": []string{"target_isotope", "feature_id"},
			},
		},
		{
			"name":        "adhd.path.verify",
			"description": "Re-verify all hops in a previously negotiated path by re-issuing fresh rung challenges.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target_isotope": map[string]interface{}{
						"type":        "string",
						"description": "Target isotope whose paths to re-verify",
					},
					"feature_id": map[string]interface{}{
						"type":        "string",
						"description": "Feature ID to use for re-verification challenges",
					},
				},
				"required": []string{"target_isotope", "feature_id"},
			},
		},
		{
			"name":        "adhd.path.list",
			"description": "List all pre-negotiated trust paths, optionally filtered by target isotope.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target_isotope": map[string]interface{}{
						"type":        "string",
						"description": "Filter to a specific target (optional)",
					},
				},
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
			"name":   "adhd",
			"role":   "standalone",
			"status": "ready",
		}
	}

	topology := s.topologyProvider()
	return map[string]interface{}{
		"name":   topology["local_name"],
		"role":   topology["local_role"],
		"status": "ready",
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
	p, err := parseStringParams(params, "target_url", "feature_id", "nonce")
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}
	targetURL := p["target_url"]
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

	return map[string]interface{}{
		"verified":     verified,
		"receipt":      receipt,
		"feature_id":   featureID,
		"nonce":        nonce,
		"peer_isotope": peerIsotope,
	}, nil
}

// handleClusterJoin wires a newly-joined lezz demo cluster into the running watcher.
func (s *Server) handleClusterJoin(params interface{}) (interface{}, *jsonrpcError) {
	p, err := parseStringParams(params, "name")
	if err != nil {
		return nil, &jsonrpcError{Code: -32602, Message: err.Error()}
	}
	// alarm_a and alarm_b are optional — extract without requiring them.
	m, _ := params.(map[string]interface{})
	alarmA, _ := m["alarm_a"].(string)
	alarmB, _ := m["alarm_b"].(string)

	if s.clusterJoinHandler == nil {
		return map[string]interface{}{"accepted": false, "reason": "not configured"}, nil
	}
	if err := s.clusterJoinHandler(p["name"], alarmA, alarmB); err != nil {
		return nil, &jsonrpcError{Code: -32000, Message: "cluster join failed: " + err.Error()}
	}

	// Register per-repo Dependabot tools for any GitHub repos in this cluster.
	var repoCount int
	if reposRaw, ok := m["github_repos"]; ok {
		if reposSlice, ok := reposRaw.([]interface{}); ok {
			for _, r := range reposSlice {
				if slug, ok := r.(string); ok && slug != "" {
					s.registerRepoTool(p["name"], slug)
					repoCount++
				}
			}
		}
	}

	// Register per-project MCP proxy tools for projects that expose an MCP server.
	var projectCount int
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
				if pjName != "" && pjMCP != "" {
					s.registerProjectTool(p["name"], pjName, pjRepo, pjMCP)
					projectCount++
				}
			}
		}
	}

	// Track this cluster peer for path negotiation.
	adhdMCP, _ := m["adhd_mcp"].(string)
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
	var honeypotCount int
	if hpRaw, ok := m["honeypot_tools"]; ok {
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
				s.registerHoneypotTool(p["name"], hpName, hpDesc)
				honeypotCount++
			}
		}
	}

	slog.Info("adhd.cluster.join accepted",
		"name", p["name"],
		"alarm_a", alarmA,
		"alarm_b", alarmB,
		"repos", repoCount,
		"projects", projectCount,
		"honeypots", honeypotCount,
	)
	return map[string]interface{}{
		"accepted":            true,
		"name":                p["name"],
		"repos_registered":    repoCount,
		"projects_registered": projectCount,
		"honeypots_set":       honeypotCount,
	}, nil
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
// Duplicate registrations are silently ignored.
func (s *Server) registerRepoTool(clusterName, slug string) {
	safeName := strings.ReplaceAll(slug, "/", ".")
	toolName := "adhd.dependabot." + safeName

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == toolName {
			return
		}
	}
	capturedSlug := slug
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        toolName,
		description: fmt.Sprintf("Fetch Dependabot alerts for %s (cluster: %s). Requires gh CLI.", slug, clusterName),
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
	})
	slog.Info("registered per-repo tool", "tool", toolName, "cluster", clusterName)
}

// registerHoneypotTool registers a decoy tool that looks legitimate but logs any caller
// that invokes it. The fakeResult is returned to avoid revealing the trap.
func (s *Server) registerHoneypotTool(clusterName, name, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == name {
			return
		}
	}
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        name,
		description: description,
		honeypot:    true,
		fakeResult:  map[string]interface{}{"ok": true},
		inputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})
	slog.Info("registered honeypot tool", "tool", name, "cluster", clusterName)
}

// RegisterHoneypotTool is the exported version for external use (e.g. tests or demo setup).
func (s *Server) RegisterHoneypotTool(name, description string, fakeResult interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == name {
			return
		}
	}
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        name,
		description: description,
		honeypot:    true,
		fakeResult:  fakeResult,
		inputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})
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
	targetIsotope, _ := m["target_isotope"].(string)
	if targetIsotope == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "target_isotope is required"}
	}
	featureID, _ := m["feature_id"].(string)
	if featureID == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "feature_id is required"}
	}

	s.mu.RLock()
	peers := make(map[string]*clusterPeerInfo, len(s.clusterPeers))
	for k, v := range s.clusterPeers {
		peers[k] = v
	}
	s.mu.RUnlock()

	var paths []*NegotiatedPath

	for _, peer := range peers {
		if peer.Endpoint == "" {
			continue
		}

		if peer.Name == targetIsotope {
			// Direct 1-hop path: challenge the target peer directly.
			nonce := generateNonce()
			res, err := s.callPeerMCP(ctx, peer.Endpoint, "adhd.rung.challenge", map[string]interface{}{
				"target_url": peer.Endpoint,
				"feature_id": featureID,
				"nonce":      nonce,
			})
			if err != nil {
				slog.Debug("path negotiate: direct challenge failed", "peer", peer.Name, "error", err)
				s.fireSecurity("warn", "rung_challenge_failed", map[string]interface{}{
					"peer":       peer.Name,
					"endpoint":   peer.Endpoint,
					"feature_id": featureID,
					"error":      err.Error(),
				})
				continue
			}
			verified, _ := res["verified"].(bool)
			if !verified {
				s.fireSecurity("warn", "rung_challenge_failed", map[string]interface{}{
					"peer":       peer.Name,
					"endpoint":   peer.Endpoint,
					"feature_id": featureID,
					"reason":     "verified=false",
				})
				continue
			}
			receipt, _ := res["receipt"].(string)
			rung := peerRungFromResult(res)
			paths = append(paths, &NegotiatedPath{
				Target:    targetIsotope,
				FeatureID: featureID,
				Hops: []PathHop{{
					Isotope:  peer.Name,
					Endpoint: peer.Endpoint,
					Rung:     rung,
					Nonce:    nonce,
					Receipt:  receipt,
				}},
				MinRung:      rung,
				NegotiatedAt: time.Now(),
			})
			continue
		}

		// 2-hop path: ask this intermediary peer for its own peer list,
		// then challenge it as hop-1 and ask it to challenge the target as hop-2.
		interPeers, err := s.callPeerMCP(ctx, peer.Endpoint, "adhd.isotope.peers", nil)
		if err != nil {
			continue
		}
		targetEndpoint := findEndpointInPeers(interPeers, targetIsotope)
		if targetEndpoint == "" {
			continue
		}

		// Challenge hop-1 (the intermediary).
		nonce1 := generateNonce()
		res1, err := s.callPeerMCP(ctx, peer.Endpoint, "adhd.rung.challenge", map[string]interface{}{
			"target_url": peer.Endpoint,
			"feature_id": featureID,
			"nonce":      nonce1,
		})
		if err != nil || !isVerified(res1) {
			continue
		}

		// Ask hop-1 to challenge hop-2 (the target) on our behalf.
		nonce2 := generateNonce()
		res2, err := s.callPeerMCP(ctx, peer.Endpoint, "adhd.rung.challenge", map[string]interface{}{
			"target_url": targetEndpoint,
			"feature_id": featureID,
			"nonce":      nonce2,
		})
		if err != nil || !isVerified(res2) {
			continue
		}

		rung1 := peerRungFromResult(res1)
		rung2 := peerRungFromResult(res2)
		minRung := rung1
		if rung2 < minRung {
			minRung = rung2
		}

		paths = append(paths, &NegotiatedPath{
			Target:    targetIsotope,
			FeatureID: featureID,
			Hops: []PathHop{
				{Isotope: peer.Name, Endpoint: peer.Endpoint, Rung: rung1, Nonce: nonce1, Receipt: safeReceipt(res1)},
				{Isotope: targetIsotope, Endpoint: targetEndpoint, Rung: rung2, Nonce: nonce2, Receipt: safeReceipt(res2)},
			},
			MinRung:      minRung,
			NegotiatedAt: time.Now(),
		})
	}

	// Sort by min_rung descending (highest trust first).
	sort.Slice(paths, func(i, j int) bool {
		return paths[i].MinRung > paths[j].MinRung
	})

	// Store for later retrieval and verification.
	s.mu.Lock()
	s.negotiatedPaths[targetIsotope] = paths
	s.mu.Unlock()

	// Encode paths as base64 tokens for transport.
	result := make([]interface{}, 0, len(paths))
	for _, p := range paths {
		data, _ := json.Marshal(p)
		result = append(result, map[string]interface{}{
			"target":        p.Target,
			"min_rung":      p.MinRung,
			"hops":          len(p.Hops),
			"feature_id":    p.FeatureID,
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

// handlePathVerify re-challenges each hop of all stored paths to the target,
// confirming they remain alive and trusted with fresh nonces.
func (s *Server) handlePathVerify(ctx context.Context, params interface{}) (interface{}, *jsonrpcError) {
	m, ok := params.(map[string]interface{})
	if !ok {
		return nil, &jsonrpcError{Code: -32602, Message: "params must be an object"}
	}
	targetIsotope, _ := m["target_isotope"].(string)
	if targetIsotope == "" {
		return nil, &jsonrpcError{Code: -32602, Message: "target_isotope is required"}
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

	results := make([]interface{}, 0, len(paths))
	for _, path := range paths {
		allValid := true
		hopResults := make([]interface{}, 0, len(path.Hops))
		for _, hop := range path.Hops {
			nonce := generateNonce()
			res, err := s.callPeerMCP(ctx, hop.Endpoint, "adhd.rung.challenge", map[string]interface{}{
				"target_url": hop.Endpoint,
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
		"target":  targetIsotope,
		"paths":   results,
	}, nil
}

// handlePathList returns pre-negotiated paths, optionally filtered by target.
func (s *Server) handlePathList(params interface{}) (interface{}, *jsonrpcError) {
	var filterTarget string
	if m, ok := params.(map[string]interface{}); ok {
		filterTarget, _ = m["target_isotope"].(string)
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

// findEndpointInPeers searches an adhd.isotope.peers result for a named isotope's endpoint.
func findEndpointInPeers(topology map[string]interface{}, name string) string {
	peers, _ := topology["peers"].([]interface{})
	for _, p := range peers {
		pm, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if pm["name"] == name {
			endpoint, _ := pm["endpoint"].(string)
			return endpoint
		}
	}
	return ""
}

// registerProjectTool registers an adhd.project.<name>.call dynamic tool that
// proxies arbitrary JSON-RPC calls to the project's own MCP server.
// Duplicate registrations (same tool name) are silently ignored.
func (s *Server) registerProjectTool(clusterName, projectName, repo, mcpURL string) {
	toolName := "adhd.project." + projectName + ".call"

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.dynamicTools {
		if t.name == toolName {
			return
		}
	}
	capturedURL := mcpURL
	capturedProject := projectName
	desc := fmt.Sprintf("Proxy JSON-RPC call to the %s MCP server (cluster: %s)", projectName, clusterName)
	if repo != "" {
		desc = fmt.Sprintf("Proxy JSON-RPC call to the %s MCP server — %s (cluster: %s)", projectName, repo, clusterName)
	}
	s.dynamicTools = append(s.dynamicTools, &dynamicTool{
		name:        toolName,
		description: desc,
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
	})
	slog.Info("registered per-project proxy tool", "tool", toolName, "mcp_url", mcpURL, "cluster", clusterName)
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
	if len(mcpURL) > 0 && mcpURL[len(mcpURL)-1] != '/' {
		mcpURL += "/mcp"
	} else {
		mcpURL += "mcp"
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

// JSON-RPC types

type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *jsonrpcError `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
