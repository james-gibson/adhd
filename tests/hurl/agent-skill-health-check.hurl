# Agent Skill: Check Cluster Health
# This HURL script simulates an agent skill that chains multiple tool calls
# to determine cluster health status. Can be invoked via adhd.hurl.run MCP tool.
#
# Usage:
#   hurl --variable endpoint=http://localhost:60460/mcp agent-skill-health-check.hurl
#   OR
#   Call via MCP: adhd.hurl.run with script content

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.status"
}

# Capture lights summary for assertions
[Captures]
total: jsonpath("$.result.lights.total")
green_count: jsonpath("$.result.lights.green")
red_count: jsonpath("$.result.lights.red")
yellow_count: jsonpath("$.result.lights.yellow")

# Assert cluster has lights
[Asserts]
status == 200
jsonpath("$.result.lights") exists
jsonpath("$.result.lights.total") isInteger

---

# Step 2: Get detailed light list
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "adhd.lights.list"
}

# Capture first light name for next request
[Captures]
light_list: jsonpath("$.result.lights")
first_light_name: jsonpath("$.result.lights[0].name")
first_light_status: jsonpath("$.result.lights[0].status")

[Asserts]
status == 200
jsonpath("$.result.lights") isArray
jsonpath("$.result.lights") count > 0

---

# Step 3: Get details of first light
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "adhd.lights.get",
  "params": {
    "name": "{{first_light_name}}"
  }
}

[Captures]
light_detail: jsonpath("$.result")

[Asserts]
status == 200
jsonpath("$.result.name") == "{{first_light_name}}"
jsonpath("$.result.status") isString

---

# Step 4: Get peer information
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "adhd.isotope.peers"
}

[Captures]
peer_list: jsonpath("$.result")

[Asserts]
status == 200
jsonpath("$.result") exists
