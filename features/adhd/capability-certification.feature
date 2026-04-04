@adhd
@z0-physical
@domain-certification
Feature: 42i Capability Certification via Domain-Targeted Feature Lights
  Each Gherkin @domain-* feature file has a corresponding runtime capability
  verifier. When a capability is exercised at runtime, feature lights for
  that domain go green (or red), providing live 42i certification evidence.

  This is distinct from aggregate cluster health: a domain light turning green
  means the specific capability named by that domain was observed working —
  not merely that the cluster is reachable. Structural presence alone (e.g.
  a target ID appearing in a smoke-alarm response) is sufficient evidence.

  # ── CapabilityVerifiedMsg ─────────────────────────────────────────────────

  Scenario: CapabilityVerifiedMsg drives all feature lights for its domain
    Given feature lights exist for the domain "discovery"
    When a CapabilityVerifiedMsg arrives with domain="discovery" and status="green"
    Then all feature lights whose SourceMeta["domain"] == "discovery" are set to "green"
    And feature lights in other domains are unchanged

  Scenario: CapabilityVerifiedMsg with status red marks domain lights red
    Given feature lights exist for the domain "smoke-alarm-network"
    When a CapabilityVerifiedMsg arrives with domain="smoke-alarm-network" and status="red"
    Then all feature lights for that domain are set to "red"

  Scenario: once a domain is capability-verified, aggregate health does not override it
    Given a CapabilityVerifiedMsg has set domain "discovery" to "green"
    When applyClusterHealthToFeatures runs with worst status "red"
    Then feature lights for domain "discovery" remain "green"
    And feature lights with no domain or an unverified domain are set to "red"

  # ── trivially-verified domains at Init ────────────────────────────────────
  # Some capabilities are proven the moment the TUI starts. Init() emits
  # CapabilityVerifiedMsg for these without waiting for any network event.

  Scenario: dashboard domain is certified green when the TUI starts
    When the BubbleTeaDashboard initialises
    Then a CapabilityVerifiedMsg is emitted with domain="dashboard" and status="green"
    And its details field contains "TUI running"

  Scenario: lights domain is certified green when any lights are configured
    Given the dashboard has at least one configured light
    When the BubbleTeaDashboard initialises
    Then a CapabilityVerifiedMsg is emitted with domain="lights" and status="green"
    And its details field contains the light count

  Scenario: mcp-server domain is certified green when the MCP server starts
    Given the config has mcp_server.enabled=true
    When the BubbleTeaDashboard initialises
    Then a CapabilityVerifiedMsg is emitted with domain="mcp-server" and status="green"

  Scenario: lights domain is not emitted when no lights are configured
    Given the dashboard has zero configured lights
    When the BubbleTeaDashboard initialises
    Then no CapabilityVerifiedMsg is emitted for domain "lights"

  # ── MarkPreVerified ────────────────────────────────────────────────────────
  # Callers that already know a domain is verified before Run() can queue
  # CapabilityVerifiedMsgs to be emitted at Init() time.

  Scenario: MarkPreVerified queues a message that is emitted during Init
    Given the caller invokes MarkPreVerified with domain="demo" and status="green"
    When the BubbleTeaDashboard initialises
    Then a CapabilityVerifiedMsg is emitted with domain="demo" and status="green"
    And the "demo" domain feature lights are set to "green"

  Scenario: multiple pre-verified domains are all emitted during Init
    Given the caller queues pre-verified messages for "demo" and "headless"
    When the BubbleTeaDashboard initialises
    Then CapabilityVerifiedMsgs are emitted for both "demo" and "headless"

  # ── domain stored in SourceMeta ────────────────────────────────────────────
  # Feature lights must carry SourceMeta["domain"] derived from the @domain-*
  # tag of their Gherkin file so applyCapabilityToFeatures can target them.

  Scenario: feature light loaded from a Gherkin file stores its domain tag
    Given a Gherkin feature file tagged @domain-discovery
    When the feature loader creates a light for that feature
    Then the light's SourceMeta["domain"] equals "discovery"

  Scenario: feature light with no domain tag has SourceMeta["domain"] empty
    Given a Gherkin feature file with no @domain-* tag
    When the feature loader creates a light for that feature
    Then the light's SourceMeta["domain"] is empty or absent
    And the light is eligible for aggregate health fallback

  # ── structural evidence from smoke-alarm targets ───────────────────────────
  # certifyFromSmokeTarget() reads the source name and target ID from each
  # smoke-alarm status response and derives capability certifications from
  # their structure — independently of whether the target is healthy.

  Scenario: a "peer" target in a smoke-alarm response certifies @domain-discovery
    Given a smoke-alarm status response contains a target with ID "peer"
    When ADHD processes the smokelink.LightUpdate for that target
    Then a CapabilityVerifiedMsg is applied with domain="discovery" and status="green"
    And the details field notes "inter-peer monitoring active"
    And the certification occurs regardless of the target's health status

  Scenario: a "self-health-check" target certifies @domain-smoke-alarm-network
    Given a smoke-alarm status response contains a target with ID "self-health-check"
    When ADHD processes the smokelink.LightUpdate for that target
    Then a CapabilityVerifiedMsg is applied with domain="smoke-alarm-network" and status="green"
    And the details field notes "smoke-alarm self-monitoring active"

  Scenario: a source name containing "demo" certifies @domain-demo
    Given a smoke-alarm endpoint is named "demo-65552/alarm-b"
    When any smokelink.LightUpdate arrives from that endpoint
    Then a CapabilityVerifiedMsg is applied with domain="demo" and status="green"
    And the details field notes the endpoint name

  Scenario: structural certification fires even when the target status is red
    Given a smoke-alarm response has a target "peer" with state "outage"
    When ADHD processes the LightUpdate
    Then the @domain-discovery feature lights are set to "green"
    And the smoke:*/peer light itself is set to "red"
    And the two states are independent

  # ── aggregate health fallback ──────────────────────────────────────────────

  Scenario: feature lights without a domain verifier follow aggregate cluster health
    Given feature lights exist with SourceMeta["domain"] = "" (no domain)
    And the cluster has all smoke: lights green
    When applyClusterHealthToFeatures runs
    Then the undeclared-domain feature lights are set to "green"

  Scenario: aggregate health does not touch lights whose domain has been certified
    Given domain "discovery" has been certified via CapabilityVerifiedMsg
    And domain "demo" has been certified via CapabilityVerifiedMsg
    When applyClusterHealthToFeatures runs with any status
    Then feature lights for "discovery" and "demo" are not modified
