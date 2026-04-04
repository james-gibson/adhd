# adhd — Alarm Dashboard Health Monitor

A minimal-footprint CLI dashboard for visualising alarm data from `ocd-smoke-alarm` instances.

Runs in two modes:

**TUI mode** — a Bubble Tea terminal UI with colour-coded light indicators, keyboard navigation, and live polling of smoke-alarm instances.

**Headless mode** — an MCP server that logs all traffic as JSONL, watches smoke-alarm SSE streams, and participates in a prime/prime-plus topology where multiple adhd instances buffer and relay logs to a designated prime. In headless mode, adhd registers itself with smoke-alarm as an isotope and advertises `_adhd-isotope._tcp` via mDNS.

---

## Quick Start

```sh
# Build
go build -o bin/adhd ./cmd/adhd

# TUI mode
./bin/adhd --config adhd.yaml

# Headless mode (MCP server)
./bin/adhd --headless --smoke-alarm http://localhost:8088

# Auto-discover a running lezz demo cluster
./bin/adhd --demo

# Debug logging
./bin/adhd -debug 2>&1
```

---

## TUI Navigation

| Key | Action |
|-----|--------|
| `j` / `↓` | Move selection down |
| `k` / `↑` | Move selection up |
| `r` | Refresh selected light |
| `s` | Show service details |
| `q` / Ctrl+C | Quit |

---

## Headless Mode

```sh
adhd --headless \
  --smoke-alarm http://localhost:8088 \
  --role prime \
  --addr :9090
```

In headless mode, adhd:
- Serves an MCP endpoint at the configured address
- Registers itself with smoke-alarm via `POST /isotope/register`
- Receives a trust rung on the 42i capability lattice
- Advertises `_adhd-isotope._tcp` via mDNS so other lab tools can discover it

The `--demo` flag browses `_lezz-demo._tcp` and fetches `http://<host>:19100/cluster` to auto-configure without a config file.

---

## Configuration

```yaml
# adhd.yaml
smoke-alarms:
  - http://localhost:8088
  - http://localhost:8089

headless:
  role: prime
  addr: :9090
```

---

## Directory Structure

```
adhd/
├── cmd/adhd/              — CLI entry point
├── internal/
│   ├── dashboard/         — Bubble Tea UI (model + view)
│   ├── lights/            — Status indicator system
│   ├── features/          — Gherkin feature discovery
│   ├── headless/          — MCP server, isotope registration, mDNS
│   └── telemetry/         — OTEL setup
├── features/adhd/         — Gherkin acceptance tests
└── tests/integration/     — Integration tests
```

---

## Testing

```sh
# Unit tests
go test ./internal/... -v

# Feature tests
go run ./cmd/adhd test --features=features/adhd/

# Integration tests
go test ./tests/integration/... -v
```

---

## In the Lab

adhd is one of several tools that share a common lab environment. See [lab-safety](https://github.com/james-gibson/lab-safety) for a full map of how all tools connect.

Peer tools:
- **lezz.go** — starts adhd in headless mode as part of `lezz demo`
- **ocd-smoke-alarm** — the health monitoring backend adhd polls and registers with
- **isotope** — the shared library used for trust registration and mDNS advertisement
- **tuner** — live data broadcaster (future: adhd will visualise tuner channels)

---

## See Also

- [lab-safety — full ecosystem overview](https://github.com/james-gibson/lab-safety)
- [ocd-smoke-alarm](https://github.com/james-gibson/smoke-alarm)
- [isotope protocol library](https://github.com/james-gibson/isotope)
- [pickled-onions seeds](../pickled-onions/seeds/adhd/) — world-builder context for this tool
