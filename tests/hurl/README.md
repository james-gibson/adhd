# Agent Skill HURL Tests

These HURL scripts simulate agent skills that chain multiple tool calls via MCP. They can be:
1. Run standalone with the `hurl` CLI
2. Invoked as MCP tools via `adhd.hurl.run`
3. Executed on remote services
4. Used for continuous testing and validation

## Available Tests

### `agent-skill-health-check.hurl`
Agent skill: "Check Cluster Health"

**Chain:**
1. Call `adhd.status` → get lights summary
2. Call `adhd.lights.list` → enumerate all lights
3. Call `adhd.lights.get` → get details of first light
4. Call `adhd.isotope.peers` → get peer information

**Run:**
```bash
# Against local instance
hurl --variable endpoint=http://localhost:60460/mcp tests/hurl/agent-skill-health-check.hurl

# Against remote cluster
hurl --variable endpoint=http://192.168.7.18:60460/mcp tests/hurl/agent-skill-health-check.hurl
```

### `agent-skill-chaos-inject.hurl`
Agent skill: "Chaos Injection Test"

**Scenarios:**
1. Successful tool call (`adhd.status`)
2. Failed tool call (non-existent light)
3. Recovery: successful call after error

**Tests error handling and resilience**

### `agent-skill-concurrent-state.hurl`
Agent skill: "Concurrent State Consistency"

**Tests:**
1. Read initial state
2. Concurrent read while first is in flight
3. Verify consistency across multiple queries

**Validates no stale data or race conditions**

### `agent-skill-multi-cluster.hurl`
Agent skill: "Multi-Cluster Health Assessment"

**Tests:**
1. Probe cluster 1 status and isotope ID
2. Probe cluster 2 status and isotope ID
3. Verify isotope uniqueness across clusters

**Run with cluster registry:**
```bash
hurl --variable registry_url=http://192.168.7.18:19100/cluster \
     --variable cluster1_endpoint=http://192.168.7.18:60460/mcp \
     --variable cluster2_endpoint=http://192.168.7.18:60461/mcp \
     tests/hurl/agent-skill-multi-cluster.hurl
```

## Using as MCP Tools

These HURL scripts can be invoked as remote MCP tools via `adhd.hurl.run`:

### Via CLI
```bash
# Call adhd.hurl.run with script content
curl -X POST http://localhost:60460/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "adhd.hurl.run",
    "params": {
      "script": "POST http://localhost:60460/mcp\nContent-Type: application/json\n\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"adhd.status\"}"
    }
  }'
```

### Via Agent Skill
An agent can invoke HURL tests as tools:

```python
# Pseudo-code
agent.call_tool("adhd.hurl.run", {
    "script": load_file("tests/hurl/agent-skill-health-check.hurl"),
    "variables": {
        "endpoint": "http://192.168.7.18:60460/mcp"
    }
})
```

## Installation

Install `hurl` from https://hurl.dev/

```bash
# macOS
brew install hurl

# Linux
cargo install hurl

# Or download binary
https://github.com/Orange-OpenSource/hurl/releases
```

## Running All Tests

```bash
# Run all HURL scripts against a single endpoint
for script in tests/hurl/agent-skill-*.hurl; do
  echo "Running $script..."
  hurl --variable endpoint=http://192.168.7.18:60460/mcp "$script"
done
```

## Running Against Live Clusters

```bash
# Test all clusters in registry
REGISTRY=http://192.168.7.18:19100/cluster

# Get clusters
curl -s "$REGISTRY" | jq -r 'to_entries[] | .value.adhd_mcp' | while read endpoint; do
  echo "Testing $endpoint..."
  hurl --variable endpoint="$endpoint" tests/hurl/agent-skill-health-check.hurl
done
```

## Continuous Testing

Run HURL scripts in CI/CD pipeline:

```yaml
# .github/workflows/agent-skill-tests.yml
name: Agent Skill Tests
on: [push]
jobs:
  hurl:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
      - uses: orangeopensource/hurl-action@v3
        with:
          glob: 'tests/hurl/agent-skill-*.hurl'
          default-variables:
            endpoint: 'http://localhost:60460/mcp'
          continue-on-error: true
```

## Assertions

Each HURL script validates:
- HTTP status codes
- JSON response structure
- Field presence and types
- Data consistency
- Cross-request correlations

Example assertions:
```hurl
[Asserts]
status == 200
jsonpath("$.result.lights") exists
jsonpath("$.result.lights.total") isInteger
concurrent_total == {{initial_total}}  # Cross-request validation
```

## Variables

Pass variables to customize tests:

```bash
hurl --variable endpoint=http://example.com:8080/mcp \
     --variable registry_url=http://example.com:19100/cluster \
     tests/hurl/agent-skill-health-check.hurl
```

## Debugging

Run with verbose output:

```bash
hurl --verbose tests/hurl/agent-skill-health-check.hurl
```

See detailed request/response:

```bash
hurl --very-verbose tests/hurl/agent-skill-health-check.hurl
```

## Integration with Chaos Tests

These HURL scripts complement the Go integration tests:
- Go tests: internal validation, testing harness control
- HURL tests: external validation, real HTTP requests, portable execution

Together they provide:
- Unit-level testing (Go)
- Integration testing (HURL on real servers)
- Portability (HURL scripts can run anywhere)
- CI/CD integration (HURL-action for GitHub)
