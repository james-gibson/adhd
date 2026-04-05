POST {{proxy_endpoint}}/mcp/proxy
Content-Type: application/json

{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.proxy",
  "params": {
    "target_endpoint": "{{auth_endpoint}}",
    "auth": {
      "type": "bearer",
      "token": "{{auth_token}}"
    },
    "call": {
      "jsonrpc": "2.0",
      "id": 1,
      "method": "tools/list"
    }
  }
}

[Asserts]
status == 200
jsonpath("$.result") exists
