# Proxy Auth Discovery: Learning What's Missing by Trying

## The Insight

HTTP 401 from an MCP server doesn't mean it **failed the MCP test**—it means **we didn't conduct the proper test**.

The servers are MCP-compliant. We just didn't authenticate.

Instead of giving up at 401, we should **proxy the call through our system** and discover what our proxy implementation is missing.

## The Pattern

### Traditional Approach (Stops at Auth)

```bash
curl -X POST https://api.adramp.ai/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# Result: HTTP 401 Unauthorized
# Conclusion: "Failed to test"
# What we learned: Nothing
```

### Proxy Discovery Approach (Learns What's Missing)

```bash
curl -X POST http://localhost:60460/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "adhd.proxy",
    "params": {
      "target_endpoint": "https://api.adramp.ai/mcp",
      "auth": {
        "type": "bearer",
        "token": "your-token-here"
      },
      "call": {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/list"
      }
    }
  }'

# Possible Results:
# 1. Success (200): "Our proxy handles bearer auth! ✓"
# 2. Unimplemented (501): "Proxy doesn't support adhd.proxy yet"
# 3. Auth fails (401): "Proxy supports adhd.proxy but bearer auth incomplete"
# 4. Parse error (400): "Proxy has issues with param structure"
# 5. Timeout (504): "Proxy can't reach upstream or too slow"

# What we learned: Exactly what feature is missing
```

## Capability Discovery Matrix

Each test result tells us what to implement next:

```
Test: Proxy a 401-protected MCP server through our system

Expected: If adhd.proxy exists:
  ├── No adhd.proxy method
  │   └── 501 Method not found
  │       → Need to implement: adhd.proxy handler
  │
  ├── adhd.proxy exists but no auth support
  │   └── 400 Bad request (auth field not recognized)
  │       → Need to implement: Bearer token handling
  │
  ├── Bearer token support exists but token format wrong
  │   └── 401 Unauthorized (from upstream)
  │       → Need to implement: Token validation/refresh
  │
  ├── Token correct but upstream rejects for other reason
  │   └── 403 Forbidden (from upstream)
  │       → Need to implement: Rate limiting, scope management
  │
  └── Full success!
      └── 200 OK (result contains tools/list)
          → Proxy is feature-complete for this auth type
```

## Test Scenarios

### Scenario 1: Bearer Token Auth (Most Common)

```bash
hurl --variable proxy_endpoint=http://localhost:60460 \
     --variable auth_endpoint=https://api.adramp.ai/mcp \
     --variable auth_token="your-api-key-here" \
  tests/hurl/proxy-auth-discovery.hurl
```

**Expected Results**:

| HTTP Status | Meaning | Next Step |
|------------|---------|-----------|
| 501 | adhd.proxy not implemented | Start implementation |
| 400 | Bad params | Fix param structure |
| 401 | Token rejected upstream | Check token validity |
| 403 | Forbidden (rate limit?) | Add rate limiting support |
| 200 | ✓ Success | Proxy is bearer-ready |

### Scenario 2: API Key Header Auth

```bash
hurl --variable proxy_endpoint=http://localhost:60460 \
     --variable auth_endpoint=https://api.adadvisor.ai/mcp \
     --variable auth_token="x-api-key: your-key" \
  tests/hurl/proxy-auth-discovery.hurl
```

Expected: Fails with 400 (bearer not supported for this endpoint)

Next: Implement API-Key header support

### Scenario 3: OAuth2 (Most Complex)

```bash
hurl --variable proxy_endpoint=http://localhost:60460 \
     --variable auth_endpoint=https://oauth.example.com/mcp \
     --variable auth_token="oauth2://refresh-token:xyz" \
  tests/hurl/proxy-auth-discovery.hurl
```

Expected: Fails with 400 (oauth2 type not recognized)

Next: Implement OAuth2 flow (token refresh, scope handling, etc.)

## Real-World Example: AdRamp Google Ads

```bash
# AdRamp returns 401 (needs auth)
curl -X POST https://mcp.adramp.ai/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/list"}'
# → 401 Unauthorized

# Test through our proxy (once implemented)
curl -X POST http://localhost:60460/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "adhd.proxy",
    "params": {
      "target_endpoint": "https://mcp.adramp.ai/mcp",
      "auth": {
        "type": "bearer",
        "token": "{{ADRAMP_API_KEY}}"
      },
      "call": {"jsonrpc":"2.0","id":1,"method":"tools/list"}
    }
  }'

# If 501: "Need to implement adhd.proxy"
# If 400: "adhd.proxy exists but no bearer auth"
# If 401: "Bearer auth exists but token format wrong"
# If 200: "✓ Can now proxy authenticated AdRamp calls!"
```

## Implementation Roadmap

### Phase 1: Proxy Infrastructure (Required)

```go
// adhd/internal/proxy/proxy.go
type ProxyRequest struct {
  TargetEndpoint string      `json:"target_endpoint"`
  Auth           AuthConfig  `json:"auth"`
  Call           interface{} `json:"call"`
}

type AuthConfig struct {
  Type  string `json:"type"` // "bearer", "api-key", "oauth2"
  Token string `json:"token"`
}

// Handle adhd.proxy MCP call
func (p *Proxy) ProxyCall(req ProxyRequest) (interface{}, error) {
  // 1. Parse auth config
  // 2. Build upstream request with auth
  // 3. Forward call to target
  // 4. Return result
}
```

