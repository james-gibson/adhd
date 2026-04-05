@adhd
@z2-relational
@domain-demo
Feature: ADHD Demo Showcase — All Features Working Together
  The demo mode is not a simplified preview — it is the full system running
  with a local cluster that exercises every feature simultaneously. The demo
  cluster provides: live isotope transits (plaintext stand-ins), skill
  certification advancing through rungs, chaos detection probes, honeypot
  nodes, and feature browsing from a real cluster.

  ADHD consolidates all of these into a single navigable view. A developer
  running demo mode sees exactly what production looks like, with local
  evidence instead of production isotopes.

  # The demo cluster (via lezz) provides:
  #   alarm-a  — primary smoke-alarm; runs health probes; emits isotopes
  #   alarm-b  — secondary; federation follower; routes MCP proxy calls
  #   adhd-mcp — headless ADHD registered as isotope with alarm-a
  #   honeypot-alpha — honeypot node; should never receive traffic
  #
  # Expected demo state after full initialisation:
  #   Cluster lights:    all green
  #   Features:          4+ certified (mdns-discovery, smoke-alarm-network, demo, headless)
  #   Skills:            open-the-pickle-jar at rung 2+ (equality certified)
  #   Honeypot:          clean (no contact detected)
  #   Trust rung:        1+ (dev-certified via plaintext isotopes)
  #   42i distance:      decreasing as transits accumulate

  Background:
    Given the lezz demo cluster is running (alarm-a, alarm-b, adhd-mcp, honeypot-alpha)
    And ADHD is started with the -demo flag

  # ── initialisation sequence ──────────────────────────────────────────────────

  Scenario: demo mode discovers the cluster and pre-certifies known domains
    Given ADHD starts with -demo flag
    When the demo cluster is discovered within 10 seconds
    Then the following domains are pre-certified via MarkPreVerified:
      | domain | details                              |
      | demo   | N cluster(s) discovered via lezz     |
    And the boot sequence completes with all cluster lights initialised

  Scenario: isotope transits begin within the first polling cycle
    Given the demo cluster is connected
    When the first /status poll completes for alarm-a
    Then at least one LightUpdate carries a non-empty IsotopeID (plaintext stand-in)
    And the transit is recorded in ADHD's transit log
    And ADHD's trust rung advances to at least rung 1 (dev-certified)

  Scenario: structural evidence certifies domains without waiting for health to stabilise
    Given alarm-a reports target "peer" in its /status response
    When the LightUpdate for "peer" is processed
    Then @domain-discovery is certified via certifyFromSmokeTarget
    And the feature light for "ADHD mDNS Smoke-Alarm Discovery" is green
    And this certification is independent of whether the peer is healthy

  # ── all features working together ────────────────────────────────────────────

  Scenario: demo mode certifies at least four feature domains simultaneously
    Given the demo cluster has been running for one polling cycle
    When ADHD evaluates its certified domains
    Then the following domains are certified with status green:
      | domain              | certifying evidence                              |
      | demo                | MarkPreVerified at startup                       |
      | discovery           | "peer" target observed on alarm-a                |
      | smoke-alarm-network | "self-health-check" target observed              |
      | headless            | probeIsotopeRegistration returned non-empty list |

  Scenario: the feature browser shows live certification from the demo cluster
    Given all four domains are certified
    When the user opens the Feature Browser panel (Tab)
    Then features for certified domains show ● green with cluster_member provenance
    And uncertified features show ○ dark
    And the browser title shows at least "4/N certified"
    And a [demo] badge appears next to each certified entry

  Scenario: skill certification advances during demo operation
    Given "open-the-pickle-jar" is announced by alarm-b
    When the smoke-alarm's heartbeat cycle runs the equality probe
    Then "open-the-pickle-jar" advances from rung 1 (existence) to rung 2 (equality)
    And the Skill view in ADHD updates to show "deterministic" for that skill

  Scenario: honeypot node is present and clean throughout the demo
    Given "honeypot-alpha" is in the demo cluster
    When all demo features are exercised (discovery, chaos probes, skill routing)
    Then "honeypot-alpha" has zero contact events
    And the Honeypot view shows "1/1 ◈ clean"
    And the "Routing Integrity: ✓" badge is green in the header

  Scenario: chaos detection probe runs during demo and finds no misbehaviour
    Given the demo cluster includes a chaos detection probe for alarm-b
    When the probe is delivered and the receipt is observed
    Then the receipt matches the expected isotope
    And the routing trace includes alarm-b's instance ID
    And no detection event is raised
    And alarm-b's trust rung does not decrease

  # ── browsing as a first-class demo activity ───────────────────────────────────
  # Demonstrating the system means showing that the features ARE the system.
  # The feature browser and skill browser are navigated as part of the demo,
  # not as a side panel — they are the primary evidence of system correctness.

  Scenario: a complete demo walkthrough navigates all four panels
    Given ADHD is in demo mode with all domains certified
    When a developer performs the demo walkthrough:
      1. View cluster lights (all green, honeypot clean)
      2. Tab to Feature Browser → see 4+ certified features
      3. Select "adhd/mdns-discovery" → detail panel shows certified scenarios
      4. Tab to Skill view → see "open-the-pickle-jar" at rung 2
      5. Tab to Honeypot view → see "1/1 clean"
      6. Press v on a certified feature → live verification probe runs
    Then each step produces visible, correct output
    And the walkthrough demonstrates isotopes, skills, honeypots, and features as one system

  Scenario: pressing v on a certified demo feature triggers live verification
    Given "adhd/mdns-discovery" is selected in the feature browser
    And ADHD is at rung 3
    When the user presses v
    Then ADHD calls "adhd.features.verify" against the certifying cluster member
    And the detail panel shows the verification result within 5 seconds
    And if verified: "still certified ✓ (via plaintext isotope)" in green
    And if lapsed: "certification lapsed — isotope rotated" in yellow

  # ── demo cluster as an integration test ──────────────────────────────────────
  # Running demo mode with -demo -test exits after all assertions pass.
  # This makes the demo cluster the integration test suite.

  Scenario: demo mode with -test flag runs assertions and exits with status 0 on success
    Given ADHD is started with -demo -test flags
    When the demo cluster is connected and one full polling cycle completes
    Then ADHD asserts:
      | assertion                                      | expected |
      | trust_rung >= 1                                | pass     |
      | certified_domain_count >= 4                    | pass     |
      | honeypot_contact_count == 0                    | pass     |
      | at least one isotope transit recorded          | pass     |
      | at least one skill at rung >= 2                | pass     |
    And ADHD exits with status 0 if all assertions pass
    And exits with status 1 and a failure report if any assertion fails

  Scenario: the -test mode failure report names the failing assertion and its evidence
    Given "honeypot_contact_count == 0" fails because a contact was detected
    When ADHD exits with status 1
    Then the failure output includes:
      "FAIL honeypot_contact_count: expected 0, got 1"
      "  detection event: honeypot-alpha contacted by [instance-id] at <timestamp>"
    And the report is written to stdout as structured text (not just a panic)

  # ── demo and production parity ───────────────────────────────────────────────

  Scenario: demo mode and production mode differ only in isotope type, not in code paths
    Given the same ADHD binary runs in both demo and production
    Then all certification, routing, rung, and verification logic is identical
    And only the isotope format differs (plaintext vs cryptographic)
    And the [demo] label in the UI is the only visible distinction
    And the -test assertions are valid in both modes
