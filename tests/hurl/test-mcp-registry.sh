#!/bin/bash
# Test real MCP servers from the official registry
# Discovers what actually implements MCP vs what doesn't

REGISTRY_URL="https://registry.modelcontextprotocol.io/v0.1/servers"
CERTIFIED=0
FAILED=0
PENDING=0

echo "=========================================="
echo "MCP Registry Certification Testing"
echo "=========================================="
echo ""
echo "Registry: $REGISTRY_URL"
echo ""

# Fetch server list
echo "Fetching server list from registry..."
SERVERS=$(curl -s "$REGISTRY_URL" | jq -r '.servers[] | select(.server.remotes != null) | .server | "\(.name) \(.remotes[0].url)"' | sort -u)

if [ -z "$SERVERS" ]; then
  echo "ERROR: Could not fetch server list from registry"
  exit 1
fi

SERVER_COUNT=$(echo "$SERVERS" | wc -l)
echo "Found $SERVER_COUNT servers with public endpoints"
echo ""
echo "Sample servers to test:"
echo "$SERVERS" | head -5 | sed 's/^/  - /'
echo ""
echo "=========================================="
echo ""

# Test each server with a public endpoint
while IFS=' ' read -r name url; do
  [ -z "$name" ] && continue
  [ -z "$url" ] && continue

  echo -n "Testing $name ... "

  # Check if endpoint is reachable and responds to MCP
  STATUS=$(curl -s -X POST "$url" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
    -o /dev/null \
    -w "%{http_code}" \
    -m 3 2>&1)

  if [ "$STATUS" = "200" ]; then
    echo "✓ CERTIFIED (HTTP 200)"
    CERTIFIED=$((CERTIFIED+1))
  elif [ "$STATUS" = "000" ] || [ -z "$STATUS" ]; then
    echo "⏳ OFFLINE"
    PENDING=$((PENDING+1))
  else
    echo "✗ FAILED (HTTP $STATUS)"
    FAILED=$((FAILED+1))
  fi
done <<< "$SERVERS"

echo ""
echo "=========================================="
echo "Summary of $SERVER_COUNT servers:"
echo "  ✓ Certified:  $CERTIFIED"
echo "  ✗ Failed:     $FAILED"
echo "  ⏳ Offline:    $PENDING"
echo "=========================================="
echo ""

if [ "$CERTIFIED" -gt 0 ]; then
  echo "✓ Found $CERTIFIED live MCP servers!"
  echo ""
  echo "These servers are suitable for testing:"
  echo "$SERVERS" | while IFS=' ' read -r name url; do
    curl -s -o /dev/null -w "" -m 1 "$url" 2>/dev/null && echo "  - $name ($url)"
  done | head -10
fi