**Test**: `proxy-auth-discovery.hurl` with bearer token
**Expected**: 501 initially (not implemented)
**Then**: 200 OK after implementation

### Phase 2: Bearer Token Support

```go
func (a *BearerAuth) AddHeaders(req *http.Request, token string) {
  req.Header.Set("Authorization", "Bearer "+token)
}
```

**Test**: Same HURL test
**Expected**: 401 if token invalid, 200 if valid

### Phase 3: Other Auth Types

Add support for:
- API Key headers (`X-API-Key: ...`)
- Basic auth (`Authorization: Basic base64(user:pass)`)
- Custom headers
- OAuth2 token exchange

**Test**: `proxy-auth-discovery.hurl` with different auth types
**Expected**: Progresses from 400 → 200 as each type is added

### Phase 4: Advanced Proxy Features

- Token refresh (OAuth2)
- Rate limiting awareness
- Timeout handling
- Retry with exponential backoff
- Logging/tracing upstream calls

## Continuous Discovery

Each test failure tells us what to build:

```
Week 1: Test proxy-auth-discovery.hurl
  Result: 501 Method not found
  Action: Implement basic adhd.proxy

Week 2: Test with bearer token
  Result: 400 Bad request (token field not recognized)
  Action: Add bearer token support

Week 3: Test with real credentials
  Result: 401 Unauthorized (invalid token)
  Action: Debug token format, coordinate with provider

Week 4: Test all registry servers
  Result: 401, 403, 2xx mixed
  Action: Implement per-server auth discovery

Week 5: Automated proxy health check
  Result: Track how many 401s → 200s as features added
  Action: Show progress via dashboard
```

## Registry Health with Proxy

```bash
# Before proxy:
bash tests/hurl/test-mcp-registry.sh
  ✓ Certified: 4 (direct access)
  ✗ Failed: 14 (401 auth wall)
  ⏳ Offline: 2

# After proxy fully implemented:
bash tests/hurl/test-mcp-registry-via-proxy.sh
  ✓ Certified: 4 (still direct)
  ✓ Proxied: 14 (now working via proxy!)
  ⏳ Offline: 2 (still unreachable)

# Result: 18/20 servers accessible (instead of 4/20)
```

## Test Suite Evolution

### Current Suite (What We Have)

```
✓ demo-real-mcp-servers.hurl
  └─ Tests direct access (works for 4/20 servers)

✓ negative-endpoint.hurl
  └─ Tests rejection of non-MCP

✗ proxy-auth-discovery.hurl (new)
  └─ Tests what proxy features are missing
```

### Next Suite (What We'll Have)

```
✓ demo-real-mcp-servers.hurl
  └─ Direct access (4/20)

✓ proxy-auth-discovery.hurl
  └─ Through proxy:
     - Phase 1: adhd.proxy exists? (501 → 0)
     - Phase 2: Bearer token? (400 → 401 → 200)
     - Phase 3: API keys? (400 → 200)
     - Phase 4: OAuth2? (400 → 200)

Result: Measure proxy completeness by auth types supported

✓ test-mcp-registry-via-proxy.sh
  └─ "How many 401s become 200s?"
```

## Dashboard Visualization

```
Proxy Capability Maturity

Bearer Token Support
  ████████░░ 80% (4/5 issues resolved)
  - ✓ Token parsing
  - ✓ Header injection
  - ✓ Upstream auth
  - ✓ Error handling
  - ⏳ Token refresh

API Key Support
  ██░░░░░░░░ 20% (1/5 features)
  - ✓ Header format recognized
  - ⏳ Custom header names
  - ⏳ Multiple API keys
  - ⏳ Key rotation
  - ⏳ Rate limiting

OAuth2 Support
  ░░░░░░░░░░ 0% (not started)
  - ⏳ Authorization code flow
  - ⏳ Token endpoint
  - ⏳ Refresh token handling
  - ⏳ Scope management
  - ⏳ PKCE support
```

## Benefits of This Approach

| Benefit | Without Proxy Discovery | With Proxy Discovery |
|---------|------------------------|----------------------|
| **Visibility** | "401 = failed" | "401 = auth needed, here's what's missing" |
| **Progress** | Hidden features | Clear roadmap (Phase 1, 2, 3, 4) |
| **Prioritization** | Unknown | Test tells us what to build first |
| **Validation** | No way to know if proxy works | HURL test proves it end-to-end |
| **Ecosystem** | 4/20 servers (20%) | 18/20 servers (90%) once proxy complete |

## Next: Create the Test

The `proxy-auth-discovery.hurl` template is ready. Once we have:

1. `adhd.proxy` endpoint implemented
2. Bearer token support
3. Real credentials for test servers

We can run:

```bash
hurl --variable proxy_endpoint=http://localhost:60460 \
     --variable auth_endpoint=https://api.adramp.ai/mcp \
     --variable auth_token="$ADRAMP_TOKEN" \
  tests/hurl/proxy-auth-discovery.hurl
```

And watch the results go from:
- 501 (not implemented)
- → 400 (bad params)
- → 401 (auth missing)
- → 200 (success!)

Each step tells us exactly what to implement next.
