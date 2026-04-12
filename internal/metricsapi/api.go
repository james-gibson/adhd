package metricsapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/smoketest"
)

// API provides metrics endpoints for smoke test scheduler data
type API struct {
	scheduler *smoketest.Scheduler
	mu        sync.RWMutex
}

// NewAPI creates a new metrics API handler
func NewAPI(scheduler *smoketest.Scheduler) *API {
	return &API{
		scheduler: scheduler,
	}
}

// SetScheduler updates the scheduler reference
func (a *API) SetScheduler(scheduler *smoketest.Scheduler) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scheduler = scheduler
}

// EndpointMetricsResponse represents metrics for a single endpoint
type EndpointMetricsResponse struct {
	ID                  string            `json:"id"`
	URL                 string            `json:"url"`
	CertLevel           int               `json:"cert_level"`
	Status              string            `json:"status"`
	LastTestedAt        string            `json:"last_tested_at"`
	ConsecutivePasses   int               `json:"consecutive_passes"`
	ConsecutiveFailures int               `json:"consecutive_failures"`
	LastPassAt          string            `json:"last_pass_at,omitempty"`
	LastFailAt          string            `json:"last_fail_at,omitempty"`
	Uptime              float64           `json:"uptime"`
	AverageLatency      int               `json:"average_latency_ms"`
	FailureCount        int               `json:"failure_history_count"`
	RecentFailures      []FailureResponse `json:"recent_failures,omitempty"`
}

// FailureResponse represents a single failure record
type FailureResponse struct {
	Time          string `json:"time"`
	ErrorCode     int    `json:"error_code"`
	ErrorMessage  string `json:"error_message"`
	FailureReason string `json:"failure_reason"`
	DurationMs    int    `json:"duration_ms"`
}

// SnapshotResponse represents the status of all endpoints
type SnapshotResponse struct {
	Timestamp  string                       `json:"timestamp"`
	Endpoints  int                          `json:"endpoints"`
	Running    bool                         `json:"running"`
	TestInterval string                    `json:"test_interval"`
	Summary    SnapshotSummary              `json:"summary"`
	Endpoints_ []EndpointMetricsResponse    `json:"endpoints_detail"`
}

// SnapshotSummary provides aggregate statistics
type SnapshotSummary struct {
	FullyCertified int `json:"fully_certified"` // 100%
	Degraded       int `json:"degraded"`        // 40-99%
	AtRisk         int `json:"at_risk"`         // 1-39%
	Revoked        int `json:"revoked"`         // 0%
}

