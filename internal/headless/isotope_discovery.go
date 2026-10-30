package headless

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/james-gibson/adhd/internal/mcpclient"
	"github.com/james-gibson/isotope"
)

// IsotopeRole describes the role of an ADHD instance in the topology
type IsotopeRole string

const (
	RolePrime      IsotopeRole = "prime"      // Primary collector and authority
	RolePrimePlus  IsotopeRole = "prime-plus" // Secondary, pushes to prime
	RoleStandalone IsotopeRole = "standalone" // No topology role
)

// IsotopeHeartbeatInterval is how often a registered instance renews its name.
// It stays well inside smoke-alarm's isotope TTL (5m by default): the registry
// treats a registration as a heartbeat and sweeps a name it has not heard from,
// so a silent instance would stop resolving (smoke-alarm#35).
const IsotopeHeartbeatInterval = time.Minute

// IsotopePeer represents a discovered ADHD isotope
type IsotopePeer struct {
	Name      string      `json:"name"`
	Role      IsotopeRole `json:"role"`
	Endpoint  string      `json:"endpoint"`
	Status    string      `json:"status"`
	TrustRung int         `json:"trust_rung"` // 42i certification rung; 0 if unknown
	LastSeen  time.Time   `json:"last_seen"`
}

// IsotopeTopology describes the current topology state
type IsotopeTopology struct {
	LocalRole    IsotopeRole   `json:"local_role"`
	LocalName    string        `json:"local_name"`
	PrimeAddr    string        `json:"prime_addr,omitempty"`
	Peers        []IsotopePeer `json:"peers"`
	DiscoveredAt time.Time     `json:"discovered_at"`
}

// DiscoverIsotopes queries smoke-alarm for all ADHD isotope peers
// Returns discovered instances and their roles
func DiscoverIsotopes(ctx context.Context, smokeAlarmURL string) ([]IsotopePeer, error) {
	client := mcpclient.NewHTTPClient(smokeAlarmURL, 10*time.Second)

	// Call smoke-alarm.isotope.list to get all registered isotopes
	resp, err := client.Call(ctx, "smoke-alarm.isotope.list", map[string]interface{}{
		"type": "adhd",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query isotopes: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("isotope.list error: %s", resp.Error.Message)
	}

	// Parse the isotopes list
	var result struct {
		Isotopes []struct {
			Name     string                 `json:"name"`
			Role     string                 `json:"role"`
			Endpoint string                 `json:"endpoint"`
			Status   string                 `json:"status"`
			Metadata map[string]interface{} `json:"metadata"`
		} `json:"isotopes"`
	}

	data, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse isotope list: %w", err)
	}

	peers := make([]IsotopePeer, 0, len(result.Isotopes))
	for _, iso := range result.Isotopes {
		// Filter out self
		if iso.Name == "adhd" {
			continue
		}

		trustRung := 0
		if iso.Metadata != nil {
			if r, ok := iso.Metadata["trust_rung"].(float64); ok {
				trustRung = int(r)
			}
		}
		peers = append(peers, IsotopePeer{
			Name:      iso.Name,
			Role:      IsotopeRole(iso.Role),
			Endpoint:  iso.Endpoint,
			Status:    iso.Status,
			TrustRung: trustRung,
			LastSeen:  time.Now(),
		})
	}

	return peers, nil
}

// QueryIsotopePeer queries a discovered isotope for its role and status
func QueryIsotopePeer(ctx context.Context, endpoint string) (*IsotopePeer, error) {
	client := mcpclient.NewHTTPClient(endpoint, 5*time.Second)

	// Call adhd.isotope.status on the remote instance
	resp, err := client.Call(ctx, "adhd.isotope.status", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query peer status: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("isotope.status error: %s", resp.Error.Message)
	}

	// Parse peer status
	var result struct {
		Name   string `json:"name"`
		Role   string `json:"role"`
		Status string `json:"status"`
	}

	data, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to parse peer status: %w", err)
	}

	return &IsotopePeer{
		Name:     result.Name,
		Role:     IsotopeRole(result.Role),
		Endpoint: endpoint,
		Status:   result.Status,
		LastSeen: time.Now(),
	}, nil
}

