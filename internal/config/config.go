package config

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete ADHD configuration
type Config struct {
	MCPServer  MCPServerConfig      `yaml:"mcp_server"`
	Health     HealthConfig         `yaml:"health"`
	SmokeAlarm []SmokeAlarmEndpoint `yaml:"smoke_alarm"`
	MCPTargets []MCPTarget          `yaml:"mcp_targets"`
	Features   FeaturesConfig       `yaml:"features"`
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

// SmokeAlarmEndpoint represents a monitored ocd-smoke-alarm instance
type SmokeAlarmEndpoint struct {
	Name     string        `yaml:"name"`
	Endpoint string        `yaml:"endpoint"` // e.g., "http://localhost:8080"
	Interval time.Duration `yaml:"interval"`
	UseSSE   bool          `yaml:"use_sse"`  // subscribe to SSE stream
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

// Binary represents a software binary that provides features
type Binary struct {
	Name          string        `yaml:"name"`
	Endpoint      string        `yaml:"endpoint"`  // e.g., "http://localhost:9090"
	Features      []Feature     `yaml:"features"`
	GherkinFiles  []string      `yaml:"gherkin_files"`  // .feature files this binary satisfies
	Interval      time.Duration `yaml:"interval"`
	Timeout       time.Duration `yaml:"timeout"`
}

// Feature represents a feature provided by a binary
type Feature struct {
	Name         string `yaml:"name"`
	GherkinFile  string `yaml:"gherkin_file"`   // path to .feature file
	GherkinFeature string `yaml:"gherkin_feature"` // feature name in Gherkin
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
