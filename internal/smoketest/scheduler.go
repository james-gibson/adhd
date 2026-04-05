package smoketest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/proxy"
)

// CertifiedEndpoint represents an endpoint that has been certified
type CertifiedEndpoint struct {
	ID        string
	URL       string
	AuthType  string // "bearer", "api-key", "oauth2", "none"
	Token     string
	Header    string // custom header name for API-Key
	TestFreq  time.Duration
	LastTest  time.Time
	CertLevel int // 0-100, where 100 is fully certified
}

// ScheduleEvent represents a state change in certification
type ScheduleEvent struct {
	Type      string // "downgrade", "upgrade", "test_pass", "test_fail"
	EndpointID string
	Timestamp time.Time
	Message   string
	CertLevel int
}

// Scheduler manages periodic testing of certified endpoints
type Scheduler struct {
	runner          *Runner
	endpoints       map[string]*CertifiedEndpoint // keyed by ID
	mu              sync.RWMutex
	events          chan ScheduleEvent
	testInterval    time.Duration // base interval between tests
	staggerInterval time.Duration // stagger between endpoint tests
	running         bool
	ctx             context.Context
	cancel          context.CancelFunc
}

// NewScheduler creates a new test scheduler
func NewScheduler(runner *Runner) *Scheduler {
	return &Scheduler{
		runner:          runner,
		endpoints:       make(map[string]*CertifiedEndpoint),
		events:          make(chan ScheduleEvent, 100),
		testInterval:    5 * time.Minute,  // test each endpoint every 5 minutes
		staggerInterval: 30 * time.Second, // stagger tests 30 seconds apart
	}
}

// RegisterEndpoint adds an endpoint to test schedule
func (s *Scheduler) RegisterEndpoint(ep *CertifiedEndpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ep.TestFreq == 0 {
		ep.TestFreq = s.testInterval
	}
	s.endpoints[ep.ID] = ep

	slog.Info("registered endpoint for smoke testing",
		"endpoint_id", ep.ID,
		"url", ep.URL,
		"test_freq", ep.TestFreq,
	)
}

// UnregisterEndpoint removes an endpoint from test schedule
func (s *Scheduler) UnregisterEndpoint(endpointID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.endpoints, endpointID)

	slog.Info("unregistered endpoint from smoke testing", "endpoint_id", endpointID)
}

// Start begins the test schedule
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	slog.Info("starting smoke test scheduler")

	// Monitor test results and handle state changes
	go s.monitorResults()

	// Schedule tests
	go s.scheduleTests()
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	s.cancel()
	slog.Info("stopping smoke test scheduler")
}

// scheduleTests orchestrates the test schedule
func (s *Scheduler) scheduleTests() {
	// Test immediately on startup
	s.testAll()

	ticker := time.NewTicker(s.testInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.testAll()
		}
	}
}

// testAll runs tests for all endpoints with staggering
func (s *Scheduler) testAll() {
	s.mu.RLock()
	endpointCount := len(s.endpoints)
	endpoints := make([]*CertifiedEndpoint, 0, endpointCount)
	for _, ep := range s.endpoints {
		endpoints = append(endpoints, ep)
	}
	s.mu.RUnlock()

	if endpointCount == 0 {
		return
	}

	slog.Debug("starting smoke test batch", "count", endpointCount)

	// Stagger test starts to avoid thundering herd
	for i, ep := range endpoints {
		go func(ep *CertifiedEndpoint, index int) {
			// Stagger the start
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(time.Duration(index) * s.staggerInterval):
			}

			s.testEndpoint(ep)
		}(ep, i)
	}
}

