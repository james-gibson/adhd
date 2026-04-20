package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/dashboard"
	"github.com/james-gibson/adhd/internal/demo"
	"github.com/james-gibson/adhd/internal/headless"
	"github.com/james-gibson/adhd/internal/proxy"
	"github.com/james-gibson/adhd/internal/smoketest"
	"github.com/james-gibson/adhd/internal/telemetry"
)

var (
	versionFlag   = flag.Bool("v", false, "show version and exit")
	debugFlag     = flag.Bool("debug", false, "enable debug logging")
	configFlag    = flag.String("config", "adhd.yaml", "path to config file")
	headlessFlag  = flag.Bool("headless", false, "run in headless mode (no TUI, JSONL logging)")
	logFileFlag   = flag.String("log", "", "log file path for JSONL output (stdout if empty)")
	smokeAlarmURL = flag.String("smoke-alarm", "", "smoke-alarm URL for isotope registration")
	primePlusFlag = flag.Bool("prime-plus", false, "run as prime-plus: buffer logs and push to prime")
	primeAddrFlag = flag.String("prime-addr", "", "address of prime smoke-alarm (required if --prime-plus)")
	bufferSizeFlag = flag.Int("buffer-size", 1000, "max number of logs to buffer before pushing to prime")
	mcpAddrFlag   = flag.String("mcp-addr", "", "override MCP server address (e.g., :0 for random port)")
	demoFlag      = flag.Bool("demo", false, "discover a lezz demo cluster via mDNS instead of loading config")
)

