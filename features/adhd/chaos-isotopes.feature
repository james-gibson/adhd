@adhd
@z0-physical
@domain-smoke-alarm-network
Feature: ADHD Chaos Isotope Awareness
  ADHD suppresses false-alarm red-light transitions when a failing health-check
  carries an isotope that has been pre-registered as a chaos isotope with an
  active window. The smoke-alarm is authoritative on classification — ADHD
  acts on the classification it receives, never re-deriving it.

  # Dependencies (must be implemented first):
  #   - ocd-smoke-alarm isotope-transit.feature  (isotope_id in /status)
  #   - ocd-smoke-alarm chaos-window.feature     (window registration + classification)
  #   - adhd isotope-transit.feature             (LightUpdate carries isotope metadata)

  Background:
    Given ADHD is monitoring a smoke-alarm endpoint
    And the smoke-alarm is classifying isotopes in its /status responses

  # ── suppressing red transitions ────────────────────────────────────────────

  Scenario: light does not turn red when failure carries a registered-chaos isotope
    Given a light "smoke:alarm-a/t1" has status "green"
    And a LightUpdate arrives for "t1" with:
      | Status                  | red               |
      | IsotopeID               | iso-chaos-001     |
      | IsotopeClassification   | registered-chaos  |
    When model.Update processes the LightUpdate
    Then the light "smoke:alarm-a/t1" status remains "green"
    And the light's Details field notes "expected chaos: iso-chaos-001 (window active)"
    And no red propagation reaches the @domain-smoke-alarm-network feature lights

  Scenario: light turns red when failure carries an unregistered isotope
    Given a light "smoke:alarm-a/t1" has status "green"
    And a LightUpdate arrives for "t1" with Status "red" and IsotopeClassification "unregistered"
    When model.Update processes the LightUpdate
    Then the light "smoke:alarm-a/t1" status becomes "red"
    And the Details field notes "unregistered isotope on failure: <isotope_id>"

  Scenario: light turns red when a chaos isotope arrives outside its declared window
    Given a light "smoke:alarm-a/t1" has status "green"
    And a LightUpdate arrives for "t1" with:
      | Status                  | red             |
      | IsotopeID               | iso-chaos-002   |
      | IsotopeClassification   | expired-window  |
    When model.Update processes the LightUpdate
    Then the light "smoke:alarm-a/t1" status becomes "red"
    And the Details field notes "chaos isotope arrived outside declared window: iso-chaos-002"

  Scenario: light turns red when failure carries no isotope at all
    Given a light "smoke:alarm-a/t1" has status "green"
    And a LightUpdate arrives for "t1" with Status "red" and IsotopeID ""
    When model.Update processes the LightUpdate
    Then the light "smoke:alarm-a/t1" status becomes "red"

  # ── feature light propagation ──────────────────────────────────────────────

  Scenario: suppressed chaos failure does not propagate to feature lights
    Given all smoke: lights are green
    And a chaos-suppressed failure arrives for "smoke:alarm-a/t1"
    When applyClusterHealthToFeatures runs
    Then the aggregate status is still "green"
    And @domain-smoke-alarm-network feature lights remain "green"

  Scenario: non-suppressed failure propagates to feature lights normally
    Given clusterEverHealthy is true
    And a LightUpdate arrives for "t1" with IsotopeClassification "unregistered" and Status "red"
    When model.Update processes the LightUpdate
    Then the light "smoke:alarm-a/t1" is red
    And @domain-smoke-alarm-network feature lights are set to "red" by aggregate health

  # ── window registration via MCP ────────────────────────────────────────────
  # ADHD exposes an MCP tool so that AI agents and test harnesses can register
  # chaos windows without direct HTTP access to the smoke-alarm.

  Scenario: adhd.chaos.register-window MCP tool forwards a window to the smoke-alarm
    Given the MCP server is running and a smoke-alarm is configured
    When a JSON-RPC call is made to "adhd.chaos.register-window" with:
      | target_id    | t1                  |
      | isotope_id   | iso-chaos-001       |
      | window_start | <now>               |
      | window_end   | <now + 10 minutes>  |
    Then ADHD sends POST /isotope/register-chaos-window to the configured smoke-alarm
    And the tool returns success with the registered window bounds

  Scenario: adhd.chaos.register-window returns an error when smoke-alarm rejects the window
    Given the smoke-alarm rejects the registration with status 400
    When a JSON-RPC call is made to "adhd.chaos.register-window"
    Then the MCP tool returns an error result
    And the error message includes the smoke-alarm's rejection reason

  # ── classification trust ───────────────────────────────────────────────────

  Scenario: ADHD does not send a follow-up request to verify an isotope classification
    Given a LightUpdate arrives with IsotopeClassification "registered-chaos"
    When model.Update processes the LightUpdate
    Then ADHD suppresses the red transition
    And no HTTP request is sent to /isotope or /isotope/chaos-windows
    And no additional /status poll is triggered

  Scenario: ADHD's decision is deterministic given the classification field
    Given two identical LightUpdates both with IsotopeClassification "registered-chaos"
    When both are processed by model.Update in sequence
    Then both result in the same suppression decision
    And the outcome does not depend on wall-clock time inside model.Update
