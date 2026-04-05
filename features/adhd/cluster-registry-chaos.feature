@adhd
@z0-physical
@domain-cluster-registry
Feature: Cluster Registry Chaos Resilience
  The cluster registry must maintain consistency and accuracy as clusters
  join, leave, and experience chaos conditions. Registry enables discovery
  and health monitoring of multi-cluster deployments.

  Background:
    Given a cluster registry at http://192.168.7.18:19100/cluster
    And it tracks 30+ demo clusters with dual smoke-alarms each

  # ── cluster enumeration and discovery ──────────────────────────────────────

  Scenario: registry enumerates all clusters consistently
    When the registry is queried for all clusters
    Then it returns all registered clusters (30+)
    And each cluster has: name, alarm_a, alarm_b, adhd_mcp
    And all URLs are valid and present
    And the list is identical on repeated queries

  Scenario: cluster endpoints in registry are reachable
    When all cluster endpoints are probed (3 endpoints × 30 clusters = 90 endpoints)
    Then all alarm_a endpoints are reachable
    And all alarm_b endpoints are reachable
    And all adhd_mcp endpoints are reachable
    And response times are under 1000ms (p95)

  # ── dual alarm resilience ─────────────────────────────────────────────────

  Scenario: cluster remains available if alarm_b is unreachable
    Given cluster "demo-10535" with alarm_a and alarm_b
    When alarm_b becomes unreachable
    Then alarm_a continues to respond normally
    And alarm_a reports dual-alarm degradation in /status
    And cluster remains functional

  Scenario: cluster degrades gracefully when both alarms are slow
    Given a cluster with both alarm_a and alarm_b
    When both alarms become slow (500ms+ response time)
    Then the cluster detects degradation
    And /status shows "degraded" not "healthy"
    And no timeouts occur waiting for both alarms

  # ── concurrent cluster access ─────────────────────────────────────────────

  Scenario: registry handles concurrent queries from 30 ADHD instances
    When 30 ADHD instances query the registry simultaneously
    Then all 30 queries complete successfully
    And each receives consistent cluster lists
    And response time doesn't degrade (still <100ms)
    And no "thundering herd" slowdown occurs

  Scenario: cluster discovery doesn't interfere with health monitoring
    Given health checks are polling all clusters every 5 seconds
    When a new cluster joins the registry
    Then existing cluster health checks continue unaffected
    And the new cluster is added to the rotation
    And no health data is lost during discovery update

  # ── cluster lifecycle chaos ───────────────────────────────────────────────

  Scenario: cluster shutdown is reflected in registry
    Given cluster "demo-10535" is running and in registry
    When the cluster is shut down
    Then the registry detects the absence (within 30 seconds)
    And subsequent queries either exclude it or mark it unavailable
    And the registry doesn't leak references to dead clusters

  Scenario: cluster rejoin after outage updates registry
    Given cluster "demo-10535" was removed due to outage
    When the cluster comes back online and re-registers
    Then the registry re-adds it
    And all endpoints are reachable again
    And no stale entries persist from before the outage

  Scenario: rapid cluster add/remove doesn't corrupt registry
    When 5 clusters are added and 3 are removed in rapid succession
    Then the registry reflects all changes
    And the final cluster list is accurate
    And no duplicate or orphaned entries exist

  # ── multi-cluster consistency ─────────────────────────────────────────────

  Scenario: all clusters report consistent isotope namespace
    When querying adhd.status from all 30 clusters
    Then each cluster reports its own isotope ID
    And no two clusters have the same isotope ID
    And isotope IDs are globally unique across registry

  Scenario: trust paths are established across cluster boundary
    Given two clusters in the registry
    When path negotiation occurs between them
    Then the path includes both alarm_a and alarm_b (redundancy)
    And the path is validated before use
    And failover to alarm_b works if alarm_a is unavailable

  # ── chaos injection ───────────────────────────────────────────────────────

  Scenario: registry survives simultaneous failure of 5 clusters
    Given 30 clusters in registry
    When 5 random clusters fail simultaneously
    Then the registry detects the failures within 30 seconds
    And remaining 25 clusters continue normal operation
    And health aggregation reflects correct count
    And no cascading failures occur

  Scenario: network partition splits cluster registry perception
    Given partition: [cluster 1-15] and [cluster 16-30]
    When clusters on both sides try to update registry
    Then eventual consistency is maintained
    And when partition heals, registry converges
    And no duplicate clusters appear

  # ── stress and performance ────────────────────────────────────────────────

  Scenario: registry handles 30 clusters with 90 endpoints at scale
    When continuous health monitoring polls all 90 endpoints every 5 seconds
    Then registry queries complete in <100ms
    And no endpoints are dropped or skipped
    And memory usage is stable (no leaks)

  Scenario: registry lookup by cluster name is fast
    When looking up a specific cluster by name from the registry
    Then lookup completes in <10ms
    And results are consistent with full enumeration

  # ── regression detection ──────────────────────────────────────────────────

  Scenario: chaos test detects missing cluster from registry
    When a cluster that was previously registered becomes absent
    Then the chaos test detects the regression
    And alerts that cluster availability decreased
    And the test would fail if a cluster was accidentally deleted

  Scenario: chaos test detects endpoint corruption in registry
    When a cluster's alarm_a URL is corrupted in registry
    And health checks fail against it
    Then the chaos test detects the inconsistency
    And alerts that the registry entry is stale
    And the test flags it for manual remediation
