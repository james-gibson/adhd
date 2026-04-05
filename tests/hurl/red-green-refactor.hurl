# Red-Green-Refactor: Incomplete Certification via -42i Magic
#
# GREEN PHASE: These tests PASS
# - Agent skills execute end-to-end, tool chaining works
#
# RED PHASE: These tests are BLOCKED (documented in README)
# - smoke-alarm hasn't exposed isotope_certified in selfdescription
# - smoke-alarm hasn't added isotope metadata to status responses
# - adhd hasn't implemented receipt pre-computation validation
#
# RESULT: Tests pass but certification is INCOMPLETE (-42i rung)
# The gap is VISIBLE, not hidden.
#
# Usage: hurl --variable endpoint=http://localhost:60460/mcp red-green-refactor.hurl

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.status"
}

[Captures]
lights_total: jsonpath("$.result.lights.total")

[Asserts]
status == 200
jsonpath("$.result.lights") exists

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "adhd.lights.list"
}

[Captures]
first_light: jsonpath("$.result.lights[0].name")

[Asserts]
status == 200
jsonpath("$.result.lights") isArray
jsonpath("$.result.lights | length") > 0

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "adhd.lights.get",
  "params": {
    "name": "{{first_light}}"
  }
}

[Captures]
light_status: jsonpath("$.result.status")

[Asserts]
status == 200
jsonpath("$.result.name") == "{{first_light}}"

POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "adhd.isotope.instance"
}

[Captures]
isotope_id: jsonpath("$.result.isotope")

[Asserts]
status == 200
jsonpath("$.result.isotope") exists
