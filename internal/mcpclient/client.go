package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// ProbeResult represents the outcome of an MCP probe
type ProbeResult struct {
	Healthy   bool
	ToolCount int
	Tools     []string
	Latency   time.Duration
	Error     string
}

// Client sends JSON-RPC requests to an MCP endpoint
type Client struct {
	endpoint string
	timeout  time.Duration
	client   *http.Client
}

// NewHTTPClient creates an MCP client for HTTP-based endpoints
func NewHTTPClient(endpoint string, timeout time.Duration) *Client {
	return &Client{
		endpoint: endpoint,
		timeout:  timeout,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Probe performs initialize + tools/list against an MCP endpoint
func (c *Client) Probe(ctx context.Context) (ProbeResult, error) {
	start := time.Now()

	// Step 1: initialize
	initResp, err := c.doRPC(ctx, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"clientInfo": map[string]string{
			"name":    "adhd",
			"version": "0.1.0",
		},
	})
	if err != nil {
		latency := time.Since(start)
		return ProbeResult{
			Healthy:   false,
			ToolCount: 0,
			Latency:   latency,
			Error:     fmt.Sprintf("initialize failed: %v", err),
		}, nil
	}

	if err := initResp.Error; err != nil {
		latency := time.Since(start)
		return ProbeResult{
			Healthy:   false,
			ToolCount: 0,
			Latency:   latency,
			Error:     fmt.Sprintf("initialize error: %s", err.Message),
		}, nil
	}

	// Step 2: tools/list
	toolsResp, err := c.doRPC(ctx, "tools/list", nil)
	if err != nil {
		latency := time.Since(start)
		return ProbeResult{
			Healthy:   false,
			ToolCount: 0,
			Latency:   latency,
			Error:     fmt.Sprintf("tools/list failed: %v", err),
		}, nil
	}

	if err := toolsResp.Error; err != nil {
		latency := time.Since(start)
		return ProbeResult{
			Healthy:   false,
			ToolCount: 0,
			Latency:   latency,
			Error:     fmt.Sprintf("tools/list error: %s", err.Message),
		}, nil
	}

	// Parse tools result
	var toolsList struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}

	data, _ := json.Marshal(toolsResp.Result)
	_ = json.Unmarshal(data, &toolsList)

	tools := []string{}
	for _, tool := range toolsList.Tools {
		tools = append(tools, tool.Name)
	}

	latency := time.Since(start)
	return ProbeResult{
		Healthy:   true,
		ToolCount: len(tools),
		Tools:     tools,
		Latency:   latency,
	}, nil
}

// JSONRPC request/response types

type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      int                    `json:"id"`
	Result  interface{}            `json:"result,omitempty"`
	Error   *jsonrpcError          `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Call sends a JSON-RPC request to the MCP endpoint with the given method and params
func (c *Client) Call(ctx context.Context, method string, params interface{}) (*jsonrpcResponse, error) {
	return c.doRPC(ctx, method, params)
}

// doRPC sends a JSON-RPC request and returns the response
func (c *Client) doRPC(ctx context.Context, method string, params interface{}) (*jsonrpcResponse, error) {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", httpResp.StatusCode)
	}

	var resp jsonrpcResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &resp, nil
}

// Prober interface for integration with dashboard
type Prober struct {
	client   *Client
	name     string
	endpoint string
}

// NewProber creates a prober for a named endpoint
func NewProber(name, endpoint string, timeout time.Duration) *Prober {
	return &Prober{
		client:   NewHTTPClient(endpoint, timeout),
		name:     name,
		endpoint: endpoint,
	}
}

// ProbeOnce performs a single health probe
func (p *Prober) ProbeOnce(ctx context.Context) (ProbeResult, error) {
	slog.Debug("probing MCP endpoint", "name", p.name, "endpoint", p.endpoint)
	return p.client.Probe(ctx)
}
