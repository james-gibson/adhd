# ADHD Architecture

## Two Deployment Models

### 1. Dashboard Mode (TUI) — Direct HTTP Observation

```
ADHD Dashboard (interactive)
    ↓ HTTP probe (direct)
    ├→ service-a:9001/mcp
    ├→ service-b:9002/mcp
    ├→ fire-marshal:9091/mcp
    └→ smoke-alarm:8080/mcp
```

**Characteristics:**
- ✅ Self-contained (no external dependencies)
- ✅ Direct observation of all endpoints
- ✅ Interactive TUI for real-time monitoring
- ✅ Independent from smoke-alarm's topology

**Usage:**
```bash
./adhd --config ./adhd.yaml
# Opens interactive dashboard with j/k navigation
```

---

### 2. Headless Mode — Proxied via Smoke-Alarm

```
ADHD Headless (logging)
    ↓ MCP proxy (via smoke-alarm)
Smoke-Alarm (client)
    ↓ monitors
    ├→ service-a
    ├→ service-b
    ├→ fire-marshal
    └→ [other services]
```

**Characteristics:**
- ✅ Executes smoke-alarm as a client proxy
- ✅ Registers itself as an "isotope" with smoke-alarm
- ✅ All MCP traffic logged as JSONL
- ✅ Centralized monitoring through smoke-alarm topology
- ✅ Background service, no TUI

**Usage:**
```bash
./adhd --config ./adhd.yaml --headless \
  --log /tmp/adhd-traffic.jsonl \
  --smoke-alarm http://smoke-alarm:9090
```

**Flow:**
1. ADHD starts MCP server
2. ADHD calls `smoke-alarm.isotope.register` (announces itself as isotope)
3. Smoke-alarm now routes monitoring through ADHD
4. All requests/responses logged as JSONL
5. Continues indefinitely until SIGINT/SIGTERM

---

## Key Difference

| Aspect | Dashboard (TUI) | Headless (Logging) |
|--------|-----------------|-------------------|
| **Network Model** | Direct HTTP probes | Proxied via smoke-alarm MCP |
| **Dependency** | None (standalone) | Smoke-alarm (intentional) |
| **Observation Pattern** | Direct observation | Centralized proxy topology |
| **Output** | Interactive visual | JSONL stream |
| **Use Case** | Operator monitoring | Centralized logging/analysis |

---

## Isotope Registration

When ADHD runs in headless mode with `--smoke-alarm`, it registers itself as an isotope:

**JSON-RPC Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "smoke-alarm.isotope.register",
  "params": {
    "name": "adhd",
    "type": "isotope",
    "endpoint": ":9090",
    "protocol": "mcp",
    "status": "ready",
    "description": "ADHD headless monitor with MCP traffic logging"
  }
}
```

This allows smoke-alarm to:
- Know ADHD is available
- Proxy requests to ADHD
- Route monitoring traffic through ADHD's MCP endpoint
- Integrate ADHD into the overall service mesh

---

## Traceability in Both Modes

Regardless of mode, all failures trace back to **Gherkin specifications**:

```
Light Status → Feature Name → Gherkin File → Specification
🔴 API Auth → authentication → features/api-service.feature → @z2-epistemic
```

Both modes provide:
- ✅ Observable health status (green/red/yellow)
- ✅ Source attribution (which service/binary)
- ✅ Specification traceability (which .feature file)
- ✅ Dependency mapping (42i z-axis, domain tags)
