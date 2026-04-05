# Complete Ecosystem Validation System

## The Vision

ADHD evolves from a simple dashboard to a **comprehensive MCP ecosystem validator** that:

1. **Discovers** what proxy features are needed (proxy auth discovery)
2. **Measures** proxy capability maturity (dashboard metrics)
3. **Monitors** endpoint health in real-time (continuous smoke tests)
4. **Enforces** certification through auto-downgrade (lifecycle management)
5. **Shares** results with the MCP Registry (transparency)

## The System Architecture

```
                    ADHD Ecosystem Validation System

    ┌─────────────────────────────────────────────────────────┐
    │  User-Facing Dashboard (Bubble Tea UI)                  │
    │  ┌─────────────────┐┌────────────┐┌──────────────────┐  │
    │  │ Proxy Maturity  ││ Registry   ││ Certification    │  │
    │  │ (Phases 1-4)    ││ Health     ││ Status (Real-Time)│  │
    │  └─────────────────┘└────────────┘└──────────────────┘  │
    └─────────────────────────────────────────────────────────┘
                              ▲
                              │
    ┌─────────────────────────────────────────────────────────┐
    │  Metrics & State Engine (Internal)                       │
    │  ┌──────────────┐┌────────────┐┌──────────────────────┐ │
    │  │ Test Results ││ Cert Status││ Auto-Downgrade Logic│ │
    │  └──────────────┘└────────────┘└──────────────────────┘ │
    └─────────────────────────────────────────────────────────┘
                              ▲
                              │
    ┌─────────────────────────────────────────────────────────┐
    │  Test Runners (Background Goroutines)                    │
    │  ┌──────────────┐┌────────────┐┌──────────────────────┐ │
    │  │ Direct Tests ││ Proxy Tests││ Smoke Tests (Real)   │ │
    │  │ (Every 5min) ││ (Every 1min││ (Every 5/1/0.5 min) │ │
    │  └──────────────┘└────────────┘└──────────────────────┘ │
    └─────────────────────────────────────────────────────────┘
                              ▲
                              │
    ┌─────────────────────────────────────────────────────────┐
    │  External Resources (Queries)                            │
    │  ┌──────────────┐┌────────────┐┌──────────────────────┐ │
    │  │ MCP Registry ││ Live Servers││ Auth Credentials     │ │
    │  └──────────────┘└────────────┘└──────────────────────┘ │
    └─────────────────────────────────────────────────────────┘
```

## Component 1: Proxy Capability Discovery

**What It Does**: Tests authenticated endpoints through the proxy to find what features are missing.

**Test Files**:
- `proxy-auth-discovery.hurl` — Template for testing auth methods
- `test-mcp-proxy-auth.sh` — Automated scan and discovery

**Output to Dashboard**:
```
Bearer Token Auth        ████░░░░░░ 40%
API-Key Support          ██░░░░░░░░ 20%
OAuth2 Support           ░░░░░░░░░░  0%
```

**Phases**:
1. adhd.proxy handler (501 → 400)
2. Bearer tokens (400 → 200 for bearer servers)
3. API-Key headers (400 → 200 for API-Key servers)
4. OAuth2 (400 → 200 for OAuth2 servers)

---

## Component 2: Registry Health Monitoring

**What It Does**: Tests all public MCP servers and categorizes them by accessibility.

**Test Files**:
- `test-mcp-registry.sh` — Scan registry and test endpoints
- `demo-real-mcp-servers.hurl` — Test template

**Output to Dashboard**:
```
Direct Access (No Auth)           4 servers (3.8%)
Proxied (Bearer Token)           10 servers (9.6%)
Proxied (API-Key)                 4 servers (3.8%)
Requires OAuth2                   2 servers (1.9%)
Offline/Unreachable              84 servers (80.8%)
─────────────────────────────────────────────────
TOTAL ACCESSIBLE                 20/104 (19.2%)
```

**Update Frequency**: Every 6 hours (full registry scan + tests)

---

## Component 3: Real-Time Certification Status

**What It Does**: Continuous monitoring of certified servers to detect regressions.

**Test Files**:
- Built into `internal/smoke_test/runner.go`
- Runs in background goroutine

