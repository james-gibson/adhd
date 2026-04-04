@adhd
@z0-physical
@domain-demo
Feature: ADHD Demo Mode Cluster Discovery
  adhd --demo discovers a running lezz demo cluster and builds its config
  from the cluster registry without requiring a hand-written config file.
  Discovery races two strategies: mDNS (_lezz-demo._tcp) and a direct HTTP
  poll of the well-known lezz demo registry at localhost:19100/cluster.
  Whichever responds first wins; the other is discarded.

  Background:
    Given lezz demo is running on the local machine

  # ── cluster discovery ────────────────────────────────────────────────────────

  Scenario: adhd discovers a cluster via the localhost HTTP registry
    Given the lezz demo registry is reachable at http://127.0.0.1:19100/cluster
    And the registry contains one cluster with alarm_a and alarm_b URLs
    When adhd starts with --demo
    Then adhd builds a config with two smoke_alarm endpoints
    And the endpoints match the alarm_a and alarm_b URLs from the cluster

  Scenario: adhd discovers a cluster via mDNS when the registry is unreachable
    Given the lezz demo registry is not reachable at localhost:19100
    And a _lezz-demo._tcp mDNS record is advertising on the LAN
    When adhd starts with --demo
    Then adhd resolves the host from the mDNS record
    And fetches the cluster registry from that host on port 19100
    And builds a config from the returned cluster data

  Scenario: adhd uses whichever discovery strategy responds first
    Given both mDNS and the localhost registry are available
    When adhd starts with --demo
    Then adhd returns cluster data from the faster strategy
    And does not wait for the slower strategy to complete

  Scenario: adhd fails gracefully when no lezz demo is running
    Given neither mDNS nor localhost:19100 returns a cluster within 10 seconds
    When adhd starts with --demo
    Then adhd prints "demo discovery failed: no lezz demo found on the LAN within 10s"
    And exits with a non-zero status

  # ── config construction ───────────────────────────────────────────────────────

  Scenario: cluster with one entry creates two smoke_alarm endpoints
    Given the cluster registry returns a single cluster "demo-001"
    With alarm_a "http://192.168.1.10:9001" and alarm_b "http://192.168.1.10:9002"
    When adhd builds the config via ConfigFromClusters
    Then the config has exactly two smoke_alarm entries
    And the first entry has name "alarm-a" and endpoint "http://192.168.1.10:9001"
    And the second entry has name "alarm-b" and endpoint "http://192.168.1.10:9002"

  Scenario: cluster with multiple entries prefixes names with cluster name
    Given the cluster registry returns two clusters "demo-001" and "demo-002"
    When adhd builds the config via ConfigFromClusters
    Then smoke_alarm entry names are prefixed with their cluster name
    And the names follow the pattern "<cluster-name>/alarm-a" and "<cluster-name>/alarm-b"

  # ── feature discovery in demo mode ───────────────────────────────────────────
  # Features are always loaded from the local filesystem, not from the remote
  # cluster. This is a deliberate design constraint: each adhd instance serves
  # the features it was deployed with, not remote features from other nodes.

  Scenario: adhd in --demo mode loads features from the local filesystem
    Given a local features/adhd/ directory exists with 40 feature files
    When adhd starts with --demo
    Then 40 features are loaded from the local path
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
