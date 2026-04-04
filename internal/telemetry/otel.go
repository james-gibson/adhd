package telemetry

import (
	"context"
	"log/slog"
)

// Initialize sets up telemetry
// For now, this is a stub. OTEL will be added once dependencies are available.
// Returns a shutdown function that should be called before program exit
func Initialize(ctx context.Context) (func(context.Context) error, error) {
	slog.Debug("telemetry initialized (stub - OTEL coming in next phase)")

	// Return no-op shutdown function
	return func(ctx context.Context) error {
		return nil
	}, nil
}
