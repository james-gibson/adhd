package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AuthConfig specifies how to authenticate with the target endpoint
type AuthConfig struct {
	Type  string `json:"type"`  // "bearer", "api-key", "oauth2"
	Token string `json:"token"` // token or API key value
	Header string `json:"header,omitempty"` // custom header name (for api-key type)
}

// ProxyRequest represents a request to proxy through to an MCP endpoint
type ProxyRequest struct {
	TargetEndpoint string                 `json:"target_endpoint"`
	Auth           *AuthConfig            `json:"auth,omitempty"`
	Call           map[string]interface{} `json:"call"` // The MCP JSON-RPC call to forward
}

// ProxyResponse wraps the result from the proxied call
type ProxyResponse struct {
	Result interface{} `json:"result,omitempty"`
	Error  *ProxyError `json:"error,omitempty"`
}

// ProxyError represents an error from the proxy operation
type ProxyError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// Executor handles proxy execution
type Executor struct {
	httpClient *http.Client
}

// NewExecutor creates a new proxy executor
func NewExecutor() *Executor {
	return &Executor{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ExecuteProxy forwards an MCP call through to a target endpoint with optional authentication
func (e *Executor) ExecuteProxy(ctx context.Context, req ProxyRequest) (*ProxyResponse, error) {
	// Validate request
	if req.TargetEndpoint == "" {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32602,
				Message: "target_endpoint is required",
			},
		}, nil
	}

	if req.Call == nil {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32602,
				Message: "call is required",
			},
		}, nil
	}

	// Ensure call has jsonrpc and id fields
	if _, hasJsonRPC := req.Call["jsonrpc"]; !hasJsonRPC {
		req.Call["jsonrpc"] = "2.0"
	}
	if _, hasID := req.Call["id"]; !hasID {
		req.Call["id"] = 1
	}

	// Serialize the call to forward
	callBytes, err := json.Marshal(req.Call)
	if err != nil {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32603,
				Message: "Failed to serialize call",
				Details: err.Error(),
			},
		}, nil
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", req.TargetEndpoint, bytes.NewReader(callBytes))
	if err != nil {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32603,
				Message: "Failed to create HTTP request",
				Details: err.Error(),
			},
		}, nil
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")

	// Add authentication if provided
	if req.Auth != nil {
		if err := e.addAuth(httpReq, *req.Auth); err != nil {
			return &ProxyResponse{
				Error: &ProxyError{
					Code:    -32603,
					Message: "Failed to add authentication",
					Details: err.Error(),
				},
			}, nil
		}
	}

	// Execute the proxied request
	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32603,
				Message: "Failed to reach target endpoint",
				Details: err.Error(),
			},
		}, nil
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32603,
				Message: "Failed to read response",
				Details: err.Error(),
			},
		}, nil
	}

	// If response is not 200, include HTTP status in error
	if resp.StatusCode != http.StatusOK {
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    -32603,
				Message: fmt.Sprintf("Target returned HTTP %d", resp.StatusCode),
				Details: string(respBody),
			},
		}, nil
	}

	// Try to parse as JSON-RPC response
	var jsonRPCResp map[string]interface{}
	if err := json.Unmarshal(respBody, &jsonRPCResp); err != nil {
		// Not JSON-RPC, return raw response
		return &ProxyResponse{
			Result: string(respBody),
		}, nil
	}

	// Return the JSON-RPC response
	return &ProxyResponse{
		Result: jsonRPCResp,
	}, nil
}

// addAuth adds authentication headers to the request based on auth config
func (e *Executor) addAuth(req *http.Request, auth AuthConfig) error {
	switch auth.Type {
	case "bearer":
		// Bearer token in Authorization header
		if auth.Token == "" {
			return fmt.Errorf("bearer token is empty")
		}
		req.Header.Set("Authorization", "Bearer "+auth.Token)

	case "api-key":
		// API key in custom header
		if auth.Token == "" {
			return fmt.Errorf("api-key token is empty")
		}
		headerName := auth.Header
		if headerName == "" {
			headerName = "X-API-Key" // default
		}
		req.Header.Set(headerName, auth.Token)

	case "oauth2":
		// OAuth2 token (similar to bearer for now)
		// In future, this could handle token refresh, scopes, etc.
		if auth.Token == "" {
			return fmt.Errorf("oauth2 token is empty")
		}
		req.Header.Set("Authorization", "Bearer "+auth.Token)

	case "":
		// No auth specified, that's fine
		return nil

	default:
		return fmt.Errorf("unsupported auth type: %s", auth.Type)
	}

	return nil
}
