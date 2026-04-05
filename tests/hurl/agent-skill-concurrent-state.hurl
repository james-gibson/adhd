# Agent Skill: Concurrent State Consistency
# Tests that concurrent tool calls see consistent state
# Run multiple instances concurrently to test race conditions

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.status"
}

[Captures]
initial_green: jsonpath("$.result.lights.green")
initial_red: jsonpath("$.result.lights.red")
initial_total: jsonpath("$.result.lights.total")

[Asserts]
status == 200
jsonpath("$.result.lights.total") isInteger
initial_total >= 0

---

# Get initial light list
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "adhd.lights.list"
}

[Captures]
lights_before: jsonpath("$.result.lights | length")

[Asserts]
status == 200
lights_before == {{initial_total}}

---

# Simulate concurrent read: another query while first is in flight
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "adhd.status"
}

[Captures]
concurrent_total: jsonpath("$.result.lights.total")

[Asserts]
status == 200
# State should be consistent (same total as initial read)
concurrent_total == {{initial_total}}

---

# Verify consistency across multiple reads
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "adhd.lights.list"
}

[Captures]
lights_after: jsonpath("$.result.lights | length")

[Asserts]
status == 200
# Should still match initial state
lights_after == {{lights_before}}
lights_after == {{initial_total}}
