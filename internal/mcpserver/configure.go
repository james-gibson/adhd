package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/entitle"
	"github.com/james-gibson/adhd/internal/stdio"
)

// Attach wires the optional surfaces a configuration asks for: the inbound
// entitlement gate (adhd#27) and the stdio transport (adhd#41).
//
// It is one function so the TUI and headless paths cannot drift: a surface that
// exists in one deployment shape and not the other is a surface an operator
// believes is there.
func Attach(s *Server, cfg *config.Config) error {
	if cfg == nil {
		return nil
	}

	if cfg.MCPServer.Gate.Enabled {
		ring, err := loadOrCreateKeyring(cfg.MCPServer.Gate.KeyFile)
		if err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		gate, err := entitle.NewGate(ring, cfg.MCPServer.Gate.Period)
		if err != nil {
			return fmt.Errorf("gate: %w", err)
		}
		s.SetGate(gate)
		slog.Info("inbound entitlement gate enabled",
			"key_file", cfg.MCPServer.Gate.KeyFile,
			"keys", ring.Len(),
			"max_rung", ring.MaxRung(),
			"period", cfg.MCPServer.Gate.Period,
		)
	}

	if len(cfg.StdioTargets) > 0 {
		targets := make([]stdio.Target, 0, len(cfg.StdioTargets))
		for _, target := range cfg.StdioTargets {
			targets = append(targets, stdio.Target{
				Name:         target.Name,
				Command:      target.Command,
				Args:         target.Args,
				Env:          target.Env,
				InheritEnv:   target.InheritEnv,
				Dir:          target.Cwd,
				Timeout:      target.Timeout,
				AllowedTools: target.AllowedTools,
			})
		}
		manager := stdio.NewManager(context.Background(), targets)
		s.SetStdioManager(manager)
		slog.Info("stdio MCP transport enabled", "targets", len(targets))
	}

	return nil
}

// loadOrCreateKeyring reads the configured keyring, creating a read/write/exec
// ring on first use. A gate with no keyring admits nobody, and that looks
// exactly like an outage; creating one and naming its path in the log is the
// operator-visible version of that choice.
func loadOrCreateKeyring(path string) (*entitle.Keyring, error) {
	if path == "" {
		return nil, errors.New("gate is enabled but no key_file is configured")
	}
	ring, err := entitle.LoadKeyring(path)
	if err == nil {
		return ring, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ring, err = entitle.GenerateKeyring(path)
	if err != nil {
		return nil, err
	}
	slog.Warn("created a gate keyring; issue its keys to the callers you intend to admit",
		"path", path)
	return ring, nil
}
