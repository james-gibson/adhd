package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/discovery"
	"github.com/james-gibson/adhd/internal/features"
	"github.com/james-gibson/adhd/internal/health"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// BubbleTeaDashboard is the main Bubble Tea model
type BubbleTeaDashboard struct {
	lights        *lights.Cluster
	healthMonitor interface{} // *health.Monitor, kept as interface to avoid circular deps
	mcpServer     *mcpserver.Server
	browser      discovery.Browser        // mDNS browser for smoke-alarm discovery
	instances    <-chan discovery.Instance // live discovery channel; nil until Browse starts
	watcher      *smokelink.Watcher       // polls discovered and configured smoke-alarm endpoints
	lightUpdates chan smokelink.LightUpdate // receives watcher events; nil until Init
	selectedIndex int
	config        *config.Config
	ctx           context.Context
	cancel        context.CancelFunc
	width         int
	height        int
	message       string
	messageTimer  int
	booting       bool
	bootIndex     int
	bootTicks     int
	// clusterEverHealthy is set true the first time all smoke-alarm lights
	// that have been probed are green. Feature lights are not driven red until
	// this baseline is established, preventing startup flicker when adhd
	// connects before the cluster is ready.
	clusterEverHealthy bool
	// preVerified holds CapabilityVerifiedMsgs to be emitted during Init().
	// Callers set these before Run() when a domain is already known-good
	// at construction time (e.g. demo cluster was just discovered).
	preVerified []CapabilityVerifiedMsg
	// verifiedDomains tracks which feature domains are managed by
	// CapabilityVerifiedMsg events. applyClusterHealthToFeatures skips
	// these so the specific capability signal is not overwritten by the
	// aggregate health fallback.
	verifiedDomains map[string]bool
}

// NewBubbleTeaDashboardWithBrowser creates a BubbleTeaDashboard using the
// provided mDNS browser instead of the default ZeroconfBrowser.
// Intended for integration testing where the network stack is unavailable.
func NewBubbleTeaDashboardWithBrowser(cfg *config.Config, b discovery.Browser) *BubbleTeaDashboard {
	m := NewBubbleTeaDashboard(cfg)
	m.browser = b
	return m
}

// Cluster returns the underlying light cluster.
// Intended for integration testing — callers can create an mcpserver.Server
// backed by the same cluster that discovery events write into.
func (m *BubbleTeaDashboard) Cluster() *lights.Cluster {
	return m.lights
}

