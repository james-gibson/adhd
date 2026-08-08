# ADHD Demo Guide: Dashboard + Headless with Auto-Discovery

This guide walks you through launching and verifying the full ADHD system with dashboard, headless instance, and auto-discovery via smoke-alarm.

## Prerequisites

- ADHD binary built: `./bin/adhd`
- Config file: `./adhd-example.yaml`
- Optional: access to smoke-alarm and fire-marshal

## Quick Start (5 minutes)

### Step 1: Start the Dashboard (Prime)

The dashboard is the authoritative instance that runs in your terminal with a visual UI.

```bash
cd /Users/james/src/prototypes/adhd

# Terminal 1: Run dashboard in TUI mode
./bin/adhd --config ./adhd-example.yaml

# You should see:
# - Lights grouped by service
# - Status indicators (green/red/yellow)
# - Interactive navigation with j/k/s/r/e
```

**What to expect:**
- Lights appear and light up (boot animation)
- All lights are green (features exist)
- Ready for requests

### Step 2: Start Headless Instance (Prime-Plus)

In a new terminal, start the headless logging instance that discovers the dashboard automatically.

```bash
cd /Users/james/src/prototypes/adhd

# Terminal 2: Run headless with auto-discovery
./bin/adhd --headless \
  --config ./adhd-example.yaml \
  --mcp-addr :0 \
  --log /tmp/adhd-headless.jsonl \
  --prime-plus \
  --smoke-alarm http://localhost:8080/mcp

# You should see:
# INFO headless mode logging to stdout
# INFO starting MCP server addr=:0
# INFO MCP server started addr=:0
# INFO auto-discovering prime instance via smoke-alarm
```

**What's happening:**
1. Headless starts on a random port (`:0`)
2. Queries smoke-alarm for ADHD isotopes
3. Finds the dashboard as prime
4. Configures log buffering → auto-push to dashboard

### Step 3: Verify Discovery

In another terminal, query the headless instance to see it discovered the dashboard:

```bash
# Terminal 3: Query headless topology
curl -X POST http://localhost:9091/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "adhd.isotope.status",
    "params": {}
  }' | jq .

# Response should show:
# {
#   "jsonrpc": "2.0",
#   "result": {
#     "name": "adhd",
#     "role": "prime-plus",
#     "status": "ready"
#   }
# }
```

### Step 4: Check Logs

Verify the headless instance is logging MCP traffic:

```bash
# Terminal 3: Monitor JSONL logs
tail -f /tmp/adhd-headless.jsonl | jq .method

# You should see traffic logs as features are probed
```

## Full Demo Setup (with smoke-alarm integration)

If you have smoke-alarm and fire-marshal running, here's the full topology:

```
                    ┌─────────────────┐
                    │  Smoke-Alarm    │
                    │   (MCP proxy)   │
                    │  :8080/mcp      │
                    └────────┬────────┘
                             │
                ┌────────────┼────────────┐
                │            │            │
        ┌───────▼────┐  ┌───▼────────┐  │
        │ Dashboard  │  │   Headless │  │
        │  (Prime)   │  │ (Prime+)   │  │
        │  :9090/mcp │  │  :random   │  │
        └────────────┘  └────────────┘  │
                                        │
                            ┌───────────▼──┐
                            │ Fire-Marshal  │
                            │  (discoverer) │
                            └───────────────┘
```

### Launch in Order

**1. Start smoke-alarm** (if available)
```bash
# Terminal A
cd /path/to/ocd-smoke-alarm
go run ./cmd/ocd-smoke-alarm serve --mode=foreground
# Expected: smoke-alarm listening on :9090
```

**2. Start fire-marshal** (if available)
```bash
# Terminal B
cd /path/to/fire-marshal
go run ./cmd/fire-marshal server
# Expected: fire-marshal listening on :8080
```

**3. Start ADHD dashboard** (prime)
```bash
# Terminal C
cd /Users/james/src/prototypes/adhd
./bin/adhd --config ./adhd-example.yaml \
  --mcp-addr :9090

# Expected: TUI dashboard with lights
```

**4. Start ADHD headless** (prime-plus, auto-discovers)
```bash
# Terminal D
./bin/adhd --headless \
  --config ./adhd-example.yaml \
  --mcp-addr :0 \
  --log /tmp/adhd-headless.jsonl \
  --prime-plus \
  --smoke-alarm http://localhost:9090/mcp

# Expected: auto-discovery succeeds, logs start flowing
```

## Verification Checklist

### Dashboard is Running
```bash
# Query dashboard status
curl -s http://localhost:9090/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.status","params":{}}' \
  -H "Content-Type: application/json" | jq .result.summary
```

Expected output:
```json
{
  "total": 5,
  "green": 5,
  "red": 0,
  "yellow": 0
}
```

### Headless is Running
```bash
# Query headless role
curl -s http://localhost:RANDOM_PORT/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.isotope.status","params":{}}' \
  -H "Content-Type: application/json" | jq .result
```

Expected output:
```json
{
  "name": "adhd",
  "role": "prime-plus",
  "status": "ready"
}
```

### Headless Discovered Prime
```bash
# Check if headless can see prime
curl -s http://localhost:RANDOM_PORT/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.isotope.peers","params":{}}' \
  -H "Content-Type: application/json" | jq .result.peers
```

Expected output:
```json
[
  {
    "name": "prime",
    "role": "prime",
    "endpoint": "http://localhost:9090/mcp",
    "status": "active"
  }
]
```

