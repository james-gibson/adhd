# ADHD Dashboard: Testing Metrics & Certification Lifecycle

## Overview

The ADHD dashboard displays three key metrics:

1. **Proxy Capability Maturity** — What features are implemented
2. **Registry Health** — How many servers are certified/accessible
3. **Certification Lifecycle** — Real-time regression detection

## Dashboard Panel 1: Proxy Maturity

```
┌─ MCP Proxy Capability Maturity ───────────────────────┐
│                                                        │
│ Bearer Token Auth                                      │
│   ████████░░ 80% (4/5 features)                       │
│   ✓ Token parsing                                      │
│   ✓ Authorization header injection                     │
│   ✓ Upstream auth passing                              │
│   ✓ Error handling                                     │
│   ⏳ Token validation/refresh                          │
│                                                        │
│ API-Key Header Auth                                    │
│   ██░░░░░░░░ 20% (1/5 features)                       │
│   ✓ Header format recognized                          │
│   ⏳ Custom header names                               │
│   ⏳ Multiple keys support                             │
│   ⏳ Key rotation                                      │
│   ⏳ Rate limiting awareness                           │
│                                                        │
│ OAuth2 Auth                                            │
│   ░░░░░░░░░░ 0% (0/5 features)                        │
│   ⏳ Authorization code flow                           │
│   ⏳ Token endpoint                                    │
│   ⏳ Token refresh                                     │
│   ⏳ Scope management                                  │
│   ⏳ PKCE support                                      │
│                                                        │
└────────────────────────────────────────────────────────┘
```

**Metrics Displayed**:
- Phase completion (%)
- Individual feature status
- Last updated
- Next phase ETA

**Updates**: Every 24 hours (automatic feature tracking)

---

## Dashboard Panel 2: Registry Server Coverage

```
┌─ MCP Registry Server Certification Status ─────────────┐
│                                                         │
│ Total Servers in Registry: 104                          │
│                                                         │
│ Direct Access (No Auth)                                 │
│   ███░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  │
│   4 servers (3.8%)                                      │
│   ✓ tandem, adadvisor, agentrapay, contabo             │
│                                                         │
│ Proxied (Bearer Token)                                  │
│   ██████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  │
│   10 servers (9.6%)                                     │
│   ✓ adramp, bezal, aDvisor, lona, ...                  │
│                                                         │
│ Proxied (API-Key)                                       │
│   ██░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  │
│   4 servers (3.8%)                                      │
│   ✓ custom-header-servers, ...                         │
│                                                         │
│ Requires OAuth2                                         │
│   ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  │
│   2 servers (1.9%)                                      │
│   ⏳ oauth2-servers                                    │
│                                                         │
│ Offline/Unreachable                                     │
│   ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  │
│   84 servers (80.8%)                                    │
│   ⏳ No public endpoint or down                         │
│                                                         │
│ ═══════════════════════════════════════════════════════ │
│ TOTAL ACCESSIBLE: 18/104 (17.3%)                        │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

**Metrics Displayed**:
- Per-authentication-type breakdown
- Server list per category
- Total coverage percentage
- Last scan date/time

**Updates**: Every 6 hours (registry scan + smoke tests)

---

## Dashboard Panel 3: Certification Lifecycle

```
┌─ Server Certification Status (Real-Time Monitoring) ───┐
│                                                         │
│ Certified Servers: 18/20 tested                         │
│                                                         │
│ ✓ CERTIFIED (Passing all tests)                         │
│   ├─ tandem/docs-mcp               🟢 HEALTHY          │
│   │  Last checked: 2 min ago        HTTP 200            │
│   │  Trend: ════════ (stable)       5 consecutive ✓    │
│   │                                                     │
│   ├─ adadvisor/mcp-server           🟢 HEALTHY          │
│   │  Last checked: 1 min ago        HTTP 200            │
│   │  Trend: ════════ (stable)       8 consecutive ✓    │
│   │                                                     │
│   └─ [16 more certified servers]                        │
│                                                         │
│ ⚠  DEGRADED (Some tests failing)                        │
│   ├─ adramp/google-ads              🟡 UNSTABLE         │
│   │  Last checked: 30 sec ago       HTTP 401/200 mixed  │
│   │  Trend: ═════╱ (declining)      3/5 last tests ✗   │
│   │  Issue: Bearer token validation failing             │
│   │  Action: Investigating token format                 │
│   │  Recommendation: Downgrade to -42i next scan?       │
│   │                                                     │
│   └─ lona/trading                   🟡 UNSTABLE         │
│      Last checked: 45 sec ago       HTTP 401/403 mixed  │
│      Trend: ══════╱ (degrading)     2/5 last tests ✗   │
│      Issue: Rate limiting or scope issues               │
│      Action: Contact lona.agency, check credentials     │
│                                                         │
│ ✗ UNCERTIFIED (Failing smoke tests)                     │
│   ├─ atars/crypto-mcp               🔴 OFFLINE          │
│   │  Last checked: 5 min ago        HTTP 000 (timeout)  │
│   │  Trend: ══════╲ (failing)       5 consecutive ✗     │
│   │  Downgrade from ✓ to ✗ at 2026-04-05 11:23 UTC     │
│   │  Reason: 100% failure rate (10 consecutive tests)   │
│   │  Action: Remove from registry until restored        │
│   │                                                     │
│   └─ agentdm/agentdm                🔴 DEGRADED         │
│      Last checked: 3 min ago        HTTP 401            │
│      Trend: ══╱╱╱ (failing)         0/3 last tests ✓    │
│      Certificate expires: 2026-04-12                    │
│      Action: Will auto-downgrade in 7 days if not fixed │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

