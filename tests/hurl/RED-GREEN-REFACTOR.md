# Red-Green-Refactor: -42i Incomplete Certification

## Overview

These tests follow the **Red-Green-Refactor** pattern at the **-42i trust rung** (certified but incomplete).

The tests **PASS** functionally but **BLOCK** full certification because critical isotope validation is incomplete.

## GREEN Phase ✓ (Tests Pass Today)

Agent skills execute end-to-end without isotope validation:

```bash
hurl --variable endpoint=http://localhost:60460/mcp tests/hurl/red-green-refactor.hurl
```

### What Works:
- ✓ Tool chaining executes: status → lights.list → lights.get → isotope.instance
- ✓ Multi-step skills see consistent state across requests
- ✓ Error recovery from invalid requests
- ✓ Isotope identity exists (cluster has a unique ID)

### Test Results:
```
adhd.status           → ✓ PASS (lights summary exists)
adhd.lights.list      → ✓ PASS (returns array of lights)
adhd.lights.get(...)  → ✓ PASS (returns light detail)
adhd.isotope.instance → ✓ PASS (returns isotope_id)
```

## RED Phase ✗ (Missing Dependencies)

These features are **intentionally NOT implemented** yet, preventing full certification:

### Gap 1: Isotope Certification in Selfdescription
**Blocked on**: `ocd-smoke-alarm::certification-gate.feature`

Smoke-alarm hasn't exposed:
```json
GET /.well-known/smoke-alarm.json
{
  "isotope_certified": true,  // ← NOT YET IMPLEMENTED
  ...
}
```

**Impact**: ADHD can't distinguish certified vs uncertified alarms

**Why it matters**: Determines whether alarm suppression rules apply

---

### Gap 2: Isotope Metadata in Status Responses
**Blocked on**: `ocd-smoke-alarm::isotope-transit.feature`

Smoke-alarm hasn't added to response:
```json
POST /mcp (adhd.status)
{
  "result": {
    "lights": {...},
    "isotope_id": "...",      // ← NOT YET IMPLEMENTED
    "feature_id": "...",      // ← NOT YET IMPLEMENTED
    "nonce": "..."            // ← NOT YET IMPLEMENTED
  }
}
```

**Impact**: ADHD can't validate receipts or detect tampering

**Why it matters**: Without isotope metadata, agent skills can't prove which capability ran or when

---

### Gap 3: Receipt Pre-Computation Validation
**Blocked on**: `adhd::isotope-transit.feature` + `adhd::receipt-validation.feature`

ADHD hasn't implemented:
```go
// Validate receipt before accepting state change
expectedReceipt := base64url(SHA256(
  feature_id + ":" + SHA256(payload) + ":" + nonce
))
if expectedReceipt != returnedReceipt {
  reject()  // ← NOT YET IMPLEMENTED
}
```

**Impact**: No protection against injection/tampering during skill execution

**Why it matters**: If network is compromised during execution, we have no way to detect it

---

## REFACTOR Phase → (Transition Path)

### Blockers to Full Certification

| Task | Owner | Status |
|------|-------|--------|
| Implement `isotope_certified` in selfdescription | ocd-smoke-alarm | ⏳ Pending |
| Add isotope metadata to `/status` responses | ocd-smoke-alarm | ⏳ Pending |
| Implement isotope-transit.feature (smoke-alarm side) | ocd-smoke-alarm | ⏳ Pending |
| Implement receipt validation in model.go | adhd | ⏳ Pending |
| Implement isotope-transit.feature (adhd side) | adhd | ⏳ Pending |

### Progression Checklist

When these are complete, we move from **-42i** to **42** (fully certified):

- [ ] smoke-alarm exposes `isotope_certified=true`
- [ ] smoke-alarm returns isotope metadata in status responses
- [ ] adhd::isotope-transit.feature passes
- [ ] adhd implements receipt pre-computation
- [ ] adhd::receipt-validation.feature passes
- [ ] fire-marshal validates full chain end-to-end
- [ ] re-run: `hurl --variable endpoint=... tests/hurl/red-green-refactor.hurl`

Then add:
```hurl
# Now we can validate the receipt matches our pre-computation
[Asserts]
jsonpath("$.result.isotope_receipt") == "{{expected_receipt}}"
```

---

## Why -42i Magic Matters

### The Problem It Solves

Without the -42i rung, we have two bad choices:

1. **Hide the gaps**: Say "it's certified" when it's not
   - False confidence → bugs in production
   - Certification is meaningless

2. **Wait for everything**: Don't test until fully done
   - Can't catch regressions meanwhile
   - Can't validate partial progress

### The -42i Solution

Make the gap **explicit and visible**:

- ✓ Tests pass (proving current functionality works)
- ✗ Certification blocked (proving we know what's missing)
- → Clear path forward (proving it's achievable)

**Result**: Developers see exactly where the project stands, with no false confidence.

---

## Example: Using This in CI/CD

```yaml
# .github/workflows/red-green-refactor.yml
name: -42i Certification Status
on: [push]
jobs:
  green:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: orangeopensource/hurl-action@v3
        with:
          glob: 'tests/hurl/red-green-refactor.hurl'
          default-variables:
            endpoint: 'http://localhost:60460/mcp'

  certification-gate:
    runs-on: ubuntu-latest
    steps:
      - name: Check -42i blockers
        run: |
          echo "✓ GREEN tests pass"
          echo "✗ RED blockers remain:"
          echo "  - isotope_certified in selfdescription"
          echo "  - isotope metadata in status responses"
          echo "  - receipt pre-computation validation"
          echo ""
          echo "Certification level: -42i (incomplete)"
          exit 0  # Don't fail; transparency is progress
```

---

## References

- **pickled-onions world-builder**: Trust rungs (↑ 42i, → 42, ↓ -42i, etc.)
- **smoke-alarm**: Must implement isotope transit before adhd can validate
- **ADHD CLAUDE.md**: Architecture and testing strategy
- **fire-marshal**: Pre-deployment validation tool that checks this rung