### Logs Are Flowing
```bash
# Check headless JSONL logs
wc -l /tmp/adhd-headless.jsonl
head -1 /tmp/adhd-headless.jsonl | jq .
```

Expected: logs appearing, valid JSONL format

## Troubleshooting

### "address already in use"

**Problem:** Port conflict when starting dashboard or headless

**Solution:** Use random port with `:0`
```bash
./bin/adhd --config ./adhd-example.yaml --mcp-addr :0
```

### Headless doesn't discover prime

**Problem:** Auto-discovery fails, logs show error
```
WARN failed to auto-discover prime error="..."
```

**Causes & fixes:**
1. **smoke-alarm not running**
   - Check: `curl http://localhost:9090/mcp`
   - Start smoke-alarm in separate terminal

2. **Wrong smoke-alarm URL**
   - Use exact URL: `--smoke-alarm http://localhost:9090/mcp`
   - Not `:9090` (needs full path)

3. **Dashboard not registered as isotope**
   - Headless registers on startup
   - If dashboard started after headless, restart headless

**Workaround:** Use explicit prime address
```bash
./bin/adhd --headless \
  --prime-plus \
  --prime-addr http://localhost:9090/mcp
```

### Dashboard lights not updating

**Problem:** All lights green, no status changes

**Causes:**
1. **Config points to non-existent services**
   - Edit `adhd-example.yaml`
   - Verify `features.binaries[].endpoint` are reachable

2. **Services not running**
   - Example config looks for services on ports 9001, 9002, etc.
   - Start those services or create mock endpoints

**Verify:**
```bash
curl http://localhost:9001/mcp -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' -H "Content-Type: application/json"
```

### Logs not appearing in headless

**Problem:** `/tmp/adhd-headless.jsonl` is empty or doesn't grow

**Causes:**
1. **No traffic being generated**
   - Lights only log when probed
   - Verify services are reachable

2. **Wrong log file path**
   - Check file exists: `ls -la /tmp/adhd-headless.jsonl`
   - Try explicit path: `--log /var/log/adhd.jsonl`

3. **Permission denied**
   - Try: `--log /tmp/adhd.jsonl` (world writable)

**Verify:**
```bash
# Watch logs in real-time
tail -f /tmp/adhd-headless.jsonl | jq '.method' -c
```

### Headless process crashes on startup

**Problem:** Process exits immediately with error

**Solutions:**
1. **Run with debug logging**
   ```bash
   ./bin/adhd --headless --debug --log /tmp/adhd.jsonl
   ```

2. **Check for MCP server errors**
   - Look for: `failed to start MCP server`
   - Solution: use `--mcp-addr :0` for random port

3. **Check message queue init errors**
   - If `--prime-plus` without `--prime-addr` or `--smoke-alarm`
   - Solution: add one of those flags

## Step-by-Step Demo Walkthrough

Here's a complete demo you can run right now:

### Terminal 1: Dashboard
```bash
cd /Users/james/src/prototypes/adhd
./bin/adhd --config ./adhd-example.yaml --debug

# Look for:
# - "TUI mode" or dashboard rendering
# - Lights lighting up
# - Ready for input
```

### Terminal 2: Headless (without smoke-alarm)
```bash
cd /Users/james/src/prototypes/adhd

# Use explicit prime address (no auto-discovery needed)
./bin/adhd --headless \
  --config ./adhd-example.yaml \
  --mcp-addr :0 \
  --log /tmp/adhd.jsonl \
  --prime-plus \
  --prime-addr http://localhost:9090/mcp \
  --debug

# Look for:
# - "headless mode logging to file"
# - "starting MCP server"
# - "successfully pushed logs to prime" (every 5 seconds)
```

### Terminal 3: Monitor
```bash
# Watch logs appear
watch -n 1 'wc -l /tmp/adhd.jsonl'

# Or see live logs
tail -f /tmp/adhd.jsonl | jq -c '.method'
```

## Expected Behavior

### First 5 seconds
- Dashboard starts, lights boot up
- Headless starts, discovers prime (or uses explicit address)
- Message queue initialized

### Seconds 5-10
- Headless MCP server listening
- Dashboard starts probing services
- Logs start appearing in JSONL file

### After 10 seconds
- Dashboard shows feature health
- Headless pushes logs to prime every 5 seconds
- Both instances in sync

## Manual Testing

Test isotope discovery manually:

```bash
# Register dashboard as prime
curl -X POST http://localhost:9090/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "smoke-alarm.isotope.register",
    "params": {
      "name": "adhd",
      "role": "prime",
      "endpoint": "http://localhost:9090/mcp",
      "status": "ready"
    }
  }'

# Query from headless perspective
curl -X POST http://localhost:HEADLESS_PORT/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "adhd.isotope.peers",
    "params": {}
  }' | jq .
```

## Next Steps

Once demo is working:

1. **Add real services** - modify config to point to actual MCP endpoints
2. **Watch dashboard** - see lights reflect real health
3. **Check logs** - verify JSONL captures all traffic
4. **Scale up** - run multiple headless instances with same prime
5. **Integrate smoke-alarm** - enable centralized monitoring

## Questions?

If stuck:
1. Check debug logs: `--debug` flag on all instances
2. Verify ports: `lsof -i :9090` (check what's listening)
3. Check network: `curl http://localhost:PORT/mcp` (test reachability)
4. Look for common issues above in Troubleshooting section
