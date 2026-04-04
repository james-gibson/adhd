# ADHD Topology Diagrams

## Simple: Dashboard Only

```
┌──────────────────────────────────┐
│   ADHD Dashboard (TUI)           │
│   • Interactive visualization    │
│   • Lights by service            │
│   • Status: green/red/yellow     │
│   Port: :9090                    │
└──────────────────────────────────┘
           │
           │ HTTP GET/POST
           │ adhd.lights.list
           │ adhd.status
           ▼
   ┌───────────────┐
   │  MCP Lights   │
   │  {name,status}│
   └───────────────┘
```

**Deployment:** One terminal
```bash
./bin/adhd --config ./adhd-example.yaml
```

---

## Two-Tier: Dashboard + Headless

```
┌────────────────────────────────┐
│  ADHD Dashboard (Prime)        │
│  • TUI interface               │
│  • Authority (primary)         │
│  • :9090/mcp                   │
└────────────────────────────────┘
           ▲
           │ MCP push-logs
           │ smoke-alarm.isotope.push-logs
           │ (every 5 seconds)
           │
┌────────────────────────────────┐
│ ADHD Headless (Prime-Plus)     │
│ • Background logging            │
│ • :random/mcp                   │
│ • Buffers + pushes to prime     │
│ • JSONL output                  │
└────────────────────────────────┘
           │
           │ JSONL logs
           │ /tmp/adhd.jsonl
           ▼
      ┌──────────┐
      │ File/Log │
      │ Storage  │
      └──────────┘
```

**Deployment:** Two terminals
```bash
# Terminal 1: Dashboard
./bin/adhd --config ./adhd-example.yaml --mcp-addr :9090

# Terminal 2: Headless
./bin/adhd --headless \
  --mcp-addr :0 \
  --log /tmp/adhd.jsonl \
  --prime-plus \
  --prime-addr http://localhost:9090/mcp
```

---

## Full Stack: With Smoke-Alarm

```
                    ┌──────────────────────┐
                    │   Smoke-Alarm        │
                    │   (MCP Proxy)        │
                    │   :8080/mcp          │
                    │                      │
                    │  • Isotope registry  │
                    │  • Discovery broker  │
                    │  • Status aggregator │
                    └──────┬───────────────┘
                           │
            ┌──────────────┼──────────────┐
            │              │              │
            │              │              │
   ┌────────▼────┐  ┌──────▼──────┐  ┌──▼──────────┐
   │  Dashboard  │  │  Headless 1 │  │ Headless 2  │
   │  (Prime)    │  │(Prime-Plus) │  │(Prime-Plus) │
   │ :9090/mcp   │  │  :random/mcp│  │ :random/mcp │
   └─────────────┘  └─────────────┘  └─────────────┘
         │                │                 │
         │ queries:       │ pushes:         │ pushes:
         │ isotope.list   │ push-logs       │ push-logs
         └────────────────┴─────────────────┘
                          │
                    ┌─────▼──────┐
                    │ Aggregated │
                    │ JSONL Logs │
                    └────────────┘
```

**Deployment:** Three terminals (+ smoke-alarm)
```bash
# Terminal 1: Smoke-Alarm
go run ./cmd/ocd-smoke-alarm serve

# Terminal 2: Dashboard (Prime)
./bin/adhd --config ./adhd-example.yaml --mcp-addr :9090

# Terminal 3: Headless 1 (Prime-Plus, auto-discovers)
./bin/adhd --headless \
  --mcp-addr :0 \
  --log /tmp/adhd-1.jsonl \
  --prime-plus \
  --smoke-alarm http://localhost:8080/mcp

# Terminal 4: Headless 2 (Prime-Plus, auto-discovers)
./bin/adhd --headless \
  --mcp-addr :0 \
  --log /tmp/adhd-2.jsonl \
  --prime-plus \
  --smoke-alarm http://localhost:8080/mcp
```

---

## With Fire-Marshal: Complete System

