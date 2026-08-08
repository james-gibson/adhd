# ADHD: Getting Started Guide

Welcome! This guide helps you launch ADHD (the distributed monitoring system) for the first time.

## 📚 Documentation Structure

**Start here for your use case:**

### 🚀 I Want to Run It Now
1. **[QUICK_START.md](QUICK_START.md)** (5 min read)
   - Copy-paste commands
   - Common scenarios
   - Troubleshooting basics
   - **→ Start here if you're in a hurry**

### 🎯 I Want a Step-by-Step Walkthrough
2. **[DEMO_GUIDE.md](DEMO_GUIDE.md)** (15 min read)
   - Detailed setup instructions
   - What to expect at each step
   - Verification checklist
   - Full troubleshooting guide
   - **→ Start here if you're new to ADHD**

### 🏗️ I Want to Understand the Architecture
3. **[TOPOLOGY.md](TOPOLOGY.md)** (20 min read)
   - Visual system diagrams
   - Data flow illustrations
   - Message sequences
   - Scaling considerations
   - **→ Start here if you're designing something**

### 🔧 I Want Headless-Specific Features
4. **[HEADLESS_MODE.md](HEADLESS_MODE.md)** (10 min read)
   - Headless-only commands
   - Buffer configuration
   - Prime-plus topology
   - Multi-instance setup
   - **→ Start here if you need background logging**

### 🏛️ I Want to Understand the Full Design
5. **[ARCHITECTURE.md](ARCHITECTURE.md)** (10 min read)
   - Two deployment models (Dashboard vs Headless)
   - TUI integration
   - Smoke-alarm proxy pattern
   - Isotope registration
   - **→ Start here if you're contributing code**

---

## ⚡ The 2-Minute Version

### What is ADHD?

A distributed monitoring system with two modes:

**Dashboard (TUI)** - Interactive visualization in your terminal
```bash
./bin/adhd --config ./adhd-example.yaml
```

**Headless (Logging)** - Background MCP traffic logger
```bash
./bin/adhd --headless --log /tmp/adhd.jsonl
```

**Together** - Auto-discover and sync topology
```bash
# Terminal 1
./bin/adhd --config ./adhd-example.yaml

# Terminal 2
./bin/adhd --headless --mcp-addr :0 --log /tmp/adhd.jsonl \
  --prime-plus --prime-addr http://localhost:9090/mcp
```

### Why Use It?

- **Dashboard** shows real-time health of MCP services
- **Headless** buffers logs and pushes to dashboard
- **Together** provide complete monitoring topology
- **Auto-discovery** via smoke-alarm (no config needed)
- **Fault-tolerant** - headless buffers if prime is down

---

## 🎬 Choose Your Demo

### Option 1: Demo Script (Recommended)
```bash
./demo.sh
# Select: 3 (Full demo)
# Opens 3 terminals automatically
```

### Option 2: Manual (Learn Step-by-Step)
Read [DEMO_GUIDE.md](DEMO_GUIDE.md) and follow the steps manually.

### Option 3: Quick Hands-On (5 min)
Follow [QUICK_START.md](QUICK_START.md)

---

## 🔧 Build & Prerequisites

### Build ADHD
```bash
cd /Users/james/src/prototypes/adhd
go build -o ./bin/adhd ./cmd/adhd
```

### Verify Build
```bash
./bin/adhd -v
# Output: adhd (version dev)
```

### Check Dependencies
```bash
# Config file
ls -la ./adhd-example.yaml

# Demo script
ls -la ./demo.sh
chmod +x ./demo.sh
```

---

## 🚀 Three Ways to Launch

### Way 1: Automated (Recommended for First-Time)
```bash
./demo.sh
# Choose option 3 (Full demo in separate terminals)
```

### Way 2: Manual Two-Terminal
```bash
# Terminal 1: Dashboard
./bin/adhd --config ./adhd-example.yaml

# Terminal 2: Headless
./bin/adhd --headless --mcp-addr :0 --log /tmp/adhd.jsonl \
  --prime-plus --prime-addr http://localhost:9090/mcp
```

### Way 3: Single Terminal (Dashboard Only)
```bash
./bin/adhd --config ./adhd-example.yaml
```

---

## ✅ Verification Checklist

After launch, verify each component:

### Dashboard Running?
```bash
curl -s http://localhost:9090/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.status","params":{}}' | jq .
```
✅ Should show: `"summary": {"total": X, "green": X, ...}`

### Headless Running?
```bash
ls -lh /tmp/adhd.jsonl
```
✅ Should exist and grow in size

### Logs Appearing?
```bash
tail /tmp/adhd.jsonl | jq .
```
✅ Should show JSONL with timestamp, type, method, etc.

### Auto-Discovery Working? (Optional)
```bash
# Check if headless found prime
curl -s http://localhost:RANDOM_PORT/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"adhd.isotope.peers","params":{}}' \
  -H "Content-Type: application/json" | jq .result.peers
```
✅ Should show prime instance in peers list

---

## 🐛 Troubleshooting Quick Links

