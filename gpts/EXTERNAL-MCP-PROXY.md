# External MCP Server Integration (Manual Path)

This document describes the **manual** method to connect external MCP servers to ADHD via the proxy mechanism. The automated path (`projects` propagation) is currently half-wired; these steps capture the working manual approach.

## Background

ADHD provides proxying capabilities for external MCP servers:

- `adhd.proxy` - Forwards arbitrary MCP JSON-RPC calls to a target endpoint with optional authentication. Exposed via the MCP server (default: `http://localhost:9090/mcp`).
- `adhd.cluster.join` with `projects` - Registers per-project proxy tools like `adhd.project.<name>.call` that forward calls to a specified `mcp_url`.

**Reference example:** MachineWitness at `https://machinewitness.eu/mcp` (registry name `eu.machinewitness/archive`) with tools `check_coverage`, `capture_status`, `suggest_domain`, and `order_capture`.

## Manual Path

### 1. Run ADHD with MCP server enabled

Start ADHD with the MCP server enabled (headless or TUI):

```bash
./bin/adhd --headless --mcp-addr :9090
```

Or in TUI mode with config enabling MCP:
```bash
./bin/adhd --config ./adhd-example.yaml
```

**Verify it's running:**

```bash
curl -s -X POST http://localhost:9090/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | jq
```

You should see `adhd.proxy` in the listed tools.

### 2. Make ADHD reachable to your agent/IDE

To call `adhd.proxy` from an agent (Claude, OpenCode, etc.), register ADHD's MCP endpoint in your client configuration. For example:

- OpenCode: add to `opencode.json` or `.mcp.json`
- Claude Code: add via `claude mcp add` or config
- Other clients: add `http://localhost:9090/mcp` as an MCP server

Currently many configs (like `.mcp.json`/`opencode.json` in this repo) list only `open-knowledge` — you'll need to add ADHD's endpoint to issue proxy calls.

### 3. Ad-hoc proxy (no registration needed)

You can call any external MCP server directly via `adhd.proxy` without pre-registering it. Pass the target endpoint and the JSON-RPC call to forward.

**Via direct JSON-RPC:**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.proxy",
  "params": {
    "target_endpoint": "https://machinewitness.eu/mcp",
    "call": {
      "method": "tools/list"
    }
  }
}
```

**Via tools/call:**

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "adhd.proxy",
    "arguments": {
      "target_endpoint": "https://machinewitness.eu/mcp",
      "call": {
        "method": "tools/list"
      }
    }
  }
}
```

**Example calling a specific tool:**

```json
{
  "target_endpoint": "https://machinewitness.eu/mcp",
  "call": {
    "method": "tools/call",
    "params": {
      "name": "check_coverage",
      "arguments": {
        "domain": "example.eu"
      }
    }
  }
}
```

### 4. Named project tool (optional registration)

If you want a persistent named tool for an external server, register it via `adhd.cluster.join` with a `projects` array:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.cluster.join",
  "params": {
    "name": "my-cluster",
    "projects": [
      {
        "name": "machinewitness",
        "repo": "martinschenk/machinewitness-mcp",
        "mcp_url": "https://machinewitness.eu/mcp"
      }
    ]
  }
}
```

After successful join, the tool `adhd.project.machinewitness.call` appears in `tools/list`. You can call it like:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.project.machinewitness.call",
  "params": {
    "method": "tools/list"
  }
}
```

Or with parameters:

```json
{
  "method": "tools/call",
  "params": {
    "name": "check_coverage",
    "arguments": {
      "domain": "example.eu"
    }
  }
}
```

## Caveats

- **Network reachability:** The proxy node (ADHD instance) must be able to resolve DNS and reach the target endpoint. A blocked-DNS client will still work as long as it can reach the proxy node.
- **No monitoring/eviction:** The proxied endpoint is unmonitored by the alarm system. There is no `τ` probe, eviction, or cycle cap. This provides bare-MCP availability (`A = a`) on the far side of one egress node.
- **Limited OAuth2 support:** `auth.type: "oauth2"` is currently implemented as a bearer token only — there is no refresh, scope handling, or token exchange logic (see `addAuth` in `adhd/internal/proxy/proxy.go`).
- **Registration scope:** While the automated propagation issue remains open, `adhd.project.<name>.call` tools will **not** appear via `lezz` demo discovery or `--demo` modes. They only appear after a direct `adhd.cluster.join` payload with `projects` is sent.
- **HTTPS only as specified:** The example targets HTTPS endpoints; ensure target endpoints are reachable from the proxy's network context.
- **Authentication:** If the external server requires auth, pass an `auth` object with `type` (`bearer|api-key|oauth2`), `token`, and optionally `header` (for `api-key` type, defaults to `X-API-Key`).
- **Validation:** The proxy validates required fields (`target_endpoint`, `call`). If missing, you'll get a JSON-RPC error with code -32602.
