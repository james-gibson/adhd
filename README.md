# ADHD — Alarm Dashboard Health Monitor

A minimal-footprint command-line dashboard for real-time monitoring of alarm systems and services.

## What It Does

ADHD displays service health via color-coded light indicators (🟢 green / 🔴 red) and integrates with:

- **fire-marshal** — pre-deployment spec analysis
- **ocd-smoke-alarm** — runtime health monitoring
- **Gherkin features** — auto-discovered test suite status

Operators watch the dashboard and issue keyboard commands to inspect services, re-run tests, or execute remediation scripts.

## Quick Start

```bash
# Build
go build -o bin/adhd ./cmd/adhd

# Run
./bin/adhd

# Debug mode
./bin/adhd -debug

# Check version
./bin/adhd -v
```

## Dashboard Navigation

- **Up/Down arrows** or **j/k** — select light
- **r** — refresh selected light
- **s** — show service details
- **e** — execute linked command
- **q** or **Ctrl+C** — quit

## Features Supported

See `features/adhd/` for the full test suite:

- Dashboard initialization and light discovery
- Keyboard navigation and input handling
- Light status transitions (green/red/yellow/dark)
- Feature file auto-discovery
- OTEL metrics export (non-blocking)

## Directory Structure

```
adhd/
├── cmd/adhd/              -- CLI entry point
├── internal/
│   ├── dashboard/         -- Bubble Tea UI (model + view)
│   ├── lights/            -- Status indicator system
│   ├── features/          -- Gherkin feature discovery
│   ├── executor/          -- Command execution (planned)
│   ├── health/            -- Health endpoint (planned)
│   └── telemetry/         -- OTEL setup
├── features/adhd/         -- Dashboard Gherkin tests
└── go.mod                 -- Dependencies
```

## Testing

```bash
# Run all feature tests
go run ./cmd/adhd test --features=features/adhd/

# Watch dashboard in a terminal
./bin/adhd

# Run with debug logging
./bin/adhd -debug 2>&1 | grep -v BUBBLES
```

## Dependencies

- **Bubble Tea** — TUI framework (Elm architecture)
- **Lipgloss** — styling and layout
- **OpenTelemetry** — metrics export (gracefully degraded if unavailable)
- **gopkg.in/yaml.v3** — config parsing (for future use)

## Configuration (Future)

ADHD will support a YAML config file for:

```yaml
lights:
  - name: "primary-alarm"
    service: "mcp-primary"
    on-failure-commands:
      - name: "restart"
        exec: "ansible-playbook restart.yml"
        timeout: 30s

search-paths:
  - features/adhd/
  - ../ocd-smoke-alarm/features/
  - ../fire-marshal/features/

otel:
  endpoint: "http://localhost:4318"
```

## Skills Integration (Future)

When ADHD is deployed with skills available, they appear as executable lights:

```bash
# List available skills
./bin/adhd --list-skills

# Execute skill from CLI
./bin/adhd --execute skill:deploy-to-prod --with-context
```

In the dashboard, press `e` on a skill light to execute it.

## Development

### Adding a new light category

1. Create a new loader in `internal/<category>/loader.go`
2. Implement the loader interface (return `[]lights.Light`)
3. Call loader in `dashboard.NewModel()`

### Adding a new keyboard command

1. Add case in `dashboard.Model.handleKey()`
2. Implement the action (update state, log, etc.)
3. Add feature test in `features/adhd/dashboard-operations.feature`

### Testing the UI locally

```bash
# Run in development mode
go run ./cmd/adhd -debug

# Test specific scenario
go test ./internal/dashboard -v -run TestModelUpdate
```

## Performance Goals

- **Startup**: < 100ms (cold) / < 50ms (warm)
- **Render**: < 16ms per frame (60 FPS target)
- **Memory**: 20-50MB typical (no memory growth over time)
- **Dashboard latency**: < 100ms from keypress to visual update

## Future Extensions

1. **HTTP API** — GET `/status`, POST `/lights/:id/run-test`
2. **Config hot-reload** — watch config file for changes
3. **Alert escalation** — notify Slack/PagerDuty on red lights
4. **Historical metrics** — trend analysis and alerting
5. **Multi-instance clustering** — federation across alarms

## See Also

- **Seed documentation**: `../pickled-onions/seeds/adhd/README.md`
- **fire-marshal**: pre-deployment analysis layer
- **ocd-smoke-alarm**: runtime monitoring and regression detection

## License

Same as parent project (prototypes repository).
