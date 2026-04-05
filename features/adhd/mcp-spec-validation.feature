@adhd
@z0-physical
@domain-mcp-spec-validation
Feature: MCP Specification Validation
  fire-marshal validates that MCP servers comply with the JSON-RPC 2.0 and MCP specifications
  before deployment. Incomplete or non-compliant implementations are flagged.

  Background:
    Given fire-marshal is configured to validate MCP servers before deployment

  # ── tools/list and tools/call consistency ─────────────────────────────────────

  Scenario: tools/call is required if tools/list is exposed
    Given an MCP server exposes the "tools/list" method
    When fire-marshal validates the server
    Then fire-marshal checks that "tools/call" is also exposed
    And if "tools/call" is missing, fire-marshal reports: "tools/list exposed but tools/call not implemented"

  Scenario: tools/call must route to all tools returned by tools/list
    Given an MCP server exposes "tools/list" and "tools/call"
    And "tools/list" returns 5 tools: [tool1, tool2, tool3, tool4, tool5]
    When fire-marshal validates the server
    Then fire-marshal attempts to invoke each tool via tools/call
    And if any tool fails to be callable, fire-marshal reports: "tool {name} listed but not callable"

  Scenario: tools/call response must follow MCP spec format
    Given an MCP server implements "tools/call"
    When fire-marshal invokes a tool
    Then fire-marshal verifies the response has a "content" field (array of content blocks)
    And if the format is invalid, fire-marshal reports: "tools/call response does not match MCP spec"

  # ── JSON-RPC 2.0 compliance ────────────────────────────────────────────────

  Scenario: all responses must include "jsonrpc": "2.0"
    Given an MCP server at a known endpoint
    When fire-marshal makes any JSON-RPC 2.0 request
    Then fire-marshal verifies all responses include "jsonrpc": "2.0"
    And if any response lacks it, fire-marshal reports: "missing jsonrpc version in response"

  Scenario: error responses must follow JSON-RPC error format
    Given an MCP server at a known endpoint
    When fire-marshal invokes a non-existent method
    Then the server returns an error object with: code, message, id
    And if the error format is wrong, fire-marshal reports: "error response does not follow JSON-RPC 2.0 spec"

  # ── required methods ────────────────────────────────────────────────────────

  Scenario: initialize method is required
    Given an MCP server endpoint
    When fire-marshal attempts to call "initialize"
    Then the server must respond (not 404 or method not found)
    And if initialize is missing, fire-marshal reports: "initialize method not found"

  Scenario: initialize response must include capabilities
    Given an MCP server exposes "initialize"
    When fire-marshal invokes initialize
    Then the response must include "capabilities" object
    And if missing, fire-marshal reports: "initialize response missing capabilities field"

  # ── reporting and blocking ──────────────────────────────────────────────────

  Scenario: fire-marshal produces a pre-deployment validation report
    Given an MCP server with spec compliance issues
    When fire-marshal validates it
    Then fire-marshal produces a report with:
      - list of all compliance violations
      - severity (error vs warning)
      - remediation suggestions

  Scenario: deployment is blocked if critical spec violations are found
    Given an MCP server is missing "tools/call" when tools/list is exposed
    And fire-marshal has validated it
    When deployment is attempted
    Then the deployment is blocked with: "MCP spec validation failed: tools/list exposed but tools/call not implemented"

  Scenario: fire-marshal exposes compliance status as a light
    Given fire-marshal has validated a server
    And the server passed all spec checks
    When ADHD queries fire-marshal status
    Then fire-marshal reports a "fire-marshal-spec-check" light with status="green"
    When the server has violations
    Then fire-marshal reports "fire-marshal-spec-check" light with status="red"
    And Details field includes the specific violation
