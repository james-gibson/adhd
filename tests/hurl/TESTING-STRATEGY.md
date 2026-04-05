# Complete Testing Strategy: -42i Magic through Real MCP Servers

## The Journey

This document summarizes the complete certification testing pattern that evolved from a simple question: **"Can we verify that example.com doesn't support an MCP server?"**

The answer opened doors to a comprehensive strategy for validating the entire MCP ecosystem.

## Layer 1: Red-Green-Refactor at -42i (Incomplete Certification)

**Files**:
- `red-green-refactor.hurl` — Positive tests
- `RED-GREEN-REFACTOR.md` — Documentation
- `red-green-refactor.feature` — Gherkin specs

**Pattern**: Tests PASS but block certification

```
GREEN: ✓ Tool chaining works, agent skills execute
RED:   ✗ Receipt validation missing (blocked on smoke-alarm isotope transit)
REFACTOR: → Clear path to full certification

Result: -42i (Incomplete but measurable)
```

**Why it matters**:
- Gaps are visible, not hidden
- Progress is measurable
- Developers know exactly what's blocking full certification

---

## Layer 2: Negative Certification (Detecting Non-MCP Endpoints)

**Files**:
- `negative-endpoint.hurl` — Negative test template
- `NEGATIVE-CERTIFICATION.md` — Use cases
- `negative-certification.feature` — Gherkin specs

**Pattern**: Tests FAIL (as designed) to prove endpoint rejection

```bash
hurl --variable endpoint=https://example.com negative-endpoint.hurl
# Result: ✓ PASS (assertion "status != 200" succeeds)
# Meaning: example.com correctly detected as non-MCP
```

**Real-world results** (verified):
```
✓ example.com          → 405 Method Not Allowed (correctly rejected)
✓ google.com           → 403 Forbidden (correctly rejected)
✓ Random endpoints     → 404/500/timeout (correctly rejected)
```

**Why it matters**:
- Detects misconfigurations early
- Prevents false confidence
- Enables registry cleanup automation
- Catches typos in endpoint configuration

---

## Layer 3: Registry Validation (Testing Real MCP Servers)

**Files**:
- `demo-real-mcp-servers.hurl` — Test template
- `DEMO-REAL-MCP-SERVERS.md` — Complete guide
- `test-mcp-registry.sh` — Automated registry scanner
- `demo-real-mcp-servers.feature` — Gherkin specs

**Pattern**: Scan official MCP Registry and test all public endpoints

```bash
bash tests/hurl/test-mcp-registry.sh

# Output:
# Testing ac.tandem/docs-mcp ... ✓ CERTIFIED (HTTP 200)
# Testing agency.lona/trading ... ✗ FAILED (HTTP 401)
# Testing ai.adadvisor/mcp-server ... ✓ CERTIFIED (HTTP 200)
# ...
# Summary: ✓ 4 certified, ✗ 14 failed, ⏳ 2 offline
```

**Real results from live registry** (as of April 2026):

| Status | Count | Examples | Meaning |
|--------|-------|----------|---------|
| ✓ Certified | 4 | tandem, adadvisor, agentrapay, contabo | Actually MCP-compliant |
| ✗ Failed | 14 | lona, atars, adramp, bezal | 401/404/406 (auth required or not MCP) |
| ⏳ Offline | 2 | alpic test, clarid | Server unreachable |

**Why it matters**:
- Real-world validation, not theoretical
- Discovers actual MCP ecosystem health
- Catches broken endpoints automatically
- Enables registry badges/certification levels

---

## The Complete Flow

```
                       TESTING PYRAMID

                    Layer 3: Real Servers
                   (Registry validation)

                      Layer 2: Negative
                    (Reject non-MCP)

                      Layer 1: Positive
                   (Tool chains work)
```

### Example: Testing a New MCP Server

```bash
# Step 1: Run positive test (does it work?)
hurl --variable endpoint=http://my-mcp.ai \
  tests/hurl/demo-real-mcp-servers.hurl
# Expected: ✓ PASS (HTTP 200, JSON-RPC valid)

# Step 2: Verify negative test still works (can we detect non-MCP?)
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
# Expected: ✓ PASS (assertion status != 200)

# Step 3: Check if it's in the registry and certified
bash tests/hurl/test-mcp-registry.sh | grep "my-mcp"
# Expected: "✓ CERTIFIED (HTTP 200)"

# Result: ✓ Server is MCP-certified and can be trusted
```

---

## Integration Points

### For Developers (Creating MCP Servers)

**Pre-deployment CI/CD gate**:
```yaml
jobs:
  mcp-certification:
    steps:
      - name: Build server
        run: npm run build

      - name: Start server
        run: npm start &

      - name: Run positive test
        run: hurl --variable endpoint=http://localhost:3000 \
          tests/hurl/demo-real-mcp-servers.hurl

      - name: Run negative test
        run: hurl --variable endpoint=https://example.com \
          tests/hurl/negative-endpoint.hurl

      - name: Report certification
        run: |
          echo "✓ MCP Certification: PASS"
          echo "✓ Ready for registry submission"
```

### For Registry (Official MCP Registry)

