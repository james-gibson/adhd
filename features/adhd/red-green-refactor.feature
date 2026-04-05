@adhd
@z0-physical
@z1-temporal
@domain-certification
@rung-42i
Feature: Red-Green-Refactor — Incomplete Certification via -42i Magic

  By intent, these tests PASS but block full certification.
  This documents visible gaps that prevent progression to the next rung.

  The -42i rung (42 incomplete) is the state where:
    ✓ Core functionality works (agent skills execute end-to-end)
    ✓ Identity exists (isotope_id is generated)
    ✗ Verification is incomplete (isotope transit not yet implemented)

  Certification cannot advance until smoke-alarm side catches up.
  This prevents false confidence; gaps are explicit, not hidden.

  Background:
    Given ADHD is running and can reach the MCP endpoint
    And at least one smoke-alarm is reachable

  # ─────────────────────────────────────────────────────────────
  # GREEN: Agent Skills Work (These pass today)
  # ─────────────────────────────────────────────────────────────

  @green @skill-execution
  Scenario: Agent skill tool chains execute end-to-end
    When an agent calls adhd.status
    And captures the total light count
    Then adhd.lights.list returns at least that many lights
    And adhd.lights.get succeeds for the first light
    And adhd.isotope.instance returns a non-empty isotope ID

  @green @tool-chaining
  Scenario: Multi-step tool chains see consistent state across calls
    When an agent calls adhd.lights.list
    And captures the first light's name
    And then calls adhd.lights.get with that name
    Then the response includes the correct light details
    And subsequent calls to adhd.lights.list return the same ordering

  @green @resilience
  Scenario: Agent skills recover from transient errors
    When an agent calls adhd.lights.get with a nonexistent light
    Then the error response is JSON-RPC compliant
    And the next call to adhd.lights.list succeeds
    And the agent state is not corrupted

  # ─────────────────────────────────────────────────────────────
  # RED: Missing Isotope Bits (These block certification)
  # ─────────────────────────────────────────────────────────────

  @red @blocked-on-smoke-alarm @isotope-certification
  Scenario: Smoke-alarm exposes isotope_certified in selfdescription
    # BLOCKED: ocd-smoke-alarm hasn't implemented this
    Given a smoke-alarm is reachable at the cluster endpoint
    When ADHD checks /.well-known/smoke-alarm.json
    Then the response includes "isotope_certified": true
    And ADHD marks this alarm as a certified source

    # Why this matters: Without this, ADHD can't distinguish
    # trusted vs untrusted alarms. Suppression of chaos effects
    # depends on knowing which alarms are verified.

  @red @blocked-on-smoke-alarm @isotope-transit
  Scenario: Smoke-alarm includes isotope metadata in adhd.status response
    # BLOCKED: ocd-smoke-alarm hasn't added isotope fields to /status
    Given ADHD calls adhd.status on the MCP endpoint
    When smoke-alarm processes the query
    Then the status response includes:
      | field      | meaning                           |
      | isotope_id | globally unique execution ID      |
      | feature_id | which capability is being tested  |
      | nonce      | one-time value (prevents replay)  |
    And ADHD receives these in the MCP result

    # Why this matters: Without isotope metadata flowing through
    # every status response, ADHD can't validate receipt or
    # detect tampering.

  @red @blocked-on-adhd @receipt-validation
  Scenario: ADHD pre-computes receipt and validates smoke-alarm response
    # BLOCKED: ADHD hasn't implemented receipt pre-computation logic
    Given smoke-alarm will emit isotope metadata in status responses
    When ADHD prepares to accept a state change
    Then ADHD pre-computes: SHA256(feature_id || ":" || SHA256(payload) || ":" || nonce)
    And encodes as base64url to produce the expected receipt
    And compares the receipt against what smoke-alarm returned
    And rejects the response if receipts do not match

    # Why this matters: Receipt validation is the only protection
    # against injection or tampering during skill execution.
    # Without it, a compromised network path could alter state
    # and ADHD would not detect it.

  # ─────────────────────────────────────────────────────────────
  # REFACTOR: Transition Path (What enables progression)
  # ─────────────────────────────────────────────────────────────

  @refactor @blocker-list
  Scenario: List of blockers preventing full certification
    Given the current test results show:
      | result  | test                                    |
      | PASS    | agent skills execute end-to-end         |
      | PASS    | tool chaining is reliable               |
      | PASS    | error recovery works                    |
      | BLOCKED | isotope_certified in selfdescription    |
      | BLOCKED | isotope metadata in adhd.status         |
      | BLOCKED | receipt pre-computation in ADHD         |

    When fire-marshal evaluates certification readiness
    Then the assessment is: -42i (incomplete certification)
    And the reason is: "Blocked on smoke-alarm isotope transit implementation"
    And progression to next rung requires:
      | task                                              | owner        |
      | Implement isotope_certified field                 | ocd-smoke-alarm |
      | Add isotope metadata to /status responses         | ocd-smoke-alarm |
      | Implement receipt validation in model.go          | adhd         |
      | Run isotope-transit.feature on both sides         | both         |

  @refactor @gap-documentation
  Scenario: Gaps are visible, not hidden
    # The -42i rung makes the incomplete state explicit.
    # We don't hide behind "we'll do it later" — we document
    # exactly what's missing and why it matters.

    Given a developer runs the full test suite
    When they check the HURL tests
    Then red-green-refactor.hurl clearly shows three sections:
      | section | status                       | action              |
      | GREEN   | Tests PASS                   | Nothing needed      |
      | RED     | Tests are BLOCKED / SKIPPED  | Wait for dependencies |
      | REFACTOR| Transition documented        | See checklist above |

    And the developer understands:
      - ✓ This cluster is functional
      - ✗ This cluster is not fully certified
      - → Here's the exact path to full certification
