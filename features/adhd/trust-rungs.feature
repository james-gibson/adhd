@adhd
@z1-temporal
@domain-certification
Feature: ADHD Trust Rung Awareness — Read Authorization and Rung Advancement
  ADHD tracks its own 42i trust rung and presents it to smoke-alarm instances
  when polling /status and invoking JSON-RPC skills. The rung determines which
  fields are returned to ADHD. As ADHD observes more certified isotope transits,
  its rung advances and additional data becomes readable.

  # Trust rungs are the encryption stand-in: authorization gating by demonstrated
  # capability rather than pre-shared credentials. When encryption is added,
  # each rung will correspond to a key tier. ADHD's code does not change.
  #
  # Dependencies:
  #   - ocd-smoke-alarm trust-rungs.feature  (rung definitions and value filtering)
  #   - adhd isotope-transit.feature         (transits advance ADHD's rung)
  #   - adhd chaos-isotopes.feature          (rung 3 unlocks suppression data)
  #   - adhd certification-gate.feature      (per-alarm trust, not just self-rung)

  Background:
    Given ADHD is running and monitoring at least one smoke-alarm endpoint

  # ── ADHD self-rung evaluation ───────────────────────────────────────────────

  Scenario: ADHD starts at rung 0 until first isotope transit is observed
    Given ADHD has just started with no prior transit history
    Then ADHD's trust rung is 0 (uncertified)
    And all /status polls are sent without a trust_rung header

  Scenario: ADHD advances to rung 1 when plaintext isotopes are configured and observed
    Given ADHD is running with allow_plaintext_isotopes=true
    And a LightUpdate arrives carrying IsotopeID "plaintext:adhd/mdns-discovery:001"
    When the transit is recorded
    Then ADHD's trust rung advances to 1
    And subsequent /status polls include header "X-Trust-Rung: 1"

  Scenario: ADHD advances to rung 2 when a cryptographic isotope verifies against its feature binding
    Given a LightUpdate arrives carrying a 43-char canonical isotope ID
    And the isotope verifies against its declared feature "adhd/mdns-discovery"
    When the transit is recorded
    Then ADHD's trust rung advances to at most 2
    And the 42i distance decreases accordingly

  Scenario: ADHD advances to rung 3 when a chaos suppression decision is confirmed
    Given ADHD is at rung 2
    And a LightUpdate with IsotopeClassification "registered-chaos" is processed
    And the suppression decision is applied (light stays green)
    When the suppression is confirmed correct by a subsequent /status response
    Then ADHD's trust rung may advance to 3

  Scenario: ADHD advances to rung 4 when probeIsotopeRegistration returns non-empty results
    Given ADHD is at rung 3
    And the probeIsotopeRegistration cmd completes with at least one registered isotope
    When the CapabilityVerifiedMsg{Domain: "headless"} is processed
    Then ADHD's trust rung advances to 4

  # ── identification, not self-declaration ───────────────────────────────────
  # ADHD does not tell the smoke-alarm what its rung is. The smoke-alarm looks
  # up ADHD's rung in its own registry based on ADHD's instance identity.
  # ADHD only needs to identify itself — the rung is the smoke-alarm's answer.

  Scenario: ADHD identifies itself to the smoke-alarm on every /status poll
    Given ADHD has a stable instance ID established at startup
    When ADHD polls /status on a smoke-alarm
    Then the HTTP request includes ADHD's instance ID (e.g. via header or connection identity)
    And ADHD does not assert a rung value in the request

  Scenario: ADHD identifies itself to the smoke-alarm on SSE subscription
    Given ADHD's instance ID is established
    When ADHD opens an SSE connection to a smoke-alarm
    Then the SSE request carries ADHD's instance ID
    And no trust rung is self-declared in the request

  Scenario: ADHD identifies itself on JSON-RPC calls
    Given ADHD's instance ID is established
    When ADHD invokes "adhd.chaos.register-window" via JSON-RPC
    Then the request carries ADHD's instance ID
    And the smoke-alarm uses its registry lookup (not ADHD's claim) to determine the rung

  # ── receiving filtered responses ────────────────────────────────────────────

  Scenario: ADHD certified at rung 0 receives /status with target id and health only
    Given the smoke-alarm's registry holds ADHD at rung 0
    When the smoke-alarm returns /status to ADHD
    Then ADHD's LightUpdate carries: target id, state (healthy/unhealthy)
    And IsotopeID is empty string
    And IsotopeClassification is empty string
    And no error is raised — the response is structurally valid

  Scenario: ADHD certified at rung 2 receives isotope_id but not isotope_classification
    Given the smoke-alarm's registry holds ADHD at rung 2
    And the target carries a canonical isotope ID with classification "registered-chaos"
    When the smoke-alarm returns /status to ADHD
    Then ADHD's LightUpdate has a non-empty IsotopeID
    And IsotopeClassification is empty (the smoke-alarm omits it — rung 3 required)

  Scenario: ADHD certified at rung 3 receives isotope_classification and can suppress
    Given the smoke-alarm's registry holds ADHD at rung 3
    And the target carries IsotopeClassification "registered-chaos"
    When the smoke-alarm returns /status to ADHD
    Then ADHD's LightUpdate has IsotopeClassification "registered-chaos"
    And model.Update can apply suppression

  Scenario: ADHD does not raise an error when a field is absent due to rung filtering
    Given the smoke-alarm holds ADHD at rung 2 and omits isotope_classification
    When /status is returned
    Then ADHD treats IsotopeClassification as empty string
    And no warning is logged about the missing field
    And the dashboard continues operating normally

  # ── rung displayed in dashboard ────────────────────────────────────────────

  Scenario: current trust rung is visible in the dashboard detail view
    Given ADHD's trust rung is 3
    When the user views the status detail panel
    Then the detail panel shows "trust rung: 3 (chaos-certified)"

  Scenario: rung advancement is reflected in the dashboard without restart
    Given the dashboard is running at rung 2
    When a chaos suppression event advances the rung to 3
    Then the detail panel updates to show "trust rung: 3 (chaos-certified)"
    And a CapabilityVerifiedMsg{Domain: "certification", Details: "rung advanced to 3"} is emitted

  # ── 42i distance drives rung ────────────────────────────────────────────────

  Scenario: isotope-variation failure increases 42i distance and may demote the rung
    Given ADHD is at rung 3 with 42i distance 38
    When an isotope-variation failure is recorded (distance += 8)
    Then 42i distance becomes 46
    And if 46 exceeds the rung-3 floor (40), ADHD is demoted to rung 2
    And a CapabilityVerifiedMsg{Domain: "certification", Status: yellow, Details: "rung demoted to 2"} is emitted

  Scenario: 42i distance is shown alongside the trust rung in the detail view
    Given ADHD's rung is 2 and 42i distance is 64
    When the detail panel is rendered
    Then it shows "42i distance: 64" next to the rung indicator

  # ── rung as encryption stand-in ────────────────────────────────────────────
  # This is the authorization layer before encryption is added.
  # When encryption arrives, the wire protocol does not change — only the
  # mechanism that proves rung membership changes (from self-declaration
  # to a signed capability token).

  Scenario: ADHD's identification mechanism is compatible with the future signed-token model
    Given ADHD currently identifies itself via an instance ID header (plaintext, no crypto)
    Then when signed capability tokens are introduced, ADHD presents a token instead of a bare ID
    And the smoke-alarm's registry lookup and filtering logic does not change
    And the rung assigned to ADHD remains the smoke-alarm's decision — ADHD never asserts it
    And ADHD does not need to know its own rung to operate correctly
