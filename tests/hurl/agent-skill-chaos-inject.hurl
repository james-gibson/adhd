# Agent Skill: Chaos Injection Test
# Tests agent resilience by simulating tool failures and retries
# This HURL script demonstrates error handling in tool chains

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.status"
}

[Captures]
lights_data: jsonpath("$.result.lights")

[Asserts]
status == 200
jsonpath("$.result.lights") exists

---

# Test: Request non-existent light (should fail gracefully)
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "adhd.lights.get",
  "params": {
    "name": "nonexistent-light-xyz-12345"
  }
}

[Asserts]
status == 200
# Should return error in result, not HTTP error
jsonpath("$.error") exists

---

# Recovery: Request valid light after error
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "adhd.lights.list"
}

[Asserts]
status == 200
jsonpath("$.result.lights") isArray
# Verify we recovered from the previous error
