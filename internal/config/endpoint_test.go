package config

import (
	"net"
	"testing"
)

// TestEndpointIdentityCollapsesOneRoute covers the spellings a self-join, a
// registry poll and a hand-written config can use for one alarm URL. They must
// resolve to one identity, otherwise the watcher starts a second poller and the
// same probe becomes two lights (adhd#49).
func TestEndpointIdentityCollapsesOneRoute(t *testing.T) {
	cases := [][2]string{
		{"http://127.0.0.1:5100", "http://127.0.0.1:5100/"},
		{"http://127.0.0.1:5100", "http://127.0.0.1:5100/status"},
		{"http://127.0.0.1:5100", "127.0.0.1:5100"},
		{"http://Alarm.Example:5100", "HTTP://alarm.example:5100"},
		{"https://alarm.example:443", "https://alarm.example"},
	}
	for _, pair := range cases {
		a, b := EndpointIdentity(pair[0]), EndpointIdentity(pair[1])
		if a == "" || a != b {
			t.Errorf("EndpointIdentity(%q) = %q, EndpointIdentity(%q) = %q; want one identity",
				pair[0], a, pair[1], b)
		}
	}
}

// TestEndpointIdentityKeepsTheHost is the boundary of the dedupe: two local
// addresses on one port can be different processes bound to different
// interfaces, so an alarm endpoint is not folded onto "local" the way an MCP
// listener is. Collapsing them would silently drop one alarm.
func TestEndpointIdentityKeepsTheHost(t *testing.T) {
	a := EndpointIdentity("http://127.0.0.1:5100")
	b := EndpointIdentity("http://192.0.2.10:5100")
	if a == "" || a == b {
		t.Fatalf("loopback and LAN identities must differ: %q vs %q", a, b)
	}
	if a == EndpointIdentity("http://127.0.0.1:5101") {
		t.Error("a different port must be a different route")
	}
}

func TestEndpointIdentityEmptyInput(t *testing.T) {
	if got := EndpointIdentity("  "); got != "" {
		t.Errorf("EndpointIdentity(blank) = %q, want empty", got)
	}
}

// TestSameListenerFoldsThisMachine is the self-join test. lezz advertises the
// outbound IP while the bind is a wildcard or loopback; a literal URL compare
// would never match, and the self-join would double every light.
func TestSameListenerFoldsThisMachine(t *testing.T) {
	same := [][2]string{
		{":9090", "http://127.0.0.1:9090/mcp"},
		{":9090", "http://localhost:9090"},
		{":9090", "http://0.0.0.0:9090/mcp"},
		{"127.0.0.1:9090", "http://127.0.0.1:9090/mcp"},
	}
	for _, pair := range same {
		if !SameListener(pair[0], pair[1]) {
			t.Errorf("SameListener(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
}

// TestSameListenerFoldsTheOutboundAddress is the exact shape lezz produces: the
// bind is a wildcard, the advertised adhd_mcp names the machine's outbound IP.
// A literal string compare would never see the self-join.
func TestSameListenerFoldsTheOutboundAddress(t *testing.T) {
	ip := outboundIPv4()
	if ip == "" {
		t.Skip("no non-loopback IPv4 address on this host")
	}
	if !SameListener(":9090", "http://"+ip+":9090/mcp") {
		t.Errorf("SameListener(%q, %q) = false, want true", ":9090", "http://"+ip+":9090/mcp")
	}
	if SameListener("127.0.0.1:9090", "http://"+ip+":9091/mcp") {
		t.Error("a different port on the same host is another listener")
	}
}

// outboundIPv4 returns one non-loopback IPv4 address of this host, or "".
func outboundIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if v4 := ipNet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	return ""
}

func TestSameListenerRejectsAnotherListener(t *testing.T) {
	cases := [][2]string{
		{":9090", "http://192.0.2.55:9090/mcp"}, // another machine, same port
		{":9090", ":9091"},
		{":9090", "http://127.0.0.1:9091/mcp"},
		{"", ":9090"},
	}
	for _, pair := range cases {
		if SameListener(pair[0], pair[1]) {
			t.Errorf("SameListener(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}