```
┌─────────────────────────────────────────────────────────┐
│           Monitoring & Service Discovery                │
│                                                         │
│  ┌──────────────┐         ┌──────────────────┐        │
│  │Fire-Marshal  │◄────────│  Smoke-Alarm     │        │
│  │              │ isotope │  (Proxy)         │        │
│  │ :8080/mcp    │ queries │  :9090/mcp       │        │
│  └──────────────┘         └──────────────────┘        │
│        ▲                           ▲                    │
│        │ discovery                 │ registers          │
│        │                           │                    │
└────────┼───────────────────────────┼────────────────────┘
         │                           │
         │                           │
    ┌────┴────────────────────┬──────┴──────┐
    │                         │             │
    │                         │             │
┌───▼──────────┐   ┌─────────▼────┐  ┌────▼────────┐
│   Dashboard  │   │  Headless 1  │  │ Headless 2  │
│   (Prime)    │   │(Prime-Plus)  │  │(Prime-Plus) │
│ :9090/mcp    │   │ :random/mcp  │  │:random/mcp  │
│              │   │              │  │             │
│ • TUI        │   │ • JSONL logs │  │ • JSONL     │
│ • Status     │   │ • Buffers    │  │ • Buffers   │
│ • Authority  │   │ • Push to    │  │ • Push to   │
│              │   │   prime      │  │   prime     │
└──────────────┘   └──────────────┘  └─────────────┘
         ▲                │                │
         │ receives       │ sends          │ sends
         │ push-logs      │ push-logs      │ push-logs
         │                │                │
         └────────────────┴────────────────┘
              Aggregated JSONL
```

**Sequence: Headless Discovering Prime via Smoke-Alarm**

```
Headless starts
    │
    ├─ Register with smoke-alarm
    │  "I'm adhd (prime-plus)"
    │
    ├─ Query smoke-alarm
    │  "Who are the ADHD isotopes?"
    │
    ├─ Smoke-Alarm responds
    │  [{name: adhd, role: prime, endpoint: ...}]
    │
    ├─ Headless auto-discovers prime
    │  address = http://localhost:9090/mcp
    │
    └─ Setup message queue
       destination = discovered prime address
       interval = 5 seconds
```

---

## Data Flow: Single Request

### Dashboard Mode (Direct)

```
User Input (j/k)
    │
    ▼
Dashboard Update Handler
    │
    ├─ Read config
    │ (which binaries/endpoints)
    │
    ├─ HTTP GET each endpoint
    │ (probe health)
    │
    ├─ Parse response
    │ (initialize result)
    │
    ├─ Update light status
    │ (green/red/yellow)
    │
    └─ Render TUI
      (show updated lights)
```

### Headless Mode (Buffered)

```
MCP Request arrives at Headless
    │
    ▼
Log entry created
    │
    ├─ Write to stdout (console)
    │
    ├─ Write to JSONL file
    │ /tmp/adhd.jsonl
    │
    └─ Enqueue to message queue
       │
       ├─ Buffer in memory
       │ (max 1000 entries)
       │
       └─ (Every 5 seconds)
           Push buffered logs to prime
           via smoke-alarm.isotope.push-logs

           On success:
             Clear buffer

           On failure:
             Keep buffer, retry in 5s

           On shutdown:
             Final push attempt
```

---

## Message Flow: Push-Logs

```
Headless (Prime-Plus)
    │
    ├─ Accumulates JSONL logs
    │ (in memory buffer)
    │
    ├─ Periodically (5s)
    │
    └─► HTTP POST to Prime
        {
          "jsonrpc": "2.0",
          "id": 1,
          "method": "smoke-alarm.isotope.push-logs",
          "params": {
            "logs": [
              {timestamp, type, method, ...},
              {timestamp, type, method, ...},
              ...
            ],
            "timestamp": "2026-03-31T18:30:00Z"
          }
        }
        │
        ▼
    Dashboard (Prime) receives
        │
        ├─ Validates JSON-RPC format
        │
        ├─ Stores logs (optional)
        │
        └─► Responds with success
            {
              "jsonrpc": "2.0",
              "id": 1,
              "result": {"received": 42}
            }
            │
            ▼
    Headless clears buffer
    (logs acknowledged)
```

---

## Auto-Discovery Flow

