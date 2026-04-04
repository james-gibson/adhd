package discovery

import "context"

// ServiceType is the mDNS service type smoke-alarm instances advertise.
const ServiceType = "_smoke-alarm._tcp"

// Instance is a discovered or departed mDNS service instance.
type Instance struct {
	Hostname string
	Addr     string
	Port     int
	// Removed is true when the instance has deregistered its mDNS record.
	Removed bool
}

// Browser browses for mDNS service instances.
// Implementations must send on the returned channel until ctx is cancelled,
// then close it. The browse loop must never close after the first result batch.
type Browser interface {
	Browse(ctx context.Context, service string) (<-chan Instance, error)
}
