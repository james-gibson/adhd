@adhd
@z0-physical
@domain-registry-health
Feature: Registry-Resolved Binary Endpoints
  A watched binary is addressed either by a URL or by a registry name. A name is
  resolved through the configured alarm's /isotope/list on every probe, so a
  surface that binds an ephemeral port and publishes itself by name can be
  watched without any harness injecting its address (adhd#62).

  Why a name and not a port: a workspace session's surface binds 127.0.0.1:0 and
  is published by name in the isotope registry, renewed every minute. Its address
  is not knowable in advance, and pinning it re-introduces the "remember the
  port" coupling the registry removed.

  Background:
    Given adhd is configured with a smoke_alarm entry that answers GET /isotope/list
    And that alarm is the registry a binaries[].endpoint name resolves through

  # ── what an endpoint may be ─────────────────────────────────────────────────

  Scenario: a URL endpoint is probed as given
    Given features.binaries[0].endpoint is "http://127.0.0.1:9090/mcp"
    When the local probe runs
    Then it POSTs the MCP handshake to that URL
    And the registry is not consulted

  Scenario: a bare name is a registry lookup, not a URL
    Given features.binaries[0].endpoint is "workspace-42"
    And the registry holds "workspace-42" at "127.0.0.1:64194"
    When the local probe runs
    Then the name resolves to "http://127.0.0.1:64194/mcp"
    And the handshake is sent there

  Scenario: a mistyped URL is not silently read as a name
    Given features.binaries[0].endpoint is "localhost:9090"
    When the local probe runs
    Then the endpoint is treated as a URL and fails as one
    And the light does not claim the name "localhost" is unregistered

  # ── resolution is a heartbeat, not a memo ──────────────────────────────────

  Scenario: a restarted surface is followed on the next tick
    Given the registry holds "workspace-42" at port 64194
    And the probe against 64194 is green
    When the session restarts on port 64210 and re-registers under the same name
    Then the next tick resolves "workspace-42" to 64210
    And the light stays green without a config change or a restart of adhd

  # ── the colour is the local observer's verdict ─────────────────────────────

  Scenario: a name the registry does not hold is red
    Given the registry answers and holds no isotope named "workspace-42"
    When the local probe runs
    Then the light is red
    And its details name the missing isotope
    And the alarm's reachability is not treated as a disagreement

  Scenario: an alarm that is not answering is yellow, not red
    Given the registry does not answer
    When the local probe runs
    Then the light is yellow
    And its details say the name could not be resolved
    And the surface is not reported as gone

  Scenario: a session-less surface reads as gone, not as a dead process
    Given the name resolves to a surface that is still listening
    And the surface answers the handshake with -32001 "no session here"
    When the local probe runs
    Then the light is red
    And the details carry the surface's own session verdict

  # ── where the observer runs ────────────────────────────────────────────────

  Scenario: headless mode starts the local observer
    Given adhd is started with --headless and a named binary endpoint
    When the session is not registered yet
    Then the feature light is red, not the green it was initialised with
    When the session registers under that name
    Then the feature light turns green
    And the local/remote rung exists for a deployment that has no TUI

  # ── the packed answer ──────────────────────────────────────────────────────

  Scenario: the registry is the alarm that is already configured
    Given a smoke_alarm entry is configured
    When a binaries[].endpoint name is resolved
    Then the registry is that alarm, not a second configured source
    And a name and the alarm's own target view cannot drift onto different registries