**Output to Dashboard**:
```
✓ CERTIFIED (18)          🟢 All passing
  ├─ tandem/docs-mcp
  ├─ adadvisor/mcp
  └─ [16 more]

⚠ DEGRADED (1)            🟡 Attention needed
  └─ adramp/google-ads (2/5 tests failing)

✗ UNCERTIFIED (0)         🔴 Removed
```

**Update Frequency**:
- ✓ Certified: Every 5 minutes
- ⚠ Degraded: Every 1 minute
- ✗ Uncertified: Every 10 minutes
- 🟡 Under Review: Every 30 seconds

---

## Component 4: Automatic Downgrade/Upgrade

**What It Does**: Moves endpoints through certification states without manual intervention.

**Rules**:

```
✓ → ⚠   if: 2 out of 5 tests fail
⚠ → ✗   if: 3+ consecutive failures OR 7+ days degraded
✗ → 🟡  if: 1 passing test after failure
🟡 → ✓  if: 10 consecutive passes
```

**Example Timeline**:
```
10:00 UTC: ✓ CERTIFIED (normal)
10:05 UTC: ⚠ DEGRADED (auto-downgrade triggered)
11:45 UTC: ✗ UNCERTIFIED (auto-downgrade triggered)
13:30 UTC: 🟡 UNDER REVIEW (auto-upgrade triggered)
14:45 UTC: ✓ CERTIFIED (auto-upgrade triggered)
```

**No manual review needed**—all automatic.

---

## Integration Points

### With MCP Registry

ADHD reports certification status back to the official registry:

```
ADHD Smoke Tests (continuous)
         ↓
    State Changes
         ↓
  ✓ Certified (18)
  ⚠ Degraded (1)
  ✗ Uncertified (1)
         ↓
   Registry Display
         ├─ ✓ Badge on certified servers
         ├─ ⚠ Warning on degraded
         └─ ✗ Mark as offline
```

Users checking registry see:
- **Certified**: "This server passed smoke tests 5 minutes ago"
- **Degraded**: "This server is having intermittent issues"
- **Uncertified**: "This server is unreachable or failing tests"

### With Developer CI/CD

When a developer creates an MCP server:

```
1. Developer pushes server code
2. CI/CD builds and starts server
3. Tests run:
   ├─ hurl demo-real-mcp-servers.hurl
   ├─ hurl proxy-auth-discovery.hurl
   └─ Smoke tests (5 consecutive)
4. Results:
   ├─ ✓ PASS: Server is MCP-certified
   └─ ✗ FAIL: Details on what failed
5. Server gets badge (or not) in registry
```

### With Operations (Ongoing Monitoring)

```
Every 5 minutes:
  ├─ Run smoke tests on certified servers
  ├─ Check for regressions
  ├─ Update dashboard
  └─ Alert if degradation detected

Every 6 hours:
  ├─ Scan full registry
  ├─ Test all public endpoints
  ├─ Update coverage metrics
  └─ Report to registry

Every 24 hours:
  ├─ Analyze trends
  ├─ Report on flaky servers
  ├─ Recommend improvements
  └─ Escalate critical issues
```

---

## Dashboard Workflow

### Opening ADHD

User opens the ADHD dashboard and sees:

```
┌─ MCP Proxy Capability Maturity ─────────────────────────┐
│ Bearer Token Auth:           ████░░░░░░ 40%             │
│ API-Key Header Auth:         ██░░░░░░░░ 20%             │
│ OAuth2 (deferred):           ░░░░░░░░░░  0%             │
└─────────────────────────────────────────────────────────┘

┌─ MCP Registry Server Coverage ──────────────────────────┐
│ Direct access:               █░░░░░░░░░░ 3.8% (4)       │
│ Proxied (Bearer):            ██░░░░░░░░░ 9.6% (10)      │
│ Proxied (API-Key):           ██░░░░░░░░░ 3.8% (4)       │
│ Requires OAuth2:             ░░░░░░░░░░░ 1.9% (2)       │
│ Total Accessible:            ████░░░░░░ 19.2% (20/104)  │
└─────────────────────────────────────────────────────────┘

┌─ Server Certification Status (Real-Time) ───────────────┐
│ ✓ CERTIFIED (18)             🟢 HEALTHY                 │
│   tandem/docs-mcp              5 min ago ✓✓✓✓✓          │
│   adadvisor/mcp                2 min ago ✓✓✓✓✓          │
│   [16 more]                                              │
│                                                         │
│ ⚠ DEGRADED (1)               🟡 UNSTABLE                │
│   adramp/google-ads            1 min ago ✓✗✓✗✗          │
│   Issue: Bearer token failing                           │
│   Downgrade countdown: 6d 23h                           │
│                                                         │
│ ✗ UNCERTIFIED (0)            🔴 OFFLINE                 │
│   None (all recovered)                                   │
│                                                         │
│ 🟡 UNDER REVIEW (0)          🟠 RECOVERING              │
│   None (none in recovery)                                │
└─────────────────────────────────────────────────────────┘
```

