# Negative Certification: Detecting Non-MCP Endpoints

## The Pattern

Just as **red-green-refactor.hurl** proves a system IS MCP-compliant, we need tests that prove a system is NOT.

This prevents false negatives: detecting when certification *should fail*.

## Test Example: example.com

```bash
hurl --variable endpoint=https://example.com tests/hurl/red-green-refactor-negative.hurl
```

**Result**:
```
POST https://example.com/mcp
  ✗ FAIL: status != 200 is false (got 405)

This is the EXPECTED result: example.com doesn't run ADHD MCP server.
```

### What Happens

1. **Request**: POST to example.com/mcp with adhd.status call
2. **Response**: 405 Method Not Allowed (or 404, or connection timeout)
3. **Assertion**: `status != 200` ✓ PASS (proves it's NOT MCP)

### Why This Matters

| Scenario | Old (No Negative Tests) | New (Negative Tests) |
|----------|------------------------|----------------------|
| Test against real MCP endpoint | ✓ PASS | ✓ PASS |
| Test against non-MCP (example.com) | ? Unknown | ✓ FAIL (expected) |
| **Problem**: False confidence | "Did we test non-MCP?" | "We verified non-MCP detection" |

---

## Real-World Use Cases

### 1. **Endpoint Registry Validation**

When provisioning clusters, verify old/stale endpoints are detected:

```bash
# After cluster shutdown, verify detection
DEAD_ENDPOINT="http://192.168.7.18:60461"
hurl --variable endpoint="$DEAD_ENDPOINT" \
  tests/hurl/red-green-refactor-negative.hurl
# Expected: ✓ FAIL (correctly identifies as non-MCP)
```

### 2. **Cluster Naming Mistakes**

Catch typos that point to wrong servers:

```bash
# Typo: wrong port
hurl --variable endpoint=http://localhost:6046/mcp \  # Missing 0
  tests/hurl/red-green-refactor-negative.hurl
# Expected: ✓ FAIL (correctly detects port mismatch)
```

### 3. **Network Partitions**

Verify timeout detection during degraded network:

```bash
# Behind misconfigured firewall
hurl --variable endpoint=http://unreachable.internal:60460/mcp \
  tests/hurl/red-green-refactor-negative.hurl
# Expected: ✓ FAIL (connection timeout = correctly detected)
```

### 4. **CI/CD Gate: Negative Certification**

```yaml
# .github/workflows/certification-gates.yml
name: Certification Validation
on: [push]
jobs:
  positive:
    runs-on: ubuntu-latest
    steps:
      - name: Verify MCP endpoints ARE certified
        run: |
          hurl --variable endpoint=http://localhost:60460/mcp \
            tests/hurl/red-green-refactor.hurl
        # Expected: ✓ PASS

  negative:
    runs-on: ubuntu-latest
    steps:
      - name: Verify non-MCP endpoints are rejected
        run: |
          hurl --variable endpoint=https://example.com \
            tests/hurl/red-green-refactor-negative.hurl
        # Expected: ✓ FAIL (assertion passes because endpoint is correctly non-MCP)
```

---

## fire-marshal Integration

When fire-marshal scans a cluster registry, it should:

1. **Positive test**: Try to call adhd.status on each endpoint
   - ✓ PASS → Endpoint is MCP-compliant
   - ✗ FAIL → Endpoint is broken/stale

2. **Negative test**: Run the negative test suite
   - ✓ FAIL (assertion) → Endpoint is correctly NOT MCP (good, cleanup it)
   - ✗ PASS (assertion) → Endpoint claims to be MCP but it's actually something else (bad, investigate)

### Example: Detecting Stale Registry Entries

```bash
# Registry has 30 clusters; 28 are dead
for endpoint in $(registry_list); do
  if ! hurl --variable endpoint="$endpoint" \
            tests/hurl/red-green-refactor.hurl 2>/dev/null; then
    # Endpoint failed positive test
    echo "❌ $endpoint is stale/dead"
  else
    # Endpoint passed positive test
    echo "✅ $endpoint is alive"
  fi
done

# Output:
# ✅ http://192.168.7.18:60460/mcp (alive)
# ❌ http://192.168.7.18:60461/mcp (dead)
# ❌ http://192.168.7.18:60462/mcp (dead)
# ...
```

Then run negative test on the dead ones to confirm they're properly non-MCP:

```bash
for dead in $(dead_endpoints); do
  if hurl --variable endpoint="$dead" \
          tests/hurl/red-green-refactor-negative.hurl 2>/dev/null; then
    # Assertion passed (status != 200) — correctly detected as non-MCP
    echo "✓ Confirmed dead: $dead"
  else
    # This shouldn't happen; investigate
    echo "⚠ Strange: $dead behaved like MCP but failed positive test"
  fi
done
```

---

## Assertion Patterns

### Pattern 1: Detect Non-Existence
```hurl
POST {{endpoint}}/mcp
Content-Type: application/json
{ "jsonrpc": "2.0", "id": 1, "method": "adhd.status" }

[Asserts]
status != 200           # Not found, forbidden, error
```

### Pattern 2: Detect Wrong Response Format
```hurl
POST {{endpoint}}/mcp
Content-Type: application/json
{ "jsonrpc": "2.0", "id": 1, "method": "adhd.status" }

[Asserts]
jsonpath("$.result") isNull     # No MCP result structure
```

### Pattern 3: Detect Timeout
```hurl
POST {{endpoint}}/mcp
Content-Type: application/json
[Options]
timeout: 2000

{ "jsonrpc": "2.0", "id": 1, "method": "adhd.status" }

[Asserts]
status == 408           # Request timeout
```

---

## Running Together

```bash
# Positive certification: endpoint MUST support MCP
hurl --variable endpoint=http://localhost:60460/mcp \
  tests/hurl/red-green-refactor.hurl

# Negative certification: example.com MUST NOT support MCP
hurl --variable endpoint=https://example.com \
  tests/hurl/red-green-refactor-negative.hurl

# Both must pass for a complete validation
```

---

## Summary

**Positive tests** prove: "This system IS MCP-compliant"

**Negative tests** prove: "This system is NOT MCP-compliant (and we can detect it)"

Together, they create a **complete certification picture**: we know which endpoints are safe to use and which ones need cleanup or replacement.
