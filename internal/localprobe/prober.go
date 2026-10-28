package localprobe

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpclient"
	"github.com/james-gibson/adhd/internal/registry"
)

// Prober runs local health checks and compares against remote smoke-alarm
//
// A binary's endpoint is either a URL, probed as given, or a registry name,
// resolved through the registry on every tick (adhd#62). Re-resolving is the
// point: a session's surface binds an ephemeral port and publishes itself by
// name, so its address is not knowable in advance and a cache would hold a port
// the surface no longer answers on.
type Prober struct {
	binary      *config.Binary
	name        string // registry name; "" when Endpoint is a URL
	endpoint    string // static endpoint; "" when Endpoint is a name
	registry    *registry.Client
	timeout     time.Duration
	remoteAlarm string // remote smoke-alarm endpoint
}

// ProbeResult combines local and remote health checks
type ProbeResult struct {
	LocalHealthy  bool
	RemoteHealthy bool
	LocalError    string
	RemoteError   string
	Consensus     bool // true if local and remote agree
	Latency       time.Duration

	// ResolutionError is set when a named endpoint could not be resolved, and
	// the local probe therefore never ran. The light judges the name, not a
	// consensus: there is no local verdict to agree or disagree with.
	ResolutionError string
	// NameNotFound separates "the registry answered and the name is gone" (the
	// surface is gone) from "the registry did not answer" (the observer lost
	// its directory).
	NameNotFound bool
}

// New creates a prober for a binary with a fixed endpoint
func New(binary *config.Binary, remoteAlarmEndpoint string) *Prober {
	return NewWithRegistry(binary, remoteAlarmEndpoint, "")
}

// NewWithRegistry creates a prober that resolves a named endpoint through
// registryURL. A URL endpoint ignores registryURL entirely.
func NewWithRegistry(binary *config.Binary, remoteAlarmEndpoint, registryURL string) *Prober {
	p := &Prober{
		binary:      binary,
		timeout:     binary.Timeout,
		remoteAlarm: remoteAlarmEndpoint,
	}

	if name, ok := binary.EndpointName(); ok {
		p.name = name
		p.registry = registry.NewClient(registryURL, binary.Timeout)
	} else {
		p.endpoint = binary.Endpoint
	}

	return p
}

// Probe runs both local and remote checks, returns consensus result
func (p *Prober) Probe(ctx context.Context) ProbeResult {
	start := time.Now()

	endpoint, err := p.resolveEndpoint(ctx)
	if err != nil {
		// No local verdict exists, so there is nothing to compare. Asking the
		// alarm whether it is reachable would answer a different question.
		result := ProbeResult{
			LocalHealthy:    false,
			LocalError:      err.Error(),
			ResolutionError: err.Error(),
			NameNotFound:    errors.Is(err, registry.ErrNotFound),
			Latency:         time.Since(start),
		}
		slog.Debug("probe could not resolve endpoint",
			"binary", p.binary.Name,
			"name", p.name,
			"error", err,
		)
		return result
	}

	// Run local probe
	localResult, _ := mcpclient.NewHTTPClient(endpoint, p.timeout).Probe(ctx)

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
		"endpoint", endpoint,
		"local", result.LocalHealthy,
		"remote", result.RemoteHealthy,
		"consensus", result.Consensus,
	)

	return result
}

// resolveEndpoint is the address this tick probes: the configured URL, or the
// name as the registry holds it right now.
func (p *Prober) resolveEndpoint(ctx context.Context) (string, error) {
	if p.name == "" {
		return p.endpoint, nil
	}
	return p.registry.Resolve(ctx, p.name)
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
	case result.NameNotFound:
		// The registry answered and the name is not in it: the surface is gone,
		// not misconfigured. That is the local observer's verdict, so it is red
		// rather than the yellow disagreement a missing local signal would give.
		light.SetStatus(lights.StatusRed)
		light.SetDetails(result.ResolutionError)
	case result.ResolutionError != "":
		// The registry did not answer, so the name could not be looked up. The
		// surface may be fine; the observer lost its directory. YELLOW says
		// "cannot tell", not "gone".
		light.SetStatus(lights.StatusYellow)
		light.SetDetails("cannot resolve: " + result.ResolutionError)
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
