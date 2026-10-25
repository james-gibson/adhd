package headless

// adhd#48: headless adhd advertised a fixed mDNS instance name ("adhd"), so two
// instances on the same link were indistinguishable to any browser that keys by
// name, and the smoke-alarm registry — keyed by name too — kept only the last
// registration. The instance label must identify the instance.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/isotope"
)

// dnsSafeLabel matches the characters a DNS-SD instance label may carry out of
// the base64url alphabet the identity uses.
var dnsSafeLabel = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TestInstanceLabelIsUniquePerInstance is the regression pin: two headless
// servers must not name themselves the same thing.
func TestInstanceLabelIsUniquePerInstance(t *testing.T) {
	a := New(&config.Config{})
	b := New(&config.Config{})

	first, second := a.instanceLabel(), b.instanceLabel()

	if first == second {
		t.Fatalf("two instances produced the same label %q; a shared name is the defect", first)
	}
	if first == "adhd" || second == "adhd" {
		t.Fatalf("labels %q/%q look like the old constant, which identifies no instance", first, second)
	}
	for _, label := range []string{first, second} {
		if !strings.HasPrefix(label, instanceLabelPrefix) {
			t.Errorf("label %q does not start with %q", label, instanceLabelPrefix)
		}
		if len(label) > 63 {
			t.Errorf("label %q is %d bytes; DNS-SD instance labels cap at 63", label, len(label))
		}
		if !dnsSafeLabel.MatchString(label) {
			t.Errorf("label %q contains characters a service browser may not round-trip", label)
		}
	}
}

// TestInstanceLabelIsStableForProcessLifetime keeps discovery sane: the same
// instance must keep one name, or every lookup looks like a new node.
func TestInstanceLabelIsStableForProcessLifetime(t *testing.T) {
	s := New(&config.Config{})

	first := s.instanceLabel()
	for i := 0; i < 5; i++ {
		if got := s.instanceLabel(); got != first {
			t.Fatalf("label changed between calls: %q then %q", first, got)
		}
	}
}

// TestInstanceLabelWithoutIdentity documents the defensive branch: no identity
// still must not produce the bare constant.
func TestInstanceLabelWithoutIdentity(t *testing.T) {
	s := &Server{}

	label := s.instanceLabel()
	if label == "adhd" {
		t.Fatalf("label fell back to the constant %q", label)
	}
	if !strings.HasPrefix(label, instanceLabelPrefix) || !dnsSafeLabel.MatchString(label) {
		t.Fatalf("fallback label %q is not a usable instance label", label)
	}
}

// TestRegisterAsIsotopeNamesRegistryAndMDNSSurfaces checks the two places the
// label is published: the smoke-alarm registration and the mDNS advertisement
// must carry the same name — and that name must differ between instances, or
// the registry overwrites one with the other.
func TestRegisterAsIsotopeNamesRegistryAndMDNSSurfaces(t *testing.T) {
	registered := make(chan string, 4)
	alarm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/isotope/register" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var reg isotope.Registration
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			t.Errorf("decode registration: %v", err)
		}
		registered <- reg.Name
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(isotope.Record{Name: reg.Name, TrustRung: 3})
	}))
	defer alarm.Close()

	advertised := make(chan string, 4)
	restore := stubAdvertise(t, func(instance string) { advertised <- instance })

	// Loopback would let the advertise guard pass on its own; force keeps this
	// test about naming and not about whether something answers on the port.
	server := New(&config.Config{MCPServer: config.MCPServerConfig{Enabled: true, Addr: "0.0.0.0:1"}})
	server.SetForceAdvertise(true)
	if err := server.RegisterAsIsotope(alarm.URL); err != nil {
		t.Fatalf("RegisterAsIsotope: %v", err)
	}

	registryName := <-registered
	mdnsName := <-advertised
	if registryName != mdnsName {
		t.Fatalf("registry name %q and mDNS instance %q differ; a consumer cannot correlate them", registryName, mdnsName)
	}
	if registryName != server.instanceLabel() {
		t.Fatalf("published name %q is not the instance label %q", registryName, server.instanceLabel())
	}

	// A second instance against the same alarm must be a second entry.
	other := New(&config.Config{MCPServer: config.MCPServerConfig{Enabled: true, Addr: "0.0.0.0:2"}})
	other.SetForceAdvertise(true)
	if err := other.RegisterAsIsotope(alarm.URL); err != nil {
		t.Fatalf("second RegisterAsIsotope: %v", err)
	}
	if secondName := <-registered; secondName == registryName {
		t.Fatalf("two instances registered under the same name %q; the registry keeps only the last", secondName)
	}

	restore()
}

// TestRegisterIsotopeWithRoleRejectsEmptyName pins the fail-loud behavior that
// replaced the silent constant.
func TestRegisterIsotopeWithRoleRejectsEmptyName(t *testing.T) {
	if _, err := RegisterIsotopeWithRole(context.Background(), "http://127.0.0.1:1", RolePrime, "localhost:0", "  "); err == nil {
		t.Fatal("expected an error for an empty registration name")
	}
}

// stubAdvertise replaces the mDNS seam for the duration of the test.
func stubAdvertise(t *testing.T, onAdvertise func(instance string)) func() {
	t.Helper()

	original := advertiseMDNS
	advertiseMDNS = func(instance, _ string, _ string, _ string, _ int) (isotope.Server, error) {
		onAdvertise(instance)
		return stubMDNSServer{}, nil
	}
	t.Cleanup(func() { advertiseMDNS = original })
	return func() { advertiseMDNS = original }
}

type stubMDNSServer struct{}

func (stubMDNSServer) Shutdown() {}