// AutoDiscoverPrime attempts to find the prime instance via smoke-alarm
// Returns the prime's endpoint if found
func AutoDiscoverPrime(ctx context.Context, smokeAlarmURL string) (string, error) {
	peers, err := DiscoverIsotopes(ctx, smokeAlarmURL)
	if err != nil {
		return "", err
	}

	// Look for a prime instance
	for _, peer := range peers {
		if peer.Role == RolePrime {
			return peer.Endpoint, nil
		}
	}

	return "", fmt.Errorf("no prime instance found in discovered isotopes")
}

// RegisterIsotopeWithRole registers this instance via smoke-alarm's REST
// /isotope/register endpoint and returns the assigned trust rung (0 if
// registration fails). It sends no project identity; callers that have one use
// RegisterIsotopeWithReferences.
//
// name is the instance label, not the product name: smoke-alarm keys its isotope
// registry by name, so a constant ("adhd") makes a second instance overwrite the
// first and the list shows one entry for two live instances (adhd#48). It is
// required rather than defaulted, because a silent default is how the constant
// got there.
func RegisterIsotopeWithRole(ctx context.Context, smokeAlarmURL string, role IsotopeRole, localAddr, name string) (int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("isotope registration name is required: a constant name lets two instances overwrite each other in the smoke-alarm registry (adhd#48)")
	}

	record, err := RegisterIsotopeWithReferences(ctx, smokeAlarmURL, isotope.Registration{
		Name:     name,
		Role:     string(role),
		Endpoint: localAddr,
		Protocol: "mcp",
	}, isotopeReferences{})
	if err != nil {
		return 0, err
	}

	slog.Info("registered as isotope",
		"role", role,
		"endpoint", localAddr,
		"smoke_alarm", smokeAlarmURL,
		"trust_rung", record.TrustRung,
		"rung_name", record.RungName,
	)

	return record.TrustRung, nil
}

// RegisterIsotopeWithReferences POSTs the shared registration plus this
// project's route and dependency pins. The registry keys by name, but a
// dependent resolves by route; the contract pins the spec that dependency was
// built against (smoke-alarm#35).
func RegisterIsotopeWithReferences(ctx context.Context, smokeAlarmURL string, reg isotope.Registration, refs isotopeReferences) (*isotope.Record, error) {
	body, err := json.Marshal(struct {
		isotope.Registration
		isotopeReferences
	}{reg, refs})
	if err != nil {
		return nil, fmt.Errorf("registration failed: marshal: %w", err)
	}

	endpoint := strings.TrimRight(smokeAlarmURL, "/") + "/isotope/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("registration failed: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // G704: the registry is the configured smoke-alarm endpoint
	if err != nil {
		return nil, fmt.Errorf("registration failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registration failed: status %d", resp.StatusCode)
	}

	var record isotope.Record
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return nil, fmt.Errorf("registration failed: decode response: %w", err)
	}
	return &record, nil
}

// ServeIsotopeHeartbeat renews this instance's registration until ctx is
// canceled. A registry entry is a heartbeat, not a name: without renewal,
// smoke-alarm sweeps the entry after its isotope TTL and the instance stops
// resolving by name (smoke-alarm#35). Renewal failures are logged and retried
// on the next tick; a transient registry outage must not take adhd down.
func (s *Server) ServeIsotopeHeartbeat(ctx context.Context, smokeAlarmURL string, interval time.Duration) {
	if interval <= 0 {
		interval = IsotopeHeartbeatInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.renewIsotopeRegistration(ctx, smokeAlarmURL); err != nil && ctx.Err() == nil {
				slog.Warn("isotope heartbeat failed", "error", err)
			}
		}
	}
}