**Automated validation**:
```bash
# On each server submission:
1. Query registry API
2. For each new server:
   - Extract public endpoints
   - Run positive test
   - Run negative test
3. Assign certification level:
   - ✓ Certified (passes all tests)
   - ⏳ Pending (auth required, offline)
   - ✗ Uncertified (failed tests, needs review)
4. Display badge in registry
```

### For fire-marshal (Deployment Validation)

**Pre-deployment checks**:
```bash
fire-marshal validate-endpoints
  ├── Query cluster registry
  ├── For each endpoint:
  │   ├── Run positive test
  │   ├── Run negative test
  │   └── Report certification level
  ├── Identify stale clusters:
  │   ├── >30 days without activity
  │   ├── Failed negative test (confirmed dead)
  │   └── Mark for cleanup
  └── Block deployment if:
      ├── Any live endpoint fails positive test
      ├── Or negative test behaves unexpectedly
```

---

## Certification Levels

| Level | Positive | Negative | Isotope | Receipt | Status |
|-------|----------|----------|---------|---------|--------|
| **-42i** | ✓ | ✓ | Pending | Missing | In use (incomplete) |
| **42** | ✓ | ✓ | ✓ | ✓ | Goal (full cert) |
| **✗ Failed** | ✗ | ✓ | — | — | Broken endpoint |

---

## Test Files Summary

### Positive Tests (Does it work?)

- `red-green-refactor.hurl` — Agent skills, tool chains
- `demo-real-mcp-servers.hurl` — Generic MCP compliance check

### Negative Tests (Does it correctly reject non-MCP?)

- `negative-endpoint.hurl` — Reject non-MCP endpoints
- Implicit in all positive tests (404/timeout = rejection)

### Documentation

- `RED-GREEN-REFACTOR.md` — -42i incomplete certification pattern
- `NEGATIVE-CERTIFICATION.md` — Registry cleanup, detection patterns
- `CERTIFICATION-ROADMAP.md` — Integration with fire-marshal
- `DEMO-REAL-MCP-SERVERS.md` — Testing real registry servers
- `TESTING-STRATEGY.md` — This file (complete overview)

### Automation

- `test-mcp-registry.sh` — Scan registry, test all public endpoints

### Gherkin Features

- `red-green-refactor.feature` — -42i pattern specs
- `negative-certification.feature` — Rejection and cleanup specs
- `demo-real-mcp-servers.feature` — Real server testing specs

---

## Quick Start

### As a Developer

```bash
# Test your MCP server before deployment
hurl --variable endpoint=http://localhost:3000 \
  tests/hurl/demo-real-mcp-servers.hurl

# Verify you can still detect non-MCP
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
```

### As Registry Operator

```bash
# Find all certified public MCP servers
bash tests/hurl/test-mcp-registry.sh

# Expected output shows:
# - Which servers are live (✓)
# - Which need auth (✗ 401)
# - Which are offline (⏳)
```

### As fire-marshal Integration

```bash
# Validate all endpoints in deployment
fire-marshal validate-endpoints \
  --positive-test tests/hurl/demo-real-mcp-servers.hurl \
  --negative-test tests/hurl/negative-endpoint.hurl \
  --registry /path/to/clusters.json
```

---

## The Story in One Picture

```
Question: Can we verify example.com isn't an MCP server?

Answer 1: Negative test
  ✓ Yes, test it returns non-200

Answer 2: -42i incomplete certification
  ✓ And show what's blocking full cert

Answer 3: Real MCP registry validation
  ✓ And test all actual servers in the ecosystem

Result: Complete testing strategy for MCP ecosystem
  - Developers can certify their servers
  - Registry can track certification status
  - fire-marshal can validate deployments
  - Users know which servers are safe to use
```

---

## Files Checklist

✓ `red-green-refactor.hurl` — Positive test template
✓ `RED-GREEN-REFACTOR.md` — -42i documentation
✓ `red-green-refactor.feature` — Gherkin specs
✓ `negative-endpoint.hurl` — Negative test template
✓ `NEGATIVE-CERTIFICATION.md` — Rejection patterns
✓ `negative-certification.feature` — Gherkin specs
✓ `CERTIFICATION-ROADMAP.md` — Integration guide
✓ `demo-real-mcp-servers.hurl` — Generic MCP test
✓ `DEMO-REAL-MCP-SERVERS.md` — Real server guide
✓ `test-mcp-registry.sh` — Registry scanner (executable)
✓ `demo-real-mcp-servers.feature` — Registry testing specs
✓ `TESTING-STRATEGY.md` — This complete overview

---

## Next Steps

1. **Short-term**: Use negative tests in CI/CD
2. **Medium-term**: Integrate with registry for badges
3. **Long-term**: Wait for smoke-alarm isotope transit
4. **Post-isotope**: Full receipt validation (-42i → 42)

---

## Credits

This testing strategy evolved from:
1. A simple question about example.com
2. The -42i trust rung (incomplete certification)
3. The MCP Registry ecosystem
4. fire-marshal deployment validation
5. The pickled-onions world-builder vision

The result: A comprehensive, measurable, transparent certification system for the MCP ecosystem.
