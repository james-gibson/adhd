@adhd @z0-physical @domain-headless
Feature: ADHD Headless Mode
  adhd --headless runs without a TUI: it starts the MCP server, polls
  smoke-alarm endpoints, logs all MCP traffic as JSONL, and registers
  itself as an isotope with the configured smoke-alarm instance.
  Headless mode is the deployment shape used by lezz demo — each demo
  cluster includes one headless adhd that serves MCP tools to the network
  while the user optionally runs a separate TUI adhd to visualize it.

  Background:
    Given adhd is started with --headless
  # ── startup ───────────────────────────────────────────────────────────────────

  Scenario: headless mode starts the MCP server on the configured address
    Given the config has mcp_server.enabled=true and addr=":9090"
    When adhd starts with --headless
    Then the MCP server binds to port 9090
    And GET /mcp returns an SSE stream
    And POST /mcp accepts JSON-RPC requests

  Scenario: headless mode starts with --mcp-addr overriding the config address
    Given the config has mcp_server.addr=":9090"
    And adhd is started with --headless --mcp-addr=":0"
    When adhd starts
    Then the MCP server binds to an OS-assigned port
    And the config address is ignored

  Scenario: headless mode logs JSONL to file when --log is specified
    Given adhd is started with --headless --log=/tmp/adhd.log
    When an MCP request is processed
    Then a JSONL entry is appended to /tmp/adhd.log
    And the entry contains timestamp, type="request", and method fields
    And no MCP payload data appears in the log fields

  Scenario: headless mode logs JSONL to stdout when --log is not specified
    Given adhd is started with --headless without --log
    When an MCP request is processed
    Then a JSONL entry is written to stdout
    And stderr is used only for structured slog output
  # ── smoke-alarm integration ───────────────────────────────────────────────────

  Scenario: headless mode polls smoke-alarm endpoints from config
    Given the config has two smoke_alarm entries with interval=5s
    When adhd starts with --headless
    Then both smoke-alarm endpoints are polled every 5 seconds
    And light states are updated from each /status response

  Scenario: headless mode registers with smoke-alarm as isotope when --smoke-alarm is provided
    Given adhd is started with --headless --smoke-alarm=http://localhost:8080
    When adhd starts
    Then adhd POSTs to http://localhost:8080/isotope/register
    And the smoke-alarm's isotope list includes adhd

  Scenario: headless mode logs a warning and continues when isotope registration fails
    Given --smoke-alarm points to an unreachable URL
    When adhd starts with --headless
    Then a WARN entry is logged: "failed to register as isotope"
    And adhd continues running
    And the MCP server is still available
  # ── feature loading ───────────────────────────────────────────────────────────
  # Headless mode loads features from the local filesystem relative to the
  # current working directory, exactly as TUI mode does. When the working
  # directory does not contain a features/adhd/ path, no features are loaded
  # and the MCP tools/list response returns an empty features list.
  # This is expected behaviour in lezz demo, where the headless adhd process
  # inherits lezz demo's working directory (not the adhd source tree).

  Scenario: headless mode loads features from local features/adhd/ when present
    Given the current working directory contains features/adhd/ with 40 files
    When adhd starts with --headless
    Then the cluster has 40 feature lights
    And adhd.features.list MCP tool returns 40 entries

  Scenario: headless mode starts with zero features when local feature path is absent
    Given the current working directory does not contain features/adhd/
    When adhd starts with --headless
    Then no feature lights are created
    And adhd.features.list MCP tool returns an empty list
    And no error is raised for the missing path
  # ── shutdown ──────────────────────────────────────────────────────────────────

  Scenario: headless mode shuts down cleanly on SIGTERM
    Given adhd is running with --headless
    When SIGTERM is delivered
    Then the MCP server stops accepting new connections
    And any in-flight requests are completed before exit
    And the log file is flushed and closed
    And the process exits with status 0

  Scenario: headless mode shuts down cleanly on SIGINT
    Given adhd is running with --headless
    When SIGINT is delivered
    Then the shutdown sequence completes without error
  # ── lezz demo topology ────────────────────────────────────────────────────────
  # In a lezz demo cluster, headless adhd and TUI adhd are separate processes.
  # The headless instance serves MCP tools; the TUI instance visualizes health.
  # They share no state — each reads from its own smoke-alarm config.

  Scenario: headless adhd and TUI adhd connect to the same smoke-alarm cluster independently
    Given lezz demo is running with headless adhd on :9090 and alarm-a on :8001
    And a user runs a TUI adhd with --config pointing to alarm-a and alarm-b
    When both adhd instances poll alarm-a /status
    Then each receives the same health state
    And their light clusters are independently consistent
    And neither instance depends on the other to function

  Scenario: TUI adhd --demo connects to the cluster without knowledge of the headless adhd
    Given lezz demo is running and the registry at localhost:19100 has cluster data
    When the user runs adhd --demo
    Then adhd builds a config from alarm_a and alarm_b URLs only
    And adhd_mcp URL is not used by the TUI adhd
    And the TUI instance operates independently of the headless instance
  # ── TUI certification of headless via isotope probe ───────────────────────
  # The TUI dashboard probes the smoke-alarm's /isotope endpoint to observe
  # whether headless adhd has registered. A non-empty response is structural
  # evidence that the headless capability is implemented and running.
  # This is the 42i inter-peer certification step: one peer leaves evidence
  # (isotope registration) that the other peer observes (isotope probe).

  Scenario: TUI adhd probes smoke-alarm /isotope endpoint to certify headless capability
    Given TUI adhd is configured with at least one smoke-alarm endpoint
    And headless adhd has registered as an isotope with that smoke-alarm
    When the BubbleTeaDashboard initialises and 6 seconds elapse
    Then adhd sends GET /isotope to the first configured smoke-alarm endpoint
    And the response contains at least one registered isotope
    Then a CapabilityVerifiedMsg is applied with domain="headless" and status="green"
    And the @domain-headless feature lights are set to "green"

  Scenario: headless domain stays dark when no isotopes are registered
    Given TUI adhd is configured with a smoke-alarm endpoint
    And no isotopes are registered with that smoke-alarm
    When the isotope probe runs
    Then no CapabilityVerifiedMsg is emitted for domain "headless"
    And @domain-headless feature lights remain "dark"

  Scenario: headless isotope probe does not fail when smoke-alarm is unreachable
    Given the configured smoke-alarm endpoint is unreachable
    When the isotope probe runs
    Then no error is raised
    And @domain-headless feature lights remain "dark"
    And ADHD continues running normally
