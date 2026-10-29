package headless

import (
	"testing"
	"time"
)

// TestClusterAttestationNeedsBothVariables: half a cluster identity is no
// identity. Without both, the instance registers unattested and the registry
// puts it at the floor — which is the honest answer, not an error (isotope#23).
func TestClusterAttestationNeedsBothVariables(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster string
		secret  string
	}{
		{"neither", "", ""},
		{"id only", "demo-1", ""},
		{"secret only", "", "s3cret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvClusterID, tc.cluster)
			t.Setenv(EnvClusterSecret, tc.secret)
			if att := clusterAttestation("127.0.0.1:9090"); att != nil {
				t.Fatalf("attestation = %+v, want nil", att)
			}
		})
	}
}

// TestClusterAttestationVerifiesForTheEndpoint is the positive case: lezz hands
// the instance a cluster id and secret, and the receipt it signs is exactly what
// the alarm's verifier recomputes for that endpoint.
func TestClusterAttestationVerifiesForTheEndpoint(t *testing.T) {
	t.Setenv(EnvClusterID, "demo-1234")
	t.Setenv(EnvClusterSecret, "launch-secret")

	att := clusterAttestation("127.0.0.1:9090")
	if att == nil {
		t.Fatal("no attestation with both variables set")
	}
	if att.Cluster != "demo-1234" {
		t.Fatalf("cluster = %q, want demo-1234", att.Cluster)
	}
	if err := att.Verify("launch-secret", "127.0.0.1:9090", time.Now(), 0); err != nil {
		t.Fatalf("receipt does not verify: %v", err)
	}
	// The MAC binds the endpoint: the same receipt is useless for another one.
	if err := att.Verify("launch-secret", "127.0.0.1:9091", time.Now(), 0); err == nil {
		t.Fatal("receipt verified for a different endpoint")
	}
	// And it is not valid forever: the verifier's skew window is minutes, not
	// hours, so the heartbeat must re-sign (it does — every registration).
	if err := att.Verify("launch-secret", "127.0.0.1:9090", time.Now().Add(10*time.Minute), 0); err == nil {
		t.Fatal("stale receipt accepted")
	}
}

// TestClusterAttestationIsOnTheWire: the receipt travels in the registration the
// alarm reads, not in a header of adhd's own invention.
func TestClusterAttestationIsOnTheWire(t *testing.T) {
	t.Setenv(EnvClusterID, "demo-1234")
	t.Setenv(EnvClusterSecret, "launch-secret")

	att := clusterAttestation(":9090")
	if att == nil {
		t.Fatal("no attestation")
	}
	// A wildcard bind is what adhd signs when --mcp-addr has no host, and the
	// verifier treats it as "every address on this host" — so the receipt for
	// ":9090" must be the one a registry entry of 127.0.0.1:9090 is judged with.
	if err := att.Verify("launch-secret", ":9090", time.Now(), 0); err != nil {
		t.Fatalf("wildcard-endpoint receipt does not verify: %v", err)
	}
}
