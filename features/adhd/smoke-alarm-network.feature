@adhd
@z0-physical
@domain-smoke-alarm-network
Feature: ADHD Smoke-Alarm Network Integration
  ADHD monitors MCP/ACP tool health via the smoke-alarm network

  Scenario: Poll smoke-alarm /status endpoint
    Given a smoke-alarm instance at http://localhost:8080
    And the smoke-alarm instance is running
    And ADHD is configured to monitor it with polling enabled
    When ADHD starts
    Then ADHD polls the /status endpoint at the configured interval
    And lights are created for each target in the response

  Scenario: Light naming from smoke-alarm
    Given a smoke-alarm endpoint named "primary"
    And a target with ID "mcp-1" in that endpoint
    When ADHD creates a light from the target
    Then the light is named "smoke:primary/mcp-1"
    And the light has source="smoke-alarm"
    And the light has SourceMeta with instance and targetID

  Scenario: Light status mapped from HealthState
    Given a smoke-alarm target with state="healthy"
    When ADHD creates a light
    Then the light status is "green"
    When the target state changes to "degraded"
    Then the light status becomes "yellow"
    When the target state changes to "outage"
    Then the light status becomes "red"

  Scenario: SSE subscription for real-time updates
    Given a smoke-alarm endpoint with use_sse=true
    When ADHD starts
    Then ADHD subscribes to the SSE stream
    When the smoke-alarm sends a status event
    Then ADHD receives it immediately (no polling delay)
    And the corresponding light is updated

  Scenario: Light update on status change
    Given ADHD is monitoring a smoke-alarm endpoint
    And a light "smoke:primary/target-1" exists with status="green"
    When the target health changes to "unhealthy"
    Then the light status is updated to "red"
    And the LastUpdated timestamp is recent
    And the Details field reflects the new status message

  Scenario: Multiple smoke-alarm endpoints
    Given ADHD is configured to monitor 2 smoke-alarm endpoints
    When ADHD starts
    Then both endpoints are polled in parallel
    And lights are created with namespaces (smoke:us-west/*, smoke:us-east/*)
    And updates from either endpoint are received and displayed

  Scenario: Graceful handling of smoke-alarm unavailability
    Given a smoke-alarm endpoint that is unreachable
    When ADHD attempts to poll it
    Then no error is raised
    And a warning is logged
    And the dashboard continues to display existing lights
    And the watcher retries at the next interval

  Scenario: Network visibility without direct connection
    Given an MCP tool monitored by a remote smoke-alarm
    And ADHD does not have direct access to that MCP tool
    When ADHD monitors the smoke-alarm endpoint
    Then ADHD displays a light for the tool (via smoke-alarm proxy)
    And the light reflects the tool's health as seen by smoke-alarm
    And the light is named "smoke:<instance>/<tool>"

  Scenario: Light state convergence
    Given a light "smoke:primary/tool-1" initially shows "green"
    When polling returns "red"
    And SSE sends a conflicting status
    Then ADHD applies the latest status (SSE is preferred over polling)
    And no duplicate updates are processed for the same target

  # ── structural evidence certification ─────────────────────────────────────
  # Certain target IDs carry semantic meaning about smoke-alarm capabilities.
  # Presence of these targets certifies the domain regardless of health status.

  Scenario: a "self-health-check" target certifies @domain-smoke-alarm-network
    Given a smoke-alarm /status response contains a target with ID "self-health-check"
    When ADHD processes the LightUpdate for that target
    Then the @domain-smoke-alarm-network feature lights are set to "green"
    And the certification fires even if the "self-health-check" target is red or yellow

  Scenario: aggregate smoke network status is computed from all smoke: target lights
    Given ADHD has smoke: lights: smoke:alarm-a/peer=red, smoke:alarm-a/self-health-check=green
    When aggregateSmokeNetworkStatus is computed
    Then the result is "red" (worst-case across all non-dark smoke: lights)

  Scenario: aggregate smoke network status is dark when no target lights exist yet
    Given no smoke: target lights have been created
    When aggregateSmokeNetworkStatus is computed
    Then the result is "dark"
    And no CapabilityVerifiedMsg is emitted for smoke-alarm-network
