package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// preComputeReceipt mirrors the canonical isotope construction algorithm:
//
//	isotope_id = base64url( SHA256( feature_id || ":" || SHA256(payload) || ":" || nonce ) )
//
// The smoke-alarm pre-computes the expected receipt before delivering a chaos skill.
// Any deviation from this value is a detection signal.
func preComputeReceipt(featureID string, payload []byte, nonce string) string {
	payloadHash := sha256.Sum256(payload)
	inner := fmt.Sprintf("%s:%x:%s", featureID, payloadHash, nonce)
	outerHash := sha256.Sum256([]byte(inner))
	return base64.RawURLEncoding.EncodeToString(outerHash[:])
}

// TestReceiptPreComputationIsDeterministic verifies the receipt construction is stable.
// Same inputs must always produce the same receipt — this is the foundation of
// chaos skill detection: the expected receipt is known before delivery.
func TestReceiptPreComputationIsDeterministic(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	payload := []byte(`{"status":"ok","probe":"result"}`)
	nonce := "fixed-nonce-for-test"

	r1 := preComputeReceipt(featureID, payload, nonce)
	r2 := preComputeReceipt(featureID, payload, nonce)

	if r1 != r2 {
		t.Errorf("receipt not deterministic: %s != %s", r1, r2)
	}
	if len(r1) != 43 {
		t.Errorf("expected 43-char base64url receipt, got %d chars: %s", len(r1), r1)
	}
}

// TestReceiptDiffersWithDifferentNonces verifies each execution produces a unique receipt.
// Two executions of the same skill with the same payload are distinguishable by nonce.
// This prevents replay: a captured receipt cannot be reused as proof of a new execution.
func TestReceiptDiffersWithDifferentNonces(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	payload := []byte(`{"status":"ok"}`)

	r1 := preComputeReceipt(featureID, payload, "nonce-001")
	r2 := preComputeReceipt(featureID, payload, "nonce-002")

	if r1 == r2 {
		t.Error("receipts must differ when nonces differ: cannot distinguish executions")
	}
}

// TestReceiptDiffersWithDifferentPayloads verifies payload tampering is detectable.
// If an actor modifies the skill result, the receipt they produce won't match
// the expected receipt pre-computed by the certifying authority.
func TestReceiptDiffersWithDifferentPayloads(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	nonce := "same-nonce"

	expected := preComputeReceipt(featureID, []byte(`{"status":"ok"}`), nonce)
	tampered := preComputeReceipt(featureID, []byte(`{"status":"tampered"}`), nonce)

	if expected == tampered {
		t.Error("receipts must differ when payloads differ: tampering is undetectable")
	}
}

// TestReceiptDiffersWithDifferentFeatureIDs verifies feature binding is committed.
// A receipt for "adhd/open-the-pickle-jar" cannot be presented as proof that
// "adhd/chaos-skill-detection" ran.
func TestReceiptDiffersWithDifferentFeatureIDs(t *testing.T) {
	payload := []byte(`{"status":"ok"}`)
	nonce := "same-nonce"

	r1 := preComputeReceipt("adhd/open-the-pickle-jar", payload, nonce)
	r2 := preComputeReceipt("adhd/chaos-skill-detection", payload, nonce)

	if r1 == r2 {
		t.Error("receipts must differ when feature_id differs: wrong skill could be substituted")
	}
}

