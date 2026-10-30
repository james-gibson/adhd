# adhd — Alarm Dashboard Health Monitor

A minimal-footprint CLI dashboard for visualising alarm data from `ocd-smoke-alarm` instances.

Runs in two modes:

**TUI mode** — a Bubble Tea terminal UI with colour-coded light indicators, keyboard navigation, and live polling of smoke-alarm instances.

**Headless mode** — an MCP server that logs all traffic as JSONL, watches smoke-alarm SSE streams, and participates in a prime/prime-plus topology where multiple adhd instances buffer and relay logs to a designated prime. In headless mode, adhd registers itself with smoke-alarm as an isotope and advertises `_adhd-isotope._tcp` via mDNS.

---

## Trust after connection

**A caller can connect to an MCP service; whether it is trusted after connecting is
a separate question, and one most transport specifications do not answer.** This lab
measures it. A caller carries a **42i distance** — the extent to which it has
departed from **nominal** (the service level a call promises) against **linear time**
(the shared tick) — and the distance maps to a **rung** that limits what the caller
may read. A caller that keeps departing is removed from the routeable set.

```
 nominal ●────────────  the level a call promises
          ╲
           ╲ distance     +8 / +16 / +32 …
            ●───────────  where it landed (linear time →)
```

One measure, regardless of source — a stale credential, a clock skew, a tampered
receipt, a honeypot contact. Model:
[`PLANE.md`](https://github.com/james-gibson/lab-safety/blob/main/lab/PLANE.md).
Figures:
[`MATH.md`](https://github.com/james-gibson/lab-safety/blob/main/lab/MATH.md).
Full page with references:
[`SPLASH.md`](https://github.com/james-gibson/lab-safety/blob/main/lab/SPLASH.md).

## Quick Start

```sh
# Build
go build -o bin/adhd ./cmd/adhd

# TUI mode
./bin/adhd --config adhd.yaml

# Headless mode, registered from the config file (launchd/systemd shape)
./bin/adhd --headless --config adhd.yaml

# Headless mode, flag-driven registration (overrides the config)
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
# Config-driven: registration follows adhd.yaml (no CLI flag needed)
adhd --headless --config adhd.yaml

# Flag-driven: --smoke-alarm overrides the config
adhd --headless --smoke-alarm http://localhost:8088
```

In headless mode, adhd:
- Serves an MCP endpoint at `mcp_server.addr`
- Registers itself with smoke-alarm via `POST /isotope/register` — from the
  `--smoke-alarm` flag when given, otherwise from the **first `smoke_alarm`
  endpoint in the config file**, so a config-file-driven deployment registers
  without a CLI flag
- Sends a cluster **attestation** with the registration when lezz handed it one
  (`LEZZ_CLUSTER_ID` + `LEZZ_CLUSTER_SECRET`): an HMAC receipt over that cluster
  and this instance's endpoint, re-signed on every heartbeat. Without it the
  registration is a self-declared endpoint and the alarm puts it at the trust
  floor (isotope#23)
- Receives a trust rung on the 42i capability lattice
- Greets the first client with the lab's MOTD (`initialize.instructions`):
  *Don't Panic. 42 is a coordinate, not a credential; trust is what you pinned.*
  It is itself a signed test vector — `isotope/testdata/burned`, key
  `zaphod-beeblebrox` — so the greeting can be checked rather than believed
- Advertises `_adhd-isotope._tcp` via mDNS so other lab tools can discover it,
  under an instance label derived from its per-process isotope identity
  (`adhd-<8 chars>`, e.g. `adhd-Xq3vA9kL`) — so two instances on the same link
  are two discoverable entries, not one name that browsers collapse

### The advertise guard (why registration can refuse)

adhd's MCP surface has **no inbound auth**, so advertising a non-loopback bind
would name an unauthenticated MCP endpoint to the LAN — an alarm offense. Before
it registers or advertises, adhd probes its own endpoint and **refuses to start**
(exit non-zero, nothing registered or advertised) unless either:

- `mcp_server.addr` binds **loopback** (`127.0.0.1:9090`), so the advertisement
  is inert to the LAN, or
- a live probe shows an **auth gateway** answering on the endpoint (HTTP
  401/403).

`--force` is the explicit override for the deliberate LAN-discovery case (for
example `lezz demo`, which binds `:PORT` on purpose). Pass it only when the
operator has chosen to expose the endpoint.

The `--demo` flag browses `_lezz-demo._tcp` and fetches `http://<host>:<port>/cluster` at the port the registry advertises in its mDNS SRV record (falling back to the well-known `19100` when none is advertised) to auto-configure without a config file. The localhost fallback registry is accepted only when a cluster it advertises answers a `/healthz` health probe, so a stale or ghost registry whose cluster has already gone away is ignored rather than adopted.

---

## Configuration

```yaml
# adhd.yaml
mcp_server:
  enabled: true
  addr: "127.0.0.1:9090"   # loopback: required for registration unless --force

smoke_alarm:
  - name: "primary"
    endpoint: "http://localhost:8088"
    interval: 10s
    use_sse: true
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
├── features/adhd/         — Gherkin acceptance tests (matched by internal/report)
├── tests/scenario/        — Scenario E2E tests (live binary → JSONL → feature match)
└── tests/integration/     — Integration tests (in-process)
```

---

## Testing

```sh
# Unit tests
go test ./internal/... -v

# Scenario (E2E) tests — drive a live adhd binary and match features/adhd/ via internal/report
go test ./tests/scenario/... -count=1

# Integration tests — exercise packages in-process against tests/integration/
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
