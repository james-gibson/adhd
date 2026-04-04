package lights

import (
	"sync"
	"time"
)

// Status represents the current state of a light
type Status string

const (
	StatusGreen  Status = "green"
	StatusRed    Status = "red"
	StatusYellow Status = "yellow"
	StatusDark   Status = "dark"
)

// Light represents a single status indicator
type Light struct {
	Name       string            // Display name (e.g., "primary-alarm")
	Type       string            // Category (e.g., "feature", "smoke-alarm", "mcp-direct", "skill")
	Source     string            // Source system (e.g., "gherkin", "smoke-alarm", "mcp-probe")
	Command    string            // Optional command to execute when activated
	SourceMeta map[string]string // Provenance metadata (e.g., instance name, target ID)

	mu          sync.RWMutex // protects Status, Details, LastUpdated
	Status      Status       // Current status
	Details     string       // Optional detailed status message
	LastUpdated time.Time    // When this light was last updated
}

// New creates a new light with a given name and type
func New(name, lightType string) *Light {
	return &Light{
		Name:        name,
		Type:        lightType,
		Status:      StatusDark,
		LastUpdated: time.Now(),
	}
}

// SetStatus updates the light's status and timestamp
func (l *Light) SetStatus(status Status) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Status = status
	l.LastUpdated = time.Now()
}

// SetDetails sets the detailed status message
func (l *Light) SetDetails(details string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Details = details
}

// GetStatus returns the current status.
func (l *Light) GetStatus() Status {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Status
}

// GetDetails returns the current details message.
func (l *Light) GetDetails() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.Details
}

// GetLastUpdated returns the timestamp of the last status change.
func (l *Light) GetLastUpdated() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.LastUpdated
}

// Cluster manages a collection of lights
type Cluster struct {
	mu     sync.RWMutex
	lights []*Light
}

// NewCluster creates an empty light cluster
func NewCluster() *Cluster {
	return &Cluster{
		lights: []*Light{},
	}
}

// Add appends a light to the cluster
func (c *Cluster) Add(light *Light) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lights = append(c.lights, light)
}

// All returns a snapshot copy of all lights in the cluster
func (c *Cluster) All() []*Light {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Light, len(c.lights))
	copy(out, c.lights)
	return out
}

// GetByName finds a light by name
func (c *Cluster) GetByName(name string) *Light {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, light := range c.lights {
		if light.Name == name {
			return light
		}
	}
	return nil
}

// Count returns the number of lights
func (c *Cluster) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.lights)
}

// Remove removes a light by name. Returns true if found and removed.
func (c *Cluster) Remove(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, light := range c.lights {
		if light.Name == name {
			c.lights = append(c.lights[:i], c.lights[i+1:]...)
			return true
		}
	}
	return false
}

// CountByStatus returns the number of lights with a given status
func (c *Cluster) CountByStatus(status Status) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	count := 0
	for _, light := range c.lights {
		if light.GetStatus() == status {
			count++
		}
	}
	return count
}
