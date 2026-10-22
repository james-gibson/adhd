@adhd
@z0-physical
@domain-proxy
@external-mcp
Feature: External MCP Server Proxying (Manual Path)
  The system allows manual proxying of external MCP servers via adhd.proxy
  and via named project tools registered through adhd.cluster.join with projects.

  The automated path (projects propagation via lezz/demo discovery) is
  half-wired; these scenarios pin the manual contract that must hold.

  Background:
    Given an ADHD MCP server is running with MCP server enabled

  # ──────────────────────────────────────────────────────────────
  # Ad-hoc proxy via adhd.proxy
  # ──────────────────────────────────────────────────────────────

  Scenario: adhd.proxy forwards calls to external MCP endpoint
    Given a target MCP server at "https://machinewitness.eu/mcp" (reference example)
    When I call adhd.proxy with:
      ```json
      {
        "target_endpoint": "https://machinewitness.eu/mcp",
        "call": {
          "jsonrpc": "2.0",
          "id": 1,
          "method": "tools/list"
        }
      }
      ```
    Then the response is successful (no error)
    And the result contains the tools list from the target server
    And the tools include:
      | tool_name       |
      | check_coverage  |
      | capture_status  |
      | suggest_domain  |
      | order_capture   |

  Scenario: adhd.proxy works via tools/call with nested arguments
    Given a target MCP server at "https://machinewitness.eu/mcp"
    When I call tools/call with:
      ```json
      {
        "name": "adhd.proxy",
        "arguments": {
          "target_endpoint": "https://machinewitness.eu/mcp",
          "call": {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "tools/list"
          }
        }
      }
      ```
    Then the response is successful (no error)
    And the result contains the target server's tools list

  Scenario: adhd.proxy validates required parameters
    When I call adhd.proxy with missing target_endpoint:
      ```json
      {
        "call": {
          "method": "tools/list"
        }
      }
      ```
    Then the response contains an error with code -32602
    And the error message indicates target_endpoint is required

    When I call adhd.proxy with missing call:
      ```json
      {
        "target_endpoint": "https://machinewitness.eu/mcp"
      }
      ```
    Then the response contains an error with code -32602
    And the error message indicates call is required

  Scenario: adhd.proxy forwards tool calls with parameters
    Given a target MCP server at "https://machinewitness.eu/mcp"
    When I call adhd.proxy to invoke "check_coverage" with domain "example.eu":
      ```json
      {
        "target_endpoint": "https://machinewitness.eu/mcp",
        "call": {
          "method": "tools/call",
          "params": {
            "name": "check_coverage",
            "arguments": {
              "domain": "example.eu"
            }
          }
        }
      }
      ```
    Then the response is successful (no error)
    And the result contains the coverage check response from the target server

  # ──────────────────────────────────────────────────────────────
  # Named project tool via adhd.cluster.join with projects
  # ──────────────────────────────────────────────────────────────

  Scenario: adhd.cluster.join registers adhd.project.<name>.call from projects array
    When I call adhd.cluster.join with:
      ```json
      {
        "name": "my-cluster",
        "projects": [
          {
            "name": "machinewitness",
            "repo": "martinschenk/machinewitness-mcp",
            "mcp_url": "https://machinewitness.eu/mcp"
          }
        ]
      }
      ```
    Then the join succeeds and returns:
      | field                 | value |
      | projects_registered   | 1     |
    And tools/list includes "adhd.project.machinewitness.call"

  Scenario: adhd.project.<name>.call proxies to registered project's MCP server
    Given cluster has joined with project machinewitness registered at "https://machinewitness.eu/mcp"
    When I call adhd.project.machinewitness.call with:
      ```json
      {
        "method": "tools/list"
      }
      ```
    Then the response is successful (no error)
    And the result contains the tools list from the machinewitness server
    And the tools include:
      | tool_name       |
      | check_coverage  |
      | capture_status  |
      | suggest_domain  |
      | order_capture   |

  Scenario: adhd.project.<name>.call forwards tool invocations with params
    Given project tool "adhd.project.machinewitness.call" is registered for mcp_url "https://machinewitness.eu/mcp"
    When I call adhd.project.machinewitness.call with:
      ```json
      {
        "method": "tools/call",
        "params": {
          "name": "check_coverage",
          "arguments": {
            "domain": "example.eu"
          }
        }
      }
      ```
    Then the response is successful (no error)
    And the result contains the target server's tool result

  # ──────────────────────────────────────────────────────────────
  # Contract invariants
  # ──────────────────────────────────────────────────────────────

  Scenario: adhd.project tool description references cluster context
    Given adhd.cluster.join registered project "machinewitness" in cluster "my-cluster"
    When I list tools
    Then the adhd.project.machinewitness.call tool description contains "machinewitness"
    And the description contains "cluster: my-cluster"

  Scenario: project tools are deduplicated on repeated registration
    Given project "machinewitness" is already registered via adhd.cluster.join
    When I call adhd.cluster.join again with the same project definition
    Then registration succeeds
    And adhd.project.machinewitness.call remains registered exactly once
    And projects_registered is 0 and projects_unchanged is 1

  # ──────────────────────────────────────────────────────────────
  # Identity: the route names the project, the name does not (adhd#40)
  # ──────────────────────────────────────────────────────────────

  Scenario: two clusters may each provide a project called "grep"
    When clusters "alpha" and "beta" both join with projects:
      | cluster | name | route                    | mcp_url                  |
      | alpha   | grep | 20261019T210927-09001816 | http://127.0.0.1:9101/mcp |
      | beta    | grep | 20261020T101010-0a0b0c0d | http://127.0.0.1:9102/mcp |
    Then each join reports projects_registered 1
    And tools/list includes "adhd.project.20261019T210927-09001816.call"
    And tools/list includes "adhd.project.20261020T101010-0a0b0c0d.call"
    And each tool proxies to its own cluster's MCP server

  Scenario: a name collision with no route is reported, not silently dropped
    When clusters "alpha" and "beta" both join with a project named "grep" and no route
    Then the first join reports projects_registered 1
    And the second join reports projects_registered 0
    And the second join reports one conflict for "adhd.project.grep.call"
    And tools/list includes "adhd.project.grep.call" exactly once

  Scenario: a rejoin moves the project tool to the new endpoint
    Given cluster "alpha" joined with project "grep" at route "route-1" on port A
    When cluster "alpha" rejoins with project "grep" at route "route-1" on port B
    Then the join reports projects_registered 1
    And adhd.project.route-1.call proxies to port B

  Scenario: project.route is preferred over the legacy seed id
    When a cluster joins with project "grep" at route "r-1" and legacy id "old"
    Then tools/list includes "adhd.project.r-1.call"
    And tools/list does not include "adhd.project.old.call"

  Scenario: adhd.cluster.join accepts canonical project fields (name, repo, mcp_url)
    When I call adhd.cluster.join with projects containing:
      ```json
      {
        "name": "machinewitness",
        "repo": "martinschenk/machinewitness-mcp",
        "mcp_url": "https://machinewitness.eu/mcp"
      }
      ```
    Then the tool is registered with the captured mcp_url and metadata intact
    And subsequent calls through the project tool reach "https://machinewitness.eu/mcp"
