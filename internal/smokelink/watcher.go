package smokelink

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// RemoteFeature is a Gherkin feature reported by a remote smoke-alarm.
// The smoke-alarm exposes these via GET /features; ADHD displays them as
// feature lights sourced from that alarm.
type RemoteFeature struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Tags        []string `json:"tags,omitempty"`
	Scenarios   int      `json:"scenarios"`
	Status      string   `json:"status"` // "certified", "unclaimed", "failed"
	CertifiedAt string   `json:"certified_at,omitempty"`
}

// LightUpdate represents a light state change from smoke-alarm
type LightUpdate struct {
	SourceName string        // smoke-alarm instance name
	TargetID   string        // target ID within that instance
	Name       string        // human-readable name
	Status     lights.Status // green, red, yellow, dark
	Details    string
	Latency    time.Duration
	Source     string // "poll" or "sse"
	// IsInstance is true when this update describes the smoke-alarm instance
	// itself (reachable/unreachable) rather than a target within it.
	IsInstance bool
	// RemoteFeatures is non-nil when this update carries a batch of feature
	// certifications from the alarm's /features endpoint rather than a target
	// health update. TargetID and Status are not meaningful in this case.
	RemoteFeatures []RemoteFeature
}

// Watcher polls and subscribes to smoke-alarm instances
type Watcher struct {
	endpoints []config.SmokeAlarmEndpoint
	client    *http.Client
	mu        sync.Mutex
	lastState map[string]lastTargetStatus // keyed by endpoint:targetID; guarded by mu
}

type lastTargetStatus struct {
	status lights.Status
	seen   time.Time
}

// StatusResponse mimics ocd-smoke-alarm's status endpoint response
type StatusResponse struct {
	Service string         `json:"service"`
	Live    bool           `json:"live"`
	Ready   bool           `json:"ready"`
	Targets []TargetStatus `json:"targets"`
	Summary StatusSummary  `json:"summary"`
}

