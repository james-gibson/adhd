@adhd
@z0-physical
@domain-registry-health
Feature: Stale Cluster Registry Detection
  Fire-marshal must detect and flag stale cluster registries where registered
  clusters are no longer reachable. A stale registry misleads service discovery
  and causes cascading failures.

  Background:
    Given a cluster registry with registered clusters
    And fire-marshal is configured to validate cluster health

  # ── detection of dead clusters ──────────────────────────────────────────────

  Scenario: fire-marshal detects when all clusters are unreachable
    Given 30 clusters are registered in the registry
    And all 30 clusters are actually dead (endpoints unreachable)
    When fire-marshal probes all endpoints
    Then fire-marshal reports: "0/30 clusters reachable"
    And fire-marshal flags this as CRITICAL
    And the fire-marshal-cluster-health light turns RED
    And a detailed report is generated showing each cluster status

  Scenario: fire-marshal detects partial cluster death
    Given 30 clusters registered, only 4-6 are actually running
    When fire-marshal probes all endpoints
    Then fire-marshal reports: "4/30 clusters healthy" (or "6/30")
    And fire-marshal flags this as WARNING or CRITICAL (depending on threshold)
    And individual cluster health is reported per endpoint
    And the registry consistency check notes stale entries

  Scenario: fire-marshal identifies which clusters are dead
    Given mixed alive and dead clusters in registry
    When fire-marshal completes its probe
    Then the report lists:
      - Alive clusters: demo-A, demo-B (both alarms + MCP responding)
      - Partially dead: demo-C (alarm_a responding, alarm_b dead)
      - Completely dead: demo-D...demo-Z (all endpoints unreachable)

  # ── registry staleness timeline ────────────────────────────────────────────

  Scenario: fire-marshal tracks when cluster went offline
    Given cluster "demo-1234" last reported healthy 1 hour ago
    And it is now unreachable
    When fire-marshal probes it
    Then fire-marshal notes: "offline for ~1 hour"
    And the report suggests manual intervention or automatic cleanup

  Scenario: fire-marshal detects registry was never cleaned after cluster death
    Given 30 clusters died 24+ hours ago
    And they remain in the registry
    When fire-marshal audits the registry
    Then it reports: "30 stale entries, not cleaned for >24h"
    And fire-marshal suggests: "Run cleanup to remove dead cluster registrations"

  # ── cascading failure prevention ───────────────────────────────────────────

  Scenario: stale registry entries prevent service discovery
    Given ADHD instances use registry to discover smoke-alarms
    And 30 clusters are registered but all dead
    When ADHD tries to discover healthy smoke-alarms
    Then ADHD gets list of 30 dead endpoints
    And ADHD wastes time probing dead clusters
    And legitimate health checks are delayed

  Scenario: fire-marshal prevents cascading failures by blocking deployment
    Given fire-marshal probe shows 0/30 clusters healthy
    When deployment is attempted
    Then fire-marshal reports: "CRITICAL: 0% cluster health - check registry freshness"
    And deployment is blocked with failure status
    And manual review is required before cleanup

  # ── registry cleanup automation ───────────────────────────────────────────

  Scenario: fire-marshal recommends automatic cleanup of dead entries
    Given 30 dead clusters in registry
    And fire-marshal policy allows auto-cleanup after threshold (e.g., 1 hour dead)
    When fire-marshal runs its audit
    Then fire-marshal reports: "Ready to clean 30 stale entries"
    And cluster registry cleanup can be triggered
    And before cleanup, a dry-run report is generated
    And after cleanup, the registry reflects only healthy clusters

  Scenario: cleanup removes only truly dead clusters, not temporarily unavailable ones
    Given 30 clusters, 25 are dead, 5 are temporarily unreachable (transient failure)
    When cleanup with 5-minute threshold runs
    Then it removes the 25 clearly dead clusters
    And it leaves the 5 temporarily unreachable (they may recover)
    And re-probe after 5 minutes moves them to cleaned or healthy

  # ── health aggregation with stale registry ──────────────────────────────────

  Scenario: health aggregation should not count dead clusters
    Given registry with 30 clusters
    And 0 clusters are actually healthy
    And health aggregation is computed
    Then aggregated health = "critical" or "outage" (not "degraded")
    And the reason is: "0/30 clusters reachable, registry is stale"

  Scenario: fire-marshal light reflects registry staleness
    Given fire-marshal-cluster-health light
    When all clusters are dead
    Then light status = "red"
    And light details include: "Registry has 30 entries, 0 reachable"
    And administrators can drill down for per-cluster status

  # ── regression: missing stale detection ────────────────────────────────────

  Scenario: chaos test detects missing stale registry detection
    When fire-marshal is deployed without stale registry checks
    And all clusters die
    Then fire-marshal does NOT flag the problem
    And the chaos test FAILS
    And alerts: "stale registry not detected"

  Scenario: chaos test detects if cleanup leaves stale entries
    When cleanup runs but some dead clusters remain
    And re-probe after cleanup shows 0/30 still reachable
    Then the test flags: "cleanup did not remove all stale entries"
    And the test fails until cleanup threshold is adjusted
