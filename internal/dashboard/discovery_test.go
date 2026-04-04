package dashboard

import (
	"context"
	"testing"

	"github.com/james-gibson/adhd/internal/discovery"
	"github.com/james-gibson/adhd/internal/lights"
)

// fakeBrowser is a test double for discovery.Browser.
// It emits the provided instances in order and then blocks until the context
// is cancelled, keeping the browse loop alive (matching the real Browser contract).
type fakeBrowser struct {
	ch chan discovery.Instance
}

func newFakeBrowser(instances ...discovery.Instance) *fakeBrowser {
	ch := make(chan discovery.Instance, len(instances))
	for _, inst := range instances {
		ch <- inst
	}
	return &fakeBrowser{ch: ch}
}

func (f *fakeBrowser) Browse(_ context.Context, _ string) (<-chan discovery.Instance, error) {
	return f.ch, nil
}

// newTestModel returns a minimal BubbleTeaDashboard suitable for Update() testing.
// It has an empty cluster and no real mDNS browser running.
func newTestModel() *BubbleTeaDashboard {
	return &BubbleTeaDashboard{
		lights: lights.NewCluster(),
		ctx:    context.Background(),
	}
}

// newTestModelWithBrowser wires up a fake browser and sets m.instances, matching
// what Init() would do, so re-issued waitForDiscovery cmds are exercisable.
func newTestModelWithBrowser(fb *fakeBrowser) *BubbleTeaDashboard {
	m := newTestModel()
	ch, _ := fb.Browse(m.ctx, discovery.ServiceType)
	m.browser = fb
	m.instances = ch
	return m
}

// ── light creation ────────────────────────────────────────────────────────────

// Scenario: light created for a smoke-alarm that comes online after startup
func TestDiscoveryCreatesLight(t *testing.T) {
	m := newTestModel()
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-c", Addr: "192.168.1.3", Port: 80})
	if m.lights.GetByName("smoke-alarm:host-c") == nil {
		t.Fatal("expected light smoke-alarm:host-c to be created")
	}
}

// Scenario: light's status is "dark" immediately after creation
func TestDiscoveredLightStartsDark(t *testing.T) {
	m := newTestModel()
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-a"})
	l := m.lights.GetByName("smoke-alarm:host-a")
	if l == nil {
		t.Fatal("light not created")
	}
	if l.GetStatus() != lights.StatusDark {
		t.Errorf("expected status dark, got %v", l.GetStatus())
	}
}

// Scenario: light's source is "mdns"
func TestDiscoveredLightSourceIsMdns(t *testing.T) {
	m := newTestModel()
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-a"})
	l := m.lights.GetByName("smoke-alarm:host-a")
	if l == nil {
		t.Fatal("light not created")
	}
	if l.Source != "mdns" {
		t.Errorf("expected source mdns, got %q", l.Source)
	}
}

// Scenario: multiple smoke-alarms at startup each receive their own light
func TestDiscoveryMultipleSmokeAlarms(t *testing.T) {
	m := newTestModel()
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-1"})
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-2"})
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-3"})

	for _, h := range []string{"host-1", "host-2", "host-3"} {
		if m.lights.GetByName("smoke-alarm:"+h) == nil {
			t.Errorf("expected light smoke-alarm:%s to exist", h)
		}
	}
	// Each light has a distinct name derived from its hostname
	if m.lights.Count() != 3 {
		t.Errorf("expected 3 lights, got %d", m.lights.Count())
	}
}

// ── coexistence with static config ────────────────────────────────────────────

// Scenario: mDNS does not create a duplicate light for a statically-configured instance
func TestDiscoveryNoDuplicateForConfigLight(t *testing.T) {
	m := newTestModel()

	// Pre-add a config-sourced light for host-a
	existing := lights.New("smoke-alarm:host-a", "smoke-alarm")
	existing.Source = "config"
	m.lights.Add(existing)

	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-a"})

	if m.lights.Count() != 1 {
		t.Errorf("expected 1 light (no duplicate), got %d", m.lights.Count())
	}
	if m.lights.GetByName("smoke-alarm:host-a").Source != "config" {
		t.Error("existing config light's source should remain 'config'")
	}
}

