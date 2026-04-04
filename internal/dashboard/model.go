package dashboard

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/features"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/mcpserver"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// Dashboard represents the text-based dashboard state
type Dashboard struct {
	// Lights cluster
	lights *lights.Cluster

	// Feature discovery
	loader *features.Loader

	// Network integration
	smokeWatcher *smokelink.Watcher
	mcpServer    *mcpserver.Server
	updatesChan  chan smokelink.LightUpdate

	// UI state
	selectedIndex int
	running       bool
	config        *config.Config
}

// NewWithConfig creates a new dashboard with full configuration
func NewWithConfig(cfg *config.Config) *Dashboard {
	c := lights.NewCluster()

	// Load features from configured paths
	loader := features.NewLoader(cfg.Features.SearchPaths)
	discoveredFeatures, err := loader.LoadFeatures()
	if err != nil {
		slog.Warn("failed to load features", "error", err)
	}

	// Create lights from features — dark until the health monitor confirms the
	// backing service is reachable. A feature file existing is not proof of health.
	for _, feature := range discoveredFeatures {
		light := lights.New(feature.Name, "feature")
		light.Source = "gherkin"
		light.SourceMeta = map[string]string{"file": feature.FilePath}
		light.Status = lights.StatusDark
		c.Add(light)
	}

	// Add placeholder lights if none discovered
	if c.Count() == 0 {
		for _, name := range []string{"fire-marshal-spec-check", "smoke-alarm-primary", "smoke-alarm-secondary"} {
			light := lights.New(name, "test")
			light.Source = "placeholder"
			c.Add(light)
		}
	}

	// Create smoke-alarm watcher if endpoints are configured
	var smokeWatcher *smokelink.Watcher
	if len(cfg.SmokeAlarm) > 0 {
		smokeWatcher = smokelink.NewWatcher(cfg.SmokeAlarm)
		slog.Info("smoke-alarm watcher configured", "endpoints", len(cfg.SmokeAlarm))
	}

	// Create MCP server if enabled
	var mcpServer *mcpserver.Server
	if cfg.MCPServer.Enabled {
		mcpServer = mcpserver.NewServer(cfg.MCPServer, c)
		slog.Info("MCP server configured", "addr", cfg.MCPServer.Addr)
	}

	return &Dashboard{
		lights:        c,
		loader:        loader,
		smokeWatcher:  smokeWatcher,
		mcpServer:     mcpServer,
		updatesChan:   make(chan smokelink.LightUpdate, 10),
		selectedIndex: 0,
		running:       true,
		config:        cfg,
	}
}

// New creates a new dashboard with default configuration
// Deprecated: use NewWithConfig instead
func New() *Dashboard {
	return NewWithConfig(config.DefaultConfig())
}

// Run starts the interactive dashboard with all subsystems
func (d *Dashboard) Run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start MCP server if enabled
	if d.mcpServer != nil {
		if err := d.mcpServer.Start(ctx); err != nil {
			slog.Error("failed to start MCP server", "error", err)
		}
		defer func() { _ = d.mcpServer.Shutdown(ctx) }()
	}

	// Start smoke-alarm watcher if configured
	if d.smokeWatcher != nil {
		d.smokeWatcher.Start(ctx, d.updatesChan)
	}

	// Start input reader in goroutine
	inputChan := make(chan string, 10) // buffered to avoid blocking
	reader := bufio.NewReader(os.Stdin)
	go func() {
		for d.running {
			input, err := reader.ReadString('\n')
			if err != nil {
				slog.Debug("input reader error", "error", err)
				break
			}
			cmd := strings.TrimSpace(input)
			if cmd != "" {
				inputChan <- cmd
			}
		}
	}()

	// Main event loop - render on every state change
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	needsRender := true
	for d.running {
		select {
		case update := <-d.updatesChan:
			d.applyLightUpdate(update)
			needsRender = true

		case cmd := <-inputChan:
			d.handleInput(cmd)
			needsRender = true

		case <-ticker.C:
			if needsRender {
				d.render()
				needsRender = false
			}
		}
	}

	cancel()
	fmt.Println("Dashboard stopped.")
}