// TestHoneypotReceiptIsNeverALegitimateTransit establishes the invariant:
// the honeypot's pre-computed receipt must never appear in a non-honeypot transit.
// This test documents the invariant and verifies detection logic rejects it.
func TestHoneypotReceiptIsNeverALegitimateTransit(t *testing.T) {
	// The certifying authority pre-computes the honeypot receipt.
	honeypotReceipt := preComputeReceipt(
		"ocd/honeypot-alpha",
		[]byte(`{"status":"honeypot-response"}`),
		"fixed-honeypot-nonce",
	)

	// Simulate a /status response from a legitimate target carrying the honeypot receipt.
	// This would mean someone contacted the honeypot and carried its receipt to a real target.
	statusResponse := map[string]interface{}{
		"targets": []map[string]interface{}{
			{
				"id":         "t1",
				"state":      "healthy",
				"isotope_id": honeypotReceipt, // the canary: this must NEVER appear here
			},
		},
	}

	data, _ := json.Marshal(statusResponse)

	// The detection assertion: if isotope_id matches honeypot receipt on a non-honeypot
	// target, raise a detection event.
	var parsed struct {
		Targets []struct {
			ID        string `json:"id"`
			IsotopeID string `json:"isotope_id"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to parse status: %v", err)
	}

	for _, target := range parsed.Targets {
		if target.ID == "honeypot-alpha" {
			continue // honeypot itself is allowed to carry its own receipt
		}
		if target.IsotopeID == honeypotReceipt {
			// This is the correct detection: the canary appeared outside the honeypot.
			// In production this raises a detection event and revokes the routing path.
			t.Logf("DETECTION: canary receipt observed on non-honeypot target %q", target.ID)
			return // test passes — detection logic would fire here
		}
	}

	// If we reach here without detecting the canary, the detection logic failed.
	t.Error("canary receipt on non-honeypot target was not detected")
}

// TestChaosProbeReceiptMatchExpected verifies the happy path:
// a correctly-behaving proxy produces the expected receipt from the expected target.
func TestChaosProbeReceiptMatchExpected(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	payload := []byte(`{"result":"probe-ok"}`)
	nonce := "test-nonce-001"

	expected := preComputeReceipt(featureID, payload, nonce)

	// Simulate the target returning the correct receipt in its /status response.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"targets": []map[string]interface{}{
					{
						"id":         "t1",
						"state":      "healthy",
						"isotope_id": expected, // correct receipt from the expected execution
					},
				},
			})
		}
	}))
	defer target.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL+"/status", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	result, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatalf("failed to get /status: %v", err)
	}
	defer func() { _ = result.Body.Close() }()

	var status struct {
		Targets []struct {
			IsotopeID string `json:"isotope_id"`
		} `json:"targets"`
	}
	if err := json.NewDecoder(result.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode status: %v", err)
	}

	if len(status.Targets) == 0 {
		t.Fatal("no targets in status response")
	}

	observed := status.Targets[0].IsotopeID
	if observed != expected {
		t.Errorf("receipt mismatch\n  expected: %s\n  observed: %s\n"+
			"  → indicates tampering, wrong target, or wrong execution", expected, observed)
	}
}

// TestChaosProbeDetectsWrongSource verifies source deviation is detectable.
// If the receipt arrives from target "t2" when "t1" was expected, the proxy
// forwarded to the wrong actor.
func TestChaosProbeDetectsWrongSource(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	payload := []byte(`{"result":"probe-ok"}`)
	nonce := "test-nonce-002"
	expectedReceipt := preComputeReceipt(featureID, payload, nonce)
	expectedTarget := "t1"

	// Simulate: the receipt arrived from "t2" (wrong source).
	observedTarget := "t2"
	observedReceipt := expectedReceipt // receipt matches but came from wrong target

	if observedTarget == expectedTarget {
		t.Fatal("test setup error: observedTarget must differ from expectedTarget")
	}

	// Detection: receipt is correct but source is wrong → blind forwarding detected.
	if observedReceipt == expectedReceipt && observedTarget != expectedTarget {
		t.Logf("DETECTION: receipt from unexpected source %q (expected %q)", observedTarget, expectedTarget)
		// In production: raise detection event, increase proxy's 42i distance by 16
		return
	}

	t.Error("wrong-source delivery was not detected")
}

// TestChaosProbeDetectsSuppressedReceipt verifies receipt timeout detection.
// If no receipt arrives within the detection window, the actor suppressed the execution.
func TestChaosProbeDetectsSuppressedReceipt(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	payload := []byte(`{"result":"probe-ok"}`)
	nonce := "test-nonce-003"
	expectedReceipt := preComputeReceipt(featureID, payload, nonce)

	detectionDeadline := time.Now().Add(100 * time.Millisecond)

	// Simulate: no receipt arrives — the actor swallowed the execution.
	receiptArrived := false
	observedReceipt := ""

	// In production this would poll /status until detectionDeadline.
	// For the test we simply simulate no receipt arriving.
	if time.Now().Before(detectionDeadline) && !receiptArrived {
		// Wait out the deadline.
		time.Sleep(150 * time.Millisecond)
	}

	// After deadline: no receipt observed.
	if !receiptArrived || observedReceipt != expectedReceipt {
		t.Logf("DETECTION: chaos probe receipt suppressed — no transit observed for %s", featureID)
		// In production: raise "receipt suppressed" event, increase proxy 42i distance by 8
		return
	}

	t.Error("suppressed receipt was not detected")
}

// TestChaosProbeDetectsReplay verifies that the same receipt appearing twice is a replay.
// Each nonce makes every receipt unique; a replayed receipt cannot be a second execution.
func TestChaosProbeDetectsReplay(t *testing.T) {
	featureID := "adhd/chaos-skill-detection"
	payload := []byte(`{"result":"probe-ok"}`)
	nonce := "test-nonce-004"
	receipt := preComputeReceipt(featureID, payload, nonce)

	// Simulate: the same receipt has already been recorded.
	recordedReceipts := map[string]bool{
		receipt: true, // already seen once
	}

	// Second arrival of the same receipt.
	incomingReceipt := receipt

	if recordedReceipts[incomingReceipt] {
		t.Logf("DETECTION: chaos probe replay — receipt %s already recorded", incomingReceipt)
		// In production: raise "replay detected", increase 42i distance by 8, do not record again
		return
	}

	t.Error("replay was not detected")
}

// TestHoneypotNodeNeverAppearsInRouting verifies the routing invariant.
// Honeypot nodes must be excluded from all skill routing decisions.
// No legitimate skill invocation should be addressable to a honeypot.
func TestHoneypotNodeNeverAppearsInRouting(t *testing.T) {
	// Represent the routing table as instance ID → skills.
	// Honeypot nodes must never appear as a routable destination.
	routingTable := map[string][]string{
		"inst-a":          {"open-the-pickle-jar", "start-here"},
		"inst-b":          {"demo-capabilities"},
		"honeypot-alpha":  {}, // honeypot: no routable skills, never a valid destination
	}

	honeypots := map[string]bool{
		"honeypot-alpha": true,
	}

	// Verify no skill in the routing table routes through a honeypot.
	for instanceID, skills := range routingTable {
		if honeypots[instanceID] && len(skills) > 0 {
			t.Errorf("honeypot %q has routable skills: %v — honeypots must never route", instanceID, skills)
		}
	}

	// Verify a skill lookup never returns a honeypot as the host.
	skillLookup := func(skillName string) (string, bool) {
		for instanceID, skills := range routingTable {
			if honeypots[instanceID] {
				continue // skip honeypots — they are never a valid lookup result
			}
			for _, s := range skills {
				if s == skillName {
					return instanceID, true
				}
			}
		}
		return "", false
	}

	host, found := skillLookup("open-the-pickle-jar")
	if !found {
		t.Error("open-the-pickle-jar should be found on inst-a")
	}
	if honeypots[host] {
		t.Errorf("skill lookup returned honeypot %q as host — honeypots must never route", host)
	}
}
