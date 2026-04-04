@adhd
@z0-physical
@domain-discovery
Feature: ADHD mDNS Smoke-Alarm Discovery
  ADHD continuously discovers smoke-alarm instances on the local network
  and creates health-check lights for each one without restart

  Background:
    Given ADHD is running with mDNS discovery enabled

  # ── initial discovery ────────────────────────────────────────────────────────

  Scenario: light created for a smoke-alarm already advertising at startup
    Given a smoke-alarm instance is advertising "_smoke-alarm._tcp" before ADHD starts
    When ADHD starts
    Then a light named "smoke-alarm:<hostname>" is created
    And the light's status is "dark"
    And the light's source is "mdns"

  Scenario: light transitions from dark to green on first successful health-check
    Given a light "smoke-alarm:host-a" has been created with status "dark"
    When ADHD polls the instance's /status endpoint and receives a healthy response
    Then the light's status transitions to "green"

  Scenario: light transitions from dark to red on first failed health-check
    Given a light "smoke-alarm:host-b" has been created with status "dark"
    When ADHD polls the instance's /status endpoint and receives an error response
    Then the light's status transitions to "red"

  Scenario: multiple smoke-alarms at startup each receive their own light
    Given 3 smoke-alarm instances are advertising "_smoke-alarm._tcp"
    When ADHD starts
    Then 3 lights are created with source "mdns"
    And each light has a distinct name derived from its hostname

  # ── continuous browse ─────────────────────────────────────────────────────────

  Scenario: light created for a smoke-alarm that comes online after startup
    Given ADHD has been running for 30 seconds with 2 existing mDNS lights
    When a new smoke-alarm "host-c" announces "_smoke-alarm._tcp"
    Then a light "smoke-alarm:host-c" is added to the dashboard
    And the dashboard updates without restart

  Scenario: browse loop remains active after the initial discovery window
    Given ADHD has been running for 60 seconds
    When a smoke-alarm announces after 60 seconds
    Then the announcement is received and a light is created

  Scenario: browse does not close after the first result batch
    Given 2 smoke-alarms are discovered in the initial browse window
    When the initial browse window closes
    Then the browser continues listening
    And a third smoke-alarm announcing after the window receives a light

  # ── instance departure ───────────────────────────────────────────────────────

  Scenario: light turns red when a smoke-alarm stops responding to health-checks
    Given a light "smoke-alarm:host-a" has status "green"
    When the smoke-alarm instance stops responding to health-checks
    Then the light's status becomes "red"
    And the light remains in the dashboard

  Scenario: light is removed when a smoke-alarm deregisters its mDNS record
    Given a light "smoke-alarm:host-b" exists in the dashboard
    When "host-b" deregisters its "_smoke-alarm._tcp" mDNS record
    Then the light "smoke-alarm:host-b" is removed from the dashboard

  # ── coexistence with static config ───────────────────────────────────────────

  Scenario: mDNS-discovered lights coexist with statically-configured lights
    Given ADHD has 2 lights from static config with source "config"
    When a smoke-alarm is discovered via mDNS
    Then a third light is added with source "mdns"
    And the 2 static lights are unchanged

  Scenario: mDNS does not create a duplicate light for a statically-configured instance
    Given a smoke-alarm "host-a" is already present as a static light with source "config"
    When "host-a" also announces via mDNS
    Then no duplicate light is created for "host-a"
    And the existing light's source remains "config"

  # ── isotope transit and privacy ───────────────────────────────────────────────
  # Health-check responses carry isotopes. ADHD records isotope transit —
  # never the response payload. Each isotope maps to a Gherkin scenario;
  # the transit is live evidence that the scenario is being exercised.

  Scenario: health-check response isotope is recorded as a transit event, not payload data
    Given a health-check response for "smoke-alarm:host-a" carries isotope "isotope-probe-healthy-001"
    When ADHD processes the response
    Then a transit event is recorded: isotope "isotope-probe-healthy-001" observed at "adhd:host-a"
    And no response payload data appears in the transit event or any metric

  Scenario: isotope transit updates the live feature coverage report
    Given isotope "isotope-probe-healthy-001" maps to the scenario "light transitions from dark to green"
    When the isotope transits through the health-check for "smoke-alarm:host-a"
    Then that scenario is marked as covered in the live coverage report

  # ── isotope transit as 42i boundary authorization ────────────────────────────
  # A transit declaration (feature_id → component) is a boundary authorization.
  # Violations raise the agent's 42i distance via smoke-alarm test dimensions.

  Scenario: an isotope transiting its declared boundary clears scope-compliance for that dimension
    Given isotope "isotope-probe-healthy-001" is declared for feature "adhd/light-transitions-dark-to-green"
    When the isotope transits the ADHD health-check component
    Then the scope-compliance test passes for this dimension
    And no 42i distance is added

  Scenario: an isotope observed at the wrong ADHD component is a scope-compliance failure
    Given isotope "isotope-probe-healthy-001" is declared for feature "adhd/light-transitions-dark-to-green"
    When the isotope is observed at the mDNS discovery component instead
    Then a scope-compliance failure is recorded
    And the agent's 42i distance increases by 20 units

  Scenario: a replayed isotope in ADHD is an isotope-variation failure
    Given isotope "isotope-probe-healthy-001" has already been observed in this monitoring window
    When ADHD observes the same isotope ID a second time
    Then an isotope-variation failure is recorded
    And the agent's 42i distance increases by 8 units

  Scenario: health-check payload in a transit event is a secret-flow-violation
    Given ADHD emits a transit event for isotope "isotope-probe-healthy-001"
    When the event contains any field from the health-check response body
    Then a secret-flow-violation is recorded
    And the agent's 42i distance increases by 24 units

  # ── isotope ID construction properties ────────────────────────────────────────
  # isotope_id = base64url( SHA256( feature_id || ":" || SHA256(payload) || ":" || nonce ) )

  Scenario: two isotope IDs for the same feature and payload are not equal
    Given feature "adhd/light-transitions-dark-to-green" and a fixed health-check payload
    When two isotope IDs are constructed for the same feature and payload
    Then the two IDs are different
    And each is 43 base64url characters

  Scenario: ADHD can verify an isotope against its declared feature binding
    Given isotope "isotope-probe-healthy-001" was constructed from feature "adhd/light-transitions-dark-to-green", a payload, and a nonce
    When ADHD verifies the isotope with those three inputs
    Then verification succeeds

  Scenario: ADHD rejects an isotope presented for the wrong feature
    Given isotope "isotope-probe-healthy-001" was constructed for feature "adhd/light-transitions-dark-to-green"
    When verification is attempted against feature "adhd/light-transitions-dark-to-red"
    Then verification fails

  Scenario: no health-check payload is recoverable from the isotope ID alone
    Given isotope "isotope-probe-healthy-001" appears in a transit event
    When an attempt is made to extract the health-check response from the ID
    Then no payload information is recoverable

  # ── chaos isotope awareness ───────────────────────────────────────────────────

  Scenario: light does not transition to red when a health-check failure carries a registered chaos isotope
    Given a light "smoke-alarm:host-a" has status "green"
    And isotope "isotope-chaos-001" is registered as a chaos isotope with an active window for "host-a"
    When a health-check failure arrives for "host-a" carrying isotope "isotope-chaos-001"
    Then the light's status remains "green"
    And the failure is noted as an expected chaos event in the light's detail field

  Scenario: light transitions to red when a failure carries an unregistered isotope
    Given a light "smoke-alarm:host-a" has status "green"
    And no chaos isotope is registered for "host-a"
    When a health-check failure arrives for "host-a" with an unregistered isotope
    Then the light's status becomes "red"

  Scenario: light transitions to red when a chaos isotope arrives outside its declared window
    Given a light "smoke-alarm:host-a" has status "green"
    And "isotope-chaos-002" was registered for "host-a" with a window that has now expired
    When a health-check failure arrives for "host-a" carrying isotope "isotope-chaos-002"
    Then the light's status becomes "red"
    And the detail field notes "chaos isotope arrived outside declared window"

  # ── Bubble Tea integration ────────────────────────────────────────────────────

  Scenario: discovery events are delivered as Bubble Tea messages
    Given the mDNS browser is running as a tea.Cmd loop
    When a new smoke-alarm is discovered
    Then a SmokeAlarmDiscoveredMsg is delivered into the Bubble Tea update cycle
    And model.Update handles the message and appends the new light to the cluster

  Scenario: model.Update is the only place lights are added from discovery
    Given a SmokeAlarmDiscoveredMsg arrives in the update cycle
    When model.Update processes it
    Then the light is appended to the cluster inside Update
    And no direct state mutation occurs outside of model.Update
