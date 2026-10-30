package config

import (
	"net"
	"net/url"
	"strings"
)

// This file holds two questions about a URL, kept deliberately apart because
// they are not the same question (adhd#49):
//
//   - EndpointIdentity: "would the watcher send the same request?" It is an
//     idempotency key, so one light does not get two pollers. It is not a route
//     identity and must never be used to merge two names: a name is the
//     adopter's claim, two claims can share one address by design (the
//     counter-rotating alarms do), and the same handler can also be reached
//     through a proxy at an address it does not own.
//   - SameListener: "is this the same listening socket?" It is the fallback
//     evidence that a cluster joined its own dashboard, used only when the
//     endpoint cannot be asked who it is.

// EndpointIdentity canonicalises an alarm endpoint into the request it names.
// Trailing slashes, a trailing "/status" (the watcher appends its own) and host
// case do not change the request; an empty result means the URL could not be
// parsed and the caller should fall back to the raw string.
func EndpointIdentity(raw string) string {
	scheme, host, port, path, ok := splitEndpoint(raw)
	if !ok {
		return ""
	}
	identity := scheme + "://"
	if host != "" {
		identity += host + ":" + port
	} else {
		// A wildcard or port-only endpoint is still addressable; keep the
		// port so ":5100" and ":5101" stay distinct.
		identity += ":" + port
	}
	return identity + path
}

// ListenerIdentity canonicalises an MCP bind or peer address into
// "scheme://host:port", folding every address that names this machine into one
// "local" host. It returns "" when no port can be recovered, because without a
// port there is no listener to compare.
func ListenerIdentity(raw string) string {
	scheme, host, port, _, ok := splitEndpoint(raw)
	if !ok || port == "" {
		return ""
	}
	if isLocalHost(host) {
		host = "local"
	}
	return scheme + "://" + host + ":" + port
}

// SameListener reports whether two addresses name the same listening socket on
// this machine: the same port, where wildcard, loopback and this machine's own
// interface addresses are one host. It is evidence, not proof — an identity
// probe of the endpoint itself outranks it.
func SameListener(a, b string) bool {
	ia := ListenerIdentity(a)
	return ia != "" && ia == ListenerIdentity(b)
}

// splitEndpoint parses an endpoint or a bind address. Bare addresses ("127.0.0.1:9090",
// ":9090", "localhost") are read as http, which is how adhd's own mcp_server.addr
// is written. The path is normalised: a trailing slash and a trailing "/status"
// are dropped, so the two ways of naming an alarm's status URL are one request.
func splitEndpoint(raw string) (scheme, host, port, path string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", "", false
	}
	scheme = strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	port = u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	path = strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/status")
	return scheme, host, port, path, true
}

// isLocalHost reports whether host names this machine: empty (a wildcard
// listener), "localhost", a loopback or unspecified address, or one of the
// host's own interface addresses.
func isLocalHost(host string) bool {
	switch host {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		var local net.IP
		switch a := addr.(type) {
		case *net.IPNet:
			local = a.IP
		case *net.IPAddr:
			local = a.IP
		}
		if local != nil && local.Equal(ip) {
			return true
		}
	}
	return false
}