**Metrics Displayed**:
- Real-time health status (🟢 🟡 🔴)
- Last check time
- Trend sparkline (stable/declining/improving)
- Recent test results (✓/✗ sequence)
- Downgrade timeline
- Recommended actions

**Updates**: Every 2-5 minutes (continuous smoke tests)

---

## Certification Lifecycle

### State Transitions

```
States: ✓ Certified → ⏳ Degraded → ✗ Uncertified → 🟡 Under Review → ✓ Certified

✓ CERTIFIED
  └─ Requirements:
     • Passed all 5 most recent smoke tests
     • Response time < 2s average
     • Uptime > 99% in last 24 hours
     • Valid authentication tokens

  └─ Auto-downgrade to ⏳ DEGRADED if:
     • 2 out of 5 tests fail
     • Response time > 3s
     • Uptime drops below 95%

⏳ DEGRADED
  └─ Requirements:
     • 0-2 tests failing (not all failing)
     • Some connectivity confirmed
     • Likely temporary auth/rate-limit issue

  └─ Auto-downgrade to ✗ UNCERTIFIED if:
     • 3+ consecutive tests fail
     • 10+ total failures in 24h
     • Status doesn't improve in 7 days

✗ UNCERTIFIED
  └─ Reasons:
     • 100% test failure rate
     • Offline for > 1 hour
     • Removed from registry

  └─ Auto-upgrade to 🟡 UNDER REVIEW if:
     • 1 passing test after extended failure
     • Registry re-adds the endpoint

🟡 UNDER REVIEW
  └─ Requirements:
     • Re-validation in progress
     • Increased test frequency (every 1 min)
     • Must pass 10 consecutive tests

  └─ Auto-upgrade to ✓ CERTIFIED if:
     • 10+ consecutive passing tests
     • Response times stable
     • Authentication confirmed working
```

### Auto-Downgrade Rules

| Condition | Current State | New State | Action |
|-----------|---------------|-----------|--------|
| 2/5 tests fail | Certified | Degraded | Alert team, increase test frequency |
| 3+ consecutive fail | Degraded | Uncertified | Remove from trusted list, notify |
| 100% failure × 10 min | Any | Uncertified | Remove from registry candidates |
| No recovery in 7 days | Degraded | Uncertified | Archive, escalate to provider |
| 1 pass after failure | Uncertified | Under Review | Begin re-validation |
| 10 consecutive pass | Under Review | Certified | Restore to full certification |

### Real-World Example

```
Timeline: AdRamp Server Certification Lifecycle

2026-04-05 10:00 UTC
  Status: ✓ CERTIFIED
  Tests: ✓✓✓✓✓ (5/5 passing)
  Coverage: Unlocked via proxy

2026-04-05 11:23 UTC
  Status: ⚠ DEGRADED
  Tests: ✓✗✓✗✗ (2/5 passing)
  Reason: Bearer token validation failing intermittently
  Action: Alert team, investigate token format
  Alert: "AdRamp auth unstable, may degrade to uncertified"

2026-04-05 12:45 UTC
  Status: ✗ UNCERTIFIED
  Tests: ✗✗✗✗✗ (0/5 passing)
  Reason: 100% failure rate for 2+ hours
  Action: Auto-downgrade complete
  Alert: "AdRamp removed from certified servers"

2026-04-05 14:30 UTC
  Status: 🟡 UNDER REVIEW
  Tests: ✓✗✗✗✗ → ✓✓✓✗✗ → ✓✓✓✓✗ (progression)
  Reason: Token format corrected by provider
  Action: Increased monitoring (every 1 min), re-validation
  Progress: 1/10 → 4/10 needed for certification

2026-04-05 16:15 UTC
  Status: ✓ CERTIFIED
  Tests: ✓✓✓✓✓✓✓✓✓✓ (10+ consecutive)
  Reason: 100% success after token fix
  Action: Auto-upgrade to certified
  Alert: "AdRamp restored to full certification"
```

---

## Dashboard Implementation

### Data Sources

