# ADHD Quick Start (2 Minutes)

## The Simplest Demo

### Terminal 1: Dashboard (Interactive TUI)
```bash
cd /Users/james/src/prototypes/adhd
./bin/adhd --config ./adhd-example.yaml
```
✅ You see lights and can navigate with j/k

### Terminal 2: Headless (Logging)
```bash
cd /Users/james/src/prototypes/adhd
./bin/adhd --headless \
  --config ./adhd-example.yaml \
  --mcp-addr :0 \
  --log /tmp/adhd.jsonl \
  --prime-plus \
  --prime-addr http://localhost:9090/mcp
```
✅ Logs appear every 5 seconds: "successfully pushed logs to prime"

### Terminal 3: Watch Logs
```bash
tail -f /tmp/adhd.jsonl | jq -c '{type, method, timestamp}'
```
✅ JSONL logs stream in real-time

---

## Common Scenarios

### I Want to See the Dashboard
```bash
./bin/adhd --config ./adhd-example.yaml
```
- Controls: j/k (navigate), q (quit), s (show), r (refresh)

### I Want Background Logging
```bash
./bin/adhd --headless --log /tmp/adhd.jsonl
```
- Logs MCP traffic to JSONL
- No buffer/push (standalone mode)

### I Want Dashboard + Headless Together
```bash
# Terminal 1
./bin/adhd --config ./adhd-example.yaml --mcp-addr :9090

# Terminal 2
./bin/adhd --headless \
  --mcp-addr :0 \
  --log /tmp/adhd.jsonl \
  --prime-plus \
  --prime-addr http://localhost:9090/mcp
```
- Headless auto-pushes logs to dashboard
- Dashboard sees real topology

### I Want Automatic Discovery (via smoke-alarm)
```bash
# Terminal 1 (Dashboard)
./bin/adhd --config ./adhd-example.yaml

# Terminal 2 (Headless - discovers dashboard automatically)
./bin/adhd --headless \
  --mcp-addr :0 \
  --log /tmp/adhd.jsonl \
  --prime-plus \
  --smoke-alarm http://localhost:8080/mcp
```
- Requires smoke-alarm running on :8080
- Headless auto-discovers dashboard

---

## Port Conflicts?

Use `:0` to let OS assign random port:
```bash
./bin/adhd --mcp-addr :0
```

Multiple instances:
```bash
./bin/adhd --mcp-addr :0 --log /tmp/adhd-1.jsonl &
./bin/adhd --mcp-addr :0 --log /tmp/adhd-2.jsonl &
./bin/adhd --mcp-addr :0 --log /tmp/adhd-3.jsonl &
```

---

## Verify It's Working

### Check Dashboard Running
```bash
curl -s http://localhost:9090/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.status","params":{}}' | jq .result.summary
```

### Check Headless is Prime-Plus
```bash
curl -s http://localhost:RANDOM_PORT/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.isotope.status","params":{}}' | jq .result.role
# Should show: "prime-plus"
```

### Check Logs Exist
```bash
ls -lh /tmp/adhd*.jsonl
head -1 /tmp/adhd.jsonl | jq .
```

---

## Troubleshooting

| Problem | Solution |
|---------|----------|
| "address already in use" | Add `--mcp-addr :0` to use random port |
| Port 9090 in use | Change to `--mcp-addr :9999` (or any free port) |
| Headless doesn't discover prime | Use explicit `--prime-addr` instead of `--smoke-alarm` |
| No logs appearing | Check `/tmp/adhd.jsonl` with `cat` or `tail -f` |
| Dashboard unresponsive | Press `q` to quit, rebuild with `go build ./cmd/adhd` |
| MCP server won't start | Use `--mcp-addr :0` for random port, or change port number |

---

## Using the Demo Script

Automated launcher for everything:
```bash
./demo.sh
```

Menu options:
1. **Dashboard only** - see TUI with lights
2. **Headless only** - see JSONL logging
3. **Full demo** - opens 3 terminals (Dashboard + Headless + Monitor)
4. **Monitor logs** - watch JSONL stream
5. **Query health** - check system status
6. **Exit** - cleanup and quit

---

## Architecture at a Glance

```
Dashboard (Prime)           Headless (Prime-Plus)
  :9090/mcp    ◄────────────   :random/mcp
  - TUI                        - JSONL logging
  - Lights                     - Buffers logs
  - Authoritative             - Pushes to prime
```

**Flow:**
1. Dashboard starts on port 9090
2. Headless starts on random port
3. Headless discovers dashboard (or uses explicit address)
4. Headless logs all MCP traffic to JSONL
5. Every 5 seconds: headless pushes buffered logs to dashboard
6. Dashboard receives logs via `smoke-alarm.isotope.push-logs` MCP method

---

## Next Steps

- **Add real services** - update `adhd-example.yaml` with your endpoints
- **Enable smoke-alarm** - integrate with centralized monitoring
- **Scale up** - run multiple headless instances with one dashboard
- **Monitor logs** - pipe JSONL to analytics/visualization tools
- **Integrate CI/CD** - use headless for automated feature testing

---

## Full Reference

See `DEMO_GUIDE.md` for detailed walkthroughs and troubleshooting.

See `HEADLESS_MODE.md` for headless-specific features.

See `ARCHITECTURE.md` for system design details.

---

**Need help?** Check logs with `--debug` flag for detailed output.
