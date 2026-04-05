@adhd
@z0-physical
@domain-discovery
Feature: ADHD Certification Gate — Trusted vs Uncertified Smoke-Alarms
  ADHD reads isotope certification status from each smoke-alarm's selfdescription
  (/.well-known/smoke-alarm.json) at discovery time. Certified alarms participate
  fully in chaos isotope classification; uncertified alarms are displayed with
  reduced trust and suppression is disabled for their targets.

  # Dependencies (must be implemented first):
  #   - ocd-smoke-alarm certification-gate.feature  (isotope_certified in selfdescription)
  #   - ocd-smoke-alarm isotope-transit.feature     (isotope_id flows through /status)
  #   - adhd isotope-transit.feature                (LightUpdate carries isotope metadata)
  #   - adhd chaos-isotopes.feature                 (suppression logic)

  Background:
    Given ADHD is running and has discovered at least one smoke-alarm
    And the smoke-alarm's /.well-known/smoke-alarm.json is readable

  # ── certification discovery ─────────────────────────────────────────────────

  Scenario: ADHD reads isotope_certified from the alarm's selfdescription
    Given a smoke-alarm at "alarm-a" returns isotope_certified=true in its selfdescription
    When ADHD adds "alarm-a" to the cluster
    Then "alarm-a" is tracked internally as a certified source

  Scenario: ADHD defaults to uncertified when isotope_certified is absent from selfdescription
    Given a smoke-alarm at "alarm-b" returns a selfdescription with no isotope_certified field
    When ADHD adds "alarm-b" to the cluster
    Then "alarm-b" is treated as uncertified

  Scenario: certification status is re-evaluated when the selfdescription changes
    Given "alarm-a" is certified in ADHD's registry
    And "alarm-a"'s selfdescription is updated with isotope_certified=false
    When ADHD next polls "alarm-a"'s selfdescription
    Then "alarm-a" is downgraded to uncertified in ADHD's registry
    And a dashboard event is emitted: "alarm-a certification revoked"

  # ── display differentiation ─────────────────────────────────────────────────

  Scenario: certified alarm's lights are displayed without a trust annotation
    Given "alarm-a" is certified
    When ADHD renders the smoke: cluster lights for "alarm-a"
    Then no "uncertified" annotation appears on those lights

  Scenario: uncertified alarm's lights are shown with an uncertified annotation
    Given "alarm-b" is uncertified
    When ADHD renders the smoke: cluster lights for "alarm-b"
    Then each light from "alarm-b" displays an "uncertified" annotation
    And the annotation is visually distinct (e.g. dimmed or labelled "~cert")

  # ── chaos suppression trust boundary ───────────────────────────────────────

  Scenario: chaos suppression is applied normally for a certified alarm's LightUpdates
    Given "alarm-a" is certified
    And a LightUpdate arrives from "alarm-a" with IsotopeClassification "registered-chaos"
    When model.Update processes the LightUpdate
    Then the light is suppressed (remains green)

  Scenario: chaos suppression is NOT applied for an uncertified alarm's LightUpdates
    Given "alarm-b" is uncertified
    And a LightUpdate arrives from "alarm-b" with IsotopeClassification "registered-chaos"
    When model.Update processes the LightUpdate
    Then the light turns red (suppression is skipped)
    And the Details field notes "suppression skipped: uncertified source alarm-b"

  Scenario: an uncertified alarm with an unregistered isotope still turns red
    Given "alarm-b" is uncertified
    And a LightUpdate arrives from "alarm-b" with Status "red" and IsotopeClassification "unregistered"
    When model.Update processes the LightUpdate
    Then the light turns red
    And no isotope details are surfaced (uncertified source is not trusted for classification)

  # ── certification gate for 42i domain coverage ─────────────────────────────
  # Certified alarms contribute to domain certification. Uncertified alarms do not.

  Scenario: structural evidence from a certified alarm certifies @domain-discovery
    Given "alarm-a" is certified
    And a /status response from "alarm-a" includes target "peer"
    When model.Update processes the LightUpdate for "peer"
    Then @domain-discovery is certified via certifyFromSmokeTarget

  Scenario: structural evidence from an uncertified alarm does not certify any domain
    Given "alarm-b" is uncertified
    And a /status response from "alarm-b" includes target "peer"
    When model.Update processes the LightUpdate for "peer"
    Then no domain certification is updated
    And a debug log entry is written: "skipping domain cert from uncertified source alarm-b"

  # ── MCP chaos window registration ──────────────────────────────────────────

  Scenario: adhd.chaos.register-window is forwarded only to certified alarms
    Given "alarm-a" is certified and "alarm-b" is uncertified
    When a JSON-RPC call is made to "adhd.chaos.register-window" with target on "alarm-a"
    Then ADHD sends POST /isotope/register-chaos-window to "alarm-a"

  Scenario: adhd.chaos.register-window returns an error for a target on an uncertified alarm
    Given "alarm-b" is uncertified
    When a JSON-RPC call is made to "adhd.chaos.register-window" with target on "alarm-b"
    Then the MCP tool returns an error result
    And the error message includes "alarm-b is not certified for chaos isotope registration"

  # ── federation-aware certification in demo cluster ──────────────────────────

  Scenario: ADHD in demo mode treats dev-certified alarms as certified
    Given ADHD is configured with allow_plaintext_isotopes=true (demo mode)
    And a smoke-alarm advertises isotope_certified=dev-certified in its selfdescription
    When ADHD adds the alarm to the cluster
    Then the alarm is treated as certified for suppression and domain certification
    And a "info" log entry is written: "accepting dev-certified alarm in demo mode"

  Scenario: ADHD in production mode treats dev-certified alarms as uncertified
    Given ADHD is configured with allow_plaintext_isotopes=false (production mode)
    And a smoke-alarm advertises isotope_certified=dev-certified
    When ADHD adds the alarm to the cluster
    Then the alarm is treated as uncertified
    And a "warn" log entry is written: "dev-certified alarm rejected in production mode"
