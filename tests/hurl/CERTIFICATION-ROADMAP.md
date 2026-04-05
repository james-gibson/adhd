# Certification Roadmap: Red-Green-Refactor Pattern

## Overview

The complete certification pattern uses **red-green-refactor** testing across three dimensions:

1. **Positive Tests** → Prove system IS MCP-compliant
2. **Negative Tests** → Prove we correctly REJECT non-MCP systems
3. **Documentation** → Explain what's missing for full certification

## Test Files Map

```
tests/hurl/
├── red-green-refactor.hurl
│   └── GREEN: Tool chains execute (adhd.status → lights.list → lights.get → isotope.instance)
│       RED: Receipt validation not yet implemented
│
├── RED-GREEN-REFACTOR.md
│   └── Documents the -42i incomplete certification rung
│       Blocker: waiting for smoke-alarm isotope transit
│
├── negative-endpoint.hurl
│   └── Prove non-MCP endpoints are correctly REJECTED
│       Test against example.com: returns 405 ✓
│
├── NEGATIVE-CERTIFICATION.md
│   └── Use cases for negative testing:
│       - Registry cleanup (dead clusters)
│       - Configuration typo detection
│       - Timeout/network partition handling
│
└── CERTIFICATION-ROADMAP.md (this file)
    └── Ties everything together
```

## The Complete Flow

### Phase 1: Positive Certification (Does it support MCP?)

```bash
hurl --variable endpoint=http://localhost:60460/mcp \
  tests/hurl/red-green-refactor.hurl
```

**Expected**: ✓ PASS
- adhd.status works
- Tool chaining works
- But isotope receipt validation fails (RED)

**Result**: **-42i (Incomplete Certification)**
- ✓ Core functionality works
- ✗ Can't fully certify without smoke-alarm isotope transit
- → Clear path to full certification (see RED-GREEN-REFACTOR.md)

### Phase 2: Negative Certification (Does it correctly REJECT non-MCP?)

```bash
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
```

**Expected**: ✓ PASS (assertion status != 200)
- example.com returns 405
- Assertion passes because 405 != 200
- We correctly identify this as non-MCP

**Result**: **Negative certification complete**
- ✓ System correctly rejects non-MCP endpoints

### Phase 3: Integration (Both Together)

```bash
# Positive: live MCP endpoint MUST pass
hurl --variable endpoint=http://localhost:60460/mcp \
  tests/hurl/red-green-refactor.hurl
# Expected: ✓ PASS

# Negative: non-MCP endpoint MUST fail
hurl --variable endpoint=https://example.com \
  tests/hurl/negative-endpoint.hurl
# Expected: ✓ PASS (assertion correct)

# If both pass: certification is complete
```

---

## fire-marshal Integration

### Current Validation

```
fire-marshal scan-registry
│
├── For each cluster in registry:
│   ├── Run positive test (red-green-refactor.hurl)
│   │   ├── ✓ PASS → Endpoint is alive/MCP-compliant (-42i or higher)
│   │   └── ✗ FAIL → Endpoint is dead/broken
│   │
│   └── If FAIL, confirm with negative test (negative-endpoint.hurl)
│       ├── ✓ PASS → Confirmed dead (mark for cleanup)
│       └── ✗ FAIL → Something wrong (investigate)
│
└── Report: X alive, Y dead, Z stale (>30 days no activity)
```

### Example Output

```
Registry: 30 clusters

✓ http://192.168.7.18:60460/mcp
  Status: ALIVE (-42i incomplete certification)
  Reason: Core functions work, waiting on smoke-alarm isotope transit
  Action: Keep

✓ http://192.168.7.18:60460/mcp (3 other instances)
  Status: ALIVE (-42i)
  Action: Keep

✗ http://192.168.7.18:60461/mcp
  Status: DEAD
  Confirmed: Negative test passes (correctly non-MCP)
  Action: Mark for cleanup

❌ (28 more dead clusters)

Summary:
  Alive & certifiable: 4
  Dead & confirmed: 26
  Stale (>30 days): 26
  Action: Remove 26 stale entries from registry
```

---

## Real-World Scenarios

### Scenario 1: Daily Registry Health Check

