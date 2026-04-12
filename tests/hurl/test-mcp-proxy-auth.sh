#!/bin/bash
# Test MCP servers through our proxy to discover missing auth features
# Each test failure tells us what to implement next

PROXY_ENDPOINT="${PROXY_ENDPOINT:-http://localhost:60460}"
REGISTRY_URL="https://registry.modelcontextprotocol.io/v0.1/servers"

DIRECT_OK=0
PROXIED_OK=0
NEEDS_BEARER=0
NEEDS_APIKEY=0
NEEDS_OAUTH2=0
OFFLINE=0

echo "=========================================="
echo "MCP Proxy Auth Discovery Testing"
echo "=========================================="
echo ""
echo "Proxy: $PROXY_ENDPOINT"
echo "Registry: $REGISTRY_URL"
echo ""

# Fetch servers with public endpoints
SERVERS=$(curl -s "$REGISTRY_URL" | jq -r '.servers[] | select(.server.remotes != null) | .server | "\(.name) \(.remotes[0].url)"' | sort -u)
SERVERS="temp https://api.pricepertoken.com/mcp\n ${SERVERS}"
SERVER_COUNT=$(echo "$SERVERS" | wc -l)
echo "Found $SERVER_COUNT servers to test"
echo ""

# Test each server
while IFS=' ' read -r name url; do
  [ -z "$name" ] && continue
  [ -z "$url" ] && continue

  echo -n "Testing $name ... "

  # First: Try direct access (no proxy)
  DIRECT=$(curl -s -X POST "$url" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
    -o /dev/null \
    -w "%{http_code}" \
    -m 2 2>&1)

  if [ "$DIRECT" = "200" ]; then
    echo "✓ Direct"
    DIRECT_OK=$((DIRECT_OK+1))
    continue
  fi

  # Second: Try through proxy (if proxy is available)
  if [ -n "$PROXY_ENDPOINT" ] && [ "$PROXY_ENDPOINT" != "http://localhost:60460" ]; then
    PROXIED=$(curl -s -X POST "$PROXY_ENDPOINT/mcp" \
      -H "Content-Type: application/json" \
      -d "{
        \"jsonrpc\": \"2.0\",
        \"id\": 1,
        \"method\": \"adhd.proxy\",
        \"params\": {
          \"target_endpoint\": \"$url\",
          \"auth\": {
            \"type\": \"bearer\",
            \"token\": \"\${$(echo $name | tr '/' '_' | tr '-' '_' | tr '.' '_')_TOKEN}\"
          },
          \"call\": {
            \"jsonrpc\": \"2.0\",
            \"id\": 1,
            \"method\": \"tools/list\"
          }
        }
      }" \
      -o /dev/null \
      -w "%{http_code}" \
      -m 2 2>&1)

    if [ "$PROXIED" = "200" ]; then
      echo "⏳ Via proxy (bearer auth) ✓"
      PROXIED_OK=$((PROXIED_OK+1))
      continue
    elif [ "$PROXIED" = "501" ]; then
      echo "⏳ Proxy not implemented yet (501)"
      NEEDS_BEARER=$((NEEDS_BEARER+1))
      continue
    elif [ "$PROXIED" = "400" ]; then
      echo "⏳ Proxy needs bearer token support"
      NEEDS_BEARER=$((NEEDS_BEARER+1))
      continue
    fi
  fi

  # Analyze direct failure reason
  case "$DIRECT" in
    401)
      echo "⏳ BLOCKED (401 - needs auth)"
      NEEDS_BEARER=$((NEEDS_BEARER+1))
      ;;
    403)
      echo "⏳ BLOCKED (403 - forbidden/rate limit)"
      NEEDS_APIKEY=$((NEEDS_APIKEY+1))
      ;;
    000|*)
      echo "⏳ OFFLINE"
      OFFLINE=$((OFFLINE+1))
      ;;
  esac
done <<< "$SERVERS"

echo ""
echo "=========================================="
echo "Proxy Auth Capability Discovery Results"
echo "=========================================="
echo ""
echo "Direct Access:     $DIRECT_OK"
echo "Proxied (working): $PROXIED_OK"
echo "Needs Bearer Auth: $NEEDS_BEARER"
echo "Needs API Key:     $NEEDS_APIKEY"
echo "Needs OAuth2:      $NEEDS_OAUTH2"
echo "Offline:           $OFFLINE"
echo ""
echo "Total accessible:  $(($DIRECT_OK + $PROXIED_OK))/$SERVER_COUNT"
echo ""

if [ "$PROXIED_OK" -eq 0 ] && [ "$NEEDS_BEARER" -gt 0 ]; then
  echo "Next Step: Implement adhd.proxy with bearer token support"
  echo "  - This would unlock $NEEDS_BEARER servers"
  echo ""
  echo "Command to test once implemented:"
  echo "  hurl --variable proxy_endpoint=$PROXY_ENDPOINT \\"
  echo "       --variable auth_endpoint={server-url} \\"
  echo "       --variable auth_token={your-token} \\"
  echo "    tests/hurl/proxy-auth-discovery.hurl"
fi

if [ "$PROXIED_OK" -gt 0 ]; then
  echo "✓ Proxy is working!"
  echo "  Bearer auth support: $PROXIED_OK servers unlocked"
  if [ "$NEEDS_BEARER" -gt 0 ]; then
    echo "  Still need: API-Key support for $NEEDS_APIKEY more servers"
  fi
fi
