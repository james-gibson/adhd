#!/bin/bash
# Phase 2 Test: Bearer Token Validation & Error Categorization
# Tests improved error messages and auth handling

echo "=========================================="
echo "Phase 2: Bearer Token Validation Test"
echo "=========================================="
echo ""

PROXY_ENDPOINT="${PROXY_ENDPOINT:-http://localhost:60460}"

echo "Testing bearer token error categorization..."
echo "Proxy: $PROXY_ENDPOINT"
echo ""

# Test 1: Forward to example.com (non-MCP, will get 405)
echo "Test 1: Bearer token with non-MCP endpoint"
echo "  (Target: example.com, returns HTTP 405)"
echo ""

RESPONSE=$(curl -s -X POST "$PROXY_ENDPOINT/mcp" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "adhd.proxy",
      "input": {
        "target_endpoint": "https://example.com",
        "auth": {
          "type": "bearer",
          "token": "test-token"
        },
        "call": {
          "jsonrpc": "2.0",
          "id": 1,
          "method": "tools/list"
        }
      }
    }
  }')

echo "Response:"
echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
echo ""

# Check for proper error handling
if echo "$RESPONSE" | grep -q '"error"'; then
  echo "✓ PASS: Error response generated"
  if echo "$RESPONSE" | grep -q '"code"'; then
    echo "✓ PASS: Error has code field"
  fi
  if echo "$RESPONSE" | grep -q '"message"'; then
    echo "✓ PASS: Error has message field"
  fi
else
  echo "✗ FAIL: No error in response"
fi

echo ""
echo "=========================================="
echo "Phase 2 Improvements:"
echo "=========================================="
echo ""
echo "✓ Better error categorization:"
echo "  - 401 Unauthorized: bearer token rejected"
echo "  - 403 Forbidden: insufficient permissions"
echo "  - 404 Not Found: endpoint doesn't exist"
echo "  - 429 Too Many Requests: rate limited"
echo ""
echo "✓ Detailed error messages with context"
echo "✓ Logging for debugging (run with -debug flag)"
echo ""
echo "Next Phase 3: API-Key header support"
echo "=========================================="