### Key Interactions

**User wants to**:
1. **Know which servers are safe to use**
   → Look at ✓ CERTIFIED list
   → Trust the badge (it's continuously validated)

2. **Debug why a server isn't working**
   → Look at ⚠ DEGRADED section
   → See "Bearer token failing"
   → Recommend fixing token format

3. **Track ecosystem growth**
   → Watch "Total Accessible" grow from 19% → 70% → 90%
   → See proxy phases complete
   → Understand when OAuth2 will be ready

4. **Know when issues are detected**
   → Alerts appear immediately (degradation or failure)
   → Countdown shows days until auto-downgrade
   → Clear action items displayed

---

## Real-World Timeline: Ecosystem Growth

### Week 1: Initial State

```
Dashboard shows:
  Proxy Maturity: 0%
    └─ adhd.proxy not implemented

  Registry Coverage: 20%
    └─ 4 direct servers, 16 blocked by auth

  Status: No proxy features yet
```

### Week 2: Phase 1 Complete

```
Dashboard shows:
  Proxy Maturity: 20%
    └─ adhd.proxy handler implemented

  Registry Coverage: 20%
    └─ Still blocked, but proxy exists

  Status: Ready for bearer token implementation
```

### Week 3: Phase 2 Complete

```
Dashboard shows:
  Proxy Maturity: 40%
    └─ Bearer token auth working

  Registry Coverage: 70%
    └─ 4 direct + 10 proxied = 14 accessible

  Status: Major unlock! 10 more servers accessible
```

### Week 4: Phase 3 Complete

```
Dashboard shows:
  Proxy Maturity: 60%
    └─ API-Key header support added

  Registry Coverage: 90%
    └─ 4 direct + 10 bearer + 4 API-Key = 18 accessible

  Status: Almost full coverage

  Degradation Alert: ⚠ adramp momentarily unstable
    └─ Detected within 5 minutes
    └─ Auto-recovered
```

### Week 5+: Ongoing Monitoring

```
Dashboard shows:
  Proxy Maturity: 60%
    └─ Phase 3 complete, Phase 4 deferred

  Registry Coverage: 90% (stable)
    └─ 18/20 accessible
    └─ 2 require OAuth2 (not yet needed)

  Certification Trend: Smooth
    └─ No regressions detected
    └─ All servers stable

  Next Goal: OAuth2 support when business asks for it
```

---

## Benefits of Complete System

| Aspect | Old (No System) | New (Complete System) |
|--------|-----------------|----------------------|
| **Discovering what to build** | Guessing | Test failures tell us |
| **Measuring progress** | Hidden | Dashboard shows % complete |
| **Endpoint reliability** | Unknown | Real-time status visible |
| **Regression detection** | Manual inspection | Auto-detected in 5 min |
| **False confidence** | Badge shown once forever | Badge shows current health |
| **Transparency** | Users don't know status | Registry shows live status |
| **Automation** | Manual review | Auto-downgrade/upgrade |

---

## Next Implementation Steps

1. **Immediate**: Add dashboard panels for metrics display
2. **Week 1**: Implement smoke test runner (background goroutine)
3. **Week 2**: Implement auto-downgrade/upgrade logic
4. **Week 3**: Connect to MCP Registry API for reporting
5. **Week 4**: Add alerting (Slack, Pagerduty, etc.)

---

## The Vision Realized

ADHD transforms from:

**A dashboard that shows cluster status**

To:

**An ecosystem validator that:**
- Discovers what features to build (proxy auth discovery)
- Measures progress toward full capability (dashboard metrics)
- Continuously validates server health (smoke tests)
- Automatically maintains trust (certification lifecycle)
- Shares results with the ecosystem (registry integration)

**Result**: A transparent, measurable, automated system for validating the entire MCP ecosystem.
