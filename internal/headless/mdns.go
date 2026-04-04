package headless

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/grandcat/zeroconf"
)

const isotopeService = "_adhd-isotope._tcp"

// zeroconfServer is the interface satisfied by *zeroconf.Server, used to allow
// storing it without a direct import in server.go.
type zeroconfServer interface {
	Shutdown()
}

// AdvertiseIsotope registers an mDNS record for this ADHD isotope instance so
// that smoke-alarms and other nodes on the LAN can discover it. The returned
// server must be shut down when the process exits.
//
// addr is the MCP listen address (e.g. ":9090" or "0.0.0.0:9090").
// role is the isotope topology role ("prime", "prime-plus", "standalone").
// trustRung is the 42i rung assigned by smoke-alarm.
func AdvertiseIsotope(addr, role string, trustRung int) (zeroconfServer, error) {
	port, err := extractPortInt(addr)
	if err != nil {
		return nil, fmt.Errorf("advertise isotope: %w", err)
	}

	txt := []string{
		"v=1",
		"role=" + role,
		"trust_rung=" + strconv.Itoa(trustRung),
	}

	srv, err := zeroconf.Register("adhd", isotopeService, "local.", port, txt, nil)
	if err != nil {
		return nil, fmt.Errorf("advertise isotope: zeroconf register: %w", err)
	}
	return srv, nil
}

// extractPortInt parses an address string and returns the port as an int.
func extractPortInt(addr string) (int, error) {
	// strip scheme if present
	if idx := strings.Index(addr, "://"); idx >= 0 {
		addr = addr[idx+3:]
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("non-numeric port in %q: %w", addr, err)
	}
	return port, nil
}
