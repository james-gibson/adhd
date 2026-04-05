@adhd
@z0-physical
@domain-smoke-alarm-chaos
Feature: Smoke-Alarm Chaos Resilience
  Smoke-alarm must remain resilient and accurate under rapid state changes,
  concurrent target monitoring, and stress conditions. Chaos testing validates
  health monitoring under pressure.

  Background:
    Given smoke-alarm is running
    And it is configured to monitor targets via polling and/or SSE

  # ── rapid state transitions ────────────────────────────────────────────────

  Scenario: target health survives rapid state changes
    Given a target initially at status="healthy"
    When the target status changes rapidly: healthy → degraded → outage → healthy → degraded (100 changes in 5 seconds)
    Then smoke-alarm processes all state changes
    And the final status matches the last reported state
    And no state is skipped or duplicated
    And response times remain under 500ms (p95)

  Scenario: multiple targets change state concurrently
    Given 10 targets being monitored
    When all 10 targets change state simultaneously (healthy → unhealthy)
    Then smoke-alarm processes all changes
    And /status response includes all 10 with updated states
    And no targets are dropped or corrupted
    And aggregated health reflects worst-case (all unhealthy)

  # ── polling under load ────────────────────────────────────────────────────

  Scenario: polling interval is respected under load
    Given a target with polling_interval=100ms
    When smoke-alarm polls it continuously
    Then polls occur approximately every 100ms (±20ms tolerance)
    And no polls are skipped even under concurrent requests
    And response times don't drift over time

  Scenario: concurrent polls to same target don't corrupt state
    When 5 concurrent /status requests come in while a poll is in progress
    Then all 5 responses return consistent state
    And the poll in progress completes without interference
    And the next scheduled poll occurs on time

  # ── SSE stream resilience ─────────────────────────────────────────────────

  Scenario: SSE stream survives target state changes
    Given an SSE subscriber connected to smoke-alarm
    When a target's health changes
    Then an event is sent over SSE immediately
    And the event includes updated target status
    And the connection remains open for subsequent events
    And no events are lost

  Scenario: SSE reconnection after network hiccup
    Given an SSE subscriber connected
    When the network connection is briefly interrupted (1 second)
    Then the client can reconnect
    And no state changes that occurred during the gap are lost
    And subsequent events flow normally

  Scenario: multiple concurrent SSE subscribers
    When 20 SSE subscribers connect simultaneously
    Then all 20 connections are established
    When a target changes state
    Then all 20 subscribers receive the event within 100ms
    And no subscriber blocks others

  # ── target availability chaos ──────────────────────────────────────────────

  Scenario: unreachable target doesn't block other targets
    Given 10 targets being monitored, target-5 is unreachable
    When polling occurs
    Then targets 1-4, 6-10 are polled successfully
    And target-5 is marked as "unreachable" or "timeout"
    And the unreachable target doesn't delay polling of others
    And timeout handling is logged appropriately

  Scenario: target becomes available after outage
    Given target-1 is unreachable and marked "down"
    When target-1 becomes reachable again
    Then the next poll succeeds
    And status changes from "unreachable" to the reported health
    And no stale "down" status is cached

  # ── stress testing ─────────────────────────────────────────────────────────

  Scenario: many targets (100+) remain manageable
    Given 100 targets being monitored with polling_interval=1000ms
    When smoke-alarm operates for 10 seconds
    Then all 100 targets are polled at least once
    And /status response time stays under 200ms (p95)
    And memory usage doesn't grow unbounded
    And CPU usage stays below 50% average

  Scenario: rapid target addition/removal
    Given 10 targets initially
    When 5 new targets are added and 3 are removed (10 operations in 2 seconds)
    Then all operations complete successfully
    And the final target list is correct
    And no polling is lost during reconfiguration
    And /status response reflects current targets only

  # ── aggregated health consistency ──────────────────────────────────────────

  Scenario: aggregated health reflects worst-case
    Given targets: t1=healthy, t2=degraded, t3=healthy, t4=outage
    When smoke-alarm computes aggregated health
    Then aggregated_health = "outage" (worst-case)
    And individual target states are all preserved
    And aggregation is immediate (not lagged)

  Scenario: aggregated health updates atomically
    Given current aggregated_health = "healthy"
    When a target changes from "healthy" to "outage"
    Then /status either returns old aggregated_health or new aggregated_health
    And no intermediate invalid state is visible (like "deaggraded")

  # ── error handling chaos ───────────────────────────────────────────────────

  Scenario: malformed target response doesn't crash smoke-alarm
    Given a target that returns invalid JSON in /status
    When smoke-alarm polls it
    Then it logs a parse error
    And the target is marked as "error"
    And smoke-alarm continues polling other targets
    And the malformed target is re-polled at the next interval

  Scenario: target with extremely large response is handled
    Given a target that returns 10MB response body
    When smoke-alarm polls it
    Then it either times out or parses successfully
    And doesn't consume unbounded memory
    And other targets continue to be polled

  # ── regression detection ──────────────────────────────────────────────────

  Scenario: chaos test detects lost state changes
    Given smoke-alarm processes 1000 state changes
    When chaos test compares expected vs actual state transitions
    Then no state changes are lost
    And all transitions are in order
    And final state matches expected

  Scenario: chaos test detects race conditions in aggregation
    When 100 concurrent state changes affect aggregation
    Then the final aggregated health is correct
    And no intermediate corrupted state is visible
    And the test would fail if aggregation wasn't thread-safe
