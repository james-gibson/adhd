# Soak tests (process level)

These are the long-running tests. Everything in `tests/integration`,
`tests/scenario` and `tests/load` finishes in seconds and runs nodes **in
process on loopback**. Nothing here is mocked: each node is a real `adhd`
binary, in its own directory, with its own config and JSONL log.

The package is behind the `soak` build tag so `go test ./...` stays fast.

```sh
# short run, 3 nodes, 2 minutes
go test -tags soak ./tests/soak/... -v -timeout 0

# the real thing
ADHD_SOAK_DURATION=8h \
ADHD_SOAK_NODES=5 \
ADHD_SOAK_RATE=10 \
ADHD_SOAK_CHAOS=1 \
go test -tags soak ./tests/soak/... -v -timeout 0
```

| env | default | meaning |
| --- | --- | --- |
| `ADHD_SOAK_DURATION` | `2m` | wall-clock length of the run |
| `ADHD_SOAK_NODES` | `3` | prime-plus processes (one prime is added) |
| `ADHD_SOAK_RATE` | `2` | MCP calls per second per node |
| `ADHD_SOAK_SAMPLE` | `10s` | metric sampling interval |
| `ADHD_SOAK_CHAOS` | off | kill and restart a node every 30s |
| `ADHD_SOAK_REPORT` | — | path for the JSON report (also written to `t.TempDir()`) |

## Tests

| test | what it covers |
| --- | --- |
| `TestSoakSteadyState` | prime + N prime-plus, real binaries, steady MCP traffic, resource trends, optional chaos restarts |
| `TestClusterAcceptsRemoteJoin` | host-side registry contract a second machine's POST depends on, over the LAN address |
| `TestMDNSRegistryDiscovery` | the intended cross-host path: registry on the LAN address only, advertised as `_lezz-demo._tcp`, no localhost fallback available |
| `TestDemoNodeConsumesExternalRegistry` | `adhd --demo` builds lights from a registry it did not start; then a cluster joins after startup |

## What a run asserts

Steady state is not "the process is still up". Each sample records RSS, open
file descriptors and JSONL log size per node. At the end:

- **no unexplained exit** — a node that dies without the chaos loop killing it
  fails the run;
- **RSS trend** — median of the last third against the first third, after a
  warm-up window. A 50% / 50 MB rise fails;
- **FD trend** — more than +32 descriptors over the run fails;
- **logs keep growing** — a flat log means traffic stopped reaching the node;
- **JSONL integrity** — every sampled line parses, and every call method is
  present in the log (the load path is actually exercised);
- **call success** — under 1% of MCP calls may fail.

The chaos loop restarts a node on its original address and requires it to
answer `initialize` again within 30s.

## Registry addressing is mDNS — and the join path ignores it

The registry address is not meant to be configured. `lezz demo`/`auto` advertise
the registry as `_lezz-demo._tcp` (`internal/demo/discovery.go`
`registerMDNS`) and carry the real port in the SRV record; `adhd --demo` browses
for it (`internal/demo/discover.go` `Browse`, strategy 1). That is the intended
cross-host path, and it works: `TestMDNSRegistryDiscovery` binds the registry to
the LAN address only, advertises it over mDNS, and the dashboard finds it with
the localhost fallback unavailable.

What does **not** use the discovered address is the join/poll side:

| side | file | what it does |
| --- | --- | --- |
| lezz | `internal/demo/auto.go` `existingRegistry` | decides a registry already exists by asking `localhost:19100` |
| lezz | `internal/demo/discovery.go` `joinDiscoveryServer` | POSTs a new cluster to `localhost:19100` |
| lezz | `internal/demo/cluster.go` `notifyExistingADHD` | asks `0.0.0.0:19100` which ADHD instances to notify |
| lezz | `internal/demo/discovery.go` `startDiscoveryServer` | binds `:19100` unconditionally, so a second host starts a competing registry instead of reusing the advertised one |
| adhd | `cmd/adhd/main.go:250` | continuous registry polling is `localhost:19100`, discarding the address `Browse` discovered |
| adhd | `cmd/adhd/main.go:158-257` | polling only exists in TUI mode, so `--headless` never sees a later join |

So: discovery over mDNS works, soaks on one host work, and `adhd --demo` on a
second machine can observe a cluster at startup. What is missing is the reverse
direction — a second `lezz` joining the registry it discovered, and either side
noticing a cluster that arrives after startup. The fix belongs in the mDNS
path (reuse the discovered instance + SRV port at those five sites), not in a
new `--registry` flag.

`--force` is not a workaround either: `internal/headless/advertise_guard.go`
refuses to advertise an unauthenticated control surface beyond loopback, and
`lezz demo`/`auto` already pass `--force` deliberately because they want the
all-interfaces bind (adhd#15, lezz.go#14).
