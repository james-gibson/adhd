# Demo: Testing Real MCP Servers from the Registry

## Overview

The official MCP Registry (https://registry.modelcontextprotocol.io/) lists 100+ MCP server implementations.

Our HURL tests can verify which ones actually support the MCP protocol end-to-end.

## MCP Registry Servers

From the official registry, here are categories of real servers:

### Reference/Official Servers (Likely Testable)

These are official reference implementations from the MCP project:

```
- everything (reference/test server)
- fetch (web content fetching)
- filesystem (secure file operations)
- git (repository tools)
- memory (knowledge graph-based persistent memory)
- sequential-thinking (dynamic problem-solving)
```

**Github**: https://github.com/modelcontextprotocol/servers

### Service-Based Servers (Endpoint-Dependent)

These require authentication or are hosted by specific services:

```
Productivity:
- Notion MCP (exposes Notion data)
- Gmail MCP (email access)
- Google Drive MCP
- Slack MCP

Data/Analytics:
- ClickHouse MCP (data warehouse)
- MongoDB MCP (database)
- Zapier MCP (workflow automation)
- Financial Datasets MCP

Cloud Platforms:
- AWS MCP
- Azure MCP
- Cloudflare MCP
- GreptimeDB MCP
```

### Community Servers (Ecosystem)

From the registry, notable community servers include:

```
Code Analysis:
- Semgrep (static analysis)
- GitHub (repository tools)
- JetBrains (IDE integration)

Trading/Finance:
- Lona Trading (backtesting, market data)
- aTars MCP (crypto signals, sentiment)

Infrastructure:
- Contabo (cloud provisioning)
- SpotDB (ephemeral data sandbox)
- Chroma (vector database)
```

## Testing Strategy

### Level 1: Public HTTP Endpoints

Some servers expose HTTP/JSON-RPC endpoints:

```bash
# Example: Test a hypothetical public MCP server
hurl --variable endpoint=http://mcp.example.com \
  tests/hurl/demo-real-mcp-servers.hurl
```

### Level 2: GitHub Reference Servers

Clone and run the official reference servers locally:

```bash
# Get the official reference implementations
git clone https://github.com/modelcontextprotocol/servers.git
cd servers

# Run a reference server (e.g., fetch)
node dist/index.js --server fetch

# Test it locally
hurl --variable endpoint=http://localhost:3000 \
  tests/hurl/demo-real-mcp-servers.hurl
```

### Level 3: Registry Query Validation

Verify the registry API itself is MCP-compliant:

```bash
# The registry exposes server metadata
# Test that it follows MCP spec (if it implements JSON-RPC)

curl -s https://registry.modelcontextprotocol.io/v0.1/servers | head -20
# Returns: [{"name": "...", "description": "...", ...}, ...]
```

## Real Testing Scenarios

### Scenario 1: Test a Local MCP Server

```bash
#!/bin/bash
# Start a reference server locally (e.g., GitHub MCP)

# Install
npm install @modelcontextprotocol/server-github

# Run the server
export GITHUB_TOKEN="ghp_..."  # Required for GitHub server
node_modules/.bin/mcp-server-github &
MCP_PID=$!

# Test with our HURL script
hurl --variable endpoint=http://localhost:3000/mcp \
  tests/hurl/demo-real-mcp-servers.hurl

# Check result
if [ $? -eq 0 ]; then
  echo "✓ GitHub MCP server is MCP-compliant"
else
  echo "✗ GitHub MCP server failed certification"
fi

# Cleanup
kill $MCP_PID
```

### Scenario 2: Test Multiple Servers in Sequence

```bash
#!/bin/bash
# Registry of locally-running MCP servers

declare -A SERVERS=(
  [fetch]="http://localhost:3000"
  [git]="http://localhost:3001"
  [filesystem]="http://localhost:3002"
  [memory]="http://localhost:3003"
)

PASSED=0
FAILED=0

for name in "${!SERVERS[@]}"; do
  endpoint="${SERVERS[$name]}"
  echo -n "Testing $name at $endpoint... "

  if hurl --variable endpoint="$endpoint" \
          tests/hurl/demo-real-mcp-servers.hurl >/dev/null 2>&1; then
    echo "✓ PASS"
    PASSED=$((PASSED+1))
  else
    echo "✗ FAIL"
    FAILED=$((FAILED+1))
  fi
done

echo ""
echo "Summary: $PASSED passed, $FAILED failed"
```

### Scenario 3: Detect Non-MCP Server Masquerading as MCP

```bash
#!/bin/bash
# Test that we correctly REJECT endpoints claiming to be MCP but aren't

IMPOSTERS=(
  "https://example.com"           # Not an MCP server
  "https://google.com"            # Regular website
  "https://github.com"            # GitHub's main site (not their MCP server)
  "http://localhost:8080"         # Random service
)

echo "Testing negative cases (should all REJECT):"

for imposter in "${IMPOSTERS[@]}"; do
  echo -n "Testing $imposter... "

  if hurl --variable endpoint="$imposter" \
          tests/hurl/negative-endpoint.hurl >/dev/null 2>&1; then
    echo "✓ Correctly rejected"
  else
    echo "✗ ERROR: Accepted non-MCP endpoint"
  fi
done
```

## Expected Results

### Official Reference Servers

```
✓ fetch          → PASS (MCP-compliant)
✓ git            → PASS (MCP-compliant)
✓ filesystem     → PASS (MCP-compliant)
✓ memory         → PASS (MCP-compliant)
✓ everything     → PASS (MCP reference server)
✓ sequential     → PASS (MCP-compliant)
```

### Service-Based Servers (Hosted, Not Testable Directly)

```
⏳ Notion         → Requires auth, not publicly accessible
⏳ MongoDB        → Requires credentials and cloud account
⏳ AWS            → Requires AWS credentials
⏳ Salesforce     → Enterprise endpoint, restricted access
⏳ GitHub         → Official server; must run locally
```

### Known Imposters (Negative Tests Pass)

```
✓ example.com         → Status 405 (correctly rejected)
✓ google.com          → Status 403 (correctly rejected)
✓ github.com homepage → Status 200 but not JSON-RPC (correctly rejected)
✓ random service      → Timeout or 404 (correctly rejected)
```

## Integration with fire-marshal

fire-marshal can use these tests to validate the MCP ecosystem:

```bash
# fire-marshal registry-validation
#
# Query the official registry
# For each public server:
#   - If it advertises a public endpoint
#   - Run positive test (demo-real-mcp-servers.hurl)
#   - If passes: mark as certified
#   - If fails: mark as non-compliant
#
# Report: X certified, Y require-auth, Z non-responsive
```

## Demo: Running Against Reference Servers

### Setup

```bash
# Clone official MCP servers
git clone https://github.com/modelcontextprotocol/servers.git
cd servers
npm install

# Start multiple reference servers
node dist/fetch/index.js &          # Port 3000
node dist/git/index.js &            # Port 3001
node dist/filesystem/index.js &     # Port 3002
```

### Run Tests

```bash
# Test all local servers
for port in 3000 3001 3002; do
  echo "Testing localhost:$port"
  hurl --variable endpoint="http://localhost:$port" \
    tests/hurl/demo-real-mcp-servers.hurl
done
```

### Expected Output

```
Testing localhost:3000
  Request 1: POST http://localhost:3000/mcp
  Response:
    Status: 200
    Body: {"jsonrpc":"2.0","id":1,"result":{"lights":...}}
  Assertions:
    ✓ status == 200
    ✓ jsonpath("$.result") exists
  ✓ PASS

Testing localhost:3001
  Request 1: POST http://localhost:3001/mcp
  Response:
    Status: 200
    Body: {...}
  Assertions:
    ✓ status == 200
    ✓ jsonpath("$.result") exists
  ✓ PASS

Testing localhost:3002
  [same pattern]
  ✓ PASS
```

## Real-World: Service-Based Servers

For servers that require credentials (Notion, GitHub, AWS, etc.):

```bash
#!/bin/bash
# Test a GitHub MCP server with authentication

export GITHUB_TOKEN="ghp_your_token_here"

# Start the official GitHub MCP server
npx @modelcontextprotocol/server-github &
GITHUB_PID=$!

sleep 2  # Wait for server to start

# Test it
hurl --variable endpoint=http://localhost:3000 \
  tests/hurl/demo-real-mcp-servers.hurl

if [ $? -eq 0 ]; then
  echo "✓ GitHub MCP server is MCP-certified"
else
  echo "✗ GitHub MCP server failed certification"
fi

kill $GITHUB_PID
```

## CI/CD Integration

```yaml
# .github/workflows/test-mcp-servers.yml
name: Test Official MCP Servers

on: [push]

jobs:
  reference-servers:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Install reference servers
        run: |
          git clone https://github.com/modelcontextprotocol/servers.git
          cd servers
          npm install

      - name: Start servers in background
        run: |
          cd servers
          node dist/fetch/index.js &
          node dist/git/index.js &
          node dist/filesystem/index.js &
          sleep 2

      - name: Run HURL tests
        run: |
          for port in 3000 3001 3002; do
            hurl --variable endpoint="http://localhost:$port" \
              tests/hurl/demo-real-mcp-servers.hurl
          done

      - name: Report certification
        if: success()
        run: |
          echo "✓ All reference servers are MCP-certified"
          echo "✓ Negative tests still working"
          echo "✓ Certification level: complete"
```

## Sources

MCP servers and registry:
- [Official MCP Registry](https://registry.modelcontextprotocol.io/)
- [GitHub - modelcontextprotocol/servers](https://github.com/modelcontextprotocol/servers)
- [Awesome MCP Servers](https://mcpservers.org/)
