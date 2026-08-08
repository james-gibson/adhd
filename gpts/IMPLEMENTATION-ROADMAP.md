# Implementation Roadmap: What Works Now vs What's Needed

## Summary: Testing & Design Complete, Implementation In Progress

### ✓ What Works Right Now (With Demo Cluster)

#### 1. Registry Server Discovery
```bash
bash tests/hurl/test-mcp-registry.sh
```
**Status**: ✓ Working
**What it does**: Scans the official MCP Registry, finds 20 servers with public endpoints, tests each one
**Results from earlier run**:
- ✓ 4 certified (HTTP 200)
- ✗ 14 failed (HTTP 401/404/406 - need auth)
- ⏳ 2 offline

**Can run**: ✓ Yes, right now

---

#### 2. Direct MCP Server Testing
```bash
hurl --variable endpoint=http://localhost:60460/mcp \
  tests/hurl/demo-real-mcp-servers.hurl
```
**Status**: ✓ Working
**What it does**: Tests if an endpoint is MCP-compliant
**Can run**: ✓ Yes, if demo cluster is running

---

#### 3. Negative Testing (Non-MCP Detection)
```bash
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
```
**Status**: ✓ Working
**What it does**: Verifies we correctly reject non-MCP endpoints
**Results**: example.com → 405 (correctly rejected)
**Can run**: ✓ Yes, right now

---

### ⏳ What's Designed But Not Implemented

#### 1. Proxy Auth Discovery (Core Missing Piece)
```bash
hurl --variable proxy_endpoint=http://localhost:60460 \
     --variable auth_endpoint=https://api.adramp.ai/mcp \
     --variable auth_token="$TOKEN" \
  tests/hurl/proxy-auth-discovery.hurl
```
**Status**: ✗ Not implemented
**What's missing**: `adhd.proxy` MCP endpoint
**Current result**: Would return 501 Method not found
**Next step**: Implement in `internal/mcpserver/server.go`

---

#### 2. Dashboard Visualization
```
Proxy Capability Maturity
  Bearer Token:    ████░░░░░░ 40%
  API-Key:         ██░░░░░░░░ 20%
  OAuth2:          ░░░░░░░░░░  0%

Registry Coverage
  Direct (4):      ███░░░░░░░░░░░░░░░░░░░
  Proxied (14):    ██████░░░░░░░░░░░░░░░░

Certification Status (Real-Time)
  ✓ 18 Certified   🟢 Healthy
  ⚠ 1 Degraded     🟡 Needs attention
  ✗ 0 Uncertified  🔴 Offline
```
**Status**: ✗ Not implemented (design only)
**What's missing**: Dashboard view implementation in `internal/dashboard/view.go`
**What we have**: Complete spec in `internal/dashboard/DASHBOARD-METRICS.md`
**Next step**: Code the three panels

---

#### 3. Smoke Test Runner (Continuous Monitoring)
**Status**: ✗ Not implemented
**What it does**:
- Every 5 minutes: Test certified servers
- Every 1 minute: Test degraded servers
- Every 30 seconds: Test under-review servers
**What's missing**: `internal/smoke_test/runner.go`
**Next step**: Implement background goroutine

---

#### 4. Auto-Downgrade/Upgrade Logic
**Status**: ✗ Not implemented
**What it does**:
```
✓ CERTIFIED  → ⚠ DEGRADED  if: 2 out of 5 tests fail
⚠ DEGRADED  → ✗ UNCERTIFIED if: 3+ consecutive fail
✗ UNCERTIFIED → 🟡 UNDER REVIEW if: 1 pass
🟡 UNDER REVIEW → ✓ CERTIFIED if: 10 consecutive passes
```
**What's missing**: `internal/smoke_test/lifecycle.go`
**Next step**: Implement state machine

---

## Implementation Phases

### Phase 0: Foundation (Testing/Design) ✓ COMPLETE

**Files Created**:
- ✓ proxy-auth-discovery.hurl (test template)
- ✓ proxy-auth-discovery.feature (Gherkin spec)
- ✓ DASHBOARD-METRICS.md (dashboard design)
- ✓ certification-lifecycle.feature (rules spec)
- ✓ ECOSYSTEM-VALIDATION-SYSTEM.md (architecture)

**What you can do right now**:
```bash
# Test direct access
hurl --variable endpoint=http://localhost:60460/mcp \
  tests/hurl/demo-real-mcp-servers.hurl

# Scan the official registry
bash tests/hurl/test-mcp-registry.sh

# Test rejection of non-MCP
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
```

---

### Phase 1: Proxy Auth Infrastructure (Needs Implementation)

**Work needed**:
```go
// internal/mcpserver/server.go
func (s *MCPServer) handleToolsCall(params interface{}) (interface{}, error) {
  // Already exists for basic tools
  // Need to add: adhd.proxy handler

  // New method needed:
  case "adhd.proxy":
    return s.handleProxyCall(params)
}

// New file: internal/proxy/proxy.go
type ProxyRequest struct {
  TargetEndpoint string
  Auth           AuthConfig
  Call           interface{}
}

func (p *Proxy) ExecuteProxy(req ProxyRequest) (interface{}, error) {
  // 1. Parse auth config
  // 2. Build HTTP request to target with auth
  // 3. Forward MCP call
  // 4. Return result
}
```

**Effort**: ~4-8 hours
**Blockers**: None

**Validation**:
```bash
# Will start returning 400 instead of 501
hurl --variable proxy_endpoint=http://localhost:60460 \
  tests/hurl/proxy-auth-discovery.hurl
```

---

### Phase 2: Bearer Token Support

