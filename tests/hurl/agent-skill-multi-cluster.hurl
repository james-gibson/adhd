# Agent Skill: Multi-Cluster Health Assessment
# Tests agent skill that probes multiple clusters and aggregates health
# Use with cluster registry to test real remote instances

# First, get list of available clusters from registry
GET {{registry_url}}

[Captures]
cluster_list: jsonpath("$")

[Asserts]
status == 200
jsonpath("$") isObject
# Should have cluster entries
jsonpath("$ | keys | length") > 0

---

# Test cluster 1: demo-9829 (from registry)
# This is one of the few clusters that might be responding
POST {{cluster1_endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.status"
}

[Captures]
cluster1_lights: jsonpath("$.result.lights")
cluster1_reachable: true

[Asserts]
status == 200

---

# Aggregate results: Check isotope ID for cluster 1
POST {{cluster1_endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "adhd.isotope.instance"
}

[Captures]
cluster1_isotope: jsonpath("$.result.isotope")

[Asserts]
status == 200
jsonpath("$.result.isotope") isString

---

# Compare with cluster 2 (if available)
# In a real scenario, we'd loop through all clusters from registry
POST {{cluster2_endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "adhd.status"
}

[Captures]
cluster2_lights: jsonpath("$.result.lights")
cluster2_reachable: true

[Asserts]
status == 200

---

# Verify isotope uniqueness
POST {{cluster2_endpoint}}
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "adhd.isotope.instance"
}

[Captures]
cluster2_isotope: jsonpath("$.result.isotope")

[Asserts]
status == 200
# Isotopes should be different
jsonpath("$.result.isotope") != {{cluster1_isotope}}
