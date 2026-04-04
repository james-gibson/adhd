@adhd
@z0-physical
@domain-mcp-server
Feature: ADHD MCP Server Integration
  ADHD hosts an MCP server exposing dashboard lights as tools

  Scenario: MCP server initialization
    Given the MCP server is enabled in config
    When ADHD starts
    Then the MCP server listens on the configured address
    And the server responds to initialize requests

  Scenario: tools/list returns dashboard tools
    Given an MCP client connected to ADHD
    When I call tools/list
    Then the response includes:
      | tool_name          | description                         |
      | adhd.lights.list   | List all lights with their status   |
      | adhd.lights.get    | Get a specific light by name        |
      | adhd.status        | Get dashboard status summary        |

  Scenario: adhd.lights.list returns all lights
    Given the dashboard has 3 lights
    And all lights have different statuses
    When I call adhd.lights.list
    Then the response includes all 3 lights
    And each light has name, type, source, status, details

  Scenario: adhd.lights.get returns a single light
    Given the dashboard has a light named "primary"
    When I call adhd.lights.get with name="primary"
    Then the response includes the light object
    And the light has all required fields

  Scenario: adhd.lights.get handles missing light
    Given the dashboard has lights
    When I call adhd.lights.get with name="nonexistent"
    Then the response is an error
    And the error message is "Light not found"

  Scenario: adhd.status returns summary counts
    Given the dashboard has:
      | status | count |
      | green  | 5     |
      | red    | 2     |
      | yellow | 1     |
      | dark   | 0     |
    When I call adhd.status
    Then the response summary shows:
      | key   | value |
      | total | 8     |
      | green | 5     |
      | red   | 2     |
      | yellow| 1     |
      | dark  | 0     |

  Scenario: MCP server disabled by config
    Given the MCP server is disabled in config
    When ADHD starts
    Then no MCP server is listening
    And the dashboard still functions normally

  Scenario: MCP server returns valid JSON-RPC responses
    Given an MCP client connected to ADHD
    When I send a JSON-RPC initialize request
    Then the response is valid JSON-RPC 2.0
    And the response has required fields: jsonrpc, id, result (or error)
    And no response contains sensitive data