**Work needed**:
```go
// internal/proxy/auth.go
type BearerAuth struct {
  Token string
}

func (b *BearerAuth) AddHeaders(req *http.Request) error {
  req.Header.Set("Authorization", "Bearer " + b.Token)
  return nil
}
```

**Effort**: ~2-4 hours
**Blockers**: Phase 1 must be complete

**Impact**: Unlocks ~10 servers from the registry

**Validation**:
```bash
# Will return 200 for bearer-auth servers (like AdRamp)
ADRAMP_TOKEN="your-token" hurl \
  --variable proxy_endpoint=http://localhost:60460 \
  --variable auth_endpoint=https://api.adramp.ai/mcp \
  --variable auth_token=$ADRAMP_TOKEN \
  tests/hurl/proxy-auth-discovery.hurl
```

---

### Phase 3: Dashboard Implementation

**Work needed**:
```go
// internal/dashboard/view.go
func (m *Model) renderCertificationPanel() string {
  // Panel 1: ProxyMaturity
  section1 := m.renderProxyMaturity()

  // Panel 2: RegistryHealth
  section2 := m.renderRegistryHealth()

  // Panel 3: CertificationStatus
  section3 := m.renderCertificationStatus()

  return lipgloss.JoinVertical(lipgloss.Top, section1, section2, section3)
}
```

**Effort**: ~8-12 hours (3 panels × rendering logic)
**Blockers**: None (can run in parallel with proxy work)

**Validation**: Open ADHD dashboard and see metrics panels

---

### Phase 4: Smoke Test Runner

**Work needed**:
```go
// internal/smoke_test/runner.go
type SmokeTestRunner struct {
  endpoints map[string]*Endpoint
  metrics   CertificationMetrics
  ticker    *time.Ticker
}

func (r *SmokeTestRunner) Start() {
  go r.runTests()
}

func (r *SmokeTestRunner) runTests() {
  for {
    for name, endpoint := range r.endpoints {
      freq := r.getTestFrequency(endpoint.Status)
      if shouldTest(endpoint.LastChecked, freq) {
        result := r.testEndpoint(endpoint)
        r.updateMetrics(name, result)
      }
    }
    time.Sleep(30 * time.Second)
  }
}
```

**Effort**: ~6-10 hours
**Blockers**: Phase 3 (metrics) should be done first

**Validation**: Metrics update in real-time on dashboard

---

### Phase 5: Auto-Downgrade/Upgrade Logic

**Work needed**:
```go
// internal/smoke_test/lifecycle.go
func (r *SmokeTestRunner) checkDowngrades() {
  for name, endpoint := range r.endpoints {
    recentTests := endpoint.RecentTests[-5:]
    failures := countFailing(recentTests)

    switch endpoint.Status {
    case Certified:
      if failures >= 2 {
        r.downgrade(name, Degraded, "2/5 tests failing")
      }
    case Degraded:
      if failures >= 3 {
        r.downgrade(name, Uncertified, "3+ consecutive failures")
      }
    // ... etc
    }
  }
}
```

**Effort**: ~4-6 hours
**Blockers**: Phase 4 (metrics) must be done

**Validation**: Watch endpoints auto-upgrade/downgrade

---

## Current State Summary

### What You Can Do RIGHT NOW

```bash
# 1. Test the official MCP registry
bash tests/hurl/test-mcp-registry.sh
# Result: 20 servers found, 4 certified, 14 blocked by auth

# 2. Test direct MCP compliance
hurl --variable endpoint=http://localhost:60460/mcp \
  tests/hurl/demo-real-mcp-servers.hurl
# Result: ✓ PASS if demo cluster is running

# 3. Verify non-MCP rejection
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
# Result: ✓ PASS (correctly rejects)
```

### What You Can't Do Yet

```bash
# 1. Proxy auth discovery (adhd.proxy not implemented)
hurl --variable proxy_endpoint=http://localhost:60460 \
  tests/hurl/proxy-auth-discovery.hurl
# Result: 501 Method not found (expected)

# 2. See dashboard metrics (not rendered yet)
# Result: Not visible in ADHD dashboard

# 3. Auto-downgrade endpoints (not implemented)
# Result: Manual status management only
```

---

## Next Action: What Should We Build First?

### Option A: Proxy Auth (Phase 1-2)
**Timeline**: 1-2 weeks
**Impact**: Unlock 10+ additional servers from registry
**Why first**: Enables the discovery pattern to actually work

### Option B: Dashboard (Phase 3)
**Timeline**: 1 week
**Impact**: Visualize current state and progress
**Why first**: Makes the work visible, easier to track progress

### Option C: Smoke Tests (Phase 4)
**Timeline**: 1-2 weeks
**Impact**: Continuous monitoring of endpoint health
**Why first**: Foundation for auto-downgrade/upgrade

**Recommendation**: Start with **Phase 1 (Proxy)** + **Phase 3 (Dashboard)** in parallel
- Proxy implementation unlocks the discovery tests
- Dashboard implementation shows the progress
- Together they validate the entire strategy works

---

## Files Ready to Implement From

All these specs are complete and ready to code from:

- `tests/hurl/proxy-auth-discovery.hurl` — Test template
- `internal/dashboard/DASHBOARD-METRICS.md` — Dashboard spec with exact panel layouts
- `features/adhd/certification-lifecycle.feature` — Acceptance criteria for auto-transitions
- `features/adhd/proxy-auth-discovery.feature` — Implementation phases
- `tests/hurl/ECOSYSTEM-VALIDATION-SYSTEM.md` — Architecture reference

Each one has exact specifications, example outputs, and success criteria.
