package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adhd.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStdioTargetDefaultsAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adhd.yaml")
	body := `
mcp_server:
  enabled: true
stdio_targets:
  - name: qdrant
    command: uvx
    args: ["mcp-server-qdrant"]
    inherit_env: [PATH]
    cwd: ./work
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.StdioTargets) != 1 {
		t.Fatalf("targets = %d, want 1", len(cfg.StdioTargets))
	}
	target := cfg.StdioTargets[0]
	if target.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s, want 30s", target.Timeout)
	}
	if target.Cwd != filepath.Join(dir, "work") {
		t.Fatalf("cwd = %q, want it resolved next to the config", target.Cwd)
	}
}

func TestStdioTargetValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "duplicate name",
			body: "stdio_targets:\n  - name: a\n    command: x\n  - name: a\n    command: y\n",
			want: "duplicate name",
		},
		{
			name: "empty name",
			body: "stdio_targets:\n  - name: \"\"\n    command: x\n",
			want: "name is required",
		},
		{
			name: "name with a colon",
			body: "stdio_targets:\n  - name: 'stdio:a'\n    command: x\n",
			want: "bare token",
		},
		{
			name: "missing command",
			body: "stdio_targets:\n  - name: a\n    command: \"\"\n",
			want: "command is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestGateDefaults(t *testing.T) {
	path := writeConfig(t, `
mcp_server:
  enabled: true
  gate:
    enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MCPServer.Gate.Period != 30*time.Second {
		t.Fatalf("period = %s, want 30s", cfg.MCPServer.Gate.Period)
	}
	if !strings.HasSuffix(cfg.MCPServer.Gate.KeyFile, filepath.Join(".config", "adhd", "gate.key")) {
		t.Fatalf("key file = %q, want the default under ~/.config/adhd", cfg.MCPServer.Gate.KeyFile)
	}
}

func TestGateKeyFileIsResolvedNextToTheConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adhd.yaml")
	body := `
mcp_server:
  enabled: true
  gate:
    enabled: true
    key_file: ./gate.key
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MCPServer.Gate.KeyFile != filepath.Join(dir, "gate.key") {
		t.Fatalf("key file = %q, want it resolved against the config directory", cfg.MCPServer.Gate.KeyFile)
	}
}
