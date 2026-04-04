package health

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/localprobe"
)

// Monitor probes binary endpoints and updates light status
type Monitor struct {
	binaries      map[string]*config.Binary
	lights        *lights.Cluster
	probers       map[string]*localprobe.Prober
	remoteAlarmURL string
	mu            sync.RWMutex
}

// New creates a new health monitor
func New(binaries []config.Binary, cluster *lights.Cluster) *Monitor {
	return NewWithRemoteAlarm(binaries, cluster, "")
}

// NewWithRemoteAlarm creates a monitor that compares against a remote smoke-alarm
func NewWithRemoteAlarm(binaries []config.Binary, cluster *lights.Cluster, remoteAlarmURL string) *Monitor {
	m := &Monitor{
		binaries:       make(map[string]*config.Binary),
		lights:         cluster,
		probers:        make(map[string]*localprobe.Prober),
		remoteAlarmURL: remoteAlarmURL,
	}

	// Index binaries by name and create probers
	for i := range binaries {
		bin := &binaries[i]
		m.binaries[bin.Name] = bin

		// Create prober for this binary
		prober := localprobe.New(bin, remoteAlarmURL)
		m.probers[bin.Name] = prober
	}

	return m
}

// Start begins monitoring all binary endpoints
func (m *Monitor) Start(ctx context.Context) {
	m.mu.RLock()
	binaries := make([]*config.Binary, 0, len(m.binaries))
	for _, b := range m.binaries {
		binaries = append(binaries, b)
	}
	m.mu.RUnlock()

	for _, bin := range binaries {
		go m.monitorBinary(ctx, bin)
	}
}

// monitorBinary probes a single binary endpoint on a schedule
func (m *Monitor) monitorBinary(ctx context.Context, bin *config.Binary) {
	interval := bin.Interval
	if interval == 0 {
		interval = 10 * time.Second
	}

	timeout := bin.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Probe immediately
	m.probeEndpoint(ctx, bin, timeout)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probeEndpoint(ctx, bin, timeout)
		}
	}
}

// probeEndpoint checks a binary endpoint and compares local vs remote
func (m *Monitor) probeEndpoint(ctx context.Context, bin *config.Binary, timeout time.Duration) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	m.mu.RLock()
	prober := m.probers[bin.Name]
	m.mu.RUnlock()

	// Run local probe and compare against remote smoke-alarm
	probeResult := prober.Probe(probeCtx)

	// Update all features for this binary
	for _, feature := range bin.Features {
		lightName := feature.Name
		existing := m.lights.GetByName(lightName)

		if existing == nil {
			// Create new light
			existing = lights.New(lightName, "feature")
			existing.Source = bin.Name
			existing.SourceMeta = map[string]string{
				"binary":   bin.Name,
				"endpoint": bin.Endpoint,
			}
			m.lights.Add(existing)
		}

		// Update status based on consensus between local and remote
		prober.UpdateLight(existing, probeResult)

		slog.Debug("probed endpoint",
			"binary", bin.Name,
			"feature", lightName,
			"status", existing.GetStatus(),
			"consensus", probeResult.Consensus,
		)
	}
}
