package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/james-gibson/adhd/internal/mcpclient"
)

// IsotopeRole describes the role of an ADHD instance in the topology
type IsotopeRole string

const (
	RolePrime      IsotopeRole = "prime"      // Primary collector and authority
	RolePrimePlus  IsotopeRole = "prime-plus" // Secondary, pushes to prime
	RoleStandalone IsotopeRole = "standalone" // No topology role
)

// IsotopePeer represents a discovered ADHD isotope
type IsotopePeer struct {
	Name     string      `json:"name"`
	Role     IsotopeRole `json:"role"`
	Endpoint string      `json:"endpoint"`
	Status   string      `json:"status"`
	LastSeen time.Time   `json:"last_seen"`
}

// IsotopeTopology describes the current topology state
type IsotopeTopology struct {
	LocalRole   IsotopeRole   `json:"local_role"`
	LocalName   string        `json:"local_name"`
	PrimeAddr   string        `json:"prime_addr,omitempty"`
	Peers       []IsotopePeer `json:"peers"`
	DiscoveredAt time.Time    `json:"discovered_at"`
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

		peers = append(peers, IsotopePeer{
			Name:     iso.Name,
			Role:     IsotopeRole(iso.Role),
			Endpoint: iso.Endpoint,
			Status:   iso.Status,
			LastSeen: time.Now(),
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

// RegisterIsotopeWithRole registers this instance as an isotope with role information
func RegisterIsotopeWithRole(smokeAlarmURL string, role IsotopeRole, localAddr string) error {
	registration := map[string]interface{}{
		"name":        "adhd",
		"type":        "isotope",
		"role":        string(role),
		"endpoint":    localAddr,
		"protocol":    "mcp",
		"status":      "ready",
		"timestamp":   time.Now().UTC(),
		"description": fmt.Sprintf("ADHD %s instance with MCP traffic logging", role),
	}

	rpcRequest := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "smoke-alarm.isotope.register",
		"params":  registration,
	}

	body, err := json.Marshal(rpcRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal registration: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, smokeAlarmURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registration returned status %d", resp.StatusCode)
	}

	var rpcResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if errField, ok := rpcResp["error"]; ok && errField != nil {
		return fmt.Errorf("isotope registration error: %v", errField)
	}

	slog.Info("registered as isotope",
		"role", role,
		"endpoint", localAddr,
		"smoke_alarm", smokeAlarmURL,
	)

	return nil
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
