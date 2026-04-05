package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
			slog.Error("failed to add authentication",
				"type", req.Auth.Type,
				"target", req.TargetEndpoint,
				"error", err.Error(),
			)
			return &ProxyResponse{
				Error: &ProxyError{
					Code:    -32603,
					Message: "Failed to add authentication",
					Details: err.Error(),
				},
			}, nil
		}
		slog.Debug("authentication added for proxy call",
			"auth_type", req.Auth.Type,
			"target", req.TargetEndpoint,
		)
	}

	// Execute the proxied request
	slog.Debug("forwarding MCP call via proxy",
		"target", req.TargetEndpoint,
		"method", callMethod(req.Call),
		"has_auth", req.Auth != nil,
	)
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

	// Log response status
	slog.Debug("proxy call completed",
		"target", req.TargetEndpoint,
		"status_code", resp.StatusCode,
	)

	// If response is not 200, categorize the error
	if resp.StatusCode != http.StatusOK {
		errCode, errMsg := categorizeHTTPError(resp.StatusCode, req.Auth)
		slog.Warn("proxy call failed",
			"target", req.TargetEndpoint,
			"status_code", resp.StatusCode,
			"auth_type", authType(req.Auth),
			"error_code", errCode,
		)
		return &ProxyResponse{
			Error: &ProxyError{
				Code:    errCode,
				Message: errMsg,
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

// categorizeHTTPError maps HTTP status codes to meaningful error messages
// Returns (code, message) for JSON-RPC error response
func categorizeHTTPError(statusCode int, auth *AuthConfig) (int, string) {
	switch statusCode {
	case http.StatusUnauthorized: // 401
		if auth != nil {
			switch auth.Type {
			case "bearer":
				return -32002, "Unauthorized: Bearer token rejected by target (invalid, expired, or wrong scope)"
			case "api-key":
				return -32002, "Unauthorized: API key rejected by target (invalid or insufficient permissions)"
			case "oauth2":
				return -32002, "Unauthorized: OAuth2 token rejected by target (expired or invalid scope)"
			default:
				return -32002, "Unauthorized: Authentication failed (HTTP 401)"
			}
		}
		return -32002, "Unauthorized: Target requires authentication (HTTP 401)"

	case http.StatusForbidden: // 403
		return -32003, "Forbidden: Access denied by target (may require additional scopes or permissions)"

	case http.StatusNotFound: // 404
		return -32004, "Not Found: Target endpoint not found (HTTP 404) - check URL"

	case http.StatusBadRequest: // 400
		return -32005, "Bad Request: Target rejected the call format (HTTP 400)"

	case http.StatusInternalServerError: // 500
		return -32006, "Target Internal Error: Server error on target endpoint (HTTP 500)"

	case http.StatusServiceUnavailable: // 503
		return -32007, "Service Unavailable: Target endpoint temporarily unavailable (HTTP 503)"

	case http.StatusTooManyRequests: // 429
		return -32008, "Too Many Requests: Rate limit exceeded on target (HTTP 429)"

	default:
		return -32603, fmt.Sprintf("Target error: HTTP %d", statusCode)
	}
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

// Helper functions for logging

// authType returns the auth type as a string, or "none" if not provided
func authType(auth *AuthConfig) string {
	if auth == nil {
		return "none"
	}
	if auth.Type == "" {
		return "unknown"
	}
	return auth.Type
}

// callMethod extracts the method name from a JSON-RPC call
func callMethod(call map[string]interface{}) string {
	if method, ok := call["method"].(string); ok {
		return method
	}
	return "unknown"
}
