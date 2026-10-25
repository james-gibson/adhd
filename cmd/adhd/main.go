package main

import (
	"context"
	"errors"
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
	versionFlag    = flag.Bool("v", false, "show version and exit")
	debugFlag      = flag.Bool("debug", false, "enable debug logging")
	configFlag     = flag.String("config", "adhd.yaml", "path to config file")
	headlessFlag   = flag.Bool("headless", false, "run in headless mode (no TUI, JSONL logging)")
	logFileFlag    = flag.String("log", "", "log file path for JSONL output (stdout if empty)")
	smokeAlarmURL  = flag.String("smoke-alarm", "", "smoke-alarm URL for isotope registration")
	primePlusFlag  = flag.Bool("prime-plus", false, "run as prime-plus: buffer logs and push to prime")
	primeAddrFlag  = flag.String("prime-addr", "", "address of prime smoke-alarm (required if --prime-plus)")
	bufferSizeFlag = flag.Int("buffer-size", 1000, "max number of logs to buffer before pushing to prime")
	mcpAddrFlag    = flag.String("mcp-addr", "", "override MCP server address (e.g., :0 for random port)")
	demoFlag       = flag.Bool("demo", false, "discover a lezz demo cluster via mDNS instead of loading config")
	forceFlag      = flag.Bool("force", false, "advertise/register even if the MCP endpoint is unguarded (non-loopback) — an explicit LAN-discovery override")
)

// resolveSmokeAlarm picks the smoke-alarm endpoint to register with. The
// --smoke-alarm flag wins; otherwise the first configured smoke_alarm endpoint
// is used, so a config-file-driven deployment (a launchd/systemd daemon) that
// sets smoke_alarm in adhd.yaml registers and advertises without needing a CLI
// flag. Before either is advertised, RegisterAsIsotope runs the advertise guard
// so an unguarded MCP bind cannot be named to the LAN by accident.
func resolveSmokeAlarm(flagValue string, cfg *config.Config) string {
	if flagValue != "" {
		return flagValue
	}
	if len(cfg.SmokeAlarm) > 0 {
		return cfg.SmokeAlarm[0].Endpoint
	}
	return ""
}

// enableMCPForAddr applies an explicit --mcp-addr override to cfg. Supplying an
// address is a request to serve MCP there, so it also enables the hosted MCP
// server even when the config left it disabled. This matters for --demo
// discovery: demo.ConfigFromClusters returns the default config with
// mcp_server.enabled=false, so without this the MCP port never opens and lezz
// auto/demo time out waiting for it.
func enableMCPForAddr(cfg *config.Config, addr string) {
	if addr == "" {
		return
	}
	cfg.MCPServer.Enabled = true
	cfg.MCPServer.Addr = addr
}

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

	// Initialize the smoke-test scheduler. The scheduler is shared by both
	// headless and TUI modes, so it is created at function scope regardless of
	// the mode or whether any certified endpoints are configured.
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
			ID:        ep.ID,
			URL:       ep.URL,
			AuthType:  ep.AuthType,
			Token:     token,
			Header:    ep.Header,
			TestFreq:  ep.TestFreq,
			CertLevel: 100,
		}
		scheduler.RegisterEndpoint(certEndpoint)
		slog.Info("registered certified endpoint", "id", ep.ID, "url", ep.URL, "auth_type", ep.AuthType)
	}

	// Start scheduler
	scheduler.Start(ctx)

	// Run in headless or TUI mode
	if *headlessFlag {
		// Headless mode: MCP traffic logging + isotope registration
		enableMCPForAddr(cfg, *mcpAddrFlag)
		server := headless.New(cfg)

		if err := server.Start(*logFileFlag); err != nil {
			slog.Warn("headless server start warning", "error", err)
			// Continue even if server startup fails - still log traffic
		}
		// Wire scheduler events to dashboard
		go func() {
			for event := range scheduler.EventsChannel() {
				slog.Debug(event.Message)
			}
		}()
		// Setup message queue for prime-plus topology if configured
		smokeTarget := resolveSmokeAlarm(*smokeAlarmURL, cfg)
		if *primePlusFlag {
			primeAddr := *primeAddrFlag

			// If no prime address specified but smoke-alarm is configured, try auto-discovery
			if primeAddr == "" && smokeTarget != "" {
				slog.Info("auto-discovering prime instance via smoke-alarm")
				if server.AutoDiscoverPrime(smokeTarget) {
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

		// Register with smoke-alarm and advertise over mDNS. The --smoke-alarm
		// flag wins, but a config-file-driven deployment registers from its
		// first smoke_alarm entry. Registration is guarded: an unguarded
		// (non-loopback) MCP endpoint is refused unless --force is passed, and
		// refusal fails closed rather than shipping the offense.
		var stopHeartbeat context.CancelFunc
		var heartbeatDone chan struct{}
		if smokeTarget != "" {
			server.SetForceAdvertise(*forceFlag)
			if err := server.RegisterAsIsotope(smokeTarget); err != nil {
				if errors.Is(err, headless.ErrUnguardedEndpoint) {
					slog.Error("refusing to advertise an unguarded MCP endpoint", "error", err)
					fmt.Fprintln(os.Stderr, "refusing to start:", err)
					_ = server.Shutdown()
					os.Exit(1)
				}
				slog.Warn("failed to register as isotope", "error", err)
			}

			// Keep the registry entry live. smoke-alarm treats a registration as
			// a heartbeat and sweeps a name it has not heard from, so a silent
			// instance would stop resolving by name (smoke-alarm#35). The
			// heartbeat also retries a registration that failed above.
			heartbeatCtx, cancel := context.WithCancel(context.Background())
			stopHeartbeat = cancel
			heartbeatDone = make(chan struct{})
			go func() {
				defer close(heartbeatDone)
				server.ServeIsotopeHeartbeat(heartbeatCtx, smokeTarget, headless.IsotopeHeartbeatInterval)
			}()
		}

		// Keep running indefinitely (respond to signals)
		slog.Info("adhd running in headless mode", "log_file", *logFileFlag, "prime_plus", *primePlusFlag)
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		slog.Info("shutting down")
		if stopHeartbeat != nil {
			stopHeartbeat()
			// Wait for an in-flight beat to abort before deleting, or the
			// registration could be re-created after deregistration.
			<-heartbeatDone
			// Best-effort: do not leave a name behind when the process can say
			// goodbye. The TTL is the backstop if this fails.
			server.DeregisterIsotope(smokeTarget)
		}
		if err := server.Shutdown(); err != nil {
			slog.Warn("server shutdown error", "error", err)
		}
	} else {
		// TUI mode: Bubble Tea dashboard
		d := dashboard.NewBubbleTeaDashboard(cfg)
		d.SetScheduler(scheduler)

		// Wire scheduler events to dashboard
		go func() {
			for event := range scheduler.EventsChannel() {
				d.Send(dashboard.SmokeTestEventMsg{Event: event})
			}
		}()

		slog.Info("smoke test scheduler started", "endpoints", len(cfg.CertifiedEndpoints))

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
