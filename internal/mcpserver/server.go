package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// TopologyProvider is a function that returns current topology info
type TopologyProvider func() map[string]interface{}

// PushLogsHandler receives log entries pushed by prime-plus isotopes
type PushLogsHandler func(logs []map[string]interface{}) error

// Server hosts an MCP-compatible endpoint exposing dashboard lights
type Server struct {
	config           config.MCPServerConfig
	cluster          *lights.Cluster
	topologyProvider TopologyProvider
	pushLogsHandler  PushLogsHandler
	middleware       func(http.Handler) http.Handler
	server           *http.Server
}

// NewServer creates an MCP server
func NewServer(cfg config.MCPServerConfig, cluster *lights.Cluster) *Server {
	return &Server{
		config:  cfg,
		cluster: cluster,
	}
}

// SetTopologyProvider sets the callback for topology queries
func (s *Server) SetTopologyProvider(provider TopologyProvider) {
	s.topologyProvider = provider
}

// SetPushLogsHandler registers the handler for incoming push-logs from prime-plus isotopes
func (s *Server) SetPushLogsHandler(h PushLogsHandler) {
	s.pushLogsHandler = h
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
