@adhd
@z0-physical
@domain-certification
@negative-test
Feature: Negative Certification — Detecting Non-MCP Endpoints

  Just as we verify that MCP endpoints ARE compliant,
  we must verify that non-MCP endpoints are correctly REJECTED.

  This prevents false negatives: accidentally trusting systems
  that don't actually implement the MCP protocol.

  Background:
    Given we have a list of endpoints (both MCP and non-MCP)

  # ──────────────────────────────────────────────────────────────
  # Detection: Non-Existent MCP Server
  # ──────────────────────────────────────────────────────────────

  @negative @detection
  Scenario: Reject endpoint that returns 404
    Given an endpoint at "https://example.com" with no MCP server
    When we attempt to call adhd.status via POST /mcp
    Then the response status is NOT 200
    And the endpoint is correctly identified as non-MCP

  @negative @detection
  Scenario: Reject endpoint that returns 405 Method Not Allowed
    Given a web server (example.com) that only serves HTTP GET
    When we attempt POST to /mcp
    Then status code is 405 (not 200)
    And we correctly identify this as a non-MCP endpoint

  @negative @detection
  Scenario: Reject endpoint with connection timeout
    Given an unreachable endpoint behind a misconfigured firewall
    When we attempt to connect with a 2-second timeout
    Then the request times out (408 or connection refused)
    And we correctly identify this as non-MCP

  # ──────────────────────────────────────────────────────────────
  # Registry Cleanup: Stale Endpoint Detection
  # ──────────────────────────────────────────────────────────────

  @negative @registry-cleanup
  Scenario: Identify stale clusters lingering in registry
    Given a cluster registry with 30 entries
    And 28 of them are from previous runs (now dead)
    And 2 of them are currently running
    When we run negative tests against all 30
    Then we correctly identify:
      | count | status  | action       |
      | 2     | alive   | Keep         |
      | 28    | dead    | Mark stale   |

  @negative @registry-cleanup
  Scenario: Confirm dead endpoints are properly non-MCP (not corrupted)
    Given endpoints that failed the positive MCP test
    When we run the negative test (expecting non-MCP response)
    Then the assertion passes (status != 200)
    And we confirm these are legitimately dead, not corrupted

  # ──────────────────────────────────────────────────────────────
  # Integration: Positive + Negative Together
  # ──────────────────────────────────────────────────────────────

  @integration @completeness
  Scenario: Complete certification requires both positive AND negative tests
    Given the certification testing suite
    When we run:
      | test                             | expected |
      | positive (MCP endpoint)          | PASS     |
      | negative (non-MCP endpoint)      | FAIL*    |
    Then certification is complete
    (* FAIL of the negative test = PASS that it correctly rejects non-MCP)

  @integration @fire-marshal-gate
  Scenario: fire-marshal uses both test suites for registry validation
    Given fire-marshal scanning a cluster registry
    When checking each endpoint
    Then fire-marshal:
      1. Runs positive test → endpoint MUST support MCP
      2. Runs negative test → dead endpoints MUST be non-MCP
      3. Reports stale entries for cleanup
      4. Blocks deployment if positive fails (broken endpoint)
      5. Allows cleanup if negative passes (confirmed dead)

  # ──────────────────────────────────────────────────────────────
  # Edge Cases
  # ──────────────────────────────────────────────────────────────

  @negative @edge-case
  Scenario: Distinguish between "dead" and "wrong response format"
    Given an endpoint that responds but not as MCP
    When we check the response structure
    Then we must distinguish:
      | case                 | status | body           | action     |
      | Dead endpoint        | 404    | HTML error     | Mark stale |
      | Wrong service       | 200    | HTML homepage  | Investigate |
      | MCP endpoint        | 200    | JSON-RPC       | Keep       |

  @negative @typo-detection
  Scenario: Catch misconfigured endpoint names
    Given developers setting up cluster references
    When they make a typo: "localhost:6046" instead of "localhost:60460"
    Then the negative test detects the connection failure
    And the configuration error is caught before deployment