### Problem: "address already in use"
→ See [QUICK_START.md](QUICK_START.md#port-conflicts)

### Problem: Headless doesn't discover prime
→ See [DEMO_GUIDE.md](DEMO_GUIDE.md#headless-doesnt-discover-prime)

### Problem: No lights appearing
→ See [DEMO_GUIDE.md](DEMO_GUIDE.md#dashboard-lights-not-updating)

### Problem: Logs not appearing
→ See [DEMO_GUIDE.md](DEMO_GUIDE.md#logs-not-appearing-in-headless)

### More issues?
→ Full troubleshooting in [DEMO_GUIDE.md](DEMO_GUIDE.md#troubleshooting)

---

## 📖 Learning Path

### Day 1: Get It Running
1. Read [QUICK_START.md](QUICK_START.md) (5 min)
2. Run `./demo.sh` (3 min)
3. Verify it works (2 min)

### Day 2: Understand What You're Running
1. Read [TOPOLOGY.md](TOPOLOGY.md) - Visual diagrams (10 min)
2. Read [ARCHITECTURE.md](ARCHITECTURE.md) - Design rationale (10 min)
3. Experiment with CLI flags (10 min)

### Day 3: Use Advanced Features
1. Read [HEADLESS_MODE.md](HEADLESS_MODE.md) (10 min)
2. Setup multi-instance with your services (30 min)
3. Integrate with smoke-alarm if available (30 min)

### Week 2: Production Setup
1. Update `adhd-example.yaml` with your endpoints
2. Setup persistent JSONL logging
3. Integrate into your monitoring stack

---

## 🔑 Key Concepts (30-Second Summary)

| Concept | Explanation |
|---------|-------------|
| **Dashboard** | TUI mode - interactive visual monitoring on your terminal |
| **Headless** | Logging mode - background MCP traffic recorder |
| **Prime** | The authoritative instance (usually dashboard) |
| **Prime-Plus** | Secondary instance (usually headless) that pushes logs to prime |
| **Isotope** | ADHD instance registered with smoke-alarm for discovery |
| **Auto-Discovery** | Headless finds prime via smoke-alarm (no hardcoding needed) |
| **Light** | A status indicator for one feature (🟢 green = OK) |
| **MCP** | Model Context Protocol - the wire protocol ADHD uses |
| **JSONL** | JSON Lines - headless log format (one JSON per line) |
| **Message Queue** | Buffer for logs when prime is unavailable |

---

## 🎯 Common Use Cases

### Use Case 1: Monitor Service Health
```bash
./bin/adhd --config ./adhd-example.yaml
# See dashboard with lights reflecting real-time service status
```
→ See [QUICK_START.md](QUICK_START.md#i-want-to-see-the-dashboard)

### Use Case 2: Log All MCP Traffic
```bash
./bin/adhd --headless --log /tmp/traffic.jsonl
# All requests/responses recorded as JSONL
```
→ See [QUICK_START.md](QUICK_START.md#i-want-background-logging)

### Use Case 3: Dashboard + Headless Together
```bash
# Two instances: one visual, one logging
# Headless auto-buffers and pushes to dashboard
```
→ See [QUICK_START.md](QUICK_START.md#i-want-dashboard--headless-together)

### Use Case 4: Centralized Monitoring
```bash
# Multiple headless instances → one dashboard
# Via smoke-alarm auto-discovery
```
→ See [HEADLESS_MODE.md](HEADLESS_MODE.md#monitoring-multiple-services)

---

## 📋 Pre-Launch Checklist

- [ ] ADHD binary built: `./bin/adhd -v` works
- [ ] Config file exists: `ls adhd-example.yaml`
- [ ] Demo script executable: `ls -x demo.sh`
- [ ] Port 9090 available: `lsof -i :9090` (empty)
- [ ] /tmp writable: `touch /tmp/test.txt` works
- [ ] You have 2 terminals open (or 3 for full demo)

---

## 🆘 Getting Help

### Check These First
1. **Documentation** - Start with doc for your use case above
2. **Demo logs** - Look at actual error messages
3. **Debug flag** - Add `--debug` for verbose output

### Run with Debug
```bash
./bin/adhd --headless --debug --log /tmp/adhd.jsonl
# Shows detailed operations
```

### Check System
```bash
# Ports in use
lsof -i :9090

# Processes running
ps aux | grep adhd

# Log files
ls -lh /tmp/adhd*.jsonl

# Config
cat ./adhd-example.yaml | head -20
```

---

## 🎓 What to Learn Next

Once demo is working:

1. **Modify config** - Point to your real MCP endpoints
2. **Explore CLI flags** - `./bin/adhd --help`
3. **Integrate with CI/CD** - Use headless for test monitoring
4. **Setup alerting** - Parse JSONL for metrics
5. **Contribute** - See [CLAUDE.md](CLAUDE.md) for guidelines

---

## 📞 Quick Reference

| Task | Command |
|------|---------|
| See help | `./bin/adhd --help` |
| Check version | `./bin/adhd -v` |
| Run demo | `./demo.sh` |
| Dashboard only | `./bin/adhd --config ./adhd-example.yaml` |
| Headless only | `./bin/adhd --headless --log /tmp/adhd.jsonl` |
| Both together | See QUICK_START.md |
| Debug output | Add `--debug` flag |
| Random port | Use `--mcp-addr :0` |
| Query status | `curl http://localhost:9090/mcp` (see DEMO_GUIDE.md) |

---

## 🎉 Ready?

**Pick one:**

- ⚡ **I want it running NOW** → [QUICK_START.md](QUICK_START.md)
- 🎯 **I want step-by-step guide** → [DEMO_GUIDE.md](DEMO_GUIDE.md)
- 🏗️ **I want to understand architecture** → [TOPOLOGY.md](TOPOLOGY.md)
- 🔧 **I want advanced features** → [HEADLESS_MODE.md](HEADLESS_MODE.md)

**Or just run:**
```bash
./demo.sh
```

---

Happy monitoring! 🚀
