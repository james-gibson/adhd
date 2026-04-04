package discovery

import (
	"context"
	"log/slog"

	"github.com/grandcat/zeroconf"
)

// ZeroconfBrowser implements Browser using grandcat/zeroconf.
// It browses continuously for the lifetime of the provided context —
// the browse loop never closes after the first result batch.
type ZeroconfBrowser struct{}

// NewBrowser creates a ZeroconfBrowser.
func NewBrowser() *ZeroconfBrowser {
	return &ZeroconfBrowser{}
}

// Browse starts a continuous mDNS browse for service (e.g. "_smoke-alarm._tcp").
// Discovered instances are sent on the returned channel until ctx is cancelled.
// grandcat/zeroconf does not deliver removal notifications; callers should treat
// health-check failure as the departure signal in the absence of an mDNS goodbye.
func (b *ZeroconfBrowser) Browse(ctx context.Context, service string) (<-chan Instance, error) {
	entries := make(chan *zeroconf.ServiceEntry)
	out := make(chan Instance, 16)

	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return nil, err
	}

	// Browse runs until ctx is cancelled. grandcat/zeroconf keeps the socket
	// open for the duration, so late-joining instances are discovered without restart.
	go func() {
		if err := resolver.Browse(ctx, service, "local.", entries); err != nil {
			slog.Error("mdns browse error", "service", service, "error", err)
		}
	}()

	go func() {
		defer close(out)
		for {
			select {
			case entry, ok := <-entries:
				if !ok {
					return
				}
				hostname := entry.HostName
				if len(hostname) > 0 && hostname[len(hostname)-1] == '.' {
					hostname = hostname[:len(hostname)-1]
				}
				addr := ""
				if len(entry.AddrIPv4) > 0 {
					addr = entry.AddrIPv4[0].String()
				} else if len(entry.AddrIPv6) > 0 {
					addr = entry.AddrIPv6[0].String()
				}
				select {
				case out <- Instance{Hostname: hostname, Addr: addr, Port: entry.Port}:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}
