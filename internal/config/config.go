package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete ADHD configuration
type Config struct {
	MCPServer         MCPServerConfig       `yaml:"mcp_server"`
	Health            HealthConfig          `yaml:"health"`
	Project           ProjectConfig         `yaml:"project"`
	SmokeAlarm        []SmokeAlarmEndpoint  `yaml:"smoke_alarm"`
	MCPTargets        []MCPTarget           `yaml:"mcp_targets"`
	Features          FeaturesConfig        `yaml:"features"`
	CertifiedEndpoints []CertifiedEndpoint  `yaml:"certified_endpoints"`
	// DiscoveredClusters carries the MCP-side context of lezz demo clusters
	// found by `adhd --demo`: their adhd_mcp endpoints and projects. Their alarm
	// endpoints are expanded into SmokeAlarm, which is the shape the watcher
	// consumes; the MCP identity has no other representation, so dropping it
	// silently drops the clusters' adhd.project.<id>.call proxy tools.
	DiscoveredClusters []DiscoveredCluster `yaml:"discovered_clusters,omitempty"`
}

// DiscoveredCluster is a lezz demo cluster discovered by `adhd --demo`, or
// added later by registry polling. It carries the cluster's MCP identity only —
// the alarm endpoints live in SmokeAlarm.
type DiscoveredCluster struct {
	Name     string           `yaml:"name"`
	AdhdMCP  string           `yaml:"adhd_mcp,omitempty"` // cluster's own MCP endpoint
	Projects []ClusterProject `yaml:"projects,omitempty"`
}

// ClusterProject is a project inside a discovered cluster that exposes its own
// MCP server. Each one becomes an adhd.project.<id>.call proxy tool.
type ClusterProject struct {
	ID     string `yaml:"id,omitempty"` // stable seed id; namespaces the tool
	Name   string `yaml:"name"`
	Repo   string `yaml:"repo,omitempty"`
	MCPURL string `yaml:"mcp_url"`
}

// HealthConfig configures health monitoring
type HealthConfig struct {
	RemoteSmokeAlarm string `yaml:"remote_smoke_alarm"` // e.g., "http://smoke-alarm:8080"
}

