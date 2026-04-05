@adhd
@z1-temporal
@domain-mcp-server
Feature: ADHD Certified Feature Browser — MCP Tools for Live Coverage
  ADHD exposes its live Gherkin feature coverage via MCP tools so that tuner
  and other agents can browse which scenarios are certified by active isotope
  transits in the cluster. Coverage is derived from transit records, not from
  static analysis — a feature is certified only when a live isotope observed
  in a real cluster member maps to its Gherkin scenario.

  The caller's trust rung (externally assigned by the smoke-alarm) determines
  which fields are returned. The feature list itself is always visible; isotope
  details and cluster member identity are rung-gated.

  # Dependencies:
  #   - adhd isotope-transit.feature   (transit records map isotope → scenario)
  #   - adhd trust-rungs.feature       (rung gates field visibility in responses)
  #   - adhd mcp-integration.feature   (MCP server infrastructure)

  Background:
    Given the ADHD MCP server is running
    And ADHD is monitoring a cluster (demo or real)

  # ── adhd.features.certified ─────────────────────────────────────────────────

  Scenario: adhd.features.certified returns the list of Gherkin features the cluster is certified for
    Given the cluster has active isotope transits mapping to 4 distinct scenarios
    When a JSON-RPC call is made to "adhd.features.certified"
    Then the response includes 4 feature entries
    And each entry contains: feature_file, scenario_name, certified (bool), last_transit

  Scenario: uncertified features are included in the response with certified=false
    Given ADHD has loaded 10 Gherkin feature files
    And only 4 have been certified by live transits
    When "adhd.features.certified" is called
    Then all 10 features appear in the response
    And 4 show certified=true and 6 show certified=false

  Scenario: caller at rung 0 receives feature names and certified flag only
    Given the caller is certified at rung 0
    When "adhd.features.certified" is called
    Then each entry contains: feature_file, scenario_name, certified
    And isotope_id is absent from all entries
    And cluster_member is absent from all entries
    And last_transit is absent from all entries

  Scenario: caller at rung 2 additionally receives isotope_id and last_transit
    Given the caller is certified at rung 2
    When "adhd.features.certified" is called
    Then each certified entry also contains: isotope_id, last_transit
    And cluster_member is still absent (min_rung 3)

  Scenario: caller at rung 3 receives full entry including cluster_member
    Given the caller is certified at rung 3
    When "adhd.features.certified" is called
    Then each certified entry contains: feature_file, scenario_name, certified,
      isotope_id, last_transit, cluster_member

  Scenario: cluster_member identifies which smoke-alarm target carried the certifying transit
    Given scenario "light transitions from dark to green" was certified by isotope
      observed at "smoke:alarm-a/t1"
    And the caller is at rung 3
    When "adhd.features.certified" is called
    Then the entry for that scenario has cluster_member="smoke:alarm-a/t1"

  # ── adhd.features.verify ────────────────────────────────────────────────────
  # Triggers a live probe of the cluster member that certified a feature,
  # returning the current status of the transit.

  Scenario: adhd.features.verify probes the certifying cluster member for a feature
    Given scenario "adhd/mdns-discovery" is certified by "smoke:alarm-a/t1"
    And the caller is at rung 3 (required to know the cluster_member)
    When a JSON-RPC call is made to "adhd.features.verify" with feature "adhd/mdns-discovery"
    Then ADHD sends a probe request to the smoke-alarm for target "t1"
    And the response includes: still_certified (bool), current_isotope_id, probe_time

  Scenario: adhd.features.verify returns still_certified=false when the isotope has changed
    Given scenario "adhd/mdns-discovery" was certified by isotope "iso-abc-001"
    When "adhd.features.verify" is called and the current probe returns isotope "iso-abc-002"
    Then still_certified=false
    And the response notes "isotope rotated: was iso-abc-001, now iso-abc-002"

  Scenario: adhd.features.verify is rejected for callers below rung 3
    Given the caller is certified at rung 1
    When "adhd.features.verify" is called
    Then the call is rejected with error code -32003
    And the error message is "insufficient trust rung: need 3, have 1"

  # ── adhd.features.cluster ───────────────────────────────────────────────────
  # Returns the cluster members ADHD is monitoring, with their certification status.
  # This is the "who can verify" surface — tuner uses it to know which members
  # to target for feature verification.

  Scenario: adhd.features.cluster returns all smoke-alarm members in the current cluster
    Given ADHD is monitoring 2 smoke-alarm instances ("alarm-a", "alarm-b")
    When "adhd.features.cluster" is called
    Then the response includes 2 members
    And each member has: name, mode (demo/real), trust_rung, certified_feature_count

  Scenario: cluster mode is "demo" when allow_plaintext_isotopes is enabled
    Given ADHD is running in demo mode
    When "adhd.features.cluster" is called
    Then the mode field is "demo"
    And a note field reads "plaintext isotopes: certification is dev-mode only"

  Scenario: cluster mode is "real" when cryptographic isotopes are in use
    Given ADHD is running against a production smoke-alarm cluster
    When "adhd.features.cluster" is called
    Then the mode field is "real"

  Scenario: caller at rung 0 sees member names and mode only
    Given the caller is at rung 0
    When "adhd.features.cluster" is called
    Then each member entry contains: name, mode
    And trust_rung and certified_feature_count are absent

  # ── demo cluster verification flow ─────────────────────────────────────────
  # In demo mode, ADHD uses plaintext isotopes. Tuner can request verification
  # of features by asking ADHD to exercise specific cluster members.

  Scenario: demo cluster features can be verified without cryptographic isotopes
    Given ADHD is in demo mode with "alarm-a" and "alarm-b" in the cluster
    And feature "adhd/mdns-discovery" is certified by a plaintext isotope on "alarm-a"
    When "adhd.features.verify" is called for "adhd/mdns-discovery"
    Then the probe is sent to "alarm-a"
    And still_certified=true if the same plaintext isotope ID is still present
    And the response notes "verified via demo cluster (plaintext isotopes)"

  Scenario: switching from demo to real cluster updates certification mode without clearing coverage
    Given ADHD has demo-mode coverage for 4 features
    When ADHD reconnects to a real smoke-alarm cluster
    Then the 4 features are marked uncertified until real isotopes re-certify them
    And the previous demo certifications are retained as "stale" entries
    And "adhd.features.certified" returns them with certified=false and a stale_demo_cert flag