// renewIsotopeRegistration re-registers the same instance label and endpoint,
// with the same project identity. The registry keeps the rung it already
// assigned to an unchanged endpoint, so a renewal does not re-run the trust
// probe. ctx bounds the request, so cancellation cannot leave a beat in flight
// after shutdown.
func (s *Server) renewIsotopeRegistration(ctx context.Context, smokeAlarmURL string) error {
	rung, err := s.registerWithIdentity(ctx, smokeAlarmURL, s.registrationRole(), s.instanceLabel(), s.cfg.MCPServer.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.trustLevel = rung
	s.mu.Unlock()
	return nil
}

// registrationRole is the role this instance claims to the registry.
func (s *Server) registrationRole() IsotopeRole {
	if s.role == RoleStandalone {
		if s.isPrimePlus {
			return RolePrimePlus
		}
		return RolePrime
	}
	return s.role
}

// Environment lezz uses to hand a cluster its evidence. lezz sets these on
// every process it spawns, so this instance can prove which cluster it belongs
// to without the material itself ever crossing the wire.
//
// There are two shapes, and they are handed over in separate variables because
// they are different things (isotope#25, isotope#26):
//
//   - LEZZ_CLUSTER_ID + LEZZ_CLUSTER_SECRET: the cluster's launch secret, which
//     this instance proves by HMAC. Verification implies minting, so only the
//     cluster and its alarm can check it (isotope#23).
//   - LEZZ_CLUSTER_CERT + LEZZ_CLUSTER_KEY: the certificate a root issued for
//     the cluster, and the private key it signs with. The receipt is ed25519 and
//     any verifier that pins the root can check it without holding the key.
//
// The certificate is public and the key is not, so an operator hands each one
// from its own store; one variable carrying both would make every log line that
// prints the environment a key leak.
const (
	EnvClusterID     = "LEZZ_CLUSTER_ID"
	EnvClusterSecret = "LEZZ_CLUSTER_SECRET"
	EnvClusterCert   = "LEZZ_CLUSTER_CERT"
	EnvClusterKey    = "LEZZ_CLUSTER_KEY"
)

// clusterAttestation is the receipt this instance presents on registration:
// either an ed25519 receipt carrying the cluster's certificate, or an HMAC over
// the cluster identity and its own endpoint.
//
// The certificate wins when both shapes are configured: it is the stronger
// evidence, because a third party can verify it without being able to mint as
// the cluster. The HMAC path is untouched for every cluster that has not adopted
// certificates.
//
// Nil when nothing usable is configured, which is the honest answer for an
// instance nobody launched as part of a cluster: the registration then stays at
// the floor, because a self-declared endpoint is not evidence (isotope#23). A
// certificate that is configured but unusable also yields nil: registering
// unattested is visible in the rung's evidence, whereas falling back to a secret
// nobody pinned would look like a cluster that quietly lost its rung. The
// receipt is rebuilt on every registration, including each heartbeat, because it
// is timestamped and only valid inside the verifier's skew window.
func clusterAttestation(endpoint string) *isotope.Attestation {
	certRaw := strings.TrimSpace(os.Getenv(EnvClusterCert))
	keyRaw := strings.TrimSpace(os.Getenv(EnvClusterKey))
	if certRaw != "" || keyRaw != "" {
		if certRaw == "" || keyRaw == "" {
			slog.Error("cluster certificate needs both " + EnvClusterCert + " and " + EnvClusterKey +
				"; registering unattested")
			return nil
		}
		att, err := certificateAttestation(certRaw, keyRaw, endpoint)
		if err != nil {
			slog.Error("cluster certificate is unusable; registering unattested", "error", err)
			return nil
		}
		return att
	}

	cluster := strings.TrimSpace(os.Getenv(EnvClusterID))
	secret := os.Getenv(EnvClusterSecret)
	if cluster == "" || secret == "" {
		return nil
	}
	att := isotope.SignAttestation(secret, cluster, endpoint, time.Now())
	return &att
}

// certificateAttestation reads the two halves lezz hands over and signs an
// ed25519 receipt for endpoint.
func certificateAttestation(certRaw, keyRaw, endpoint string) (*isotope.Attestation, error) {
	cert, err := decodeClusterCertificate(certRaw)
	if err != nil {
		return nil, err
	}
	key, err := decodeClusterKey(keyRaw)
	if err != nil {
		return nil, err
	}
	if cert.Cluster == "" {
		return nil, errors.New("certificate names no cluster")
	}
	// A key that is not the certificate's pair would produce receipts the
	// verifier rejects one round trip later. Refuse it here, where the operator
	// can see which variable is wrong.
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cert.PublicKey))
	if err != nil {
		return nil, fmt.Errorf("certificate public_key is not base64: %w", err)
	}
	if !ed25519.PublicKey(pub).Equal(key.Public()) {
		return nil, errors.New(EnvClusterKey + " does not match the certificate's public_key")
	}
	att := isotope.SignReceiptEd25519(key, cert, endpoint, time.Now())
	return &att, nil
}