// HandleMetrics returns metrics for a specific endpoint
func (a *API) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	scheduler := a.scheduler
	a.mu.RUnlock()

	if scheduler == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler not initialized")
		return
	}

	// Extract endpoint ID from path: /api/endpoints/{id}/metrics
	endpointID := r.PathValue("id")
	if endpointID == "" {
		respondError(w, http.StatusBadRequest, "missing endpoint id")
		return
	}

	// Get endpoint from scheduler
	endpoint := scheduler.GetEndpoint(endpointID)
	if endpoint == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("endpoint not found: %s", endpointID))
		return
	}

	// Get metrics from runner
	runner := scheduler.GetRunner()
	if runner == nil {
		respondError(w, http.StatusInternalServerError, "runner not available")
		return
	}

	metrics := runner.GetMetrics(endpointID)
	if metrics == nil {
		// Endpoint registered but not yet tested
		resp := EndpointMetricsResponse{
			ID:        endpointID,
			URL:       endpoint.URL,
			CertLevel: endpoint.CertLevel,
			Status:    statusFromLevel(endpoint.CertLevel),
		}
		respondJSON(w, http.StatusOK, resp)
		return
	}

	// Build response
	resp := EndpointMetricsResponse{
		ID:                  endpointID,
		URL:                 endpoint.URL,
		CertLevel:           endpoint.CertLevel,
		Status:              statusFromLevel(endpoint.CertLevel),
		ConsecutivePasses:   metrics.ConsecutivePasses,
		ConsecutiveFailures: metrics.ConsecutiveFailures,
		Uptime:              metrics.Uptime,
		FailureCount:        len(metrics.FailureHistory),
	}

	if !metrics.LastTestedAt.IsZero() {
		resp.LastTestedAt = metrics.LastTestedAt.Format(time.RFC3339)
	}
	if !metrics.LastPassAt.IsZero() {
		resp.LastPassAt = metrics.LastPassAt.Format(time.RFC3339)
	}
	if !metrics.LastFailAt.IsZero() {
		resp.LastFailAt = metrics.LastFailAt.Format(time.RFC3339)
	}
	if metrics.AverageLatency > 0 {
		resp.AverageLatency = int(metrics.AverageLatency.Milliseconds())
	}

	// Include last 5 failures
	if len(metrics.FailureHistory) > 0 {
		start := len(metrics.FailureHistory) - 5
		if start < 0 {
			start = 0
		}
		for _, failure := range metrics.FailureHistory[start:] {
			resp.RecentFailures = append(resp.RecentFailures, FailureResponse{
				Time:          failure.Time.Format(time.RFC3339),
				ErrorCode:     failure.ErrorCode,
				ErrorMessage:  failure.ErrorMessage,
				FailureReason: failure.FailureReason,
				DurationMs:    int(failure.Duration.Milliseconds()),
			})
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// HandleSnapshot returns status of all endpoints
func (a *API) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	scheduler := a.scheduler
	a.mu.RUnlock()

	if scheduler == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler not initialized")
		return
	}

	endpoints := scheduler.ListEndpoints()
	runner := scheduler.GetRunner()

	resp := SnapshotResponse{
		Timestamp:    time.Now().Format(time.RFC3339),
		Endpoints:    len(endpoints),
		Running:      true,
		TestInterval: "5m",
		Summary: SnapshotSummary{
			FullyCertified: 0,
			Degraded:       0,
			AtRisk:         0,
			Revoked:        0,
		},
		Endpoints_: make([]EndpointMetricsResponse, 0, len(endpoints)),
	}

	for _, ep := range endpoints {
		metrics := runner.GetMetrics(ep.ID)
		epResp := EndpointMetricsResponse{
			ID:        ep.ID,
			URL:       ep.URL,
			CertLevel: ep.CertLevel,
			Status:    statusFromLevel(ep.CertLevel),
		}

		if metrics != nil {
			epResp.ConsecutivePasses = metrics.ConsecutivePasses
			epResp.ConsecutiveFailures = metrics.ConsecutiveFailures
			epResp.Uptime = metrics.Uptime
			epResp.FailureCount = len(metrics.FailureHistory)
			if !metrics.LastTestedAt.IsZero() {
				epResp.LastTestedAt = metrics.LastTestedAt.Format(time.RFC3339)
			}
			if metrics.AverageLatency > 0 {
				epResp.AverageLatency = int(metrics.AverageLatency.Milliseconds())
			}
		}

		resp.Endpoints_ = append(resp.Endpoints_, epResp)

		// Update summary
		if ep.CertLevel >= 80 {
			resp.Summary.FullyCertified++
		} else if ep.CertLevel >= 40 {
			resp.Summary.Degraded++
		} else if ep.CertLevel > 0 {
			resp.Summary.AtRisk++
		} else {
			resp.Summary.Revoked++
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// HandleEvents streams real-time scheduler events via SSE
func (a *API) HandleEvents(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	scheduler := a.scheduler
	a.mu.RUnlock()

	if scheduler == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler not initialized")
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Create event channel subscription
	eventsChan := make(chan smoketest.ScheduleEvent, 50)

	// Goroutine to forward events from scheduler to this client
	go func() {
		for event := range scheduler.EventsChannel() {
			select {
			case eventsChan <- event:
			case <-r.Context().Done():
				return
			}
		}
	}()

	// Send ping every 30 seconds to keep connection alive
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			close(eventsChan)
			return

		case event := <-eventsChan:
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", string(data))
			flusher.Flush()
			slog.Debug("SSE event sent", "type", event.Type, "endpoint_id", event.EndpointID)

		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// Helper functions

func statusFromLevel(level int) string {
	if level >= 80 {
		return "certified"
	} else if level >= 40 {
		return "degraded"
	} else if level > 0 {
		return "at_risk"
	}
	return "revoked"
}

func respondJSON(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	
 _ = json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
_=	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":  message,
		"code":   code,
		"status": http.StatusText(code),
	})
}