// TargetStatus mirrors ocd-smoke-alarm's /status JSON target format
type TargetStatus struct {
	ID         string `json:"id"`
	Protocol   string `json:"protocol,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	State      string `json:"state"` // healthy, degraded, unhealthy, outage, regression, unknown
	Severity   string `json:"severity,omitempty"`
	Message    string `json:"message,omitempty"`
	Regression bool   `json:"regression"`
	CheckedAt  string `json:"checked_at"` // RFC3339 timestamp
	LatencyMs  int    `json:"latency_ms"`
}

type StatusSummary struct {
	Total     int `json:"total"`
	Healthy   int `json:"healthy"`
	Degraded  int `json:"degraded"`
	Unhealthy int `json:"unhealthy"`
	Outage    int `json:"outage"`
	Unknown   int `json:"unknown"`
}

// NewWatcher creates a smoke-alarm watcher
func NewWatcher(endpoints []config.SmokeAlarmEndpoint) *Watcher {
	return &Watcher{
		endpoints: endpoints,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		lastState: make(map[string]lastTargetStatus),
	}
}

// Start begins polling and SSE subscription for all endpoints
// Sends LightUpdate messages to the provided channel
func (w *Watcher) Start(ctx context.Context, updates chan<- LightUpdate) {
	for _, endpoint := range w.endpoints {
		go w.watchEndpoint(ctx, endpoint, updates)
	}
}

// WatchEndpoint starts watching a dynamically added endpoint.
// Safe to call after Start — used to add mDNS-discovered instances at runtime.
func (w *Watcher) WatchEndpoint(ctx context.Context, endpoint config.SmokeAlarmEndpoint, updates chan<- LightUpdate) {
	go w.watchEndpoint(ctx, endpoint, updates)
}

// watchEndpoint monitors a single smoke-alarm instance
func (w *Watcher) watchEndpoint(ctx context.Context, endpoint config.SmokeAlarmEndpoint, updates chan<- LightUpdate) {
	// Determine strategy: SSE if enabled and interval allows, otherwise polling
	if endpoint.UseSSE && endpoint.Interval == 0 {
		w.watchViaSSE(ctx, endpoint, updates)
	} else {
		w.watchViaPolling(ctx, endpoint, updates)
	}
}

// watchViaPolling periodically queries /status
func (w *Watcher) watchViaPolling(ctx context.Context, endpoint config.SmokeAlarmEndpoint, updates chan<- LightUpdate) {
	// Jitter: spread initial polls uniformly across the interval so a cluster
	// of watchers starting together doesn't fire in a synchronized burst.
	jitter := time.Duration(rand.Int64N(int64(endpoint.Interval)))
	slog.Debug("starting smoke-alarm polling", "endpoint", endpoint.Name, "interval", endpoint.Interval, "jitter", jitter)
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	ticker := time.NewTicker(endpoint.Interval)
	defer ticker.Stop()

	// Poll immediately after jitter so the boot sequence gets real state
	// without waiting a full interval.
	w.pollOnce(ctx, endpoint, updates)

	for {
		select {
		case <-ctx.Done():
			slog.Debug("stopping smoke-alarm polling", "endpoint", endpoint.Name)
			return
		case <-ticker.C:
			w.pollOnce(ctx, endpoint, updates)
		}
	}
}

// pollOnce fetches status once from /status endpoint
func (w *Watcher) pollOnce(ctx context.Context, endpoint config.SmokeAlarmEndpoint, updates chan<- LightUpdate) {
	baseURL := strings.TrimSuffix(endpoint.Endpoint, "/status")
	statusURL := baseURL + "/status"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		slog.Warn("failed to create request", "endpoint", endpoint.Name, "error", err)
		return
	}

	resp, err := w.client.Do(req)
	if err != nil {
		slog.Debug("failed to poll status", "endpoint", endpoint.Name, "error", err)
		select {
		case updates <- LightUpdate{
			SourceName: endpoint.Name,
			Status:     lights.StatusRed,
			Details:    "unreachable",
			Source:     "poll",
			IsInstance: true,
		}:
		default:
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		slog.Warn("failed to parse status response", "endpoint", endpoint.Name, "error", err)
		return
	}

	// After a successful /status poll, also fetch remote feature certifications.
	// Failures are silent — not all smoke-alarms expose /features yet.
	w.pollFeatures(ctx, endpoint, updates)

	// Emit updates for each target that changed
	for _, target := range status.Targets {
		key := fmt.Sprintf("%s:%s", endpoint.Name, target.ID)
		newStatus := mapHealthState(target.State)

		w.mu.Lock()
		last, seen := w.lastState[key]
		changed := !seen || last.status != newStatus
		if changed {
			w.lastState[key] = lastTargetStatus{status: newStatus, seen: time.Now()}
		}
		w.mu.Unlock()

		if changed {
			updates <- LightUpdate{
				SourceName: endpoint.Name,
				TargetID:   target.ID,
				Name:       target.Endpoint, // use endpoint as display name
				Status:     newStatus,
				Details:    target.Message,
				Latency:    time.Duration(target.LatencyMs) * time.Millisecond,
				Source:     "poll",
			}
		}
	}

	// Always emit a heartbeat instance update so the Bubble Tea waitForLightUpdate
	// command is re-armed on every poll cycle, even when all target statuses are
	// stable (no changed==true above). Without this, the dashboard's light update
	// pipeline goes silent once targets stop changing state.
	select {
	case updates <- LightUpdate{
		SourceName: endpoint.Name,
		Status:     lights.StatusGreen,
		Source:     "poll",
		IsInstance: true,
	}:
	default:
	}
}

// pollFeatures fetches GET /features from the smoke-alarm and, if the endpoint
// exists, sends a single LightUpdate carrying the full RemoteFeatures list.
// 404 responses are silently ignored — older alarms don't expose /features.
func (w *Watcher) pollFeatures(ctx context.Context, endpoint config.SmokeAlarmEndpoint, updates chan<- LightUpdate) {
	baseURL := strings.TrimSuffix(endpoint.Endpoint, "/features")
	featURL := baseURL + "/features"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, featURL, nil)
	if err != nil {
		return
	}
	resp, err := w.client.Do(req)
	if err != nil || resp.StatusCode == http.StatusNotFound {
		if resp != nil {
			_ = resp.Body.Close()
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var features []RemoteFeature
	if err := json.NewDecoder(resp.Body).Decode(&features); err != nil || len(features) == 0 {
		return
	}
	select {
	case updates <- LightUpdate{
		SourceName:     endpoint.Name,
		Source:         "poll",
		RemoteFeatures: features,
	}:
	default:
	}
}

// watchViaSSE subscribes to SSE stream from smoke-alarm
func (w *Watcher) watchViaSSE(ctx context.Context, endpoint config.SmokeAlarmEndpoint, updates chan<- LightUpdate) {
	baseURL := strings.TrimSuffix(endpoint.Endpoint, "/status")
	statusURL := baseURL + "/status"

	slog.Debug("starting SSE subscription", "endpoint", endpoint.Name, "url", statusURL)

	for {
		select {
		case <-ctx.Done():
			slog.Debug("stopping SSE subscription", "endpoint", endpoint.Name)
			return
		default:
		}

		w.sseSubscribe(ctx, statusURL, endpoint.Name, updates)

		// Backoff on SSE disconnect (1-5 seconds)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

// sseSubscribe handles a single SSE connection until it closes
func (w *Watcher) sseSubscribe(ctx context.Context, url string, endpointName string, updates chan<- LightUpdate) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("failed to create SSE request", "endpoint", endpointName, "error", err)
		return
	}

	req.Header.Set("Accept", "text/event-stream")

	resp, err := w.client.Do(req)
	if err != nil {
		slog.Debug("SSE connection failed", "endpoint", endpointName, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("SSE request returned non-OK status", "endpoint", endpointName, "status", resp.StatusCode)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() && ctx.Err() == nil {
		line := scanner.Text()

		// SSE: "data: " prefix
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		eventData := strings.TrimPrefix(line, "data: ")
		var update LightUpdate
		if err := json.Unmarshal([]byte(eventData), &update); err != nil {
			slog.Debug("failed to parse SSE event", "endpoint", endpointName, "error", err)
			continue
		}

		update.SourceName = endpointName
		update.Source = "sse"

		// Track state change
		// Note: SSE events are already LightUpdate, so update.TargetID is pre-populated
		key := fmt.Sprintf("%s:%s", endpointName, update.TargetID)
		w.mu.Lock()
		last, seen := w.lastState[key]
		changed := !seen || last.status != update.Status
		if changed {
			w.lastState[key] = lastTargetStatus{status: update.Status, seen: time.Now()}
		}
		w.mu.Unlock()

		if changed {
			updates <- update
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Debug("SSE scanner error", "endpoint", endpointName, "error", err)
	}
}

// mapHealthState converts ocd-smoke-alarm's HealthState to lights.Status
func mapHealthState(state string) lights.Status {
	switch strings.ToLower(state) {
	case "healthy":
		return lights.StatusGreen
	case "degraded":
		return lights.StatusYellow
	case "regression", "unhealthy", "outage":
		return lights.StatusRed
	default:
		return lights.StatusDark
	}
}

// Snapshot returns a copy of the last known state of all watched targets
func (w *Watcher) Snapshot() map[string]lastTargetStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]lastTargetStatus, len(w.lastState))
	for k, v := range w.lastState {
		out[k] = v
	}
	return out
}
