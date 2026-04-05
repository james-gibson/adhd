@adhd @z3-epistemic @domain-rung-validation
Feature: Rung Validation Challenge-Response

  ADHD participates in the 42i trust mesh by issuing and responding to
  cryptographic rung validation challenges. Each instance generates a
  random startup secret at launch and exposes a derived public isotope
  identifier. External clients can challenge an instance to prove it has
  a real, running implementation by requesting a challenge receipt — a
  value only computable by an agent that knows its own secret.

  The receipt formula is:
    receipt = base64url(SHA256(instance_secret ":" feature_id ":" nonce))

  SHA256 preimage resistance means random guessing requires ~2^256 attempts,
  making it infeasible without a valid ADHD client. Each nonce binds the
  receipt to a specific challenge, preventing replay attacks.

  Background:
    Given ADHD is running in headless mode
    And the MCP server is accepting connections on its configured address

  Scenario: Instance exposes a stable public isotope identifier
    When a client calls "adhd.isotope.instance"
    Then the response includes an "isotope" field
    And the isotope is a non-empty base64url string
    And repeated calls within the same process return the same isotope

  Scenario: Instance responds to a rung validation challenge
    Given a feature ID "adhd/lights-status" and nonce "challenge-abc"
    When a client calls "adhd.rung.respond" with those parameters
    Then the response includes a "receipt" field
    And the receipt is a non-empty base64url string

  Scenario: Instance verifies its own receipt
    Given the instance has computed a receipt for feature "adhd/lights-status" and nonce "verify-nonce"
    When a verifier calls "adhd.rung.verify" with that receipt, feature, and nonce
    Then the response returns valid=true

  Scenario: Replay attack fails — receipt is nonce-bound
    Given the instance produced receipt R for feature "adhd/lights-status" and nonce "nonce-1"
    When a verifier calls "adhd.rung.verify" with receipt R, feature "adhd/lights-status", and a different nonce "nonce-2"
    Then the response returns valid=false

  Scenario: Random receipt fails verification
    When a verifier calls "adhd.rung.verify" with a randomly generated receipt for feature "adhd/lights-status" and nonce "nonce-x"
    Then the response returns valid=false
    And the probability of a random receipt passing is cryptographically negligible (less than 1 in 2^200)

  Scenario: Challenger issues a challenge to a peer and the peer verifies
    Given a peer ADHD instance is running and reachable at a known URL
    When the local instance calls "adhd.rung.challenge" with the peer's URL, feature "adhd/lights-status", and a fresh nonce
    Then the local instance sends "adhd.rung.respond" to the peer
    And the peer returns a receipt
    And the local instance sends "adhd.rung.verify" to the peer with that receipt
    And the response includes verified=true

  Scenario: Challenge to unreachable peer returns verified=false
    Given a target URL that is not reachable
    When a client calls "adhd.rung.challenge" with that URL
    Then the response includes verified=false
    And the response includes an error description
