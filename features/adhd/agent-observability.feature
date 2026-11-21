@adhd
@z2-relational
@domain-registry-health
Feature: An Agent Is Watched By Name, Not By Its Wire
  A workspace session is two processes: the surface that answers MCP, and the
  agent the editor launched over stdio. The observer speaks MCP over HTTP, so it
  can watch the surface but not the process that drives it — and the two can be
  wrong about different things. A surface can answer after the agent has died,
  and a live agent can be attached to a session the surface has already lost.

  There is no ACP/stdio probe in adhd, and this feature records that decision
  rather than its absence. A stdio process's pipes belong to whoever spawned it:
  attach is impossible, so a "stdio probe" could only spawn a *second* agent and
  report on that one — not the process the editor is driving. A green light
  about a stranger is worse than no light, so the probe is refused rather than
  approximated.

  The shape that works reuses the registry contract unchanged. The agent serves
  the session's surface in its own process and registers under a name of its own
  — `workspace-agent-42`, distinct from the session's `workspace-42` — so the
  same name resolution that watches every other surface watches the agent, and
  the surface it answers on dies with it. Two processes, two names, two lights.

  # Dependencies:
  #   lab-safety lab/agent/README.md — the agent's -addr / -smoke-alarm flags
  #   features/adhd/registry-endpoint-resolution.feature — how a name resolves
  #   adhd#63, adhd#62, lab-safety#37

  Background:
    Given adhd is configured with a smoke_alarm entry that answers GET /isotope/list
    And that alarm is the registry a binaries[].endpoint name resolves through

  # ── two processes, two lights ──────────────────────────────────────────────

  Scenario: the agent's own surface is a light of its own
    Given binaries holds "workspace" at "workspace-42"
    And binaries holds "workspace-agent" at "workspace-agent-42"
    And the registry holds both names
    When the local probe runs
    Then there are two lights, not one
    And both are green while both processes answer

  Scenario: the agent dies while the surface keeps answering
    Given both lights are green
    When the agent process exits and "workspace-agent-42" leaves the registry
    Then "workspace-agent" is red on the next tick
    And its details name the isotope that is gone
    And "workspace" stays green, because it is another process

  Scenario: the surface loses its session while the agent is still attached
    Given the session surface answers the handshake with -32001 "no session here"
    And the agent's own surface still answers
    When the local probe runs
    Then "workspace" is red with the surface's own session verdict
    And "workspace-agent" is green: the process the editor launched is alive

  # ── what adhd deliberately does not do ─────────────────────────────────────

  Scenario: a stdio-only agent is never a green light
    Given a workspace-agent runs over stdio with no HTTP surface
    And nothing registers a name for it
    When adhd is configured with a binaries entry naming it
    Then the light is red, naming the missing isotope
    And adhd does not spawn the agent to probe it

  Scenario: an endpoint is never a command
    Given a binaries[].endpoint that is a path rather than a URL or a name
    When the local probe runs
    Then it is probed as a URL and fails as one
    And no process is started from the endpoint
