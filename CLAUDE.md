# CLAUDE.md — ADHD Project Guidelines

## Project Overview

ADHD is a minimal-footprint CLI dashboard for alarm system monitoring. Single binary, text-based UI (Bubble Tea), keyboard-driven, displays service health via color-coded light indicators.

## Build & Test Commands

| Task | Command |
|---|---|
| Build binary | `go build -o bin/adhd ./cmd/adhd` |
| Run dashboard | `./bin/adhd` |
| Debug mode | `./bin/adhd -debug 2>&1` |
| Feature tests | `go run ./cmd/adhd test --features=features/adhd/` |
| Unit tests | `go test ./internal/... -v` |

## Architecture

### Bubble Tea Elm Pattern

ADHD follows Elm Architecture strictly:

- **Model** (`internal/dashboard/model.go`) — state
  - No side effects in Update()
  - All mutations return (Model, Cmd) pairs
  - Async work via tea.Cmd

- **Update** (`internal/dashboard/model.go`) — event handling
  - Pure function: msg + model → new model + cmd
  - No direct I/O, no logging side effects

- **View** (`internal/dashboard/view.go`) — rendering
  - Pure function: model → string
  - No state mutations
  - No side effects

### Core Packages

1. **dashboard/** — UI layer (Bubble Tea)
   - model.go — state machine
   - view.go — rendering (100% pure)

2. **lights/** — status indicator system
   - light.go — Light and Cluster data structures
   - No I/O, no side effects

3. **features/** — Gherkin feature discovery
   - loader.go — file walking and parsing
   - Returns Feature structs (no UI coupling)

4. **telemetry/** — OTEL setup
   - otel.go — metric exporter initialization
   - Gracefully degrades if endpoint unavailable

5. **executor/** — Command execution (planned)
6. **health/** — Health endpoint (planned)

## Code Style

- **Imports**: stdlib → external → internal (goimports)
- **Naming**: packages lowercase; interfaces with `er` suffix
- **Logging**: log/slog structured logging; never log sensitive data
- **Errors**: wrap with `fmt.Errorf("%w")`; use sentinel for predictable failures
- **Testing**: Bubble Tea requires pure functions; test model.Update() without side effects

## Testing Strategy

### Unit Tests

Test pure functions directly:

```go
// ✓ Good: test pure function
func TestModelUpdate(t *testing.T) {
    m := NewModel()
    msg := tea.KeyMsg{Runes: []rune{'j'}}
    m2, _ := m.Update(msg)
    // assert m2 state
}
```

### Feature Tests

Gherkin features in `features/adhd/` are the acceptance test suite:

```gherkin
Feature: Dashboard Navigation
  Scenario: Navigate with j/k keys
    Given a dashboard with 5 lights
    When I press 'j'
    Then the selection moves forward
```

Features follow naming convention:
- `@adhd` — ADHD-specific
- `@z0-physical`, `@z1-temporal`, `@z2-relational`, `@z3-epistemic` — layer
- `@domain-<domain>` — feature domain (e.g., `@domain-dashboard`, `@domain-lights`)

### Integration Tests

Test full dashboard flow (in `tests/integration/`):

```go
// Test: dashboard starts, renders, responds to keys
func TestDashboardIntegration(t *testing.T) {
    m := NewModel()
    p := tea.NewProgram(m)
    // Simulate inputs, check rendering
}
```

## Critical Constraints

1. **No state mutations in View()** — it's pure; return string only
2. **No blocking I/O in Update()** — use tea.Cmd for async work
3. **Feature discovery non-blocking** — if paths don't exist or fail, log + continue
4. **OTEL is optional** — graceful degradation; dashboard works without metrics
5. **No external commands in tests** — feature tests are deterministic

## Task Workflow

When adding features:

1. Write Gherkin scenario first (features/adhd/*.feature)
2. Implement model changes (internal/dashboard/model.go)
3. Update view if needed (internal/dashboard/view.go)
4. Add unit tests for pure functions
5. Run `go test ./internal/...` before committing

## Known Limitations

- **No persistent state** — dashboard is ephemeral (by design)
- **No config parsing yet** — hardcoded paths and defaults (TODO: YAML support)
- **No HTTP API yet** — planned for future release
- **Single-threaded rendering** — Bubble Tea handles concurrency; don't spawn goroutines without care

## Common Patterns

### Adding a new light category

1. Create `internal/<category>/loader.go` with LoadLights() function
2. Call from `dashboard.NewModel()` and append results to cluster
3. Test with feature in `features/adhd/*.feature`

### Adding a new keyboard command

1. Add case in `model.handleKey()`
2. Update state or send Cmd
3. Add test scenario in `dashboard-operations.feature`

### Safe async work

```go
// ✓ Good: async work via tea.Cmd
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    return m, tea.ExecProcess(cmd, ...)  // Non-blocking
}

// ✗ Bad: blocking I/O in Update()
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    result := runCommandSync()  // ❌ blocks UI
    return m, nil
}
```

## See Also

- **Bubble Tea docs**: https://github.com/charmbracelet/bubbletea (Elm architecture)
- **Seed documentation**: `../pickled-onions/seeds/adhd/README.md` (world context)
- **fire-marshal**: pre-deployment analysis
- **ocd-smoke-alarm**: runtime monitoring
