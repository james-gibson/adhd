package config

import "testing"

func TestEndpointNameReadsABareNameAsARegistryName(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
		ok       bool
	}{
		{endpoint: "workspace-42", want: "workspace-42", ok: true},
		{endpoint: "adhd.lab_2", want: "adhd.lab_2", ok: true},
		{endpoint: "  workspace-42  ", want: "workspace-42", ok: true},

		// A mistyped URL stays a URL and fails as one: reading it as a name
		// would report "not in the registry" for what is really a typo.
		{endpoint: "http://127.0.0.1:9090/mcp"},
		{endpoint: "https://api.example.com/mcp"},
		{endpoint: "localhost:9090"},
		{endpoint: ":9090/mcp"},
		{endpoint: "127.0.0.1:64194"},
		{endpoint: "workspace/42"},
		{endpoint: ""},
	}

	for _, tc := range cases {
		got, ok := Binary{Endpoint: tc.endpoint}.EndpointName()
		if ok != tc.ok || got != tc.want {
			t.Errorf("Binary{Endpoint: %q}.EndpointName() = (%q, %v), want (%q, %v)",
				tc.endpoint, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRegistryEndpointPrefersAConfiguredAlarm(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.RegistryEndpoint(); got != "" {
		t.Errorf("no alarm configured: RegistryEndpoint() = %q, want empty", got)
	}

	cfg.SmokeAlarm = []SmokeAlarmEndpoint{
		{Name: "first", Endpoint: "http://127.0.0.1:6100"},
		{Name: "second", Endpoint: "http://127.0.0.1:6200"},
	}
	if got, want := cfg.RegistryEndpoint(), "http://127.0.0.1:6100"; got != want {
		t.Errorf("RegistryEndpoint() = %q, want the first alarm %q", got, want)
	}

	// A probe target is a fallback only when no alarm is configured: it may be
	// a discovery name rather than an HTTP endpoint.
	cfg.Health.RemoteSmokeAlarm = "http://_smoke-alarm._tcp"
	if got, want := cfg.RegistryEndpoint(), "http://127.0.0.1:6100"; got != want {
		t.Errorf("RegistryEndpoint() = %q, want the configured alarm %q", got, want)
	}

	cfg.SmokeAlarm = nil
	if got, want := cfg.RegistryEndpoint(), "http://_smoke-alarm._tcp"; got != want {
		t.Errorf("RegistryEndpoint() = %q, want the probe target %q", got, want)
	}
}
