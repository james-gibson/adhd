package smoketest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/james-gibson/adhd/internal/proxy"
)

// TestResult represents the outcome of a single smoke test
type TestResult struct {
	EndpointID    string
	EndpointURL   string
	TestTime      time.Time
	Duration      time.Duration
	Passed        bool
	ErrorCode     int    // JSON-RPC error code if failed
	ErrorMessage  string // Human-readable error message
	AuthType      string // "bearer", "api-key", "oauth2", "none"
	FailureReason string // "timeout", "auth_failed", "bad_response", "network_error", etc.
}

// EndpointMetrics tracks historical performance of an endpoint
type EndpointMetrics struct {
	EndpointID          string
	LastTestedAt        time.Time
	ConsecutivePasses   int
	ConsecutiveFailures int
	LastPassAt          time.Time
	LastFailAt          time.Time
	Uptime              float64 // percentage over last 7 days
	AverageLatency      time.Duration
	FailureHistory      []FailureRecord
}

// FailureRecord tracks a single failure event
type FailureRecord struct {
	Time          time.Time
	ErrorCode     int
	ErrorMessage  string
	FailureReason string
	Duration      time.Duration
}

// Runner executes smoke tests against MCP endpoints
type Runner struct {
	proxyExecutor     *proxy.Executor
	metricsStore      map[string]*EndpointMetrics // keyed by endpointID
	results           chan TestResult
	downgradeThreshold int // consecutive failures before downgrade
	upgradeThreshold   int // consecutive passes before upgrade
}

// NewRunner creates a new smoke test runner
func NewRunner(proxyExecutor *proxy.Executor) *Runner {
	return &Runner{
		proxyExecutor:      proxyExecutor,
		metricsStore:       make(map[string]*EndpointMetrics),
		results:            make(chan TestResult, 100),
		downgradeThreshold: 3, // downgrade after 3 consecutive failures
		upgradeThreshold:   5, // upgrade after 5 consecutive successes
	}
}

// TestEndpoint runs a single smoke test against a certified endpoint
func (r *Runner) TestEndpoint(
	ctx context.Context,
	endpointID string,
	targetURL string,
	authConfig *proxy.AuthConfig,
) TestResult {
	start := time.Now()
	result := TestResult{
		EndpointID:  endpointID,
		EndpointURL: targetURL,
		TestTime:    start,
	}

	if authConfig != nil {
		result.AuthType = authConfig.Type
	}

	// Execute a simple tools/list call via proxy
	proxyReq := proxy.ProxyRequest{
		TargetEndpoint: targetURL,
		Auth:           authConfig,
		Call: map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/list",
		},
	}

	// Create a context with timeout for this test
	testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	proxyResp, err := r.proxyExecutor.ExecuteProxy(testCtx, proxyReq)
	result.Duration = time.Since(start)

	// Check for proxy execution errors
	if err != nil {
		result.Passed = false
		result.ErrorMessage = fmt.Sprintf("proxy execution failed: %v", err)
		result.FailureReason = "proxy_error"
		slog.Warn("smoke test failed",
			"endpoint_id", endpointID,
			"target", targetURL,
			"error", err,
			"duration", result.Duration,
		)
		r.recordTestResult(result)
		return result
	}

	// Check proxy response
	if proxyResp.Error != nil {
		result.Passed = false
		result.ErrorCode = proxyResp.Error.Code
		result.ErrorMessage = proxyResp.Error.Message
		if proxyResp.Error.Details != "" {
			result.ErrorMessage = fmt.Sprintf("%s: %s", result.ErrorMessage, proxyResp.Error.Details)
		}

		// Categorize failure reason
		switch proxyResp.Error.Code {
		case -32002:
			result.FailureReason = "auth_failed"
		case -32003:
			result.FailureReason = "forbidden"
		case -32004:
			result.FailureReason = "not_found"
		case -32007:
			result.FailureReason = "unavailable"
		case -32008:
			result.FailureReason = "rate_limited"
		default:
			result.FailureReason = "proxy_error"
		}

		slog.Warn("smoke test returned error",
			"endpoint_id", endpointID,
			"target", targetURL,
			"error_code", result.ErrorCode,
			"error_message", result.ErrorMessage,
			"failure_reason", result.FailureReason,
			"duration", result.Duration,
		)
		r.recordTestResult(result)
		return result
	}

	// Success!
	result.Passed = true
	slog.Info("smoke test passed",
		"endpoint_id", endpointID,
		"target", targetURL,
		"duration", result.Duration,
	)
	r.recordTestResult(result)
	return result
}

// recordTestResult updates metrics and publishes to results channel
func (r *Runner) recordTestResult(result TestResult) {
	// Update metrics
	metrics, exists := r.metricsStore[result.EndpointID]
	if !exists {
		metrics = &EndpointMetrics{
			EndpointID:     result.EndpointID,
			FailureHistory: make([]FailureRecord, 0),
		}
		r.metricsStore[result.EndpointID] = metrics
	}

	metrics.LastTestedAt = result.TestTime

	if result.Passed {
		metrics.ConsecutivePasses++
		metrics.ConsecutiveFailures = 0
		metrics.LastPassAt = result.TestTime
	} else {
		metrics.ConsecutiveFailures++
		metrics.ConsecutivePasses = 0
		metrics.LastFailAt = result.TestTime

		// Record failure
		metrics.FailureHistory = append(metrics.FailureHistory, FailureRecord{
			Time:          result.TestTime,
			ErrorCode:     result.ErrorCode,
			ErrorMessage:  result.ErrorMessage,
			FailureReason: result.FailureReason,
			Duration:      result.Duration,
		})

		// Keep only last 50 failures
		if len(metrics.FailureHistory) > 50 {
			metrics.FailureHistory = metrics.FailureHistory[len(metrics.FailureHistory)-50:]
		}
	}

	// Send to results channel
	select {
	case r.results <- result:
	default:
		// Channel full, drop result
		slog.Warn("results channel full, dropping test result", "endpoint_id", result.EndpointID)
	}
}

// GetMetrics returns metrics for an endpoint
func (r *Runner) GetMetrics(endpointID string) *EndpointMetrics {
	return r.metricsStore[endpointID]
}

// ShouldDowngrade returns true if endpoint should be downgraded
func (r *Runner) ShouldDowngrade(endpointID string) bool {
	metrics := r.GetMetrics(endpointID)
	if metrics == nil {
		return false
	}
	return metrics.ConsecutiveFailures >= r.downgradeThreshold
}

// ShouldUpgrade returns true if endpoint should be upgraded
func (r *Runner) ShouldUpgrade(endpointID string) bool {
	metrics := r.GetMetrics(endpointID)
	if metrics == nil {
		return false
	}
	return metrics.ConsecutivePasses >= r.upgradeThreshold
}

// ResultsChannel returns the channel for test results
func (r *Runner) ResultsChannel() <-chan TestResult {
	return r.results
}

// Snapshot returns a copy of all endpoint metrics
func (r *Runner) Snapshot() map[string]*EndpointMetrics {
	out := make(map[string]*EndpointMetrics)
	for id, metrics := range r.metricsStore {
		// Make a shallow copy
		m := *metrics
		out[id] = &m
	}
	return out
}
