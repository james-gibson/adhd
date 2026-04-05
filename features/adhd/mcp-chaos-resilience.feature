@adhd
@z0-physical
@domain-mcp-resilience
Feature: MCP Tool Chaos Resilience
  ADHD MCP server must remain resilient and spec-compliant under rapid,
  random, and concurrent tool invocation. Chaos testing continuously validates
  tool availability and compliance.

  Background:
    Given ADHD MCP server is running
    And the server has initialized with tools/list endpoint
    And all tools are discoverable

  # ── rapid invocation ───────────────────────────────────────────────────────

  Scenario: all tools remain callable under rapid invocation
    When all discovered tools are invoked in rapid succession
    Then all tools respond (either with result or with valid error)
    And no tool returns method-not-found errors
    And all responses include proper JSON-RPC envelope
    And response time percentile(p95) is under 100ms

  # ── random invocation ──────────────────────────────────────────────────────

  Scenario: tools handle random input patterns
    When 50 random tools are invoked with varied input (missing params, empty objects, null)
    Then all invocations complete
    And tools with missing required params return -32602 (invalid params)
    And tools without params still respond correctly when given empty input
    And no tools return -32601 (method not found) — regression check

  # ── tool list consistency ──────────────────────────────────────────────────

  Scenario: tools/list response remains stable during chaos
    Given tools/list returns 16 tools initially
    When tools/list is called repeatedly during chaos test
    Then tool count remains 16 across all calls
    And tool names remain consistent
    And tool descriptions are identical
    And no tools appear and disappear unpredictably

  # ── error compliance ──────────────────────────────────────────────────────

  Scenario: error responses follow JSON-RPC 2.0 spec
    When a non-existent tool is invoked
    Then the response includes "error" object with:
      - code: integer (e.g. -32601 for method not found)
      - message: string (human-readable)
    And the response includes "id" (request ID)
    And the response includes "jsonrpc": "2.0"
    And no partial or malformed error responses occur

  # ── response format consistency ───────────────────────────────────────────

  Scenario: tools/call responses follow MCP spec format
    When successful tools are invoked via tools/call
    Then every response includes "content" field (array)
    And each content block has "type" and "text" fields
    And "type" is "text" (no mixing of types)
    And "text" field contains serialized result

  # ── concurrent load resilience ────────────────────────────────────────────

  Scenario: tools remain callable under concurrent load
    When 20 goroutines each invoke 50 random tools concurrently
    Then all 1000 invocations complete successfully
    And no goroutine encounters a deadlock or panic
    And response times remain stable (no tail latency spikes)
    And concurrent requests don't interfere with each other

  Scenario: concurrent invocations of same tool don't corrupt state
    When 10 concurrent goroutines invoke "adhd.status" simultaneously
    Then all 10 responses are returned
    And all responses are identical (no state corruption)
    And light counts in responses are consistent

  # ── regression detection ──────────────────────────────────────────────────

  Scenario: chaos test detects missing tool handler (regression catch)
    Given a new MCP method "new-method" is added to tools/list
    But the handler for "new-method" is not implemented
    When chaos tests run and invoke all listed tools
    Then the test fails with: "tool new-method listed but not callable"
    And the regression is caught before production deployment

  Scenario: chaos test detects broken response format
    Given a tool's response format is accidentally changed
    And the response no longer includes "content" field
    When chaos tests run
    Then the test fails with: "result missing 'content' field"
    And deployment is blocked

  # ── load patterns ──────────────────────────────────────────────────────────

  Scenario: tools survive burst traffic
    When 100 requests are sent to the server in 1 second
    Then the server handles all requests
    And no timeouts occur
    And response times remain under 200ms (p95)

  Scenario: tools recover gracefully from transient errors
    Given a tool's backend service is briefly unavailable
    When tool invocations continue during the outage
    Then invocations return appropriate error responses
    And when the service recovers, tools start returning results again
    And no permanent corruption occurs
