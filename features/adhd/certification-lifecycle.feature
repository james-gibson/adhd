@adhd
@z0-physical
@z1-temporal
@domain-certification
@continuous-monitoring
Feature: Certification Lifecycle — Real-Time Regression Detection

  Certification is not a one-time achievement. Endpoints must continuously
  pass smoke tests or their certification level is automatically downgraded.

  This prevents false confidence: a badge that means "currently healthy"
  not "was once tested."

  Background:
    Given the ADHD smoke test runner is active
    And endpoints are tested every 5 minutes (certified) or more frequently (degraded)
    And certification status is displayed on the dashboard in real-time

  # ──────────────────────────────────────────────────────────────
  # Initial Certification: ✓ CERTIFIED
  # ──────────────────────────────────────────────────────────────

  @lifecycle @start
  Scenario: Endpoint achieves certification status
    Given a new MCP server endpoint
    When we run smoke tests
    And it passes 5 consecutive tests
    Then status becomes: ✓ CERTIFIED
    And badge appears on dashboard
    And testing frequency: every 5 minutes (baseline)

  @lifecycle @stable
  Scenario: Certified endpoint remains stable
    Given an endpoint with status ✓ CERTIFIED
    When we run smoke tests continuously
    And all 5 most recent tests pass
    Then status remains: ✓ CERTIFIED
    And dashboard shows: 🟢 HEALTHY (green)
    And trend line: ════════ (flat/stable)

  # ──────────────────────────────────────────────────────────────
  # Degradation: ⚠ DEGRADED
  # ──────────────────────────────────────────────────────────────

  @lifecycle @downgrade-level-1
  Scenario: Certification degrades when tests start failing
    Given an endpoint with status ✓ CERTIFIED
    When 2 out of the last 5 smoke tests fail
    Then status changes: ✓ CERTIFIED → ⚠ DEGRADED
    And dashboard shows: 🟡 UNSTABLE (yellow alert)
    And trend line: ════╱ (declining)
    And actions appear:
      | Action | Impact |
      | Increase test frequency | Every 1 minute (not 5) |
      | Alert team | Notification sent |
      | Log reason | HTTP 401, timeout, etc. |
      | Show issue | "Bearer token validation failing" |

  @lifecycle @degradation-progression
  Scenario: Degradation timeline shows when issue appeared
    Given endpoint status: ⚠ DEGRADED
    When we look at the dashboard
    Then we see:
      | Time | Test | Result | Cumulative |
      | 10:00 | 1 | ✓ PASS | 5/5 (certified) |
      | 10:05 | 2 | ✗ FAIL | 4/5 (degrading) |
      | 10:06 | 3 | ✓ PASS | 4/5 |
      | 10:07 | 4 | ✗ FAIL | 3/5 |
      | 10:08 | 5 | ✓ PASS | 3/5 |

    And can see:
      - Last Pass: 10:08 UTC
      - Last Fail: 10:07 UTC
      - Consecutive fails: 1
      - Issue started: 10:05 UTC (5 minutes ago)
      - Auto-downgrade date: 2026-04-12 (7 days)

  @lifecycle @temporary-degradation
  Scenario: Degraded status recovers to certified
    Given endpoint status: ⚠ DEGRADED
    When tests start passing again
    And we get 5 consecutive passes (within 5 minutes)
    Then status upgrades: ⚠ DEGRADED → ✓ CERTIFIED
    And dashboard shows: 🟢 HEALTHY (green)
    And alert sent: "AdRamp recovered to full certification"
    And testing returns to: every 5 minutes

  # ──────────────────────────────────────────────────────────────
  # Uncertified: ✗ UNCERTIFIED
  # ──────────────────────────────────────────────────────────────

  @lifecycle @downgrade-level-2
  Scenario: Certification fails completely
    Given endpoint status: ⚠ DEGRADED
    When 3 or more consecutive tests fail
    Then status changes: ⚠ DEGRADED → ✗ UNCERTIFIED
    And dashboard shows: 🔴 OFFLINE (red)
    And badge disappears from registry
    And actions:
      | Action | Timing |
      | Remove from trusted list | Immediately |
      | Notify provider | Email alert |
      | Archive endpoint | Mark inactive |
      | Increase diagnostics | Log all errors |

  @lifecycle @100-percent-failure
  Scenario: Extreme failure triggers immediate removal
    Given an endpoint that passes 100% of tests
    When 10 consecutive tests ALL fail
    Then status: ✓ CERTIFIED → ✗ UNCERTIFIED (same test run)
    And badge: Removed immediately (no grace period)
    And registry: Updated to "offline" within 1 minute
    And alert: "Critical: Server is unreachable"
    And reason: "100% failure rate for 10+ minutes"

  @lifecycle @downgrade-timeline
  Scenario: Downgrade timeline is transparent
    Given endpoint status: ⚠ DEGRADED
    When we look at certificate expiry
    Then we see:
      ```
      Downgrade Timeline:
        - Now: ⚠ Degraded (2 tests failing)
        - In 7 days: Auto-downgrade to ✗ Uncertified
        - Condition: If status doesn't improve
        - Action: Mark as offline, remove from registry
      ```
    And dashboard shows countdown:
      ```
      Certificate expires: 2026-04-12 10:23 UTC
      Time remaining: 6 days, 23 hours, 45 minutes
      ```

  # ──────────────────────────────────────────────────────────────
  # Recovery: 🟡 UNDER REVIEW
  # ──────────────────────────────────────────────────────────────

  @lifecycle @recovery-start
  Scenario: Uncertified endpoint can begin recovery
    Given endpoint status: ✗ UNCERTIFIED
    When 1 test passes after extended failure
    Then status: ✗ UNCERTIFIED → 🟡 UNDER REVIEW
    And dashboard shows: 🟠 RECOVERING (orange)
    And actions:
      | Action | Purpose |
      | Increase test frequency | Every 30 seconds |
      | Track recovery progress | Show 0/10, 1/10, etc. |
      | Rapid feedback | Quick validation |
      | Email provider | "Service appears recovered" |

  @lifecycle @rapid-revalidation
  Scenario: Under review requires rapid consecutive passes
    Given endpoint status: 🟡 UNDER REVIEW
    When we run tests every 30 seconds
    Then progress bar shows:
      ```
      Recovery Progress: 4/10 consecutive passes needed

      Tests: ✓ ✓ ✗ ✓ ✓ ✓ ... ✓ ✓ ✓ ✓
             1 2 ! 3 4 5     8 9 10 ✓

      Status: 5 consecutive passes, 1 failure resets counter
      ```

    And if fails mid-recovery:
      ```
      ✓ ✓ ✓ ✓ ✗ ← Failed, counter resets to 0
                (must start over)
      ```

  @lifecycle @recovery-complete
  Scenario: Recovery succeeds after 10 consecutive passes
    Given endpoint: 🟡 UNDER REVIEW with 9 consecutive passes
    When test #10 passes
    Then status: 🟡 UNDER REVIEW → ✓ CERTIFIED
    And dashboard shows: 🟢 HEALTHY (green)
    And badge restored to registry
    And alert: "Server restored to full certification after 2-hour outage"
    And testing returns to: every 5 minutes

  # ──────────────────────────────────────────────────────────────
  # Real-World Scenario: Server Degradation and Recovery
  # ──────────────────────────────────────────────────────────────

  @lifecycle @real-scenario @complete-timeline
  Scenario: AdRamp server: degradation, failure, and recovery
    Timeline:

    10:00 UTC
      Status: ✓ CERTIFIED
      Tests: ✓ ✓ ✓ ✓ ✓
      Action: All green, normal operation

    10:05 UTC
      Status: ✓ CERTIFIED → ⚠ DEGRADED
      Tests: ✓ ✓ ✓ ✗ ✗
      Alert: "AdRamp bearer token validation unstable"
      Team Action: Check token format with AdRamp

    10:23 UTC
      Status: ⚠ DEGRADED (still failing)
      Tests: Last 10 = 3✓ 7✗ (30% pass rate)
      Alert: "AdRamp approaching downgrade to uncertified"
      Downgrade if no recovery by: 2026-04-12

    11:45 UTC
      Status: ⚠ DEGRADED → ✗ UNCERTIFIED
      Tests: 0✓ 5✗ (0% pass rate for 1+ hour)
      Action: Badge removed, removed from trusted list
      Alert: "AdRamp uncertified after 100+ minute outage"
      Team Action: File incident, contact AdRamp support

    13:30 UTC (1h 45m after failure)
      Status: ✗ UNCERTIFIED → 🟡 UNDER REVIEW
      Tests: First pass after recovery
      Alert: "AdRamp appears recovered, starting re-validation"
      Action: Increase test frequency to every 30s

    14:00 UTC
      Status: 🟡 UNDER REVIEW
      Progress: 4/10 consecutive passes
      Trend: ✓ ✓ ✗ ✓ ✓ ✓ ... (reset at failure)
      Alert: "Recovery in progress, 4 of 10 passes needed"

    14:15 UTC
      Status: 🟡 UNDER REVIEW
      Progress: 3/10 (failed test #4, reset counter)
      Trend: ✓ ✓ ✓ ✗ ... (back to 0)

    14:45 UTC
      Status: 🟡 UNDER REVIEW
      Progress: 10/10 consecutive passes ✓
      Alert: "AdRamp fully restored to certification"
      Action: Badge re-added to registry
      Testing: Return to every 5 minutes

    Summary:
      - Total downtime: ~2 hours
      - Root cause: Token format changed by AdRamp API
      - Detection: Automatic within 5 minutes of degradation
      - Recovery time: 2 hours from first failure to re-certification
      - Manual action: Only incident filing + support contact
      - Everything else: Automated

  # ──────────────────────────────────────────────────────────────
  # Dashboard Display
  # ──────────────────────────────────────────────────────────────

  @lifecycle @dashboard @display
  Scenario: Dashboard shows full certification status in real-time
    When we open the ADHD dashboard
    Then we see three panels:

    Panel 1: Proxy Capability Maturity
      ████░░ 60% (bearer token, API-Key, 3/5 features)

    Panel 2: Registry Coverage
      ███████░ 70% (18/20 servers accessible)

    Panel 3: Certification Status (Real-Time)
      ✓ CERTIFIED (18)
        • tandem/docs-mcp 🟢 HEALTHY (✓✓✓✓✓)
        • adadvisor/mcp 🟢 HEALTHY (✓✓✓✓✓)
        • [16 more]

      ⚠ DEGRADED (1)
        • adramp/google-ads 🟡 UNSTABLE (✓✗✓✗✗)
        • Issue: Bearer token intermittent
        • Downgrade in: 6d 23h 45m
        • Action: Investigating token format

      ✗ UNCERTIFIED (0)
        • None (all recovered or removed)

    And each entry updates every 30-60 seconds

  @lifecycle @alerting
  Scenario: Dashboard alerts notify teams of issues
    When an endpoint status changes
    Then:
      | Change | Alert | Severity | Action |
      | ✓ → ⚠ | Degraded warning | MEDIUM | Review logs |
      | ⚠ → ✗ | Critical failure | HIGH | Incident open |
      | ✗ → 🟡 | Recovery started | LOW | Monitor progress |
      | 🟡 → ✓ | Recovered | LOW | Close incident |

    And alerts go to:
      - Slack (ops channel)
      - Email (admin list)
      - Pagerduty (if critical)
      - Dashboard banner (always)

  # ──────────────────────────────────────────────────────────────
  # Policy: Downgrade Rules
  # ──────────────────────────────────────────────────────────────

  @lifecycle @policy
  Scenario: Automatic downgrade rules are transparent
    Then the system enforces these rules:

    ```
    ✓ CERTIFIED → ⚠ DEGRADED if:
      • 2 out of last 5 tests fail
      • Response time > 3 seconds
      • Uptime < 95% in 24 hours
      • Any authentication error

    ⚠ DEGRADED → ✗ UNCERTIFIED if:
      • 3+ consecutive tests fail
      • 10+ total failures in 24 hours
      • No recovery within 7 days
      • OR all 10 consecutive tests fail (immediate)

    ✗ UNCERTIFIED → 🟡 UNDER REVIEW if:
      • 1 passing test after extended failure
      • Provider indicates recovery

    🟡 UNDER REVIEW → ✓ CERTIFIED if:
      • 10+ consecutive tests pass
      • Response time stable
      • All auth methods working
    ```

    And these rules apply to ALL endpoints, no exceptions.
