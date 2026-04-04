# ADHD Headless Mode

ADHD can run in headless mode for background MCP traffic logging and integration with smoke-alarm.

## Basic Usage

```bash
./adhd --headless --log /tmp/adhd-traffic.jsonl
```

This starts ADHD in headless mode, logging all MCP traffic to a JSONL file.

## Avoiding Port Conflicts

When running multiple ADHD instances (especially for testing or distributed monitoring), use `--mcp-addr :0` to let the OS assign a random available port:

```bash
# Instance 1 (on random port)
./adhd --headless --mcp-addr :0 --log /tmp/adhd-1.jsonl

# Instance 2 (on different random port)
./adhd --headless --mcp-addr :0 --log /tmp/adhd-2.jsonl

# Instance 3 (on yet another random port)
./adhd --headless --mcp-addr :0 --log /tmp/adhd-3.jsonl
```

All instances will start without port conflicts.

## Prime-Plus Topology (Distributed Buffering)

Run as a secondary instance that buffers logs and pushes them to a primary:

```bash
./adhd --headless \
  --mcp-addr :0 \
  --log /var/log/adhd-secondary.jsonl \
  --prime-plus \
  --prime-addr http://primary-smoke-alarm:9090/mcp \
  --buffer-size 5000
```

**Behavior**:
- All MCP traffic logged locally
- Logs buffered in memory (max 5000 entries)
- Every 5 seconds: attempt to push to primary via `smoke-alarm.isotope.push-logs`
- On success: buffer cleared
- On failure: logs stay buffered, retry in 5 seconds
- On shutdown: final push attempt before exit

## Registering with Smoke-Alarm

Register this instance as an isotope with smoke-alarm:

```bash
./adhd --headless \
  --mcp-addr :0 \
  --log /tmp/adhd.jsonl \
  --smoke-alarm http://fire-marshal:8080/mcp
```

This allows smoke-alarm to discover and route requests to ADHD's MCP endpoint.

## Configuration File

Alternatively, configure in `adhd.yaml`:

```yaml
mcp_server:
  enabled: true
  addr: ":0"  # Use random port

health:
  remote_smoke_alarm: "http://smoke-alarm:8080"

features:
  binaries:
    - name: my-service
      endpoint: "http://localhost:9001"
      gherkin_files:
        - ../my-repo/features/**/*.feature
```

Then run:

```bash
./adhd --headless --config adhd.yaml --log traffic.jsonl
```

## CLI Flags Reference

| Flag | Default | Description |
|------|---------|-------------|
| `--headless` | false | Enable headless mode (no TUI) |
| `--log FILE` | "" | Write JSONL logs to file (stdout if empty) |
| `--mcp-addr ADDR` | (from config) | Override MCP server address (use `:0` for random port) |
| `--smoke-alarm URL` | "" | Register as isotope with smoke-alarm |
| `--prime-plus` | false | Run as secondary: buffer logs and push to primary |
| `--prime-addr ADDR` | "" | Address of primary smoke-alarm (required with --prime-plus) |
| `--buffer-size N` | 1000 | Max logs to buffer before pushing to prime |
| `--config FILE` | adhd.yaml | Config file path |
| `--debug` | false | Enable debug logging |

## Monitoring Multiple Services

Create a config that monitors multiple endpoints:

```yaml
mcp_server:
  enabled: true
  addr: ":0"

features:
  binaries:
    - name: service-a
      endpoint: "http://service-a:9001"
      gherkin_files:
        - /features/service-a/*.feature

    - name: service-b
      endpoint: "http://service-b:9002"
      gherkin_files:
        - /features/service-b/*.feature

    - name: service-c
      endpoint: "http://service-c:9003"
      gherkin_files:
        - /features/service-c/*.feature
```

Run and aggregate logs:

```bash
./adhd --headless --config multi-service.yaml --log aggregated.jsonl
```

All traffic from all services will be logged to a single JSONL file.

## Troubleshooting

### Port Already in Use

If you get "address already in use", use `--mcp-addr :0`:

```bash
./adhd --headless --mcp-addr :0
```

### Logs Not Being Pushed to Prime

- Check prime address is correct: `--prime-addr http://primary:9090/mcp`
- Verify primary is running and reachable
- Check logs for "failed to push" warnings
- Logs remain buffered locally until push succeeds

### No Logs Appearing

- Check log file path is writable: `--log /var/log/adhd.jsonl`
- Verify traffic is being sent to ADHD's MCP endpoint
- Check debug logs: `--debug`

## Performance

- **Headless startup**: < 100ms
- **Log write latency**: < 1ms per entry
- **Buffer push overhead**: < 50ms for 1000 entries
- **Memory footprint**: ~1MB + 1KB per buffered log entry
