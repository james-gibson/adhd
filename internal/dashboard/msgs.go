package dashboard

import (
	"github.com/james-gibson/adhd/internal/discovery"
	"github.com/james-gibson/adhd/internal/smokelink"

	tea "github.com/charmbracelet/bubbletea"
)

// SmokeAlarmDiscoveredMsg is delivered into the Bubble Tea update cycle when
// the mDNS browser finds a new smoke-alarm instance. model.Update is the only
// place lights are added from discovery.
type SmokeAlarmDiscoveredMsg struct {
	Hostname string
	Addr     string
	Port     int
}

// SmokeAlarmRemovedMsg is delivered when a smoke-alarm instance deregisters
// its mDNS record. model.Update removes the light from the cluster.
type SmokeAlarmRemovedMsg struct {
	Hostname string
}

// waitForLightUpdate returns a Cmd that blocks until the next LightUpdate arrives
// from the smokelink watcher, then delivers it into the Bubble Tea update cycle.
// Callers re-issue waitForLightUpdate after each message to keep the loop alive.
func waitForLightUpdate(ch <-chan smokelink.LightUpdate) tea.Cmd {
	return func() tea.Msg {
		update, ok := <-ch
		if !ok {
			return nil
		}
		return update
	}
}

// waitForDiscovery returns a Cmd that blocks until the next instance arrives on
// ch, then converts it to a SmokeAlarmDiscoveredMsg or SmokeAlarmRemovedMsg.
// Callers re-issue waitForDiscovery after each message to keep the loop alive.
func waitForDiscovery(ch <-chan discovery.Instance) tea.Cmd {
	return func() tea.Msg {
		inst, ok := <-ch
		if !ok {
			return nil
		}
		if inst.Removed {
			return SmokeAlarmRemovedMsg{Hostname: inst.Hostname}
		}
		return SmokeAlarmDiscoveredMsg{
			Hostname: inst.Hostname,
			Addr:     inst.Addr,
			Port:     inst.Port,
		}
	}
}