func main() {
	flag.Parse()

	if *versionFlag {
		fmt.Println(version())
		os.Exit(0)
	}

	// Setup logging - use Warn level in interactive mode to reduce noise
	level := slog.LevelWarn
	if *debugFlag {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	ctx := context.Background()

	// Setup telemetry (non-blocking; failures are logged but don't block startup)
	shutdown, err := telemetry.Initialize(ctx)
	if err != nil {
		slog.Warn("telemetry initialization failed", "error", err)
	} else {
		defer func() {
			if err := shutdown(ctx); err != nil {
				slog.Error("telemetry shutdown failed", "error", err)
			}
			}()
	}

	// Load configuration — either from file or by discovering a lezz demo cluster.
	var cfg *config.Config
	var clusters []demo.ClusterInfo
	if *demoFlag {
		fmt.Fprintf(os.Stderr, "browsing for lezz demo cluster (timeout %s)...\n", demo.DefaultTimeout)
		var discoverErr error
		clusters, discoverErr = demo.Browse(ctx, demo.DefaultTimeout)
		if discoverErr != nil {
			fmt.Fprintln(os.Stderr, "demo discovery failed:", discoverErr)
			os.Exit(1)
			}
		fmt.Fprintf(os.Stderr, "found %d cluster(s)\n", len(clusters))
		cfg = demo.ConfigFromClusters(clusters)
	} else {
		var loadErr error
		cfg, loadErr = config.Load(*configFlag)
		if loadErr != nil {
			slog.Error("failed to load config", "path", *configFlag, "error", loadErr)
			os.Exit(1)
			}
	}

	if cfg.IsNetworkingEnabled() {
		slog.Info("network integration enabled", "endpoints", len(cfg.SmokeAlarm), "mcp_server", cfg.MCPServer.Enabled)
	}

	// Run in headless or TUI mode
	if *headlessFlag {
		// Headless mode: MCP traffic logging + isotope registration
		server := headless.New(cfg)

		// Override MCP address if specified (useful for avoiding port conflicts)
		if *mcpAddrFlag != "" {
			cfg.MCPServer.Addr = *mcpAddrFlag
			}

		if err := server.Start(*logFileFlag); err != nil {
			slog.Warn("headless server start warning", "error", err)
			// Continue even if server startup fails - still log traffic
			}

			// Setup message queue for prime-plus topology if configured
		if *primePlusFlag {
			primeAddr := *primeAddrFlag

			// If no prime address specified but smoke-alarm is configured, try auto-discovery
			if primeAddr == "" && *smokeAlarmURL != "" {
				slog.Info("auto-discovering prime instance via smoke-alarm")
				if server.AutoDiscoverPrime(*smokeAlarmURL) {
					primeAddr = server.GetPrimeAddr()
				} else {
					slog.Warn("prime auto-discovery failed, will continue without buffering")
				}
			}

			// Setup message queue (with or without discovered prime)
			if primeAddr != "" {
				server.SetupMessageQueue(*primePlusFlag, primeAddr, *bufferSizeFlag)
				slog.Info("message queue configured for prime-plus", "prime_addr", primeAddr, "buffer_size", *bufferSizeFlag)
			}
			}

		// Register with smoke-alarm if configured
		if *smokeAlarmURL != "" {
			if err := server.RegisterAsIsotope(*smokeAlarmURL); err != nil {
				slog.Warn("failed to register as isotope", "error", err)
			}
			}

		// Keep running indefinitely (respond to signals)
		slog.Info("adhd running in headless mode", "log_file", *logFileFlag, "prime_plus", *primePlusFlag)
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		slog.Info("shutting down")
		if err := server.Shutdown(); err != nil {
			slog.Warn("server shutdown error", "error", err)
			}
	} else {
		// TUI mode: Bubble Tea dashboard
		d := dashboard.NewBubbleTeaDashboard(cfg)

		// Initialize scheduler if certified endpoints are configured
		if len(cfg.CertifiedEndpoints) > 0 {
			proxyExecutor := proxy.NewExecutor()
			runner := smoketest.NewRunner(proxyExecutor)
			scheduler := smoketest.NewScheduler(runner)

			// Register endpoints from config
			for _, ep := range cfg.CertifiedEndpoints {
				// Get token from environment variable
				token := os.Getenv(ep.TokenEnv)
				if token == "" && ep.AuthType != "none" {
					slog.Warn("certified endpoint token not found", "endpoint_id", ep.ID, "token_env", ep.TokenEnv)
					continue
				}

				certEndpoint := &smoketest.CertifiedEndpoint{
					ID:       ep.ID,
					URL:      ep.URL,
					AuthType: ep.AuthType,
					Token:    token,
					Header:   ep.Header,
					TestFreq: ep.TestFreq,
					CertLevel: 100,
				}
				scheduler.RegisterEndpoint(certEndpoint)
				slog.Info("registered certified endpoint", "id", ep.ID, "url", ep.URL, "auth_type", ep.AuthType)
			}

			// Start scheduler
			scheduler.Start(ctx)
			d.SetScheduler(scheduler)


			// Wire scheduler events to dashboard
			go func() {
				for event := range scheduler.EventsChannel() {
					d.Send(dashboard.SmokeTestEventMsg{Event: event})
				}
			}()

			slog.Info("smoke test scheduler started", "endpoints", len(cfg.CertifiedEndpoints))
			}

		// If the config was built from a lezz demo cluster discovery, mark the
		// @domain-demo feature lights pre-verified so they go green at Init time,
		// and arm continuous polling of the registry so newly-joined clusters
		// are picked up without a restart.
		if *demoFlag && len(clusters) > 0 {
			d.MarkPreVerified(dashboard.CapabilityVerifiedMsg{
				Domain:  "demo",
				Status:  "green",
				Details: fmt.Sprintf("%d cluster(s) discovered via lezz demo registry", len(clusters)),
			})
			initialNames := make([]string, len(clusters))
			for i, c := range clusters {
				initialNames[i] = c.Name
			}
			registryURL := fmt.Sprintf("http://localhost:%d/cluster", demo.DiscoveryPort)
			d.SetRegistryURL(registryURL, initialNames)
			}

		if err := d.Run(); err != nil {
			slog.Error("dashboard error", "error", err)
			}
	}
}

func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "adhd dev"
	}

	v := bi.Main.Version
	if v == "" || v == "(devel)" {
		// Fall back to commit hash when running outside a module release
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 8 {
				return "adhd " + s.Value[:8]
			}
			}
		return "adhd dev"
	}

	return "adhd " + v
}
