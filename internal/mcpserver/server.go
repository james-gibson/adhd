package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
}

// NewServer creates an MCP server
func NewServer(cfg config.MCPServerConfig, cluster *lights.Cluster) *Server {
	return &Server{
		config:  cfg,
		cluster: cluster,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
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

	default:
		respErr = &jsonrpcError{
			Code:    -32601,
			Message: fmt.Sprintf("Method not found: %s", req.Method),
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

// handleToolsList returns available tools
func (s *Server) handleToolsList() interface{} {
	return map[string]interface{}{
		"tools": []map[string]interface{}{
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
				"description": "Get list of discovered peer ADHD isotopes",
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
							"description": "Base URL of the peer MCP server (e.g. http://localhost:9001)",
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
					},
					"required": []string{"name"},
				},
			},
		},
	}
}

// handleStatus returns dashboard summary
func (s *Server) handleStatus() interface{} {
	allLights := s.cluster.All()

	summary := map[string]int{
		"total":  s.cluster.Count(),
		"green":  s.cluster.CountByStatus(lights.StatusGreen),
		"red":    s.cluster.CountByStatus(lights.StatusRed),
		"yellow": s.cluster.CountByStatus(lights.StatusYellow),
		"dark":   s.cluster.CountByStatus(lights.StatusDark),
	}

	return map[string]interface{}{
		"summary": summary,
		"lights":  len(allLights),
	}
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

// handleIsotopePeers returns discovered peer ADHD isotopes
func (s *Server) handleIsotopePeers() interface{} {
	if s.topologyProvider == nil {
		return map[string]interface{}{
			"peers": []interface{}{},
		}
	}

	topology := s.topologyProvider()
	peers, ok := topology["peers"].([]interface{})
	if !ok {
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
	slog.Info("adhd.cluster.join accepted", "name", p["name"], "alarm_a", alarmA, "alarm_b", alarmB)
	return map[string]interface{}{"accepted": true, "name": p["name"]}, nil
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
