@adhd
@z0-physical
@domain-lights
Feature: ADHD Lights Status Indicators
  Lights accurately represent service and feature status

  Scenario: Light starts with unknown status
    Given a newly created light
    Then its status is "dark" (unknown)
    And the last-updated timestamp is recent

  Scenario: Light status transitions are valid
    Given a light with status "dark"
    When the status is set to "green"
    Then the light shows green
    And the last-updated timestamp is updated
    When the status is set to "red"
    Then the light shows red

  Scenario: Cluster counts lights by status
    Given a cluster with 3 lights
    When 2 lights are set to "green"
    And 1 light is set to "red"
    Then cluster.CountByStatus("green") returns 2
    And cluster.CountByStatus("red") returns 1
    And cluster.CountByStatus("dark") returns 0

  Scenario: Light can be retrieved by name
    Given a cluster with lights:
      | name          | type    |
      | primary       | service |
      | secondary     | service |
      | feature-test  | feature |
    When I query for light "secondary"
    Then a light object is returned
    And the light's name is "secondary"
    And the light's type is "service"

  Scenario: Light lookup returns nil for unknown name
    Given a cluster with existing lights
    When I query for light "does-not-exist"
    Then nil is returned
    And no error is raised

  Scenario: Light displays current status
    Given a light with status "green"
    When I render the light
    Then the indicator shows 🟢
    When the status is changed to "red"
    And I render the light
    Then the indicator shows 🔴
    When the status is changed to "yellow"
    And I render the light
    Then the indicator shows 🟡
    When the status is changed to "dark"
    And I render the light
    Then the indicator shows ⚫

  # ── startup flicker guard ─────────────────────────────────────────────────
  # Feature lights must not go red during startup before the cluster has had
  # a chance to reach a known-good baseline. Early probe failures are transient,
  # not evidence of a feature being broken.

  Scenario: feature lights stay dark until the cluster has been fully green at least once
    Given ADHD starts and smoke-alarm endpoints are reachable but not yet ready
    When the first poll returns red for one or more targets
    Then feature lights remain "dark"
    And clusterEverHealthy is false

  Scenario: feature lights start tracking health after the first all-green probe
    Given clusterEverHealthy is false
    When all smoke: target lights report "green" for the first time
    Then clusterEverHealthy is set to true
    And feature lights are set to "green"

  Scenario: feature lights turn red after baseline when cluster degrades
    Given clusterEverHealthy is true
    When a smoke: target light transitions to "red"
    Then feature lights without a domain-specific verifier are set to "red"

  Scenario: feature lights with domain-specific verifiers are not affected by the flicker guard
    Given domain "discovery" has been certified via CapabilityVerifiedMsg
    And clusterEverHealthy is false
    When any smoke: target goes red
    Then @domain-discovery feature lights are unchanged by the flicker guard