// decodeClusterCertificate accepts the certificate the way seed42 prints it (the
// base64 blob) or as its JSON, so an operator can paste either without a
// conversion step.
func decodeClusterCertificate(raw string) (isotope.Certificate, error) {
	body := []byte(raw)
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		body = decoded
	}
	var cert isotope.Certificate
	if err := json.Unmarshal(body, &cert); err != nil {
		return isotope.Certificate{}, fmt.Errorf("certificate is neither base64 JSON nor JSON: %w", err)
	}
	return cert, nil
}

// decodeClusterKey reads an ed25519 private key as base64, accepting either the
// 64-byte private key or its 32-byte seed — the two shapes an operator's key
// store and `seed42 init` respectively produce.
func decodeClusterKey(raw string) (ed25519.PrivateKey, error) {
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is not base64: %w", EnvClusterKey, err)
	}
	switch len(decoded) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	default:
		return nil, fmt.Errorf("%s is %d bytes, want %d (private key) or %d (seed)",
			EnvClusterKey, len(decoded), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}

// registerWithIdentity registers this instance with its project route and the
// dependencies it pins, and returns the registry's trust rung.
func (s *Server) registerWithIdentity(ctx context.Context, smokeAlarmURL string, role IsotopeRole, name, addr string) (int, error) {
	record, err := RegisterIsotopeWithReferences(ctx, smokeAlarmURL, isotope.Registration{
		Name:        name,
		Role:        string(role),
		Endpoint:    addr,
		Protocol:    "mcp",
		Attestation: clusterAttestation(addr),
	}, s.isotopeReferences())
	if err != nil {
		return 0, err
	}
	return record.TrustRung, nil
}

// DeregisterIsotope removes this instance's registry entry. It is best-effort:
// the entry expires on its own, so failing to reach the registry on shutdown is
// not an error worth blocking on.
func (s *Server) DeregisterIsotope(smokeAlarmURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	endpoint := strings.TrimRight(smokeAlarmURL, "/") + "/isotope/" + url.PathEscape(s.instanceLabel())
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, http.NoBody)
	if err != nil {
		slog.Warn("isotope deregistration: build request", "error", err)
		return
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: the registry is the configured smoke-alarm endpoint
	if err != nil {
		slog.Warn("isotope deregistration failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// 404 means it already expired or was removed: the desired end state.
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		slog.Warn("isotope deregistration rejected", "status", resp.StatusCode)
	}
}

// PeriodicDiscovery runs discovery at intervals and auto-configures prime if not set
// This allows headless instances to auto-discover their prime
func PeriodicDiscovery(ctx context.Context, smokeAlarmURL string, interval time.Duration, onPrimeDiscovered func(string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			primeAddr, err := AutoDiscoverPrime(ctx, smokeAlarmURL)
			if err != nil {
				slog.Debug("prime discovery failed", "error", err)
				continue
			}

			slog.Info("discovered prime instance", "endpoint", primeAddr)
			if onPrimeDiscovered != nil {
				onPrimeDiscovered(primeAddr)
			}

		case <-ctx.Done():
			return
		}
	}
}