```go
// adhd/internal/dashboard/metrics.go

type CertificationMetrics struct {
  // Proxy capability
  ProxyPhase1      FeatureStatus
  ProxyPhase2      FeatureStatus
  ProxyPhase3      FeatureStatus
  ProxyPhase4      FeatureStatus

  // Registry coverage
  DirectAccessCount    int
  ProxiedBearerCount   int
  ProxiedAPIKeyCount   int
  ProxiedOAuth2Count   int
  OfflineCount         int

  // Certification status per server
  Servers map[string]*ServerCertification
}

type ServerCertification struct {
  Name              string
  Status            CertStatus      // Certified, Degraded, Uncertified, UnderReview
  LastChecked       time.Time
  LastPassTime      time.Time
  LastFailTime      time.Time
  RecentTests       []TestResult    // Last 5-10 tests
  TrendLine         string          // Sparkline: ════╱╲
  DowngradeIfAfter  time.Time       // Auto-downgrade date
  HTTPStatus        int
  ResponseTime      time.Duration
}

type CertStatus string
const (
  StatusCertified   CertStatus = "✓"
  StatusDegraded    CertStatus = "⚠"
  StatusUncertified CertStatus = "✗"
  StatusUnderReview CertStatus = "🟡"
)
```

### Rendering in Bubble Tea

```go
// adhd/internal/dashboard/view.go

func (m *Model) renderCertificationPanel() string {
  // Panel 1: Proxy Maturity
  section1 := m.renderProxyMaturity()

  // Panel 2: Registry Health
  section2 := m.renderRegistryHealth()

  // Panel 3: Certification Lifecycle
  section3 := m.renderCertificationStatus()

  return lipgloss.JoinVertical(
    lipgloss.Top,
    section1,
    section2,
    section3,
  )
}

func (m *Model) renderCertificationStatus() string {
  certified := []ServerCertification{}
  degraded := []ServerCertification{}
  uncertified := []ServerCertification{}

  for _, srv := range m.metrics.Servers {
    switch srv.Status {
    case StatusCertified:
      certified = append(certified, *srv)
    case StatusDegraded:
      degraded = append(degraded, *srv)
    case StatusUncertified:
      uncertified = append(uncertified, *srv)
    }
  }

  // Render with colors:
  // ✓ Certified = green
  // ⚠ Degraded = yellow
  // ✗ Uncertified = red
  // 🟡 Under Review = blue
}
```

---

## Update Loop

### Smoke Test Frequency

```
Schedule by Status:

✓ Certified servers
  └─ Every 5 minutes
  └─ Alert if fails

⚠ Degraded servers
  └─ Every 1 minute
  └─ Track recovery

✗ Uncertified servers
  └─ Every 10 minutes
  └─ Check for recovery

🟡 Under Review servers
  └─ Every 30 seconds
  └─ Rapid re-validation
```

### Auto-Downgrade Job

```go
// adhd/internal/smoke_test/auto_downgrade.go

func (s *SmokeTestRunner) CheckDowngrades() {
  for _, srv := range s.metrics.Servers {
    // Count recent failures
    recentTests := srv.RecentTests[-10:]
    failures := count(recentTests, Test.Failed)

    switch srv.Status {
    case StatusCertified:
      if failures >= 2 {
        s.Downgrade(srv, StatusDegraded, "2/5 tests failing")
      }

    case StatusDegraded:
      if failures >= 3 {
        s.Downgrade(srv, StatusUncertified, "3+ consecutive failures")
      }
      if time.Now().After(srv.DowngradeIfAfter) {
        s.Downgrade(srv, StatusUncertified, "No recovery in 7 days")
      }

    case StatusUncertified:
      if hasRecentPass(srv.RecentTests) {
        s.Upgrade(srv, StatusUnderReview, "Recovery detected, re-validating")
      }

    case StatusUnderReview:
      if countPassing(srv.RecentTests[-10:]) >= 10 {
        s.Upgrade(srv, StatusCertified, "Verified recovered")
      }
    }
  }
}
```

---

## Benefits

| Benefit | Impact |
|---------|--------|
| **Visibility** | Teams see exactly which servers are healthy |
| **Regression Detection** | Auto-downgrade catches degradation immediately |
| **Accountability** | Providers see certification status, incentivize stability |
| **Automation** | No manual review needed for up/downgrade |
| **Trust** | Badge only shown if continuously passing tests |
| **Transparency** | Timeline shows when issues appeared and were fixed |

---

## Integration with Registry

Certified status flows back to official registry:

```
ADHD Smoke Tests
  ↓
  ├─ ✓ Certified (18/20)
  ├─ ⚠ Degraded (1/20)
  └─ ✗ Uncertified (1/20)

  ↓

MCP Registry Display
  └─ ✓ Certified badge
  └─ ⚠ Unstable warning
  └─ ✗ Offline/removed
```

Users checking registry see:
- Which servers are reliably available
- Which ones have known issues
- Which ones are being monitored for recovery
