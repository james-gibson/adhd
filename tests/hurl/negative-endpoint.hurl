POST {{endpoint}}/mcp
Content-Type: application/json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "adhd.status"
}
[Asserts]
status != 200