```bash
#!/bin/bash
# cron: 0 9 * * *

REGISTRY_FILE="/etc/adhd/clusters.json"
ALIVE=0
DEAD=0

while read endpoint; do
  if hurl --variable endpoint="$endpoint" \
          tests/hurl/red-green-refactor.hurl >/dev/null 2>&1; then
    ALIVE=$((ALIVE+1))
    echo "✓ $endpoint"
  else
    DEAD=$((DEAD+1))
    echo "✗ $endpoint"
  fi
done < <(jq -r '.[] | .adhd_mcp' < "$REGISTRY_FILE")

echo ""
echo "Summary: $ALIVE alive, $DEAD dead"

if [ "$DEAD" -gt 20 ]; then
  echo "WARNING: >67% of clusters are dead. Triggering cleanup."
  fire-marshal cleanup-stale-clusters
fi
```

### Scenario 2: Pre-Deployment Validation

```yaml
# .github/workflows/pre-deploy-validation.yml
name: Pre-Deploy Certification

on:
  pull_request:
    paths:
      - 'cmd/adhd/**'
      - 'internal/**'

jobs:
  certification:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Build ADHD
        run: go build -o bin/adhd ./cmd/adhd

      - name: Start local MCP endpoint
        run: ./bin/adhd &
        timeout-minutes: 1

      - name: Positive certification (RED-GREEN-REFACTOR)
        run: |
          hurl --variable endpoint=http://localhost:60460/mcp \
            tests/hurl/red-green-refactor.hurl

      - name: Negative certification
        run: |
          hurl --variable endpoint=https://example.com \
            tests/hurl/negative-endpoint.hurl

      - name: fire-marshal validation
        run: fire-marshal validate --features=features/adhd/

      - name: Report certification level
        if: always()
        run: |
          echo "✓ Positive tests passed"
          echo "✓ Negative tests passed"
          echo "📊 Certification level: -42i"
          echo "⏳ Waiting on smoke-alarm::isotope-transit"
```

### Scenario 3: Cluster Bootstrap

```bash
#!/bin/bash
# When provisioning a new cluster, verify it's MCP-ready

NEW_CLUSTER="http://192.168.7.100:60460"

echo "Validating new cluster: $NEW_CLUSTER"

# Positive test: must work
if ! hurl --variable endpoint="$NEW_CLUSTER/mcp" \
          tests/hurl/red-green-refactor.hurl; then
  echo "ERROR: New cluster failed positive certification"
  echo "Action: Check MCP server startup logs"
  exit 1
fi

# Negative test: example.com must remain non-MCP
if ! hurl --variable endpoint=https://example.com \
          tests/hurl/negative-endpoint.hurl; then
  echo "ERROR: Negative test failed (shouldn't happen)"
  echo "Action: Investigate test infrastructure"
  exit 1
fi

# Both tests passed
echo "✓ Cluster $NEW_CLUSTER is ready"
echo "✓ Negative tests still working"
echo "Proceeding with deployment..."
```

---

## Certification Levels

| Level | Positive | Negative | Isotope | Receipt | Status |
|-------|----------|----------|---------|---------|--------|
| **-42i** | ✓ | ✓ | Pending | Missing | Current |
| **42** | ✓ | ✓ | ✓ | ✓ | Blocked on smoke-alarm |
| **∞** | ✓ | ✓ | ✓ | ✓ | Future (post-smoke-alarm) |

### Progression Path

```
Now: -42i (Incomplete)
  ↓ (smoke-alarm implements isotope transit)
→ 42 (Certified)
  ↓ (additional hardening)
↑ ∞ (Fully hardened)
```

---

## Benefits of This Pattern

| Benefit | Without | With This Pattern |
|---------|---------|-------------------|
| **Confidence** | "Tests pass" | "Tests pass, and we can detect non-MCP" |
| **Gaps** | Hidden | Explicit and documented |
| **Registry** | Stale clusters accumulate | Automated cleanup via negative tests |
| **Debugging** | "Why did deployment fail?" | "Negative test caught bad endpoint" |
| **Progress** | Unknown completion status | -42i → 42 → ∞ roadmap visible |

---

## Next Steps

1. **Immediate**: Run both test suites in CI/CD
   - Positive: `red-green-refactor.hurl`
   - Negative: `negative-endpoint.hurl`

2. **Short-term**: Integrate with fire-marshal
   - Detect stale clusters automatically
   - Report -42i status with blockers

3. **Medium-term**: Wait for smoke-alarm isotope transit
   - Then implement receipt validation
   - Progress from -42i → 42

4. **Long-term**: Full hardening
   - Reach ∞ certification level
   - All chaos skills are verifiable
