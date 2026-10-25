@adhd @z0-physical @domain-demo
Feature: ADHD Demo Mode Cluster Discovery
  adhd --demo discovers a running lezz demo cluster and builds its config
  from the cluster registry without requiring a hand-written config file.
  Discovery races two strategies: mDNS (_lezz-demo._tcp) and a direct HTTP
  poll of the well-known lezz demo registry at localhost:19100/cluster.
  Whichever responds with a usable registry first wins; the other is discarded.

  The mDNS strategy follows the registry's own port from the SRV record, so a
  registry that is not on the well-known port is still found; 19100 is only the
  fallback for a record that omits the port.

  The localhost poll is a fallback with no advertiser vouching for it, so it
  trusts a registry only while one of the cluster's alarms answers a health
  probe. A registry process that outlives its cluster — a ghost — would
  otherwise produce lights that can never go green, making adhd look broken
  when the real cause is a stale registry entry. An mDNS answer needs no such
  proof: an active advertisement is itself evidence the advertiser is running.

  Background:
    Given lezz demo is running on the local machine
  # ── cluster discovery ────────────────────────────────────────────────────────

  Scenario: adhd discovers a cluster via the localhost HTTP registry
    Given the lezz demo registry is reachable at http://localhost:19100/cluster
    And the registry contains one cluster with alarm_a and alarm_b URLs
    And at least one of its alarms answers a health probe
    When adhd starts with --demo
    Then adhd builds a config with two smoke_alarm endpoints
    And the endpoints match the alarm_a and alarm_b URLs from the cluster

  Scenario: adhd discovers a cluster via mDNS when the registry is unreachable
    Given the lezz demo registry is not reachable at localhost:19100
    And a _lezz-demo._tcp mDNS record is advertising on the LAN
    When adhd starts with --demo
    Then adhd resolves the host from the mDNS record
    And fetches the cluster registry from that host on the port advertised in the SRV record
    And falls back to port 19100 when the SRV record carries no port
    And builds a config from the returned cluster data

  Scenario: adhd uses whichever discovery strategy responds first
    Given both mDNS and the localhost registry are available
    And the localhost registry's cluster is alive
    When adhd starts with --demo
    Then adhd returns cluster data from the faster strategy
    And does not wait for the slower strategy to complete

  Scenario: adhd fails gracefully when no lezz demo is running
    Given neither mDNS nor localhost:19100 returns a cluster within 10 seconds
    When adhd starts with --demo
    Then adhd prints "demo discovery failed: no lezz demo found on the LAN within 10s"
    And exits with a non-zero status

  Scenario: adhd ignores a ghost local registry whose cluster is gone
    Given the lezz demo registry is reachable at http://localhost:19100/cluster
    And its cluster advertises alarm URLs that no longer answer
    And no _lezz-demo._tcp mDNS record is advertising
    When adhd starts with --demo
    Then adhd does not build a config from the ghost registry
    And discovery fails within the timeout
    And the failure names the stale registry entry
  # ── config construction ───────────────────────────────────────────────────────

  Scenario: cluster with one entry creates two smoke_alarm endpoints
    Given the cluster registry returns a single cluster "demo-001"
    When adhd builds the config via ConfigFromClusters
    Then the config has exactly two smoke_alarm entries
    And the first entry has name "alarm-a" and endpoint "http://192.168.1.10:9001"
    And the second entry has name "alarm-b" and endpoint "http://192.168.1.10:9002"

  Scenario: cluster with multiple entries prefixes names with cluster name
    Given the cluster registry returns two clusters "demo-001" and "demo-002"
    When adhd builds the config via ConfigFromClusters
    Then smoke_alarm entry names are prefixed with their cluster name
    And the names follow the pattern "<cluster-name>/alarm-a" and "<cluster-name>/alarm-b"
  # ── project context propagation ──────────────────────────────────────────────
  # A cluster advertises the projects whose MCP servers it fronts. Those entries
  # have no representation in smoke_alarm, so ConfigFromClusters carries them in
  # discovered_clusters; dropping them silently drops the proxy tools.

  Scenario: projects advertised by a discovered cluster become proxy tools
    Given the cluster registry returns a cluster "demo-001" with an adhd_mcp endpoint
    And that cluster advertises a project "tuner" with mcp_url "http://192.168.1.10:9200/mcp"
    When adhd builds the config via ConfigFromClusters
    Then the config carries the cluster's adhd_mcp endpoint
    And the config carries the project "tuner" with its mcp_url intact
    And adhd.project.tuner.call is registered on the hosted MCP server
    And the tool description names the project and the cluster

  Scenario: a project with a seed id is namespaced by that id, not the display name
    Given a discovered cluster advertises a project with id "01JABCDEF" and name "tuner"
    When adhd builds the config via ConfigFromClusters
    Then the project keeps id "01JABCDEF" as its tool namespace
    And the proxy tool is named adhd.project.01JABCDEF.call
    And the tool description contains "tuner"

  Scenario: the same seed id on a new endpoint replaces the project's proxy tool
    Given a discovered cluster advertises project id "01JABCDEF" at "http://192.168.1.10:9200/mcp"
    When the same cluster is discovered again with id "01JABCDEF" at "http://192.168.1.10:9400/mcp"
    Then adhd.project.01JABCDEF.call remains registered exactly once
    And it proxies to "http://192.168.1.10:9400/mcp"

  Scenario: a discovered cluster with no projects still contributes its MCP peer
    Given the cluster registry returns a cluster "demo-001" with an adhd_mcp endpoint and no projects
    When adhd builds the config via ConfigFromClusters
    Then the config carries the cluster's adhd_mcp endpoint
    And no project proxy tools are registered
    And the cluster is addressable by path negotiation

  Scenario: a cluster that joins the registry after startup still gets its projects
    Given adhd started with --demo and is polling the lezz demo registry
    And a new cluster joins the registry advertising a project "tuner"
    When the registry poll returns the new cluster
    Then adhd.project.tuner.call is registered without a restart

  Scenario: a project advertised without an mcp_url is not registered
    Given a discovered cluster advertises a project with an empty mcp_url
    When the hosted MCP server wires the discovered clusters
    Then no adhd.project tool is registered for that entry
  # ── feature discovery in demo mode ───────────────────────────────────────────
  # Features are always loaded from the local filesystem, not from the remote
  # cluster. This is a deliberate design constraint: each adhd instance serves
  # the features it was deployed with, not remote features from other nodes.

  Scenario: adhd in --demo mode loads features from the local filesystem
    Given a local features/adhd/ directory exists with feature files
    When adhd starts with --demo
    Then all feature files from the local path are loaded
    And no features are fetched from the remote cluster nodes

  Scenario: adhd in --demo mode shows no features when local path is absent
    Given no features/adhd/ directory exists in the current working directory
    When adhd starts with --demo
    Then zero feature lights are created
    And no error is raised for missing feature paths
    And the dashboard still connects to the smoke_alarm endpoints from the cluster
  # ── stable config file ────────────────────────────────────────────────────────

  Scenario: lezz demo writes a stable config to ~/.lezz/demo-adhd.yaml
    When lezz demo starts
    Then the file ~/.lezz/demo-adhd.yaml is created
    And it contains the smoke_alarm endpoints for the running cluster
    And running "adhd --config ~/.lezz/demo-adhd.yaml" connects to the same cluster as "--demo"

  Scenario: stable config is removed when lezz demo exits
    Given lezz demo is running and ~/.lezz/demo-adhd.yaml exists
    When lezz demo receives SIGINT
    Then ~/.lezz/demo-adhd.yaml is deleted
    And subsequent "adhd --demo" invocations will not see the old cluster
  # ── 42i demo capability certification ────────────────────────────────────
  # @domain-demo feature lights are certified green via two mechanisms:
  #
  # 1. MarkPreVerified() — when adhd --demo discovers the cluster at startup,
  #    main.go queues a CapabilityVerifiedMsg before Run() so the lights go
  #    green at Init() time without waiting for a smoke-alarm poll.
  #
  # 2. certifyFromSmokeTarget() — any smoke: light whose source name contains
  #    "demo" (e.g. "demo-65552/alarm-b") triggers demo certification. This
  #    catches the case where the config was loaded from a stable config file
  #    rather than via --demo, but the endpoints still have "demo-" names.

  Scenario: adhd --demo pre-certifies @domain-demo before any smoke-alarm poll
    Given adhd discovers a lezz demo cluster via Browse()
    When the BubbleTeaDashboard is created and Run() is called
    Then a CapabilityVerifiedMsg with domain="demo" and status="green" is emitted at Init()
    And @domain-demo feature lights are green before the first smoke-alarm poll completes

  Scenario: structural evidence from a "demo-*" endpoint name certifies @domain-demo
    Given adhd is configured with a smoke-alarm endpoint named "demo-65552/alarm-a"
    And adhd was NOT started with --demo (e.g. loaded a stable config file)
    When any LightUpdate arrives from the "demo-65552/alarm-a" endpoint
    Then a CapabilityVerifiedMsg is applied with domain="demo" and status="green"
    And @domain-demo feature lights are set to "green"