// applyLightUpdate processes a light status update from smoke-alarm
func (d *Dashboard) applyLightUpdate(update smokelink.LightUpdate) {
	// Instance-level updates (reachability) are handled by BubbleTeaDashboard;
	// the legacy model only processes target-level events.
	if update.IsInstance {
		return
	}

	// Create a light key that includes the source
	lightName := fmt.Sprintf("smoke:%s/%s", update.SourceName, update.TargetID)

	// Try to find existing light
	existing := d.lights.GetByName(lightName)
	if existing != nil {
		// Update existing light
		existing.Status = update.Status
		existing.Details = update.Details
		existing.LastUpdated = time.Now()
	} else {
		// Create new light
		light := lights.New(lightName, "smoke-alarm")
		light.Source = "smoke-alarm"
		light.Status = update.Status
		light.Details = update.Details
		light.SourceMeta = map[string]string{
			"instance": update.SourceName,
			"targetID": update.TargetID,
			"source":   update.Source,
		}
		d.lights.Add(light)
	}

	slog.Debug("light updated", "name", lightName, "status", update.Status)
}

// handleInput processes user keyboard input
func (d *Dashboard) handleInput(cmd string) {
	switch cmd {
	case "q":
		d.running = false
		fmt.Println("\n✓ Exiting dashboard...")
	case "j", "down":
		if d.selectedIndex < d.lights.Count()-1 {
			d.selectedIndex++
		}
	case "k", "up":
		if d.selectedIndex > 0 {
			d.selectedIndex--
		}
	case "r":
		if d.lights.Count() > d.selectedIndex {
			light := d.lights.All()[d.selectedIndex]
			slog.Info("refreshing light", "light", light.Name)
			fmt.Printf("\n✓ Refreshing: %s\n", light.Name)
		}
	case "s":
		if d.lights.Count() > d.selectedIndex {
			light := d.lights.All()[d.selectedIndex]
			fmt.Printf("\n=== %s Details ===\n", light.Name)
			fmt.Printf("Type: %s\n", light.Type)
			fmt.Printf("Source: %s\n", light.Source)
			fmt.Printf("Status: %s\n", light.Status)
			fmt.Printf("Details: %s\n", light.Details)
			fmt.Printf("Last Updated: %v\n", light.LastUpdated)
			fmt.Println("(press enter to continue)")
		}
	case "e":
		if d.lights.Count() > d.selectedIndex {
			light := d.lights.All()[d.selectedIndex]
			slog.Info("execute command for light", "light", light.Name)
			fmt.Printf("\n✓ Executing action for: %s\n", light.Name)
		}
	default:
		if cmd != "" {
			slog.Debug("unknown command", "cmd", cmd)
		}
	}
}

// render displays the dashboard
func (d *Dashboard) render() {
	// Clear screen and move cursor to home (ANSI escape codes)
	fmt.Print("\033[H\033[2J")

	// Header
	fmt.Println("ADHD Health Dashboard")
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	// Lights
	allLights := d.lights.All()
	if len(allLights) == 0 {
		fmt.Println(" (no lights configured)")
	} else {
		for i, light := range allLights {
			prefix := " "
			if i == d.selectedIndex {
				prefix = ">"
			}
			status := statusIndicator(light.Status)
			fmt.Printf("%s %s %-30s [%s]\n", prefix, status, light.Name, light.Type)
		}
	}

	// Status line
	fmt.Println()
	if len(allLights) > d.selectedIndex {
		selected := allLights[d.selectedIndex]
		fmt.Printf("(%d/%d) %s [%s]\n", d.selectedIndex+1, len(allLights), selected.Name, selected.Type)
		if selected.Details != "" {
			fmt.Printf("Details: %s\n", selected.Details)
		}
	}
	fmt.Println()
	fmt.Println("[Commands] j/k=up/down  s=show  r=refresh  e=execute  q=quit")
}

// statusIndicator returns the emoji or character for a light status
func statusIndicator(status lights.Status) string {
	switch status {
	case lights.StatusGreen:
		return "🟢"
	case lights.StatusRed:
		return "🔴"
	case lights.StatusYellow:
		return "🟡"
	case lights.StatusDark:
		return "⚫"
	default:
		return "?"
	}
}
