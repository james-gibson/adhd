package entitle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testGate(t *testing.T) (*Gate, *Keyring) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gate.key")
	ring, err := GenerateKeyring(path)
	if err != nil {
		t.Fatalf("GenerateKeyring: %v", err)
	}
	gate, err := NewGate(ring, 30*time.Second)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	return gate, ring
}

func shield(ts int64, key Key, method string, params map[string]interface{}, nonce string, claimed int, epoch *int64) map[string]interface{} {
	clean := make(map[string]interface{}, len(params))
	for k, v := range params {
		clean[k] = v
	}
	canonical, _ := Canonical(clean)
	sig := Signature(key.Secret, method, canonical, ts, nonce, claimed, key.ID, epoch)
	clean[ShieldParam] = map[string]interface{}{
		"kid":   key.ID,
		"ts":    ts,
		"nonce": nonce,
		"rung":  claimed,
		"sig":   sig,
	}
	if epoch != nil {
		clean[ShieldParam].(map[string]interface{})["epoch"] = *epoch
	}
	return clean
}

func TestGeneratedKeyringIsOwnerOnlyAndHasThreeRungs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "gate.key")
	ring, err := GenerateKeyring(path)
	if err != nil {
		t.Fatalf("GenerateKeyring: %v", err)
	}
	if ring.Len() != 3 || ring.MaxRung() != 6 {
		t.Fatalf("ring len=%d max=%d, want 3 and 6", ring.Len(), ring.MaxRung())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("keyring mode %o, want 600", perm)
	}
}

func TestLoadLegacySingleKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.key")
	if err := os.WriteFile(path, []byte(`{"key":"s3cret","rung":4}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ring, err := LoadKeyring(path)
	if err != nil {
		t.Fatalf("LoadKeyring: %v", err)
	}
	key, ok := ring.keys["default"]
	if !ok || key.Secret != "s3cret" || key.Rung != 4 {
		t.Fatalf("legacy key = %+v, want default/s3cret/4", key)
	}
}

func TestSignedRequestIsEntitledAtKeyRung(t *testing.T) {
	gate, ring := testGate(t)
	key := ring.keys["read"]
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })

	params := shield(now.Unix(), key, "tools/list", nil, "nonce-1", 0, nil)
	verdict := gate.Verify("tools/list", params, "")
	if !verdict.Entitled {
		t.Fatalf("not entitled: %s", verdict.Reason)
	}
	if verdict.Rung != 0 {
		t.Fatalf("rung = %d, want 0", verdict.Rung)
	}
	if _, leaked := verdict.Params.(map[string]interface{})["_shield"]; leaked {
		t.Fatal("_shield survived into dispatch params")
	}
}

func TestClaimedRungIsCappedByTheKey(t *testing.T) {
	gate, ring := testGate(t)
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })

	read := ring.keys["read"]
	verdict := gate.Verify("adhd.gh.run", shield(now.Unix(), read, "adhd.gh.run", nil, "n-1", 6, nil), "")
	if !verdict.Entitled || verdict.Rung != 0 {
		t.Fatalf("read key claiming 6: entitled=%v rung=%d, want true/0", verdict.Entitled, verdict.Rung)
	}

	exec := ring.keys["exec"]
	verdict = gate.Verify("adhd.gh.run", shield(now.Unix(), exec, "adhd.gh.run", nil, "n-2", 3, nil), "")
	if !verdict.Entitled || verdict.Rung != 3 {
		t.Fatalf("exec key claiming 3: entitled=%v rung=%d, want true/3", verdict.Entitled, verdict.Rung)
	}
}

func TestBadSignatureIsRefused(t *testing.T) {
	gate, ring := testGate(t)
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })
	key := ring.keys["exec"]

	params := shield(now.Unix(), key, "adhd.gh.run", nil, "n-1", 6, nil)
	params[ShieldParam].(map[string]interface{})["sig"] = "deadbeef"
	verdict := gate.Verify("adhd.gh.run", params, "")
	if verdict.Entitled {
		t.Fatal("a forged signature was accepted")
	}
	if verdict.Identity != "anonymous" {
		t.Fatalf("identity = %q, want anonymous", verdict.Identity)
	}
}

func TestSignatureCoversParams(t *testing.T) {
	gate, ring := testGate(t)
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })
	key := ring.keys["exec"]

	params := shield(now.Unix(), key, "tools/call", map[string]interface{}{"name": "adhd.status"}, "n-1", 6, nil)
	// Change the payload after signing: the signature must no longer verify.
	params["name"] = "adhd.gh.run"
	if gate.Verify("tools/call", params, "").Entitled {
		t.Fatal("a signature verified after its params changed")
	}
}

func TestStaleTimestampIsRefused(t *testing.T) {
	gate, ring := testGate(t)
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })
	key := ring.keys["read"]

	params := shield(now.Add(-3*time.Minute).Unix(), key, "tools/list", nil, "n-1", 0, nil)
	if gate.Verify("tools/list", params, "").Entitled {
		t.Fatal("a signature older than the skew window was accepted")
	}
}

func TestNonceIsSingleUse(t *testing.T) {
	gate, ring := testGate(t)
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })
	key := ring.keys["read"]

	params := shield(now.Unix(), key, "tools/list", nil, "the-nonce", 0, nil)
	if !gate.Verify("tools/list", params, "").Entitled {
		t.Fatal("first use was refused")
	}
	if gate.Verify("tools/list", params, "").Entitled {
		t.Fatal("a replayed nonce was accepted")
	}
}

func TestStaleEpochIsRefusedAndPreviousEpochAccepted(t *testing.T) {
	gate, ring := testGate(t)
	now := time.Unix(1_700_000_000, 0)
	gate.SetClock(func() time.Time { return now })
	key := ring.keys["read"]
	current := CurrentEpoch(now, 30*time.Second)
	previous := current - 1
	old := current - 2

	if !gate.Verify("tools/list", shield(now.Unix(), key, "tools/list", nil, "n-prev", 0, &previous), "").Entitled {
		t.Fatal("the previous epoch was refused")
	}
	if gate.Verify("tools/list", shield(now.Unix(), key, "tools/list", nil, "n-old", 0, &old), "").Entitled {
		t.Fatal("an epoch two periods old was accepted")
	}
}

func TestBearerCapabilityIsAccepted(t *testing.T) {
	gate, ring := testGate(t)
	exec := ring.keys["exec"]
	verdict := gate.Verify("tools/list", nil, "Bearer "+exec.ID+"."+exec.Secret)
	if !verdict.Entitled || verdict.Rung != 6 {
		t.Fatalf("bearer: entitled=%v rung=%d, want true/6", verdict.Entitled, verdict.Rung)
	}
	if verdict.Identity != "bearer:exec" {
		t.Fatalf("identity = %q, want bearer:exec", verdict.Identity)
	}

	if gate.Verify("tools/list", nil, "Bearer exec.wrong").Entitled {
		t.Fatal("a wrong bearer secret was accepted")
	}
}

func TestAnonymousHasNoCapability(t *testing.T) {
	gate, _ := testGate(t)
	verdict := gate.Verify("adhd.gh.run", nil, "")
	if verdict.Entitled || verdict.Rung != 0 {
		t.Fatalf("anonymous: entitled=%v rung=%d", verdict.Entitled, verdict.Rung)
	}
	if verdict.Reason == "" {
		t.Fatal("a refusal must carry a reason for the audit record")
	}
}

func TestCanonicalSortsAndKeepsUnicode(t *testing.T) {
	got, err := Canonical(map[string]interface{}{
		"zzz": 1,
		"aaa": map[string]interface{}{"b": "é", "a": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"aaa":{"a":true,"b":"é"},"zzz":1}`
	if got != want {
		t.Fatalf("Canonical = %s, want %s", got, want)
	}

	// A JSON client reads the same canonical form kb's gate would produce.
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("canonical is not JSON: %v", err)
	}
}