```
Headless Instance Startup
    │
    ├─ Parse flags
    │ --prime-plus (yes, we're secondary)
    │ --prime-addr "" (no explicit address)
    │ --smoke-alarm http://localhost:8080/mcp (discovery enabled)
    │
    ├─ Register with smoke-alarm
    │ POST http://localhost:8080/mcp
    │ {method: "smoke-alarm.isotope.register", params: {role: "prime-plus"}}
    │
    ├─ Query smoke-alarm for isotopes
    │ POST http://localhost:8080/mcp
    │ {method: "smoke-alarm.isotope.list", params: {type: "adhd"}}
    │
    ├─ Parse response
    │ [{name: "adhd", role: "prime", endpoint: "http://localhost:9090/mcp"}]
    │
    ├─ Find prime instance
    │ Filter by role == "prime"
    │
    ├─ Extract address
    │ prime_address = "http://localhost:9090/mcp"
    │
    ├─ Configure message queue
    │ MessageQueue(isPrimePlus=true, primeAddr=prime_address, maxSize=1000)
    │
    └─ Start retry loop
      (attempt push every 5 seconds)
```

---

## Port Assignment

### Fixed Ports (Known Beforehand)
```
Dashboard   :9090/mcp
Fire-Marshal :8080/mcp
Smoke-Alarm :9090 (if separate service)
```

### Dynamic Ports (Auto-Assigned)
```
Headless instance 1  :0  → randomly assigned (e.g., :52341)
Headless instance 2  :0  → randomly assigned (e.g., :52342)
Headless instance 3  :0  → randomly assigned (e.g., :52343)

Avoids conflicts!
```

---

## State Machine: Headless Lifecycle

```
┌──────────────────────────────────┐
│         START                    │
└──────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────┐
│ Parse Config & Flags             │
│ • Determine role (prime-plus?)   │
│ • Read endpoints                 │
│ • Setup logging                  │
└──────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────┐
│ Start MCP Server                 │
│ • Bind to :mcp-addr              │
│ • Listen for requests            │
└──────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────┐
│ If Prime-Plus:                   │
│ • Try auto-discover prime        │
│   (via smoke-alarm)              │
│ OR use explicit prime-addr       │
└──────────────────────────────────┘
           │
    ┌──────┴────────────┐
    │                   │
    │ Success           │ Failure
    ▼                   ▼
 ┌─────────┐     ┌────────────┐
 │ READY   │     │ WAITING    │
 │ (queue) │     │ (retry)    │
 └────┬────┘     └──────┬─────┘
      │                 │
      │    Every 10s    │
      │    retry        │
      │  discovery      │
      │                 │
      └────────┬────────┘
               │
      ┌────────▼────────┐
      │  Signal Handler │
      │ (SIGINT/TERM)   │
      └────────┬────────┘
               │
       ┌───────▼──────────┐
       │ If Prime-Plus:   │
       │ Final push       │
       │ to prime         │
       └───────┬──────────┘
               │
        ┌──────▼────────┐
        │   SHUTDOWN    │
        └───────────────┘
```

---

## Resource Usage

### Dashboard (TUI)
- Memory: ~30MB (lights + config)
- CPU: < 5% (idle), ~2% (active)
- Network: Periodic probes to endpoints
- Ports: 1 (configurable)

### Headless (Logging)
- Memory: ~5MB base + buffer
  - 1KB per buffered log entry
  - At 1000 max = ~5MB additional
- CPU: < 1% (logging only)
- Network: Periodic push to prime
- Ports: 1 (random or configured)

### With 100 Headless Instances
- Memory: ~5GB (5MB × 100)
- Disk: ~100GB for JSONL (1MB per instance/day)
- Network: 100 push operations every 5s
- Port usage: 100 random ports (all available)

---

## Scaling Considerations

### Single Dashboard, Multiple Headless
```
✅ Works great
✓ Dashboard = single authority
✓ Headless instances = secondary collectors
✓ All push to dashboard
✓ Dashboard aggregates logs
```

### Multiple Dashboards (Not Recommended)
```
❌ Conflict risk
✗ Who is authoritative?
✗ Inconsistent state
Recommendation: Use one dashboard, multiple headless
```

### With Message Persistence
```
Dashboard discovers headless failure
  │
  ├─ Headless buffered logs (in memory)
  │
  └─ On recovery:
     Final push flushes buffer
     to dashboard
```
