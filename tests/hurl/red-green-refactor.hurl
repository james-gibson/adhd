# Red-Green-Refactor: Incomplete Certification via -42i Magic
#
# These tests validate agent skills work end-to-end BUT intentionally do NOT
# verify isotope transit, receipt pre-computation, or chaos skill detection.
#
# By documenting what's MISSING, we block full certification until:
#   - smoke-alarm implements isotope_certified in selfdescription
#   - smoke-alarm emits isotope_id in status responses
#   - adhd validates receipt pre-computation before delivery
#
# This is the RED phase: we PASS the tests but FAIL certification.
# Usage: hurl --variable endpoint=http://localhost:60460/mcp red-green-refactor.hurl

---
### PHASE 1: GREEN — Agent Skills Work (No Isotope Validation)

# Step 1: Health check passes
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

---

# Step 2: Tool chain executes: status -> lights.list -> lights.get
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

---

# Step 3: Get detail on first light
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

---

# Step 4: Isotope identity exists (but not yet verified)
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
# GREEN: isotope_id field exists
jsonpath("$.result.isotope") exists
# RED: but we don't verify its construction or pre-computation yet
# This is where full certification would require smoke-alarm support

---

### PHASE 2: RED — Missing Isotope Bits (Document Gaps)

# Gap 1: smoke-alarm.json isotope_certified status
# BLOCKED: smoke-alarm hasn't exposed isotope_certified in /.well-known/smoke-alarm.json
# Expected: GET /.well-known/smoke-alarm.json -> {"isotope_certified": true, ...}
# Impact: ADHD can't distinguish certified vs uncertified alarms
GET {{endpoint}}/../.well-known/smoke-alarm.json

[Asserts]
# This will likely fail (404 or 405) — that's the RED phase
# Once smoke-alarm implements this, we move to GREEN

---

# Gap 2: isotope_id in adhd.status (from smoke-alarm's status response)
# BLOCKED: smoke-alarm hasn't added isotope metadata to /status endpoint
# Expected: adhd.status response includes {"isotope_id": "...", "feature_id": "...", "nonce": "..."}
# Impact: ADHD can't validate receipt or detect tampering
POST {{endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "adhd.status"
}

[Asserts]
status == 200
# GREEN: basic status works
jsonpath("$.result.lights") exists
# RED: but response lacks isotope metadata
# We document this as a gap for future certification

---

# Gap 3: Receipt pre-computation validation
# BLOCKED: ADHD hasn't implemented pre-compute logic to challenge smoke-alarm
# Expected: ADHD computes SHA256(feature_id || ":" || SHA256(payload) || ":" || nonce)
#           then verifies returned receipt matches before accepting state change
# Impact: No protection against tampering or injection during skill execution
# This gap can only be filled by smoke-alarm emitting feature_id + nonce,
# then ADHD implementing the crypto validation

# This is intentional DOCUMENTATION of the missing piece via test absence.
# Once both sides implement isotope transit, this test will be added.

---

### PHASE 3: REFACTOR — Transition Path

# This HURL script demonstrates the progression:
#   1. Tool chaining WORKS (agents can execute multi-step skills)
#   2. Isotope identity EXISTS (cluster has a unique ID)
#   3. But VALIDATION is incomplete (no receipt pre-computation)
#
# Certification is BLOCKED until:
#   ✓ ocd-smoke-alarm::certification-gate.feature passes
#   ✓ ocd-smoke-alarm::isotope-transit.feature passes
#   ✓ adhd::isotope-transit.feature passes
#   ✓ adhd implements receipt validation in model.go
#
# This is the -42i rung: tests PASS but certification FAILS.
# The gap is VISIBLE, not hidden.