// MCPServerConfig configures ADHD's hosted MCP server
type MCPServerConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`    // e.g., ":9090"
	SSEAddr string `yaml:"sse_addr"` // e.g., ":9091" for SSE (optional)
}

// ProjectConfig is this project's registry identity. Route is the lezz
// `seed.json` project.route: a dependent resolves the project by it, not by the
// endpoint it happens to be on. Contract is the signed spec id this instance
// implements, if it declares one.
type ProjectConfig struct {
	Route    string `yaml:"route,omitempty"`
	Contract string `yaml:"contract,omitempty"`
}

// SmokeAlarmEndpoint represents a monitored ocd-smoke-alarm instance
type SmokeAlarmEndpoint struct {
	Name     string        `yaml:"name"`
	Endpoint string        `yaml:"endpoint"` // e.g., "http://localhost:8080"
	Interval time.Duration `yaml:"interval"`
	UseSSE   bool          `yaml:"use_sse"` // subscribe to SSE stream

	// ProjectRoute and Contract pin the dependency this endpoint is: the route
	// is where the smoke-alarm project lives (lezz seed.json project.route),
	// and the contract is the signed spec it was built against. Both are
	// published as a depends_on reference so another consumer can resolve the
	// same project without a remembered URL.
	ProjectRoute string `yaml:"project_route,omitempty"`
	Contract     string `yaml:"contract,omitempty"`
}

// MCPTarget represents a direct MCP probe target
type MCPTarget struct {
	Name     string        `yaml:"name"`
	Endpoint string        `yaml:"endpoint"`  // "http://host:port/mcp" or "stdio:command"
	Tools    []string      `yaml:"tools"`     // specific tools to monitor (empty = all)
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// FeaturesConfig configures feature discovery
type FeaturesConfig struct {
	SearchPaths []string  `yaml:"search_paths"`
	Binaries    []Binary  `yaml:"binaries"`
}

// Binary represents a software binary that provides features.
//
// Endpoint is either a URL ("http://localhost:9090/mcp") or a registry name
// ("workspace-42"). A name is resolved through the configured alarm's
// /isotope/list on every probe, so a surface that binds an ephemeral port and
// publishes itself by name can be watched without the harness injecting its
// address (adhd#62). See EndpointName.
type Binary struct {
	Name          string        `yaml:"name"`
	Endpoint      string        `yaml:"endpoint"`  // URL, or a registry name (see EndpointName)
	Features      []Feature     `yaml:"features"`
	GherkinFiles  []string      `yaml:"gherkin_files"`  // .feature files this binary satisfies
	Interval      time.Duration `yaml:"interval"`
	Timeout       time.Duration `yaml:"timeout"`
}

// endpointName matches a bare registry name: one token, no scheme, no port, no
// path.
var endpointNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// EndpointName reports the registry name this binary is addressed by, when
// Endpoint holds a name instead of a URL.
//
// A name is a bare token. A mistyped URL — "localhost:9090", "htp://host/mcp",
// ":9090/mcp" — therefore stays a URL and fails as one, instead of silently
// becoming a registry lookup that reports "not in the registry" for what is
// really a typo. The cost is that a name may not contain a colon or a slash;
// isotope names do not.
func (b Binary) EndpointName() (string, bool) {
	name := strings.TrimSpace(b.Endpoint)
	if !endpointNameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// Feature represents a feature provided by a binary
type Feature struct {
	Name         string `yaml:"name"`
	GherkinFile  string `yaml:"gherkin_file"`   // path to .feature file
	GherkinFeature string `yaml:"gherkin_feature"` // feature name in Gherkin
}

// CertifiedEndpoint represents an MCP endpoint that has been certified
// and is monitored by the smoke test scheduler
type CertifiedEndpoint struct {
	ID       string        `yaml:"id"`        // unique identifier (e.g., "stripe-api")
	URL      string        `yaml:"url"`       // MCP endpoint URL (e.g., "https://api.stripe.com/mcp")
	AuthType string        `yaml:"auth_type"` // "bearer", "api-key", "oauth2", or "none"
	TokenEnv string        `yaml:"token_env"` // environment variable name for token (e.g., "STRIPE_API_KEY")
	Header   string        `yaml:"header,omitempty"` // custom header name for api-key type
	TestFreq time.Duration `yaml:"test_freq"` // how often to test (default 5 minutes)
}

// Load reads and parses a YAML config file
// Returns default config if file not found
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// File not found; use defaults
			return cfg, nil
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	// Apply defaults where zero values are problematic
	cfg.applyDefaults()

	// Resolve relative paths relative to config file directory
	configDir := filepath.Dir(path)
	cfg.resolveRelativePaths(configDir)

	return cfg, nil
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	return &Config{
		MCPServer: MCPServerConfig{
			Enabled: false,
			Addr:    ":0", // OS assigns a free port; avoids conflicts in multi-daemon clusters
		},
		SmokeAlarm: []SmokeAlarmEndpoint{},
		MCPTargets: []MCPTarget{},
		Features: FeaturesConfig{
			SearchPaths: []string{
				"features/adhd/",
			},
			Binaries: []Binary{},
		},
	}
}

// RegistryEndpoint is where a binaries[].endpoint name is resolved: a
// configured alarm, the shape that serves GET /isotope/list. The configured
// smoke_alarm entries are preferred over health.remote_smoke_alarm because the
// watcher already polls them over REST, so they are known to be HTTP endpoints;
// remote_smoke_alarm is only a probe target and may be a discovery name. There
// is no second, separate registry setting, so a name and the alarm's own view
// cannot drift onto different registries.
func (c *Config) RegistryEndpoint() string {
	for _, alarm := range c.SmokeAlarm {
		if ep := strings.TrimSpace(alarm.Endpoint); ep != "" {
			return ep
		}
	}
	return strings.TrimSpace(c.Health.RemoteSmokeAlarm)
}

// resolveRelativePaths converts relative paths to absolute, based on config file directory
func (c *Config) resolveRelativePaths(configDir string) {
	// Resolve search paths
	for i, path := range c.Features.SearchPaths {
		if !filepath.IsAbs(path) {
			c.Features.SearchPaths[i] = filepath.Join(configDir, path)
		}
	}

	// Resolve gherkin file paths in binaries
	for i := range c.Features.Binaries {
		for j, gfile := range c.Features.Binaries[i].GherkinFiles {
			if !filepath.IsAbs(gfile) {
				c.Features.Binaries[i].GherkinFiles[j] = filepath.Join(configDir, gfile)
			}
		}
		for j := range c.Features.Binaries[i].Features {
			if c.Features.Binaries[i].Features[j].GherkinFile != "" && !filepath.IsAbs(c.Features.Binaries[i].Features[j].GherkinFile) {
				c.Features.Binaries[i].Features[j].GherkinFile = filepath.Join(configDir, c.Features.Binaries[i].Features[j].GherkinFile)
			}
		}
	}
}

// applyDefaults fills in zero values with reasonable defaults
func (c *Config) applyDefaults() {
	// Default feature search paths if not specified
	if len(c.Features.SearchPaths) == 0 {
		c.Features.SearchPaths = DefaultConfig().Features.SearchPaths
	}

	// Default intervals for smoke-alarm endpoints
	for i := range c.SmokeAlarm {
		if c.SmokeAlarm[i].Interval == 0 {
			c.SmokeAlarm[i].Interval = 10 * time.Second
		}
	}

	// Default timeout for MCP targets
	for i := range c.MCPTargets {
		if c.MCPTargets[i].Interval == 0 {
			c.MCPTargets[i].Interval = 30 * time.Second
		}
		if c.MCPTargets[i].Timeout == 0 {
			c.MCPTargets[i].Timeout = 5 * time.Second
		}
	}
}

// IsNetworkingEnabled returns true if any external integration is configured
func (c *Config) IsNetworkingEnabled() bool {
	return len(c.SmokeAlarm) > 0 || len(c.MCPTargets) > 0 || c.MCPServer.Enabled
}
