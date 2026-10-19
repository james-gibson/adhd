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

  # BLOCKED: none of the adhd.features.* tools described here exist yet. The
  # MCP server serves a fixed, static tool surface (internal/mcpserver/server.go,
  # handleToolsList) with no adhd.features.list / .certified / .verify / .cluster
  # entry, so every scenario in this file is @red: it documents the intended
  # surface, not verified behaviour. Tracked in james-gibson/adhd#24.
  # Without the @red tags a reader — and the report matcher — cannot tell a
  # declared-but-absent tool from a real, verified one. That is the whole point
  # of the -42i convention.

  # Dependencies:
  #   - adhd isotope-transit.feature   (transit records map isotope → scenario)
  #   - adhd trust-rungs.feature       (rung gates field visibility in responses)
  #   - adhd mcp-integration.feature   (MCP server infrastructure)

  Background:
    Given the ADHD MCP server is running
    And ADHD is monitoring a cluster (demo or real)

  # ── adhd.features.certified ─────────────────────────────────────────────────

  @red @blocked-on-adhd
  Scenario: adhd.features.certified returns the list of Gherkin features the cluster is certified for
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: tuner browses certification through this tool; with no tool there is nothing to observe.
    Given the cluster has active isotope transits mapping to 4 distinct scenarios
    When a JSON-RPC call is made to "adhd.features.certified"
    Then the response includes 4 feature entries
    And each entry contains: feature_file, scenario_name, certified (bool), last_transit

  @red @blocked-on-adhd
  Scenario: uncertified features are included in the response with certified=false
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: the certified=false path is part of the contract and is currently untestable.
    Given ADHD has loaded 10 Gherkin feature files
    And only 4 have been certified by live transits
    When "adhd.features.certified" is called
    Then all 10 features appear in the response
    And 4 show certified=true and 6 show certified=false

  @red @blocked-on-adhd
  Scenario: caller at rung 0 receives feature names and certified flag only
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: rung-gating is the privacy boundary; an untested gate is a false privacy claim.
    Given the caller is certified at rung 0
    When "adhd.features.certified" is called
    Then each entry contains: feature_file, scenario_name, certified
    And isotope_id is absent from all entries
    And cluster_member is absent from all entries
    And last_transit is absent from all entries

  @red @blocked-on-adhd
  Scenario: caller at rung 2 additionally receives isotope_id and last_transit
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: field visibility per rung is the authorization contract; here it is asserted but unenforced.
    Given the caller is certified at rung 2
    When "adhd.features.certified" is called
    Then each certified entry also contains: isotope_id, last_transit
    And cluster_member is still absent (min_rung 3)

  @red @blocked-on-adhd
  Scenario: caller at rung 3 receives full entry including cluster_member
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: cluster_member must not leak below rung 3; the gate is claimed but unimplemented.
    Given the caller is certified at rung 3
    When "adhd.features.certified" is called
    Then each certified entry contains: feature_file, scenario_name, certified,
      isotope_id, last_transit, cluster_member

  @red @blocked-on-adhd
  Scenario: cluster_member identifies which smoke-alarm target carried the certifying transit
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: provenance — which member certified a scenario — is claimed without a tool to return it.
    Given scenario "light transitions from dark to green" was certified by isotope
      observed at "smoke:alarm-a/t1"
    And the caller is at rung 3
    When "adhd.features.certified" is called
    Then the entry for that scenario has cluster_member="smoke:alarm-a/t1"

  # ── adhd.features.verify ────────────────────────────────────────────────────
  # Triggers a live probe of the cluster member that certified a feature,
  # returning the current status of the transit.

  @red @blocked-on-adhd
  Scenario: adhd.features.verify probes the certifying cluster member for a feature
    # BLOCKED: adhd has not implemented the adhd.features.verify MCP tool (james-gibson/adhd#24)
    # Why this matters: live verification is the demo's headline action; it currently calls nothing.
    Given scenario "adhd/mdns-discovery" is certified by "smoke:alarm-a/t1"
    And the caller is at rung 3 (required to know the cluster_member)
    When a JSON-RPC call is made to "adhd.features.verify" with feature "adhd/mdns-discovery"
    Then ADHD sends a probe request to the smoke-alarm for target "t1"
    And the response includes: still_certified (bool), current_isotope_id, probe_time

  @red @blocked-on-adhd
  Scenario: adhd.features.verify returns still_certified=false when the isotope has changed
    # BLOCKED: adhd has not implemented the adhd.features.verify MCP tool (james-gibson/adhd#24)
    # Why this matters: staleness detection is the point; with no tool, certification lapse is undetectable.
    Given scenario "adhd/mdns-discovery" was certified by isotope "iso-abc-001"
    When "adhd.features.verify" is called and the current probe returns isotope "iso-abc-002"
    Then still_certified=false
    And the response notes "isotope rotated: was iso-abc-001, now iso-abc-002"

  @red @blocked-on-adhd
  Scenario: adhd.features.verify is rejected for callers below rung 3
    # BLOCKED: adhd has not implemented the adhd.features.verify MCP tool (james-gibson/adhd#24)
    # Why this matters: an unenforced rung gate is an open surface, not a rejected call.
    Given the caller is certified at rung 1
    When "adhd.features.verify" is called
    Then the call is rejected with error code -32003
    And the error message is "insufficient trust rung: need 3, have 1"

  # ── adhd.features.cluster ───────────────────────────────────────────────────
  # Returns the cluster members ADHD is monitoring, with their certification status.
  # This is the "who can verify" surface — tuner uses it to know which members
  # to target for feature verification.

  @red @blocked-on-adhd
  Scenario: adhd.features.cluster returns all smoke-alarm members in the current cluster
    # BLOCKED: adhd has not implemented the adhd.features.cluster MCP tool (james-gibson/adhd#24)
    # Why this matters: tuner targets members via this tool; the "who can verify" surface does not exist.
    Given ADHD is monitoring 2 smoke-alarm instances ("alarm-a", "alarm-b")
    When "adhd.features.cluster" is called
    Then the response includes 2 members
    And each member has: name, mode (demo/real), trust_rung, certified_feature_count

  @red @blocked-on-adhd
  Scenario: cluster mode is "demo" when allow_plaintext_isotopes is enabled
    # BLOCKED: adhd has not implemented the adhd.features.cluster MCP tool (james-gibson/adhd#24)
    # Why this matters: demo/production parity is claimed for a tool that was never built.
    Given ADHD is running in demo mode
    When "adhd.features.cluster" is called
    Then the mode field is "demo"
    And a note field reads "plaintext isotopes: certification is dev-mode only"

  @red @blocked-on-adhd
  Scenario: cluster mode is "real" when cryptographic isotopes are in use
    # BLOCKED: adhd has not implemented the adhd.features.cluster MCP tool (james-gibson/adhd#24)
    # Why this matters: the production mode claim rests on an absent tool's response.
    Given ADHD is running against a production smoke-alarm cluster
    When "adhd.features.cluster" is called
    Then the mode field is "real"

  @red @blocked-on-adhd
  Scenario: caller at rung 0 sees member names and mode only
    # BLOCKED: adhd has not implemented the adhd.features.cluster MCP tool (james-gibson/adhd#24)
    # Why this matters: another rung-gated field claim with nothing enforcing it.
    Given the caller is at rung 0
    When "adhd.features.cluster" is called
    Then each member entry contains: name, mode
    And trust_rung and certified_feature_count are absent

  # ── demo cluster verification flow ─────────────────────────────────────────
  # In demo mode, ADHD uses plaintext isotopes. Tuner can request verification
  # of features by asking ADHD to exercise specific cluster members.

  @red @blocked-on-adhd
  Scenario: demo cluster features can be verified without cryptographic isotopes
    # BLOCKED: adhd has not implemented the adhd.features.verify MCP tool (james-gibson/adhd#24)
    # Why this matters: the demo path advertises end-to-end verification; it does not exercise it.
    Given ADHD is in demo mode with "alarm-a" and "alarm-b" in the cluster
    And feature "adhd/mdns-discovery" is certified by a plaintext isotope on "alarm-a"
    When "adhd.features.verify" is called for "adhd/mdns-discovery"
    Then the probe is sent to "alarm-a"
    And still_certified=true if the same plaintext isotope ID is still present
    And the response notes "verified via demo cluster (plaintext isotopes)"

  @red @blocked-on-adhd
  Scenario: switching from demo to real cluster updates certification mode without clearing coverage
    # BLOCKED: adhd has not implemented the adhd.features.certified MCP tool (james-gibson/adhd#24)
    # Why this matters: coverage and staleness transitions are claimed without a tool to observe them.
    Given ADHD has demo-mode coverage for 4 features
    When ADHD reconnects to a real smoke-alarm cluster
    Then the 4 features are marked uncertified until real isotopes re-certify them
    And the previous demo certifications are retained as "stale" entries
    And "adhd.features.certified" returns them with certified=false and a stale_demo_cert flag

  # ── refactor: what unblocks this file ───────────────────────────────────────

  @refactor @blocker-list
  Scenario: List of blockers preventing the certified feature browser from being verified
    Given the current test results show:
      | result  | test                                          |
      | BLOCKED | adhd.features.certified returns coverage      |
      | BLOCKED | adhd.features.verify probes a member          |
      | BLOCKED | adhd.features.cluster lists members           |
      | BLOCKED | adhd.features.list is served by tools/list    |

    When a reviewer asks which scenarios are verified
    Then the assessment is: -42i (declared, absent)
    And progression requires:
      | task                                          | owner  |
      | Implement adhd.features.list                  | adhd   |
      | Implement adhd.features.certified             | adhd   |
      | Implement adhd.features.verify                | adhd   |
      | Implement adhd.features.cluster               | adhd   |
      | Expose them in handleToolsList + handleToolsCall | adhd |
      | Update TestServerToolsList expected names      | adhd   |
      | Remove @red tags once served and verified      | adhd   |