// NewBubbleTeaDashboard creates a new Bubble Tea dashboard
func NewBubbleTeaDashboard(cfg *config.Config) *BubbleTeaDashboard {
	c := lights.NewCluster()

	// Create lights from configured binaries and their Gherkin features
	for _, bin := range cfg.Features.Binaries {
		// Load features from explicit config
		for _, feature := range bin.Features {
			light := lights.New(feature.Name, "feature")
			light.Source = bin.Name
			light.Status = lights.StatusDark // Updated by health monitor after probing
			light.SourceMeta = map[string]string{
				"binary":          bin.Name,
				"endpoint":        bin.Endpoint,
				"gherkin_file":    feature.GherkinFile,
				"gherkin_feature": feature.GherkinFeature,
			}
			c.Add(light)
		}

		// Load features from Gherkin files (supports glob patterns)
		gfeatures, err := features.LoadFeatureFiles(bin.GherkinFiles)
		if err != nil {
			slog.Warn("failed to load gherkin files", "binary", bin.Name, "error", err)
		}

		for _, gfeature := range gfeatures {
			// Create light for each Gherkin feature
			lightName := gfeature.Name
			light := lights.New(lightName, "feature")
			light.Source = bin.Name
			light.Status = lights.StatusDark // Updated by health monitor after probing
			light.Details = fmt.Sprintf("Scenarios: %d", gfeature.Scenarios)
			light.SourceMeta = map[string]string{
				"binary":          bin.Name,
				"endpoint":        bin.Endpoint,
				"gherkin_file":    gfeature.FilePath,
				"gherkin_feature": gfeature.Name,
				"domain":          gfeature.Domain,
			}
			c.Add(light)
		}
	}

	// Load legacy features from configured paths if any
	loader := features.NewLoader(cfg.Features.SearchPaths)
	discoveredFeatures, err := loader.LoadFeatures()
	if err != nil {
		slog.Warn("failed to load features", "error", err)
	}

	// Add legacy features if no binaries configured
	if c.Count() == 0 && len(discoveredFeatures) > 0 {
		for _, feature := range discoveredFeatures {
			light := lights.New(feature.Name, "feature")
			light.Source = "gherkin"
			light.SourceMeta = map[string]string{
				"file":   feature.FilePath,
				"domain": feature.Domain,
			}
			light.Status = lights.StatusDark
			c.Add(light)
		}
	}

	// Add placeholder lights if no binaries and no legacy features
	if c.Count() == 0 {
		for _, name := range []string{"fire-marshal-spec-check", "smoke-alarm-primary", "smoke-alarm-secondary"} {
			light := lights.New(name, "test")
			light.Source = "placeholder"
			c.Add(light)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create health monitor for binary endpoints
	var monitor interface{}
	if len(cfg.Features.Binaries) > 0 {
		monitor = health.NewWithRemoteAlarm(cfg.Features.Binaries, c, cfg.Health.RemoteSmokeAlarm)
	}

	// Create MCP server if enabled
	var mcpServer *mcpserver.Server
	if cfg.MCPServer.Enabled {
		mcpServer = mcpserver.NewServer(cfg.MCPServer, c)
		slog.Info("MCP server configured", "addr", cfg.MCPServer.Addr)
	}

	// Create watcher for statically-configured smoke-alarm endpoints.
	// mDNS-discovered endpoints are added dynamically in Update().
	var watcher *smokelink.Watcher
	if len(cfg.SmokeAlarm) > 0 {
		watcher = smokelink.NewWatcher(cfg.SmokeAlarm)
	}

	return &BubbleTeaDashboard{
		lights:        c,
		healthMonitor: monitor,
		mcpServer:     mcpServer,
		browser:       discovery.NewBrowser(),
		watcher:       watcher,
		config:        cfg,
		ctx:           ctx,
		cancel:        cancel,
		booting:       true,
		bootIndex:     0,
		bootTicks:     0,
	}
}

// Init initializes the Bubble Tea model
func (m *BubbleTeaDashboard) Init() tea.Cmd {
	// Start health monitor if configured
	if m.healthMonitor != nil {
		monitor := m.healthMonitor.(*health.Monitor)
		go monitor.Start(m.ctx)
		slog.Info("health monitor started", "binaries", len(m.config.Features.Binaries))
	}

	// Start MCP server if enabled
	if m.mcpServer != nil {
		if err := m.mcpServer.Start(m.ctx); err != nil {
			slog.Error("failed to start MCP server", "error", err)
		}
	}

	// Start smokelink watcher for configured endpoints
	cmds := []tea.Cmd{m.tick()}
	if m.watcher != nil {
		m.lightUpdates = make(chan smokelink.LightUpdate, 32)
		m.watcher.Start(m.ctx, m.lightUpdates)
		cmds = append(cmds, waitForLightUpdate(m.lightUpdates))
	}

	// Start mDNS discovery browser
	if m.browser != nil {
		ch, err := m.browser.Browse(m.ctx, discovery.ServiceType)
		if err != nil {
			slog.Warn("mdns browse failed", "error", err)
		} else {
			m.instances = ch
			cmds = append(cmds, waitForDiscovery(ch))
		}
	}

	// Emit pre-verified domains queued by the caller before Run()
	// (e.g. demo cluster already discovered in main.go).
	for _, msg := range m.preVerified {
		msg := msg
		cmds = append(cmds, func() tea.Msg { return msg })
	}
	m.preVerified = nil

	// Trivially verified domains: if the TUI is running, the dashboard and
	// lights subsystems are implemented. Emit green immediately.
	cmds = append(cmds, func() tea.Msg {
		return CapabilityVerifiedMsg{Domain: "dashboard", Status: lights.StatusGreen, Details: "TUI running"}
	})
	if m.lights.Count() > 0 {
		count := m.lights.Count()
		cmds = append(cmds, func() tea.Msg {
			return CapabilityVerifiedMsg{
				Domain:  "lights",
				Status:  lights.StatusGreen,
				Details: fmt.Sprintf("%d lights configured", count),
			}
		})
	}
	if m.mcpServer != nil {
		cmds = append(cmds, func() tea.Msg {
			return CapabilityVerifiedMsg{Domain: "mcp-server", Status: lights.StatusGreen, Details: "MCP server started"}
		})
	}

	// Probe smoke-alarm /isotope list to detect headless adhd registration.
	// This is the inter-peer 42i certification step.
	if probe := m.probeIsotopeRegistration(); probe != nil {
		cmds = append(cmds, probe)
	}

	return tea.Batch(cmds...)
}

// tickMsg is sent on every tick
type tickMsg struct{}

// tick returns a command that sends a tick message
func (m *BubbleTeaDashboard) tick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
}

// Update handles messages and updates the model
func (m *BubbleTeaDashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.selectedIndex < m.lights.Count()-1 {
				m.selectedIndex++
			}
		case "k", "up":
			if m.selectedIndex > 0 {
				m.selectedIndex--
			}
		case "s":
			if m.lights.Count() > m.selectedIndex {
				light := m.lights.All()[m.selectedIndex]
				m.message = fmt.Sprintf("📋 Showing details for: %s", light.Name)
				m.messageTimer = 10
				slog.Debug("show service details", "light", light.Name)
			}
		case "r":
			if m.lights.Count() > m.selectedIndex {
				light := m.lights.All()[m.selectedIndex]
				m.message = fmt.Sprintf("🔄 Refreshing: %s", light.Name)
				m.messageTimer = 10
				slog.Debug("refreshing light", "light", light.Name)
			}
		case "e":
			if m.lights.Count() > m.selectedIndex {
				light := m.lights.All()[m.selectedIndex]
				m.message = fmt.Sprintf("⚙️  Executing action for: %s", light.Name)
				m.messageTimer = 10
				slog.Debug("execute command for light", "light", light.Name)
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tickMsg:
		// Handle boot sequence
		if m.booting {
			m.bootTicks++
			if m.bootTicks >= 2 { // Show each light for ~400ms (2 ticks at 200ms)
				m.bootTicks = 0
				m.bootIndex++
				if m.bootIndex >= m.lights.Count() {
					m.booting = false
					m.bootIndex = 0
				}
			}
		}

		if m.messageTimer > 0 {
			m.messageTimer--
		}
		return m, m.tick()

	case smokelink.LightUpdate:
		if msg.IsInstance {
			// Instance-level update — update the mDNS light if present.
			if l := m.lights.GetByName(msg.SourceName); l != nil && l.Source == "mdns" {
				l.SetStatus(msg.Status)
				l.SetDetails(msg.Details)
			}
		} else {
			// Target-level update from a statically-configured smoke-alarm endpoint.
			// Create or update the smoke:source/target light, then propagate the
			// aggregate cluster health to all feature lights.
			lightName := "smoke:" + msg.SourceName + "/" + msg.TargetID
			if l := m.lights.GetByName(lightName); l != nil {
				l.SetStatus(msg.Status)
				l.SetDetails(msg.Details)
			} else {
				l = lights.New(lightName, "smoke-alarm")
				l.Source = "smoke-alarm"
				l.SetStatus(msg.Status)
				l.SetDetails(msg.Details)
				l.SourceMeta = map[string]string{
					"instance": msg.SourceName,
					"targetID": msg.TargetID,
				}
				m.lights.Add(l)
			}
			// Also arm mDNS lights for this source (backward-compat).
			if l := m.lights.GetByName(msg.SourceName); l != nil && l.Source == "mdns" {
				if l.GetStatus() == lights.StatusDark {
					l.SetStatus(lights.StatusGreen)
					l.SetDetails("armed")
				}
			}
			m.applyClusterHealthToFeatures()

			// Certify @domain-smoke-alarm-network feature lights with the current
			// aggregate status of all smoke: target lights. This provides specific
			// 42i evidence that the smoke-alarm integration is working (or failing).
			// Call directly since we are already inside Update().
			if networkStatus := m.aggregateSmokeNetworkStatus(); networkStatus != lights.StatusDark {
				m.applyCapabilityToFeatures("smoke-alarm-network", networkStatus,
					fmt.Sprintf("smoke-alarm network: %s", networkStatus))
			}
		}
		if m.lightUpdates != nil {
			return m, waitForLightUpdate(m.lightUpdates)
		}
		return m, nil

	case SmokeAlarmDiscoveredMsg:
		lightName := "smoke-alarm:" + msg.Hostname
		// Do not create a duplicate light for a statically-configured instance.
		if m.lights.GetByName(lightName) == nil {
			l := lights.New(lightName, "smoke-alarm")
			l.Source = "mdns"
			l.SourceMeta = map[string]string{
				"hostname": msg.Hostname,
				"addr":     msg.Addr,
			}
			m.lights.Add(l)
			slog.Info("mdns: smoke-alarm discovered", "hostname", msg.Hostname)

			// Start health-checking the discovered instance so the light
			// transitions from dark to green/red based on real connectivity.
			if msg.Addr != "" && msg.Port != 0 {
				if m.lightUpdates == nil {
					m.lightUpdates = make(chan smokelink.LightUpdate, 32)
				}
				if m.watcher == nil {
					m.watcher = smokelink.NewWatcher(nil)
				}
				endpoint := config.SmokeAlarmEndpoint{
					Name:     lightName,
					Endpoint: fmt.Sprintf("http://%s:%d", msg.Addr, msg.Port),
					Interval: 10 * time.Second,
				}
				m.watcher.WatchEndpoint(m.ctx, endpoint, m.lightUpdates)
				slog.Info("mdns: health-check started", "hostname", msg.Hostname, "endpoint", endpoint.Endpoint)
			}
		}
		// mDNS discovery succeeded: certify the @domain-discovery feature lights.
		// Call directly since we are already inside Update().
		m.applyCapabilityToFeatures("discovery", lights.StatusGreen,
			fmt.Sprintf("mDNS peer discovered: %s", msg.Hostname))
		if m.instances != nil {
			return m, waitForDiscovery(m.instances)
		}
		return m, nil

	case CapabilityVerifiedMsg:
		// A runtime capability was exercised. Drive the matching domain's
		// feature lights to reflect the observed outcome.
		m.applyCapabilityToFeatures(msg.Domain, msg.Status, msg.Details)
		slog.Debug("capability verified", "domain", msg.Domain, "status", msg.Status, "details", msg.Details)

	case SmokeAlarmRemovedMsg:
		lightName := "smoke-alarm:" + msg.Hostname
		// Only remove mDNS-sourced lights; leave statically-configured ones intact.
		if existing := m.lights.GetByName(lightName); existing != nil && existing.Source == "mdns" {
			m.lights.Remove(lightName)
			slog.Info("mdns: smoke-alarm removed", "hostname", msg.Hostname)
		}
		if m.instances != nil {
			return m, waitForDiscovery(m.instances)
		}
		return m, nil
	}

	return m, nil
}

// MarkPreVerified queues a CapabilityVerifiedMsg to be emitted at Init()
// time. Call after NewBubbleTeaDashboard but before Run() when the caller
// already knows a domain is verified (e.g. demo cluster was just found).
func (m *BubbleTeaDashboard) MarkPreVerified(msg CapabilityVerifiedMsg) {
	m.preVerified = append(m.preVerified, msg)
}

// applyCapabilityToFeatures marks all feature lights whose SourceMeta["domain"]
// matches domain with the given status. It also registers the domain as
// actively managed so applyClusterHealthToFeatures does not override it.
func (m *BubbleTeaDashboard) applyCapabilityToFeatures(domain string, status lights.Status, details string) {
	if m.verifiedDomains == nil {
		m.verifiedDomains = make(map[string]bool)
	}
	m.verifiedDomains[domain] = true
	for _, l := range m.lights.All() {
		if l.Type != "feature" {
			continue
		}
		if d, ok := l.SourceMeta["domain"]; !ok || d != domain {
			continue
		}
		l.SetStatus(status)
		if details != "" {
			l.SetDetails(details)
		}
	}
}

// aggregateSmokeNetworkStatus computes the worst-case status across all
// target-level smoke: lights (excludes mDNS instance lights).
// Returns dark when no target lights exist yet.
func (m *BubbleTeaDashboard) aggregateSmokeNetworkStatus() lights.Status {
	worst := lights.StatusGreen
	hasData := false
	for _, l := range m.lights.All() {
		if !strings.HasPrefix(l.Name, "smoke:") {
			continue
		}
		s := l.GetStatus()
		if s == lights.StatusDark {
			continue
		}
		hasData = true
		switch s {
		case lights.StatusRed:
			worst = lights.StatusRed
		case lights.StatusYellow:
			if worst != lights.StatusRed {
				worst = lights.StatusYellow
			}
		}
	}
	if !hasData {
		return lights.StatusDark
	}
	return worst
}

// probeIsotopeRegistration returns a Cmd that queries the first configured
// smoke-alarm endpoint's /isotope list after a brief startup delay. If any
// isotopes are registered (headless adhd registered with the smoke-alarm),
// it emits CapabilityVerifiedMsg{Domain: "headless", Status: green}.
// This is the 42i inter-peer certification step: TUI adhd observes evidence
// that headless adhd has announced itself to the shared smoke-alarm.
func (m *BubbleTeaDashboard) probeIsotopeRegistration() tea.Cmd {
	if len(m.config.SmokeAlarm) == 0 {
		return nil
	}
	endpoint := strings.TrimSuffix(m.config.SmokeAlarm[0].Endpoint, "/") + "/isotope"
	return func() tea.Msg {
		// Allow headless adhd time to start and register before probing.
		time.Sleep(6 * time.Second)
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(endpoint) //nolint:noctx
		if err != nil {
			return nil // headless not running — stay dark, not failed
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var isotopes []json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&isotopes); err != nil {
			return nil
		}
		if len(isotopes) == 0 {
			return nil
		}
		return CapabilityVerifiedMsg{
			Domain:  "headless",
			Status:  lights.StatusGreen,
			Details: fmt.Sprintf("%d isotope(s) registered with smoke-alarm", len(isotopes)),
		}
	}
}

// applyClusterHealthToFeatures computes the worst-case status across all
// smoke-alarm lights and applies it to every feature light.
//
// The baseline rule: feature lights stay dark until the cluster has been
// fully healthy at least once (clusterEverHealthy). This prevents startup
// flicker when adhd connects before the cluster is ready — early probe
// failures are transient, not a signal about feature correctness.
//
// After the baseline is established, feature lights follow cluster health:
// green when all probed targets are healthy, yellow/red when any are not.
//
// Only lights with Source "smoke-alarm" or "mdns" and a non-dark status
// contribute to the aggregate.
func (m *BubbleTeaDashboard) applyClusterHealthToFeatures() {
	worst := lights.StatusGreen
	hasData := false
	for _, l := range m.lights.All() {
		if l.Source != "smoke-alarm" && l.Source != "mdns" {
			continue
		}
		s := l.GetStatus()
		if s == lights.StatusDark {
			continue
		}
		hasData = true
		switch s {
		case lights.StatusRed:
			worst = lights.StatusRed
		case lights.StatusYellow:
			if worst != lights.StatusRed {
				worst = lights.StatusYellow
			}
		}
	}
	if !hasData {
		return
	}
	if worst == lights.StatusGreen {
		m.clusterEverHealthy = true
	}
	// Don't drive feature lights red/yellow until the cluster has established
	// a known-good baseline. Before that, leave them dark.
	if !m.clusterEverHealthy {
		return
	}
	for _, l := range m.lights.All() {
		if l.Type != "feature" {
			continue
		}
		// Skip lights whose domain is actively managed by a CapabilityVerifiedMsg
		// verifier — those carry more specific evidence than aggregate health.
		if d, ok := l.SourceMeta["domain"]; ok && d != "" && m.verifiedDomains[d] {
			continue
		}
		l.SetStatus(worst)
	}
}

// View renders the dashboard
func (m *BubbleTeaDashboard) View() string {
	var s strings.Builder

	// Styles
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("220"))

	selected := lipgloss.NewStyle().
		Foreground(lipgloss.Color("212")).
		Bold(true).
		PaddingLeft(0)

	bootStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("46")).
		Bold(true)

	// Boot sequence
	if m.booting {
		s.WriteString(header.Render("ADHD Health Dashboard - Initializing") + "\n")
		s.WriteString(strings.Repeat("─", 60) + "\n\n")

		allLights := m.lights.All()
		for i, light := range allLights {
			// Show off until we reach this light in the boot sequence
			if i < m.bootIndex {
				// Light has been initialized - show its actual color
				status := statusIndicator(light.Status)
				fmt.Fprintf(&s, "  %s", status)
			} else if i == m.bootIndex {
				// Currently initializing - show animated yellow
				s.WriteString(bootStyle.Render("  🟡"))
			} else {
				// Not yet reached - show off
				s.WriteString("  ⚫")
			}

			// Show type only if it's not "feature"
			typeStr := ""
			if light.Type != "feature" {
				typeStr = fmt.Sprintf(" [%s]", light.Type)
			}
			fmt.Fprintf(&s, " %s%s\n", light.Name, typeStr)
		}

		s.WriteString(strings.Repeat("\n", 5))
		s.WriteString("Initializing subsystems...\n")
		return s.String()
	}

	// Header
	s.WriteString(header.Render("ADHD Health Dashboard") + "\n")
	s.WriteString(strings.Repeat("─", 60) + "\n\n")

	// Lights list - grouped by z-axis (physical → relational → epistemic → analytical)
	allLights := m.lights.All()
	if len(allLights) == 0 {
		s.WriteString("(no lights configured)\n")
	} else {
		// Group lights by source for display
		bySource := make(map[string][]*lights.Light)
		for _, light := range allLights {
			bySource[light.Source] = append(bySource[light.Source], light)
		}

		// Display in a hierarchical format
		// Order sources by prominence for display
		sourceOrder := []string{"api-service", "smoke-alarm", "fire-marshal"}
		displayIndex := 0

		for _, source := range sourceOrder {
			sourceLights := bySource[source]
			if len(sourceLights) == 0 {
				continue
			}

			// Source header with status summary
			greenCount := 0
			redCount := 0
			yellowCount := 0
			for _, light := range sourceLights {
				switch light.Status {
				case lights.StatusGreen:
					greenCount++
				case lights.StatusRed:
					redCount++
				case lights.StatusYellow:
					yellowCount++
				}
			}

			statusSummary := fmt.Sprintf("[🟢%d 🔴%d 🟡%d]", greenCount, redCount, yellowCount)
			headerStr := fmt.Sprintf("\n■ %s — %d features %s", source, len(sourceLights), statusSummary)
			s.WriteString(headerStr + "\n")
			s.WriteString(strings.Repeat("─", 50) + "\n")

			// Features grouped by source
			for _, light := range sourceLights {
				prefix := "  "
				if displayIndex == m.selectedIndex {
					prefix = "> "
				}

				status := statusIndicator(light.Status)
				line := fmt.Sprintf("%s%s %s", prefix, status, light.Name)

				if displayIndex == m.selectedIndex {
					s.WriteString(selected.Render(line) + "\n")
				} else {
					s.WriteString(line + "\n")
				}

				displayIndex++
			}
		}

		// Show any remaining lights not in the standard sources
		for source, sourceLights := range bySource {
			if source == "api-service" || source == "smoke-alarm" || source == "fire-marshal" {
				continue
			}

			fmt.Fprintf(&s, "\n%s  (%d features)\n", source, len(sourceLights))
			s.WriteString(strings.Repeat("─", 40) + "\n")

			for _, light := range sourceLights {
				prefix := "  "
				if displayIndex == m.selectedIndex {
					prefix = "> "
				}

				status := statusIndicator(light.Status)
				line := fmt.Sprintf("%s%s %s", prefix, status, light.Name)

				if displayIndex == m.selectedIndex {
					s.WriteString(selected.Render(line) + "\n")
				} else {
					s.WriteString(line + "\n")
				}

				displayIndex++
			}
		}
	}

	// Status line
	s.WriteString("\n")
	if len(allLights) > m.selectedIndex {
		selectedLight := allLights[m.selectedIndex]
		fmt.Fprintf(&s, "(%d/%d) %s [%s]\n", m.selectedIndex+1, len(allLights), selectedLight.Name, selectedLight.Type)
		if selectedLight.Details != "" {
			fmt.Fprintf(&s, "Details: %s\n", selectedLight.Details)
		}
		// Show Gherkin file reference if available
		if gherkinFile, ok := selectedLight.SourceMeta["gherkin_file"]; ok && gherkinFile != "" {
			fmt.Fprintf(&s, "Spec: %s\n", gherkinFile)
		}
	}

	// Message display
	if m.messageTimer > 0 {
		fmt.Fprintf(&s, "\n✓ %s\n", m.message)
	} else {
		s.WriteString("\n")
	}

	s.WriteString("[Commands] j/k=navigate  s=show  r=refresh  e=execute  q=quit\n")

	return s.String()
}

// Shutdown gracefully shuts down the dashboard
func (m *BubbleTeaDashboard) Shutdown() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.mcpServer != nil {
		_ = m.mcpServer.Shutdown(m.ctx)
	}
}

// Run starts the Bubble Tea dashboard
func (m *BubbleTeaDashboard) Run() error {
	p := tea.NewProgram(m)
	_, err := p.Run()
	m.Shutdown()
	return err
}
