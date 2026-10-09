# ADHD Gherkin pattern catalog

Companion to the `adhd-gherkin-patterns` SKILL.md. The full tag taxonomy and
copy-paste templates for `features/adhd/`.

## Tag taxonomy

### Feature-level (header)

| Tag | Rule | Notes |
|---|---|---|
| `@adhd` | always, first | Marks the file as part of the ADHD suite |
| `@z0-physical` | layer, pick one | Concrete state of running components (24/29 files) |
| `@z1-temporal` | layer | Change over time: lifecycle, certification, trust advancement |
| `@z2-relational` | layer | Relationships/coordination between actors |
| `@z3-epistemic` | layer | Proof/knowledge: challenge-response, receipts |
| `@domain-<domain>` | exactly one | Capability under test |

Domains seen in the suite: `dashboard`, `lights`, `certification`,
`discovery`, `mcp-server`, `mcp-resilience`, `mcp-spec-validation`,
`smoke-alarm-network`, `smoke-alarm-chaos`, `cluster-registry`,
`registry-health`, `demo`, `headless`, `agent-skills`, `proxy`,
`rung-validation`.

Cross-cutting: `@continuous-monitoring`, `@negative-test`,
`@capability-discovery`, `@demo`, `@rung-42i`, `@integration`,
`@official-reference`, `@methodology`.

### Scenario-level

| Group | Tags |
|---|---|
| Lifecycle | `@lifecycle`, `@start`, `@stable`, `@downgrade-level-1`, `@downgrade-level-2`, `@degradation-progression`, `@temporary-degradation`, `@recovery-start`, `@rapid-revalidation`, `@recovery-complete`, `@downgrade-timeline`, `@100-percent-failure`, `@policy`, `@alerting`, `@display`, `@dashboard`, `@real-scenario`, `@complete-timeline` |
| Red/green/refactor | `@green`, `@red`, `@refactor`, `@blocked-on-smoke-alarm`, `@blocked-on-adhd`, `@isotope-certification`, `@isotope-transit`, `@receipt-validation`, `@blocker-list`, `@gap-documentation` |
| Negative | `@negative`, `@detection`, `@registry-cleanup`, `@edge-case`, `@typo-detection` |
| Phased work | `@phase-1`…`@phase-4`, `@bearer-token`, `@bearer-success`, `@api-key`, `@api-key-implementation`, `@oauth2`, `@oauth2-blocked`, `@complex`, `@blocker` |
| Signals | `@impact`, `@metrics`, `@test-first`, `@visibility`, `@completeness` |

## Templates

### Lifecycle / state machine

```gherkin
Feature: Certification Lifecycle — Real-Time Regression Detection

  Certification is not a one-time achievement. Endpoints must continuously
  pass smoke tests or their certification level is automatically downgraded.
  This prevents false confidence.

  Background:
    Given the ADHD smoke test runner is active

  @lifecycle @downgrade-level-1
  Scenario: Certification degrades when tests start failing
    Given an endpoint with status ✓ CERTIFIED
    When 2 out of the last 5 smoke tests fail
    Then status changes: ✓ CERTIFIED → ⚠ DEGRADED
    And actions appear:
      | Action | Impact |
      | Increase test frequency | Every 1 minute (not 5) |
      | Alert team | Notification sent |
```

### Rung-gated field visibility

```gherkin
  Scenario: caller at rung 2 additionally receives isotope_id and last_transit
    Given the caller is certified at rung 2
    When "adhd.features.certified" is called
    Then each certified entry also contains: isotope_id, last_transit
    And cluster_member is still absent (min_rung 3)
```

### Chaos / load

```gherkin
  Scenario: multiple targets change state concurrently
    Given 10 targets being monitored
    When all 10 targets change state simultaneously (healthy → unhealthy)
    Then smoke-alarm processes all changes
    And no targets are dropped or corrupted
    And aggregated health reflects worst-case (all unhealthy)

  Scenario: chaos test detects race conditions in aggregation
    When 100 concurrent state changes affect aggregation
    Then the final aggregated health is correct
    And the test would fail if aggregation wasn't thread-safe
```

### Negative test

```gherkin
Feature: Negative Certification — Detecting Non-MCP Endpoints

  Just as we verify that MCP endpoints ARE compliant, we must verify that
  non-MCP endpoints are correctly REJECTED. This prevents false negatives.

  @negative @detection
  Scenario: Reject endpoint that returns 404
    Given an endpoint at "https://example.com" with no MCP server
    When we attempt to call adhd.status via POST /mcp
    Then the response status is NOT 200
    And the endpoint is correctly identified as non-MCP
```

### Red / green / refactor

```gherkin
  @red @blocked-on-smoke-alarm @isotope-certification
  Scenario: Smoke-alarm exposes isotope_certified in selfdescription
    # BLOCKED: ocd-smoke-alarm hasn't implemented this
    Given a smoke-alarm is reachable at the cluster endpoint
    When ADHD checks /.well-known/smoke-alarm.json
    Then the response includes "isotope_certified": true

    # Why this matters: Without this, ADHD can't distinguish trusted
    # vs untrusted alarms.

  @refactor @blocker-list
  Scenario: List of blockers preventing full certification
    Given the current test results show:
      | result  | test                                 |
      | PASS    | agent skills execute end-to-end      |
      | BLOCKED | isotope_certified in selfdescription |
    When fire-marshal evaluates certification readiness
    Then the assessment is: -42i (incomplete certification)
    And progression to next rung requires:
      | task                       | owner           |
      | Implement isotope_certified | ocd-smoke-alarm |
```

### JSON-RPC tool surface

```gherkin
  Scenario: adhd.features.verify probes the certifying cluster member
    Given scenario "adhd/mdns-discovery" is certified by "smoke:alarm-a/t1"
    And the caller is at rung 3 (required to know the cluster_member)
    When a JSON-RPC call is made to "adhd.features.verify" with feature "adhd/mdns-discovery"
    Then ADHD sends a probe request to the smoke-alarm for target "t1"
    And the response includes: still_certified (bool), current_isotope_id, probe_time
```

## Vocabulary

| Term | Meaning |
|---|---|
| rung | Trust level 0–4; authorization by demonstrated capability |
| -42i | "42 incomplete": explicit incomplete-certification state |
| 42i distance | Numeric progress metric; failures increase it, can demote a rung |
| isotope_id | Globally unique execution ID carried by a health-check response |
| isotope_classification | Chaos classification; readable at rung 3 (e.g. `registered-chaos`) |
| transit | A recorded isotope observation; live evidence a scenario ran |
| receipt | `base64url(SHA256(secret ":" feature_id ":" nonce))`, nonce-bound proof |
| light | A dashboard status indicator (green/yellow/red) for a capability |
| smoke-alarm | External health-monitoring peer in the cluster |
| fire-marshal | Pre-deployment validation service |
| honeypot | Node that must never receive traffic |

## Anti-patterns

- `Scenario Outline:` / `Examples:` — not used in this suite; use explicit scenarios or
  a table inside a step.
- A `Feature:` description that summarizes steps instead of explaining intent and
  the failure it prevents.
- Restating totals that drift ("40 feature files", "16 tools") as if asserted.
- Asserting internal state or implementation instead of observable evidence.
- A client that asserts its own trust rung — identity is presented, the rung is
  assigned by the smoke-alarm.
