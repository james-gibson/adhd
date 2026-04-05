@adhd
@z1-temporal
@domain-mcp-server
Feature: ADHD Skill Certification Awareness — Dynamic Trust for Cluster Skills
  ADHD tracks the certified rung of every skill in the cluster it monitors.
  Skill rungs are assigned by the smoke-alarm (externally, not self-declared)
  and change dynamically as evidence accumulates or lapses. ADHD exposes
  the current skill registry via MCP tools, gated by the caller's trust rung.

  # Skills and isotopes are symmetric:
  #   isotope → proves a scenario ran
  #   skill   → proves a capability is available and behaves correctly
  #
  # Both use the same rung scale:
  #   rung 0 = declared only (existence claimed, not verified)
  #   rung 1 = existence verified (skill responds to a probe)
  #   rung 2 = equality verified (deterministic output for equal inputs)
  #   rung 3 = isotope-certified (execution mapped to a Gherkin scenario)
  #
  # Dependencies:
  #   - ocd-smoke-alarm skill-certification.feature  (rung assignment, distance, variation)
  #   - adhd trust-rungs.feature                     (caller rung gates response fields)
  #   - adhd certified-features.feature              (feature coverage from isotope transits)

  Background:
    Given ADHD is monitoring a cluster
    And the cluster's smoke-alarm is certifying skill rungs dynamically

  # ── receiving skill certification updates ────────────────────────────────────

  Scenario: ADHD receives skill rung updates from the smoke-alarm's /membership endpoint
    Given the smoke-alarm's /membership response includes skill rungs for each peer
    When ADHD polls /membership
    Then ADHD updates its local skill registry with the current certified rungs
    And a CapabilityVerifiedMsg is emitted for each skill that changed rung

  Scenario: CapabilityVerifiedMsg is emitted when a skill advances from rung 1 to rung 2
    Given "open-the-pickle-jar" on "alarm-a" was at rung 1
    When /membership reports "open-the-pickle-jar" at rung 2
    Then ADHD emits CapabilityVerifiedMsg{Domain: "mcp-server", Status: green,
      Details: "skill open-the-pickle-jar equality-certified at rung 2"}

  Scenario: ADHD demotes a skill when /membership reports a rung decrease
    Given "open-the-pickle-jar" is tracked at rung 3 in ADHD's registry
    When /membership reports "open-the-pickle-jar" at rung 2
    Then ADHD updates the skill to rung 2
    And emits CapabilityVerifiedMsg{Status: yellow, Details: "skill rung demoted: open-the-pickle-jar 3→2"}

  # ── adhd.skills.certified MCP tool ──────────────────────────────────────────

  Scenario: adhd.skills.certified returns all skills with their current certified rung
    Given the cluster has 5 skills at various rungs
    When a JSON-RPC call is made to "adhd.skills.certified"
    Then the response includes all 5 skills
    And each entry contains: skill_name, certified_rung, distance, host_instance

  Scenario: caller at rung 0 sees skill names and certified_rung only
    Given the caller is certified at rung 0
    When "adhd.skills.certified" is called
    Then each entry contains: skill_name, certified_rung
    And distance and host_instance are absent
    And skills at rung 0 are excluded (existence not verified)

  Scenario: caller at rung 2 additionally receives distance and host_instance
    Given the caller is certified at rung 2
    When "adhd.skills.certified" is called
    Then each entry includes: skill_name, certified_rung, distance, host_instance
    And isotope_binding is absent (min_rung 3)

  Scenario: caller at rung 3 receives isotope_binding linking the skill to a Gherkin scenario
    Given the caller is certified at rung 3
    And "open-the-pickle-jar" is isotope-certified with binding "adhd/open-the-pickle-jar"
    When "adhd.skills.certified" is called
    Then the entry includes isotope_binding="adhd/open-the-pickle-jar"

  # ── equality as certification signal ─────────────────────────────────────────
  # Equality certification means the skill is deterministic. ADHD can use this
  # to verify that a skill hasn't changed behaviour between invocations —
  # a skill-variation failure from the smoke-alarm appears in ADHD's registry.

  Scenario: ADHD records a skill-variation failure when the smoke-alarm reports one
    Given "start-here" on "alarm-b" had a skill-variation failure (non-equal outputs)
    When ADHD processes the membership update
    Then ADHD records a variation failure for "start-here"
    And the skill's 42i distance increases by 8 in ADHD's view
    And a CapabilityVerifiedMsg{Status: yellow, Details: "skill-variation failure: start-here"} is emitted

  Scenario: ADHD's 42i distance for a skill reflects accumulated variation failures
    Given "start-here" has had 3 variation failures (total distance penalty 24)
    When ADHD renders the skill list
    Then "start-here" shows 42i-distance: 24 in the detail view

  # ── existence as the minimal certification ───────────────────────────────────
  # A skill at rung 0 has only been declared in the InstanceRecord — it has never
  # been probed. ADHD treats rung-0 skills as unreliable and does not expose them
  # to callers or forward routing to them.

  Scenario: ADHD does not expose rung-0 skills in adhd.skills.certified responses
    Given "ghost-skill" on "alarm-b" is at rung 0 (existence never confirmed)
    When "adhd.skills.certified" is called by any caller
    Then "ghost-skill" does not appear in the response

  Scenario: ADHD shows a summary count of rung-0 skills for diagnostics at rung 4
    Given the cluster has 2 rung-0 skills
    And the caller is at rung 4
    When "adhd.skills.certified" is called
    Then the response summary includes unverified_skill_count=2
    And a note reads "skills at rung 0 are excluded from routing and not shown"

  # ── skill distance and light propagation ─────────────────────────────────────
  # Skill distance (proxy hops from the certifying alarm) affects which feature
  # lights ADHD certifies. A skill at distance 0 certifies its domain fully;
  # a skill at distance 3 certifies with reduced confidence.

  Scenario: a skill at distance 0 fully certifies its domain's feature lights
    Given "open-the-pickle-jar" at distance 0 is isotope-certified for "adhd/skill-system"
    When ADHD processes the skill certification
    Then applyCapabilityToFeatures("mcp-server", green, ...) is called
    And the feature lights for @domain-mcp-server are set green

  Scenario: a skill at distance 3 certifies its domain lights as yellow (reduced confidence)
    Given "open-the-pickle-jar" at distance 3 is isotope-certified for "adhd/skill-system"
    When ADHD processes the skill certification
    Then applyCapabilityToFeatures("mcp-server", yellow, "skill certified at distance 3") is called
    And the feature lights for @domain-mcp-server are set yellow, not green

  # ── dynamic certification in the dashboard ───────────────────────────────────

  Scenario: the dashboard detail panel shows current skill certification status
    Given the dashboard has a skill panel in its detail view
    When the user inspects "open-the-pickle-jar"
    Then the panel shows: certified_rung, distance, variation_failures, isotope_binding (if rung permits)

  Scenario: a skill rung change triggers a dashboard re-render without restart
    Given the dashboard is showing "start-here" at rung 2
    When a CapabilityVerifiedMsg arrives with "start-here" promoted to rung 3
    Then the dashboard updates "start-here" to show rung 3 in real time
    And the corresponding @domain-mcp-server feature lights are re-evaluated