// Scenario: mDNS-discovered lights coexist with statically-configured lights
func TestDiscoveryCoexistsWithStaticLights(t *testing.T) {
	m := newTestModel()

	for _, name := range []string{"static-1", "static-2"} {
		l := lights.New(name, "smoke-alarm")
		l.Source = "config"
		m.lights.Add(l)
	}

	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-new"})

	if m.lights.Count() != 3 {
		t.Errorf("expected 3 lights, got %d", m.lights.Count())
	}
	for _, name := range []string{"static-1", "static-2"} {
		if m.lights.GetByName(name).Source != "config" {
			t.Errorf("static light %q source changed", name)
		}
	}
}

// ── instance departure ─────────────────────────────────────────────────────

// Scenario: light is removed when a smoke-alarm deregisters its mDNS record
func TestRemovalRemovesMdnsLight(t *testing.T) {
	m := newTestModel()
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "host-b"})
	if m.lights.GetByName("smoke-alarm:host-b") == nil {
		t.Fatal("precondition: light not created")
	}

	m.Update(SmokeAlarmRemovedMsg{Hostname: "host-b"})

	if m.lights.GetByName("smoke-alarm:host-b") != nil {
		t.Error("expected light to be removed after mDNS deregistration")
	}
}

// Scenario: mDNS deregistration does not remove a statically-configured light
func TestRemovalLeavesConfigLight(t *testing.T) {
	m := newTestModel()
	l := lights.New("smoke-alarm:host-a", "smoke-alarm")
	l.Source = "config"
	m.lights.Add(l)

	m.Update(SmokeAlarmRemovedMsg{Hostname: "host-a"})

	if m.lights.GetByName("smoke-alarm:host-a") == nil {
		t.Error("config-sourced light must not be removed by mDNS deregistration")
	}
}

// ── Bubble Tea loop continuity ─────────────────────────────────────────────

// Scenario: model.Update is the only place lights are added from discovery
// Scenario: discovery events are delivered as Bubble Tea messages
func TestDiscoveryViaWaitForDiscoveryCmd(t *testing.T) {
	fb := newFakeBrowser(
		discovery.Instance{Hostname: "host-c", Addr: "10.0.0.1", Port: 80},
	)
	m := newTestModelWithBrowser(fb)

	// Simulate what the Bubble Tea runtime does: run the cmd, deliver its message.
	cmd := waitForDiscovery(m.instances)
	msg := cmd() // blocks until the buffered instance arrives
	m.Update(msg)

	if m.lights.GetByName("smoke-alarm:host-c") == nil {
		t.Fatal("expected light created from waitForDiscovery pipeline")
	}
}

// Scenario: browse does not close after the first result batch —
// waitForDiscovery re-issued after each message keeps the loop alive.
func TestDiscoveryLoopReissuedAfterEachMessage(t *testing.T) {
	fb := newFakeBrowser(
		discovery.Instance{Hostname: "host-1"},
		discovery.Instance{Hostname: "host-2"},
	)
	m := newTestModelWithBrowser(fb)

	// First message
	cmd1 := waitForDiscovery(m.instances)
	_, nextCmd := m.Update(cmd1())
	if nextCmd == nil {
		t.Fatal("Update must re-issue waitForDiscovery after a discovery message")
	}
	if m.lights.GetByName("smoke-alarm:host-1") == nil {
		t.Error("expected host-1 light after first message")
	}

	// Second message — delivered via the re-issued cmd
	cmd2 := nextCmd
	m.Update(cmd2())
	if m.lights.GetByName("smoke-alarm:host-2") == nil {
		t.Error("expected host-2 light after second message")
	}
}

// Scenario: light name is derived from the hostname
func TestLightNameDerivedFromHostname(t *testing.T) {
	m := newTestModel()
	m.Update(SmokeAlarmDiscoveredMsg{Hostname: "alarm-primary"})
	if m.lights.GetByName("smoke-alarm:alarm-primary") == nil {
		t.Error("light name must be smoke-alarm:<hostname>")
	}
}
