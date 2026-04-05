@adhd
@z0-physical
@domain-discovery
Feature: ADHD Isotope Transit Recording
  ADHD records isotope IDs observed in smoke-alarm health-check responses
  as transit events — never the response payload. Each isotope maps to a
  Gherkin scenario; a transit is live evidence that the scenario is being
  exercised in the running system.

  This is the foundational prerequisite for chaos isotope support.
  Until isotope_id flows from the smoke-alarm through LightUpdate into
  the dashboard, no chaos classification logic can function.

  # Dependency: ocd-smoke-alarm isotope-transit.feature must be implemented
  # first — ADHD can only record transits that the smoke-alarm emits.

  Background:
    Given ADHD is monitoring a smoke-alarm endpoint that carries isotope IDs

  # ── LightUpdate carries isotope metadata ───────────────────────────────────

  Scenario: smokelink watcher extracts isotope_id from /status response
    Given a /status response for target "t1" includes isotope_id "iso-abc-001"
    When the smokelink watcher processes the response in pollOnce()
    Then the LightUpdate emitted for "t1" has IsotopeID "iso-abc-001"

  Scenario: smokelink watcher extracts isotope_classification from /status response
    Given a /status response for target "t1" includes isotope_classification "registered-chaos"
    When the smokelink watcher processes the response
    Then the LightUpdate emitted for "t1" has IsotopeClassification "registered-chaos"

  Scenario: LightUpdate has empty IsotopeID when /status carries none
    Given a /status response for target "t1" has no isotope_id field
    When the smokelink watcher processes the response
    Then the LightUpdate emitted for "t1" has IsotopeID ""
    And IsotopeClassification is ""

  # ── transit recording in the dashboard ────────────────────────────────────

  Scenario: ADHD records a transit event when a LightUpdate carries an isotope ID
    Given a LightUpdate arrives for "smoke:alarm-a/t1" with IsotopeID "iso-abc-001"
    When model.Update processes the LightUpdate
    Then a transit event is recorded: isotope "iso-abc-001" observed at "smoke:alarm-a/t1"
    And no response payload data appears in the transit event

  Scenario: transit events are deduplicated within a monitoring window
    Given isotope "iso-abc-001" has already been recorded as a transit for "smoke:alarm-a/t1"
    When a second LightUpdate arrives for the same target with the same isotope ID
    Then an isotope-variation failure is recorded
    And the agent's 42i distance increases by 8 units
    And the duplicate is not recorded as a new transit

  Scenario: transit events from different targets with the same isotope ID are independent
    Given isotope "iso-abc-001" is observed at "smoke:alarm-a/t1"
    When the same isotope "iso-abc-001" is observed at "smoke:alarm-a/t2"
    Then both transits are recorded independently
    And no variation failure is raised (different targets)

  # ── isotope maps to scenario coverage ─────────────────────────────────────

  Scenario: isotope transit updates live feature coverage when it maps to a scenario
    Given isotope "iso-abc-001" maps to the scenario "light transitions from dark to green"
      in feature "adhd/mdns-discovery"
    When "iso-abc-001" transits through the health-check for "smoke:alarm-a/t1"
    Then that scenario is marked as covered in the live coverage report
    And the corresponding feature light for "ADHD mDNS Smoke-Alarm Discovery" reflects coverage

  Scenario: isotope with no declared feature mapping does not update coverage
    Given isotope "iso-unknown-001" has no declared feature binding
    When "iso-unknown-001" transits through a health-check
    Then no scenario is marked as covered
    And a warning is logged: "isotope with no feature binding observed"

  # ── privacy boundary ───────────────────────────────────────────────────────

  Scenario: health-check payload is not recoverable from a transit event
    Given a transit event records isotope "iso-abc-001" at "smoke:alarm-a/t1"
    When an attempt is made to extract the health-check response from the event
    Then no payload information is recoverable
    And the transit event contains only: isotope_id, target name, timestamp

  Scenario: transit event fields contain no data from the probe response body
    Given a LightUpdate arrives with IsotopeID "iso-abc-001" and Details "healthy"
    When model.Update records the transit
    Then the transit event has no field sourced from the probe response body
    And "Details" from the LightUpdate is not included in the transit record