// testEndpoint runs a single test and handles state changes
func (s *Scheduler) testEndpoint(ep *CertifiedEndpoint) {
	// Build auth config
	var authConfig *proxy.AuthConfig
	if ep.AuthType != "" && ep.AuthType != "none" {
		authConfig = &proxy.AuthConfig{
			Type:   ep.AuthType,
			Token:  ep.Token,
			Header: ep.Header,
		}
	}

	// Run test
	result := s.runner.TestEndpoint(s.ctx, ep.ID, ep.URL, authConfig)

	// Update endpoint record
	s.mu.Lock()
	ep.LastTest = result.TestTime
	s.mu.Unlock()

	// Check for certification state change
	oldCertLevel := ep.CertLevel
	newCertLevel := s.calculateCertLevel(ep.ID)

	if newCertLevel != oldCertLevel {
		s.mu.Lock()
		ep.CertLevel = newCertLevel
		s.mu.Unlock()

		if newCertLevel < oldCertLevel {
			// Downgrade
			s.emitEvent(ScheduleEvent{
				Type:       "downgrade",
				EndpointID: ep.ID,
				Timestamp:  result.TestTime,
				Message:    fmt.Sprintf("Downgraded from %d to %d due to test failures", oldCertLevel, newCertLevel),
				CertLevel:  newCertLevel,
			})
		} else {
			// Upgrade
			s.emitEvent(ScheduleEvent{
				Type:       "upgrade",
				EndpointID: ep.ID,
				Timestamp:  result.TestTime,
				Message:    fmt.Sprintf("Upgraded from %d to %d due to test successes", oldCertLevel, newCertLevel),
				CertLevel:  newCertLevel,
			})
		}
	}

	// Emit test result event
	eventType := "test_pass"
	if !result.Passed {
		eventType = "test_fail"
	}

	s.emitEvent(ScheduleEvent{
		Type:       eventType,
		EndpointID: ep.ID,
		Timestamp:  result.TestTime,
		Message:    result.ErrorMessage,
		CertLevel:  newCertLevel,
	})
}

// calculateCertLevel computes current certification level based on metrics
func (s *Scheduler) calculateCertLevel(endpointID string) int {
	metrics := s.runner.GetMetrics(endpointID)
	if metrics == nil {
		return 0
	}

	// Start at 100 (fully certified)
	level := 100

	// Degrade based on consecutive failures
	if metrics.ConsecutiveFailures > 0 {
		failureImpact := metrics.ConsecutiveFailures * 20 // 20% per consecutive failure
		level -= failureImpact
		if level < 0 {
			level = 0
		}
	}

	// Restore based on consecutive successes
	if metrics.ConsecutivePasses > 0 {
		successBoost := metrics.ConsecutivePasses * 10 // 10% per consecutive success
		level += successBoost
		if level > 100 {
			level = 100
		}
	}

	// Factor in uptime over last 7 days (when available)
	if metrics.Uptime > 0 {
		uptimeImpact := int(metrics.Uptime) - 100 // -0 to 100% impact
		level += uptimeImpact / 2                   // soften the impact
		if level < 0 {
			level = 0
		}
		if level > 100 {
			level = 100
		}
	}

	return level
}

// monitorResults processes test results from the runner
func (s *Scheduler) monitorResults() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case result := <-s.runner.ResultsChannel():
			// Log for dashboard integration
			slog.Debug("test result recorded",
				"endpoint_id", result.EndpointID,
				"passed", result.Passed,
				"duration", result.Duration,
				"auth_type", result.AuthType,
			)
		}
	}
}

// emitEvent sends an event to the events channel
func (s *Scheduler) emitEvent(event ScheduleEvent) {
	select {
	case s.events <- event:
	default:
		// Channel full, drop event
		slog.Warn("events channel full, dropping event",
			"type", event.Type,
			"endpoint_id", event.EndpointID,
		)
	}
}

// EventsChannel returns the channel for schedule events
func (s *Scheduler) EventsChannel() <-chan ScheduleEvent {
	return s.events
}

// GetEndpoint returns a registered endpoint by ID
func (s *Scheduler) GetEndpoint(endpointID string) *CertifiedEndpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endpoints[endpointID]
}

// ListEndpoints returns all registered endpoints
func (s *Scheduler) ListEndpoints() []*CertifiedEndpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	endpoints := make([]*CertifiedEndpoint, 0, len(s.endpoints))
	for _, ep := range s.endpoints {
		endpoints = append(endpoints, ep)
	}
	return endpoints
}

// Snapshot returns status snapshot of all endpoints
func (s *Scheduler) Snapshot() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := map[string]interface{}{
		"timestamp":  time.Now(),
		"endpoints":  len(s.endpoints),
		"running":    s.running,
		"test_interval": s.testInterval.String(),
		"endpoints_detail": make([]map[string]interface{}, 0),
	}

	for _, ep := range s.endpoints {
		metrics := s.runner.GetMetrics(ep.ID)
		detail := map[string]interface{}{
			"id":         ep.ID,
			"url":        ep.URL,
			"cert_level": ep.CertLevel,
			"last_test":  ep.LastTest,
		}
		if metrics != nil {
			detail["passes"] = metrics.ConsecutivePasses
			detail["failures"] = metrics.ConsecutiveFailures
			detail["uptime"] = metrics.Uptime
		}
		snapshot["endpoints_detail"] = append(snapshot["endpoints_detail"].([]map[string]interface{}), detail)
	}

	return snapshot
}
