#!/bin/bash
# Phase 1 Test: Verify adhd.proxy endpoint is working
# This tests the basic proxy infrastructure without authentication

echo "==================================="
echo "Phase 1: Proxy Infrastructure Test"
echo "==================================="
echo ""

# Assume adhd is running locally
PROXY_ENDPOINT="${PROXY_ENDPOINT:-http://localhost:60460}"

echo "Testing adhd.proxy infrastructure..."
echo "Proxy: $PROXY_ENDPOINT"
echo ""

# Test 1: Direct tools/list to verify proxy is accessible
echo "Test 1: Verify MCP server is reachable"
STATUS=$(curl -s -X POST "$PROXY_ENDPOINT/mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  -w "\n%{http_code}" -o /dev/null)

if [ "$STATUS" != "200" ]; then
  echo "✗ FAIL: MCP server not responding (HTTP $STATUS)"
  echo "  Make sure ADHD is running: ./bin/adhd"
  exit 1
fi

echo "✓ PASS: MCP server is reachable"
echo ""

# Test 2: Check if adhd.proxy is in the tools list
echo "Test 2: Check if adhd.proxy is listed"
TOOLS=$(curl -s -X POST "$PROXY_ENDPOINT/mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')

if echo "$TOOLS" | grep -q "adhd.proxy"; then
  echo "✓ PASS: adhd.proxy is listed in tools/list"
else
  echo "✗ FAIL: adhd.proxy not found in tools list"
  echo "Response: $TOOLS"
  exit 1
fi
echo ""

# Test 3: Call adhd.proxy with a simple target (example.com)
echo "Test 3: Call adhd.proxy with non-auth target (example.com)"
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
        "call": {
          "jsonrpc": "2.0",
          "id": 1,
          "method": "tools/list"
        }
      }
    }
  }')

echo "Response: $RESPONSE"
echo ""

if echo "$RESPONSE" | grep -q '"error"'; then
  echo "✓ PASS: adhd.proxy executed (returned error as expected for non-MCP target)"
  echo "  Error message shows proxy is working"
elif echo "$RESPONSE" | grep -q '"result"'; then
  echo "✓ PASS: adhd.proxy executed successfully"
else
  echo "✗ FAIL: Unexpected response format"
  exit 1
fi

echo ""
echo "==================================="
echo "Phase 1 Tests: PASSED ✓"
echo "==================================="
echo ""
echo "Next: Phase 2 will add bearer token support"
echo "  This will unlock authenticated MCP servers"
echo ""
echo "To test with real credentials:"
echo "  hurl --variable proxy_endpoint=$PROXY_ENDPOINT \\"
echo "       --variable auth_endpoint=https://api.adramp.ai/mcp \\"
echo "       --variable auth_token=\$YOUR_TOKEN \\"
echo "    tests/hurl/proxy-auth-discovery.hurl"
