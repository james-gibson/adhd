package localprobe

import (
	"context"
	"log/slog"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpclient"
)

// Prober runs local health checks and compares against remote smoke-alarm
type Prober struct {
	binary       *config.Binary
	localClient  *mcpclient.Client
	remoteAlarm  string // remote smoke-alarm endpoint
}

// ProbeResult combines local and remote health checks
type ProbeResult struct {
	LocalHealthy  bool
	RemoteHealthy bool
	LocalError    string
	RemoteError   string
	Consensus     bool // true if local and remote agree
	Latency       time.Duration
}

// New creates a prober for a binary
func New(binary *config.Binary, remoteAlarmEndpoint string) *Prober {
	return &Prober{
		binary:      binary,
		localClient: mcpclient.NewHTTPClient(binary.Endpoint, binary.Timeout),
		remoteAlarm: remoteAlarmEndpoint,
	}
}

// Probe runs both local and remote checks, returns consensus result
func (p *Prober) Probe(ctx context.Context) ProbeResult {
	start := time.Now()

	// Run local probe
	localResult, _ := p.localClient.Probe(ctx)

	result := ProbeResult{
		LocalHealthy: localResult.Healthy,
		LocalError:   localResult.Error,
		Latency:      time.Since(start),
	}

	// Run remote probe (query remote smoke-alarm for this target)
	if p.remoteAlarm != "" {
		remoteResult := p.probeRemote(ctx, p.binary.Name)
		result.RemoteHealthy = remoteResult
	} else {
		// No remote alarm configured, use local as source of truth
		result.RemoteHealthy = result.LocalHealthy
	}

	// Determine consensus
	result.Consensus = result.LocalHealthy == result.RemoteHealthy

	slog.Debug("probe completed",
		"binary", p.binary.Name,
		"local", result.LocalHealthy,
		"remote", result.RemoteHealthy,
		"consensus", result.Consensus,
	)

	return result
}

// probeRemote queries the remote smoke-alarm for a target's health
func (p *Prober) probeRemote(ctx context.Context, targetName string) bool {
	// Query remote smoke-alarm's /status endpoint for this target
	// For now, assume healthy if we can reach it
	// In the future, parse the actual status response

	remoteClient := mcpclient.NewHTTPClient(p.remoteAlarm, 5*time.Second)
	result, _ := remoteClient.Probe(ctx)
	return result.Healthy
}

// UpdateLight updates a light based on probe results
func (p *Prober) UpdateLight(light *lights.Light, result ProbeResult) {
	// Light status based on consensus:
	// GREEN = local and remote agree it's healthy
	// RED = local and remote agree it's failing
	// YELLOW = local and remote disagree (misconfiguration)

	switch {
	case !result.Consensus:
		light.SetStatus(lights.StatusYellow)
		light.SetDetails("local/remote mismatch (config issue?)")
	case result.LocalHealthy && result.RemoteHealthy:
		light.SetStatus(lights.StatusGreen)
		light.SetDetails("healthy (consensus)")
	case !result.LocalHealthy && !result.RemoteHealthy:
		light.SetStatus(lights.StatusRed)
		light.SetDetails("unhealthy (consensus)")
	default:
		light.SetStatus(lights.StatusGreen)
		light.SetDetails("unknown state")
	}
}
