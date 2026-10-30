@adhd
@z3-epistemic
@domain-surface-gate
Feature: The Inbound Entitlement Gate
  adhd's MCP surface is a control plane, not a status page. Before the gate,
  "reachable ⇒ driveable" was the actual contract: dispatch checked the method
  and nothing else, tools/list returned the real surface to any caller, and
  driving adhd.gh.run or adhd.proxy filed no alarm (adhd#27).

  The intended posture is a facade. An unentitled caller sees an empty adhd —
  no tools, no peers, no lights — and the attempt to drive the real surface is
  filed as unentitled_tool_call. Entitlement is a capability whose key carries a
  maximum rung, using kb's shield-gate contract so one client addresses adhd and
  a KB scope the same way:

    R/C -> 0     W -> 3     E -> 4     X -> 6

  Detection and authorization stay separate. The capability authorizes; the
  knock and the honeypot only report. A refusal is shaped as "method not found",
  never 401/403, so a probe learns neither that the gate exists nor that the
  tool it guessed at does.

  Background:
    Given adhd's MCP server is running with mcp_server.gate.enabled = true
    And the keyring holds read (0), write (3) and exec (6) capabilities

  # ── the empty facade ──────────────────────────────────────────────────────

  Scenario: an unentitled tools/list is empty
    Given a caller presents no capability
    When the caller asks for "tools/list"
    Then the tool list is empty
    And the response is not a 401

  Scenario: an unentitled status is an empty cluster
    Given a caller presents no capability
    When the caller asks for "adhd.status"
    Then the response has no cluster peers
    And the response has zero lights
    And the response has zero dynamic tools
    And the response carries no isotope

  Scenario: an unentitled lights list is empty
    Given a caller presents no capability
    When the caller asks for "adhd.lights.list"
    Then the light list is empty

  Scenario: an unentitled initialize still connects
    Given a caller presents no capability
    When the caller sends "initialize"
    Then the handshake answers the protocol version
    And the caller is connected to an empty surface, not refused

  # ── a refusal files ──────────────────────────────────────────────────────

  Scenario: an unentitled tool call is unknown and files an alarm
    Given a caller presents no capability
    When the caller calls "tools/call" for "adhd.gh.run"
    Then the call answers "unknown tool"
    And no command is executed
    And an "unentitled_tool_call" event is filed
    And a red "security/unentitled_tool_call" light is raised

  Scenario: an unentitled direct method call files an alarm
    Given a caller presents no capability
    When the caller invokes "adhd.proxy"
    Then the call answers "Method not found"
    And an "unentitled_tool_call" event is filed naming "adhd.proxy"
    And no proxy request is made

  Scenario: a read probe is quiet
    Given a caller presents no capability
    When the caller asks for "adhd.status"
    And the caller calls "tools/call" for "adhd.status"
    And the caller challenges "adhd.rung.respond"
    Then the empty facade answers
    And no security event is filed
    # The alarm is for driving the surface, not for a monitor reading it. Every
    # probe filing would bury the signal that matters under a monitor's polling.

  Scenario: guessing an unknown method files nothing
    Given a caller presents no capability
    When the caller invokes "not.a.real.method"
    Then the call answers "Method not found"
    And no security event is filed

  # ── capabilities are tiers, not a flag ────────────────────────────────────

  Scenario: a read capability sees only the read surface
    Given a caller presents the read capability
    When the caller asks for "tools/list"
    Then "adhd.status" is present
    And "adhd.proxy" is absent
    And "adhd.gh.run" is absent
    And "adhd.cluster.join" is absent

  Scenario: a write capability adds cluster membership but not exec
    Given a caller presents the write capability
    When the caller asks for "tools/list"
    Then "adhd.cluster.join" is present
    And "adhd.proxy" is absent
    And "adhd.path.call" is absent
    And "adhd.gh.run" is absent

  Scenario: an exec capability sees the proxy and the paths
    Given a caller presents the exec capability
    When the caller asks for "tools/list"
    Then "adhd.proxy" is present
    And "adhd.path.call" is present

  Scenario: a read capability cannot invoke an exec tool
    Given a caller presents the read capability
    When the caller calls "tools/call" for "adhd.gh.run"
    Then the call answers "unknown tool"
    And an "unentitled_tool_call" event is filed
    And the event records the required capability

  Scenario: a claimed rung above the key's is capped
    Given the write capability signs a request claiming rung 6
    When the caller asks for "tools/list"
    Then the tool list is the write surface
    And "adhd.gh.run" is absent

  # ── the signature is bound, not a password ────────────────────────────────

  Scenario: a signature covers the method and the params
    Given a caller signs "tools/call" for "adhd.status"
    When the payload is changed to "adhd.gh.run"
    Then the signature no longer verifies
    And the caller sees the empty facade

  Scenario: a stale signature is refused
    Given a caller signs a request with a timestamp three minutes old
    When the caller sends it
    Then the caller sees the empty facade

  Scenario: a replayed nonce is refused
    Given a caller signs a request once
    When the same request is sent twice
    Then the first is served
    And the second sees the empty facade

  Scenario: a stale tumble epoch is refused
    Given a caller signs for an epoch two periods old
    When the caller sends it
    Then the caller sees the empty facade

  Scenario: the previous tumble epoch is accepted
    Given a caller signs for the epoch before the current one
    When the caller sends it
    Then the caller is entitled

  Scenario: a bearer capability is accepted
    Given a caller presents "Authorization: Bearer <kid>.<secret>"
    When the caller asks for "tools/list"
    Then the caller sees the surface its key's rung allows

  # ── negotiation stays public; content does not ────────────────────────────

  Scenario: an unentitled peer can still challenge the isotope
    Given a peer presents no capability
    When the peer calls "adhd.rung.respond" with a feature and nonce
    Then the response carries a receipt
    And the peer still sees an empty tools/list afterwards

  Scenario: a gate that is off leaves the surface open
    Given mcp_server.gate.enabled is false
    When any caller asks for "tools/list"
    Then the whole surface is returned
    And the behaviour matches every build before the gate existed

  # ── the keyring is local and owner-only ───────────────────────────────────

  Scenario: a first start with no keyring creates one
    Given the gate is enabled and the keyring file does not exist
    When adhd starts
    Then a keyring is written 0600 in a 0700 directory
    And the log names its path
    And the keys are read (0), write (3) and exec (6)

  Scenario: an unreadable keyring fails closed
    Given the gate is enabled and the keyring file is corrupt
    When adhd starts
    Then startup fails with the keyring's error
    And the surface is never served ungated
