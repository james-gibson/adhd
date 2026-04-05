package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/demo"
	"github.com/james-gibson/adhd/internal/discovery"
	"github.com/james-gibson/adhd/internal/lights"
	"github.com/james-gibson/adhd/internal/smokelink"
)

// CapabilityVerifiedMsg is delivered when a runtime capability has been
// exercised and its outcome is known. It drives feature lights for the
// matching @domain-* tag, providing live 42i certification evidence
// between peers.
//
// Domain is the suffix of the @domain-<Domain> Gherkin tag on the feature
// file (e.g. "discovery", "smoke-alarm-network", "demo", "headless").
// Status green means the capability is verified working at this instant;
// red means the capability failed; dark means it has not yet been observed.
type CapabilityVerifiedMsg struct {
	Domain  string
	Status  lights.Status
	Details string
}

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

// ClusterRegistryUpdateMsg is delivered when the lezz demo cluster registry
// at /cluster has new entries since the last poll. NewEndpoints contains the
// SmokeAlarmEndpoints derived from newly-seen clusters; NewNames is the list
// of cluster names that should be marked as known after this update.
type ClusterRegistryUpdateMsg struct {
	NewEndpoints []config.SmokeAlarmEndpoint
	NewNames     []string
	RegistryURL  string
}

// pollClusterRegistry returns a Cmd that sleeps for registryPollInterval, then
// fetches the /cluster registry and returns a ClusterRegistryUpdateMsg with any
// clusters that are not already in knownNames. Re-arm it after every delivery.
//
// The knownNames map is copied by value so it is safe to pass m.knownClusterNames
// directly — mutations in Update() don't race with the blocking goroutine.
const registryPollInterval = 5 * time.Second

func pollClusterRegistry(registryURL string, knownNames map[string]bool) tea.Cmd {
	// Snapshot current known names so the goroutine carries its own copy.
	snapshot := make(map[string]bool, len(knownNames))
	for k := range knownNames {
		snapshot[k] = true
	}
	return func() tea.Msg {
		time.Sleep(registryPollInterval)
		clusters, err := demo.FetchRegistry(context.Background(), registryURL)
		if err != nil {
			// Registry temporarily unreachable — re-arm with no new endpoints.
			return ClusterRegistryUpdateMsg{RegistryURL: registryURL}
		}
		var newEndpoints []config.SmokeAlarmEndpoint
		var newNames []string
		for _, c := range clusters {
			if snapshot[c.Name] {
				continue
			}
			newNames = append(newNames, c.Name)
			if c.AlarmA != "" {
				newEndpoints = append(newEndpoints, config.SmokeAlarmEndpoint{
					Name:     c.Name + "/alarm-a",
					Endpoint: c.AlarmA,
					Interval: 10 * time.Second,
				})
			}
			if c.AlarmB != "" {
				newEndpoints = append(newEndpoints, config.SmokeAlarmEndpoint{
					Name:     c.Name + "/alarm-b",
					Endpoint: c.AlarmB,
					Interval: 10 * time.Second,
				})
			}
		}
		return ClusterRegistryUpdateMsg{
			NewEndpoints: newEndpoints,
			NewNames:     newNames,
			RegistryURL:  registryURL,
		}
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

// probeHealthz returns a Cmd that immediately GETs /healthz on the endpoint.
// On HTTP 200 it emits CapabilityVerifiedMsg{Domain:"smoke-alarm-network"};
// on any error or non-200 it returns nil (silent — the watcher will report
// the real health once its first poll fires).
func probeHealthz(name, endpoint string) tea.Cmd {
	return func() tea.Msg {
		url := strings.TrimSuffix(endpoint, "/") + "/healthz"
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(url) //nolint:noctx
		if err != nil {
			return nil
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		return CapabilityVerifiedMsg{
			Domain:  "smoke-alarm-network",
			Status:  lights.StatusGreen,
			Details: fmt.Sprintf("%s /healthz OK", name),
		}
	}
}
