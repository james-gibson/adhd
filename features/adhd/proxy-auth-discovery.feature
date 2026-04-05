@adhd
@z0-physical
@z1-temporal
@domain-proxy
@capability-discovery
Feature: Proxy Auth Discovery — Learning What's Missing by Testing

  HTTP 401 doesn't mean "test failed" — it means "we didn't test properly."

  By proxying authenticated MCP calls through our system, we discover
  exactly which proxy features are missing, in order of priority.

  Each test failure becomes a clear implementation task.

  Background:
    Given the adhd proxy system is ready to test
    And we have credentials for authenticated MCP servers
    And the test harness can make requests to our proxy

  # ──────────────────────────────────────────────────────────────
  # Phase 1: Does adhd.proxy exist?
  # ──────────────────────────────────────────────────────────────

  @phase-1 @discovery-start
  Scenario: Attempt to proxy an authenticated MCP call
    Given an authenticated MCP server:
      | Property | Value |
      | Endpoint | https://api.adramp.ai/mcp |
      | Auth Type | Bearer token |
      | Reason for 401 | Missing Authorization header |

    When we call adhd.proxy with:
      ```json
      {
        "method": "adhd.proxy",
        "params": {
          "target_endpoint": "https://api.adramp.ai/mcp",
          "auth": {
            "type": "bearer",
            "token": "api-key-here"
          },
          "call": {
            "method": "tools/list"
          }
        }
      }
      ```

    Then we discover:
      | HTTP Status | Meaning | Implementation Task |
      | 501 | adhd.proxy not implemented | Phase 1: Add adhd.proxy handler |
      | 400 | Bad param structure | Debug: Fix JSON structure |
      | 401 | Token rejected | Phase 2: Fix token format |
      | 403 | Rate limit/scope | Phase 3: Add rate limit handling |
      | 200 | ✓ Success | Feature complete for bearer auth |

  @phase-1 @blocker
  Scenario: Result 501 tells us to start implementation
    Given adhd.proxy returns 501 Method not found
    When we read the error
    Then we know:
      - "We need to implement adhd.proxy handler"
      - "It should accept: target_endpoint, auth, call"
      - "It should return: proxied response or error"
    And we start Phase 1 implementation

  # ──────────────────────────────────────────────────────────────
  # Phase 2: Bearer Token Support
  # ──────────────────────────────────────────────────────────────

  @phase-2 @bearer-token
  Scenario: Proxy recognizes but incorrectly handles bearer token
    Given adhd.proxy handler exists
    And it receives auth type "bearer"
    But the token handling is incomplete
    When we test with a valid token
    Then we might get:
      | Result | Meaning | Next Task |
      | 400 | Token not in Authorization header | Add Authorization: Bearer {token} |
      | 401 | Token format wrong | Validate token format |
      | 500 | Internal error in token parsing | Debug token handling code |

  @phase-2 @bearer-success
  Scenario: Bearer token support is complete
    Given bearer token auth is fully implemented
    When we proxy a call with:
      ```json
      "auth": {
        "type": "bearer",
        "token": "valid-token-here"
      }
      ```
    Then the proxy:
      - Adds header: "Authorization: Bearer valid-token-here"
      - Forwards call to target
      - Returns: 200 with tools/list result

  # ──────────────────────────────────────────────────────────────
  # Phase 3: API Key Header Support
  # ──────────────────────────────────────────────────────────────

  @phase-3 @api-key
  Scenario: Different servers need different auth types
    Given servers with different auth requirements:
      | Server | Auth Type | Header Format |
      | AdRamp | Bearer | Authorization: Bearer {token} |
      | Bezal | API Key | X-API-Key: {key} |
      | aDvisor | Custom | x-api-token: {token} |

    When we test via proxy
    Then Phase 2 (bearer) works:
      - ✓ AdRamp returns 200
    But Phase 3+ fail:
      - ✗ Bezal returns 400 (API-Key not recognized)
      - ✗ aDvisor returns 400 (custom header not recognized)

  @phase-3 @api-key-implementation
  Scenario: Implement API-Key header support
    Given adhd.proxy now supports type: "api-key"
    When we call with:
      ```json
      "auth": {
        "type": "api-key",
        "header": "X-API-Key",
        "token": "my-api-key"
      }
      ```
    Then proxy adds: "X-API-Key: my-api-key"
    And ✓ Bezal returns 200

  # ──────────────────────────────────────────────────────────────
  # Phase 4: OAuth2 (Most Complex)
  # ──────────────────────────────────────────────────────────────

  @phase-4 @oauth2 @complex
  Scenario: OAuth2 requires token refresh capability
    Given a server that uses OAuth2:
      | Property | Value |
      | Token Type | OAuth2 |
      | Required | Refresh token handling |
      | Complexity | High |

    When token expires during proxy call
    Then we need:
      - 1. Detect 401 from upstream
      - 2. Use refresh token to get new token
      - 3. Retry original call with new token
      - 4. Return result to user

  @phase-4 @oauth2-blocked
  Scenario: Phase 4 is blocked by complexity
    Given OAuth2 support would require:
      - Token endpoint integration
      - Refresh token storage
      - Scope management
      - PKCE support
      - Token expiration tracking

    When we test oauth2-type servers
    Then we correctly return 400:
      ```json
      {
        "error": "oauth2 auth type not yet implemented",
        "why": "Requires secure token storage and refresh flow",
        "timeline": "Phase 4, after bearer and API-key work"
      }
      ```

  # ──────────────────────────────────────────────────────────────
  # Continuous Discovery: Measuring Progress
  # ──────────────────────────────────────────────────────────────

  @continuous-discovery @metrics
  Scenario: Track proxy maturity as tests succeed
    Given the test suite with real credentials:
      | Server | Auth Type | Current Result | After Phase 2 | After Phase 3 | After Phase 4 |
      | tandem | Direct | ✓ 200 | ✓ 200 | ✓ 200 | ✓ 200 |
      | adadvisor | Direct | ✓ 200 | ✓ 200 | ✓ 200 | ✓ 200 |
      | adramp | Bearer | ✗ 401 | ✓ 200 | ✓ 200 | ✓ 200 |
      | bezal | API-Key | ✗ 401 | ✗ 400 | ✓ 200 | ✓ 200 |
      | lona | OAuth2 | ✗ 401 | ✗ 400 | ✗ 400 | ✓ 200 |

    When we run proxy-auth-discovery.sh monthly
    Then we see:
      | Metric | Week 1 | Week 4 | Week 8 | Week 12 |
      | Servers working | 2/5 | 4/5 | 4/5 | 5/5 |
      | Proxy completeness | 0% | 40% | 60% | 100% |
      | Direct access | 40% | 40% | 40% | 40% |
      | Proxied access | 0% | 40% | 40% | 40% |
      | Total accessible | 40% | 80% | 80% | 80% |

  # ──────────────────────────────────────────────────────────────
  # Real-World Impact: Registry Health
  # ──────────────────────────────────────────────────────────────

  @impact @registry-health
  Scenario: Proxy unlocks the 401-blocked servers
    Given the current registry test results:
      ```
      ✓ Certified (direct): 4 servers
      ✗ Failed (401): 14 servers
      ⏳ Offline: 2 servers
      Total accessible: 4/20 (20%)
      ```

    When adhd proxy is fully implemented with all auth types
    Then the registry changes:
      ```
      ✓ Direct access: 4 servers
      ✓ Proxied via adhd: 12 servers (of the 14 that need auth)
      ✗ Still failing: 2 servers (OAuth2, not yet supported)
      ⏳ Offline: 2 servers
      Total accessible: 18/20 (90%)
      ```

    And we go from 4/20 (20%) to 18/20 (90%)

  # ──────────────────────────────────────────────────────────────
  # Implementation-Driven Development
  # ──────────────────────────────────────────────────────────────

  @methodology @test-first
  Scenario: Test failures drive implementation priority
    When we run proxy-auth-discovery.hurl
    Then failures tell us, in order:
      1. 501 → "Implement adhd.proxy"
      2. 400 → "Fix parameter handling"
      3. 401 → "Add bearer token support"
      4. 403 → "Add rate limit handling"
      5. 200 → "Feature complete"

    Instead of guessing what to build:
      - Tests show exactly what's missing
      - Each failure is an actionable task
      - Progress is measurable

  @methodology @visibility
  Scenario: Make proxy development transparent
    Given weekly proxy-auth-discovery.sh runs
    When results are published
    Then teams see:
      ```
      Proxy Maturity Report (Week 3)

      Bearer Token
        ████████░░ 80% complete
        - ✓ Token parsing
        - ✓ Authorization header
        - ✓ Upstream auth
        - ⏳ Token validation

      API-Key Support
        ██░░░░░░░░ 20% complete
        - ✓ Header format
        - ⏳ Custom headers
        - ⏳ Multiple keys

      OAuth2
        ░░░░░░░░░░ 0% complete
        - ⏳ All features
      ```

    And everyone knows:
      - Where development stands
      - What's being worked on
      - When features will land
