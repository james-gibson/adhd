package headless

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/james-gibson/isotope"
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

// certificateFixture mints a root and the certificate it issues for `cluster`,
// returning the two environment values lezz would hand this instance.
func certificateFixture(t *testing.T, cluster string) (certEnv, keyEnv string, rootPub ed25519.PublicKey) {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root: %v", err)
	}
	clusterPub, clusterPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate cluster key: %v", err)
	}
	cert, err := isotope.SignCertificate(isotope.Certificate{
		Cluster:   cluster,
		PublicKey: base64.StdEncoding.EncodeToString(clusterPub),
		Issuer:    isotope.PublicSeedCluster,
		Scope:     []string{isotope.ScopeAttest},
	}, rootPriv)
	if err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	body, err := json.Marshal(cert)
	if err != nil {
		t.Fatalf("marshal certificate: %v", err)
	}
	return base64.StdEncoding.EncodeToString(body),
		base64.StdEncoding.EncodeToString(clusterPriv),
		rootPub
}

// TestClusterAttestationFromACertificate is the certificate handover: lezz sets
// a certificate and the key it was issued for, and the receipt this instance
// signs is checked by a verifier that holds only the root.
func TestClusterAttestationFromACertificate(t *testing.T) {
	certEnv, keyEnv, rootPub := certificateFixture(t, "demo-1234")
	t.Setenv(EnvClusterCert, certEnv)
	t.Setenv(EnvClusterKey, keyEnv)

	att := clusterAttestation("127.0.0.1:9090")
	if att == nil {
		t.Fatal("no attestation from a certificate and key")
	}
	if att.EffectiveScheme() != isotope.SchemeEd25519 {
		t.Fatalf("scheme = %q, want %q", att.EffectiveScheme(), isotope.SchemeEd25519)
	}
	if att.Cert == nil || att.Cert.Cluster != "demo-1234" {
		t.Fatalf("receipt carries the wrong certificate: %+v", att.Cert)
	}
	keyring := isotope.Keyring{Roots: map[string]ed25519.PublicKey{isotope.PublicSeedCluster: rootPub}}
	if err := att.VerifyWith(keyring, "127.0.0.1:9090", time.Now(), 0); err != nil {
		t.Fatalf("certificate receipt does not verify against the pinned root: %v", err)
	}
	// The endpoint is bound into the signature, as it is for the HMAC shape.
	if err := att.VerifyWith(keyring, "127.0.0.1:9091", time.Now(), 0); err == nil {
		t.Fatal("receipt verified for a different endpoint")
	}
}

// TestClusterAttestationPrefersTheCertificate: a cluster that has adopted
// certificates keeps its evidence even if a launch secret is also in the
// environment, because the certificate is the shape a third party can check.
func TestClusterAttestationPrefersTheCertificate(t *testing.T) {
	certEnv, keyEnv, _ := certificateFixture(t, "demo-1234")
	t.Setenv(EnvClusterCert, certEnv)
	t.Setenv(EnvClusterKey, keyEnv)
	t.Setenv(EnvClusterID, "demo-1234")
	t.Setenv(EnvClusterSecret, "launch-secret")

	att := clusterAttestation("127.0.0.1:9090")
	if att == nil {
		t.Fatal("no attestation")
	}
	if att.EffectiveScheme() != isotope.SchemeEd25519 {
		t.Fatalf("scheme = %q, want the certificate shape to win", att.EffectiveScheme())
	}
}

// TestClusterAttestationFailsClosedOnABrokenCertificate: a certificate that is
// configured but unusable must not silently fall back to a secret. An unattested
// registration is visible in the rung's evidence; a quiet downgrade to an HMAC
// nobody pinned looks like a working cluster that lost its rung.
func TestClusterAttestationFailsClosedOnABrokenCertificate(t *testing.T) {
	t.Run("certificate without key", func(t *testing.T) {
		certEnv, _, _ := certificateFixture(t, "demo-1234")
		t.Setenv(EnvClusterCert, certEnv)
		t.Setenv(EnvClusterKey, "")
		t.Setenv(EnvClusterID, "demo-1234")
		t.Setenv(EnvClusterSecret, "launch-secret")
		if att := clusterAttestation("127.0.0.1:9090"); att != nil {
			t.Fatalf("attestation = %+v, want nil (no key to sign with)", att)
		}
	})

	t.Run("key without certificate", func(t *testing.T) {
		_, keyEnv, _ := certificateFixture(t, "demo-1234")
		t.Setenv(EnvClusterCert, "")
		t.Setenv(EnvClusterKey, keyEnv)
		t.Setenv(EnvClusterID, "demo-1234")
		t.Setenv(EnvClusterSecret, "launch-secret")
		if att := clusterAttestation("127.0.0.1:9090"); att != nil {
			t.Fatalf("attestation = %+v, want nil (no certificate to present)", att)
		}
	})

	t.Run("key is not the certificate's pair", func(t *testing.T) {
		certEnv, _, _ := certificateFixture(t, "demo-1234")
		_, otherKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		t.Setenv(EnvClusterCert, certEnv)
		t.Setenv(EnvClusterKey, base64.StdEncoding.EncodeToString(otherKey))
		if att := clusterAttestation("127.0.0.1:9090"); att != nil {
			t.Fatalf("attestation = %+v, want nil (the key does not match the certificate)", att)
		}
	})

	t.Run("certificate is not JSON", func(t *testing.T) {
		t.Setenv(EnvClusterCert, base64.StdEncoding.EncodeToString([]byte("not a certificate")))
		t.Setenv(EnvClusterKey, base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)))
		if att := clusterAttestation("127.0.0.1:9090"); att != nil {
			t.Fatalf("attestation = %+v, want nil", att)
		}
	})
}

// TestClusterAttestationAcceptsRawCertificateJSON lets an operator paste the
// certificate a tool printed instead of its base64 wrapper.
func TestClusterAttestationAcceptsRawCertificateJSON(t *testing.T) {
	certEnv, keyEnv, _ := certificateFixture(t, "demo-1234")
	decoded, err := base64.StdEncoding.DecodeString(certEnv)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	t.Setenv(EnvClusterCert, string(decoded))
	t.Setenv(EnvClusterKey, keyEnv)

	att := clusterAttestation("127.0.0.1:9090")
	if att == nil || att.EffectiveScheme() != isotope.SchemeEd25519 {
		t.Fatalf("raw JSON certificate was not accepted: %+v", att)
	}
}
