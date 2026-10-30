@adhd
@z2-relational
@domain-stdio-transport
Feature: The MCP stdio Transport
  adhd.proxy can reach any HTTP MCP endpoint. It could not reach a stdio server,
  and neither could the adhd.project.*.call forwarder minted by
  adhd.cluster.join, so a stdio-only server (Qdrant's, an ACP agent, a local
  tool server) stayed a direct client context (adhd#41).

  The correction is "does not", not "cannot": nothing in adhd's design prevents
  it, the transport was simply unimplemented. It is implemented here as a
  persistent session per named target — one initialize per child, JSON-RPC ids
  remapped, one stdout stream demultiplexed — not as a per-call probe.

  The reason it is a subsystem rather than a prefix check is the safety trap. A
  caller-supplied command on an unauthenticated surface is arbitrary command
  execution, a strict worsening of the SSRF adhd.proxy already is. So a stdio
  server is addressed by NAME from local config, and no request field can name
  or construct a command. Requests that try are refused and filed.

  Background:
    Given adhd is configured with a stdio_targets entry named "qdrant"
    And its command, args, env and cwd come from that file, never from a request

  # ── reachability ──────────────────────────────────────────────────────────

  Scenario: a named target is reachable for tools/list
    When a caller invokes "adhd.stdio.qdrant.call" with method "tools/list"
    Then the child is started once and initialized once
    And the result is the child's tools list

  Scenario: a named target is reachable for tools/call
    When a caller invokes "adhd.stdio.qdrant.call" with a tool and arguments
    Then the call is forwarded as a tools/call to the child
    And the child's content blocks are returned

  Scenario: every tool the child publishes is a namespaced surface entry
    Given the child's tools/list names "qdrant-find" and "qdrant-store"
    When the session starts
    Then "adhd.stdio.qdrant.qdrant-find" is present in tools/list
    And "adhd.stdio.qdrant.qdrant-store" is present
    And no bare "qdrant-find" is exposed

  Scenario: two targets may publish the same tool name
    Given targets "a" and "b" both publish "search"
    When both sessions start
    Then "adhd.stdio.a.search" and "adhd.stdio.b.search" both exist
    And neither shadows the other

  Scenario: a name collision is refused and reported
    Given "adhd.stdio.qdrant.search" is already held by another tool
    When the child publishes "search"
    Then the name is not overwritten
    And "adhd.stdio.list" reports the collision
    And the tool is not silently dropped

  Scenario: a project endpoint may name a configured stdio target
    Given a cluster project publishes "mcp_url: stdio:qdrant"
    When a caller invokes "adhd.project.qdrant.call" with method "tools/list"
    Then the call is served over the stdio transport
    And the call shape is unchanged from the HTTP case

  # ── no request can name a command ────────────────────────────────────────

  Scenario: adhd.proxy refuses a stdio target and files a knock
    When a caller calls "adhd.proxy" with "target_endpoint": "stdio:uvx mcp-server-qdrant"
    Then the call is refused with -32602
    And a "stdio_knock" event is filed
    And no process is started

  Scenario: a proxy request carrying a command is a knock
    When a caller calls "adhd.proxy" with "command": "sh" and "args": ["-c", "id"]
    Then the call is refused
    And a "stdio_knock" event is filed
    And no process is started

  Scenario: an unconfigured name never reaches exec
    When a caller invokes "adhd.stdio.not-configured.call"
    Then the response is "unknown target"
    And no process is started

  Scenario: the bridge does not forward initialize
    When a caller invokes "adhd.stdio.qdrant.call" with method "initialize"
    Then the call is refused
    And the session's own handshake is untouched

  # ── the child is a process, and processes end ────────────────────────────

  Scenario: sessions start lazily
    Given adhd has a qdrant target configured
    When nobody has called it
    Then tools/list shows "adhd.stdio.qdrant.call"
    And no child process exists
    And "adhd.stdio.list" reports it as not running

  Scenario: killing adhd leaves no surviving child
    Given a session is running
    When adhd shuts down
    Then the child is signalled by process group
    And a wrapper's grandchild is killed with its group

  Scenario: a child that ignores SIGTERM is killed
    Given the child ignores SIGTERM
    When the session is closed
    Then SIGKILL is sent after a bounded wait
    And Close returns within five seconds

  Scenario: a crash is a monitored failure, not a respawn loop
    Given the child exits during the handshake
    When a caller calls it twice
    Then the first call reports the failure
    And the second is refused during the cooldown
    And no second process is spawned

  Scenario: concurrent calls are demultiplexed
    Given eight callers call the same target at once
    When the child answers out of order
    Then each caller receives its own response
    And no id is delivered twice

  # ── the environment is explicit ──────────────────────────────────────────

  Scenario: the parent environment is not inherited
    Given adhd was started with an API key in its environment
    When a child is started
    Then that variable is not in the child's environment
    And only inherit_env names pass through

  Scenario: no shell interprets the command
    Given a target's args contain "&&" and "$HOME"
    When the child is started
    Then the arguments are passed literally
    And no shell is invoked

  Scenario: allowed_tools bounds the child
    Given a target lists allowed_tools: ["qdrant-find"]
    When a caller asks for "qdrant-store"
    Then the call is refused
    And the child never sees the request

  # ── what the bridge cannot do, it says ───────────────────────────────────

  Scenario: a server-to-client request is answered, not dropped
    Given the child sends "roots/list" with an id
    When the bridge receives it
    Then it answers with -32601 and names the method
    And the child is not left waiting

  Scenario: a server-to-client notification is logged and dropped
    Given the child sends "notifications/message"
    When the bridge receives it
    Then it is logged
    And no response is written

  Scenario: a non-JSON line does not take the session down
    Given the child prints a banner to stdout
    When the bridge reads it
    Then the line is skipped
    And the next response is still delivered

  # ── the gate still decides who may reach it ──────────────────────────────

  Scenario: an unentitled caller cannot see or invoke stdio
    Given the gate is enabled and the caller presents no capability
    When the caller asks for "tools/list"
    Then no "adhd.stdio.*" tool is visible
    And a call to "adhd.stdio.qdrant.call" is unknown
    And an "unentitled_tool_call" event is filed
    And no process is started

  Scenario: a read or write capability cannot reach stdio
    Given a caller presents the read or write capability
    When the caller asks for "tools/list"
    Then no "adhd.stdio.*" tool is visible

  Scenario: an exec capability can
    Given a caller presents the exec capability
    When the caller asks for "tools/list"
    Then "adhd.stdio.qdrant.call" is visible
