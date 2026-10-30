// Package entitle decides whether an inbound MCP caller may see adhd's real
// surface (adhd#27).
//
// The surface is a control plane, and "reachable ⇒ driveable" was the actual
// contract before this package existed. The intended posture is a facade: an
// unentitled caller sees an empty adhd — no tools, no peers, no lights — and
// driving it files an alarm instead of executing. Entitlement is a bearer
// capability whose key carries a maximum rung:
//
//	R/C -> 0     W -> 3     E -> 4     X -> 6
//
// The wire contract is kb's shield gate (kb/scripts/cluster/shield_gate.py):
// the same keyring file, the same `_shield` object, the same HMAC payload. One
// client can therefore address adhd and a KB scope without a second scheme, and
// the lab has one answer to "how does a caller prove a rung".
//
// This is a capability with a rung, not an isotope-certified identity. The
// isotope chain authenticates a peer's implementation; the shield authenticates
// a caller that already holds a secret. Detection (the knock, the honeypot) and
// authorization (the key) stay separate: a knock never admits.
package entitle

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ShieldParam is the reserved params member carrying the signature. A
	// caller's real params never contain it: the signature covers them without
	// it, and the gate removes it before dispatch.
	ShieldParam = "_shield"

	// MaxSkew is how far a signature's timestamp may drift from local time.
	MaxSkew = 120 * time.Second

	// nonceTTL bounds the replay ledger. A nonce only has to outlive the skew
	// window: once a signature is stale it is refused on its timestamp alone.
	nonceTTL = 5 * time.Minute

	// DefaultPeriod is the tumble epoch length. The current epoch and the
	// previous one are accepted, so a signature issued at an epoch boundary is
	// not refused for being 200ms early.
	DefaultPeriod = 30 * time.Second
)

// ErrNoKeyring is returned when the gate is asked to enforce without keys. It
// is an error and not an empty keyring: a gate that admits nobody looks exactly
// like an outage, and silently serving the facade forever hides the mistake.
var ErrNoKeyring = errors.New("gate has no usable keys")

// Key is one capability. Rung is the most it may ever claim; a caller may ask
// for less, in which case the effective rung is the lower of the two.
type Key struct {
	ID     string `json:"id"`
	Secret string `json:"key"`
	Rung   int    `json:"rung"`
}

// Keyring is the loaded set of capabilities. A keyring is a local file, not a
// network service: it is the operator's decision, read at startup.
type Keyring struct {
	keys map[string]Key
}

// LoadKeyring reads a keyring file.
//
// Two shapes are accepted, matching kb's gate: a keyring
// `{"keys":[{"id","key","rung"}, …]}` so one instance can issue a read-only
// credential separately from an exec one, and the legacy single key
// `{"key":"…","rung":N}`.
func LoadKeyring(path string) (*Keyring, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var file struct {
		Key  string `json:"key"`
		Rung int    `json:"rung"`
		Keys []Key  `json:"keys"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("keyring %s: %w", path, err)
	}

	ring := &Keyring{keys: make(map[string]Key)}
	if len(file.Keys) > 0 {
		for _, k := range file.Keys {
			if strings.TrimSpace(k.ID) == "" || k.Secret == "" {
				continue
			}
			ring.keys[k.ID] = k
		}
	} else if file.Key != "" {
		ring.keys["default"] = Key{ID: "default", Secret: file.Key, Rung: file.Rung}
	}
	if len(ring.keys) == 0 {
		return nil, fmt.Errorf("keyring %s: %w", path, ErrNoKeyring)
	}

	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		slog.Warn("gate keyring is readable beyond its owner", "path", path, "mode", info.Mode().Perm())
	}
	return ring, nil
}

// GenerateKeyring writes a fresh read/write/exec keyring at path (0600, in a
// 0700 directory) and returns it. It refuses to overwrite an existing file.
func GenerateKeyring(path string) (*Keyring, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("keyring %s exists", path)
	}

	rungs := []struct {
		id   string
		rung int
	}{
		{"read", 0},
		{"write", 3},
		{"exec", 6},
	}
	ring := &Keyring{keys: make(map[string]Key, len(rungs))}
	file := struct {
		Keys []Key `json:"keys"`
	}{}
	for _, r := range rungs {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		key := Key{ID: r.id, Secret: hex.EncodeToString(secret), Rung: r.rung}
		ring.keys[key.ID] = key
		file.Keys = append(file.Keys, key)
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	return ring, nil
}

// Key returns the capability with id.
func (k *Keyring) Key(id string) (Key, bool) {
	key, ok := k.keys[id]
	return key, ok
}

// MaxRung is the highest rung any key in the ring may claim.
func (k *Keyring) MaxRung() int {
	max := 0
	for _, key := range k.keys {
		if key.Rung > max {
			max = key.Rung
		}
	}
	return max
}

// Len is the number of keys.
func (k *Keyring) Len() int { return len(k.keys) }

// Verdict is the gate's answer for one request.
type Verdict struct {
	// Entitled is whether the caller may see and drive the real surface.
	Entitled bool
	// Rung is the effective capability: min(key rung, claimed rung). It is 0
	// for an unentitled caller, which is also the read rung — so the caller
	// that failed to prove anything is exactly as powerful as a read key, and
	// that is the point rather than an accident.
	Rung int
	// Params is the request's params with ShieldParam removed, ready to
	// dispatch. It is nil when the request carried no params.
	Params interface{}
	// Identity names the credential that answered, for the audit record.
	// "anonymous" when none did.
	Identity string
	// Reason is why the caller is unentitled, in the gate's own words. It is
	// logged and filed; it is not returned to the caller, because a refusal
	// that explains itself is a map.
	Reason string
}

// Gate verifies capabilities and remembers spent nonces.
type Gate struct {
	keys   *Keyring
	period time.Duration
	now    func() time.Time

	mu     sync.Mutex
	nonces map[string]time.Time
}

// NewGate returns a gate over ring. A nil or empty ring is ErrNoKeyring.
func NewGate(ring *Keyring, period time.Duration) (*Gate, error) {
	if ring == nil || len(ring.keys) == 0 {
		return nil, ErrNoKeyring
	}
	if period <= 0 {
		period = DefaultPeriod
	}
	return &Gate{
		keys:   ring,
		period: period,
		now:    time.Now,
		nonces: make(map[string]time.Time),
	}, nil
}

// SetClock overrides the gate's clock. Tests use it; nothing else should.
func (g *Gate) SetClock(now func() time.Time) { g.now = now }

// Verify decides whether a request is entitled.
//
// It accepts either a bearer credential (`Authorization: Bearer <kid>.<secret>`)
// or a signed `_shield` member in params. The bearer form is a capability too —
// it is possession of the secret — but it is replayable on the wire, so it is
// for a caller whose transport is already private.
func (g *Gate) Verify(method string, params interface{}, authorization string) Verdict {
	now := g.now()

	if token, ok := bearerToken(authorization); ok {
		kid, secret, found := strings.Cut(token, ".")
		if !found {
			return Verdict{Params: params, Identity: "anonymous", Reason: "malformed bearer"}
		}
		key, ok := g.keys.keys[kid]
		if !ok {
			return Verdict{Params: params, Identity: "anonymous", Reason: "unknown key id"}
		}
		if subtle.ConstantTimeCompare([]byte(secret), []byte(key.Secret)) != 1 {
			return Verdict{Params: params, Identity: "anonymous", Reason: "bad bearer"}
		}
		return Verdict{
			Entitled: true,
			Rung:     key.Rung,
			Params:   stripShield(params),
			Identity: "bearer:" + kid,
		}
	}

	paramMap, ok := params.(map[string]interface{})
	if !ok {
		return Verdict{Params: params, Identity: "anonymous", Reason: "missing params"}
	}
	shield, ok := paramMap[ShieldParam].(map[string]interface{})
	if !ok {
		return Verdict{Params: params, Identity: "anonymous", Reason: "missing _shield"}
	}

	kid := stringField(shield, "kid")
	if kid == "" {
		kid = "default"
	}
	key, ok := g.keys.keys[kid]
	if !ok {
		return Verdict{Params: params, Identity: "anonymous", Reason: "unknown key id"}
	}

	ts, err := intField(shield, "ts")
	if err != nil {
		return Verdict{Params: params, Identity: "anonymous", Reason: "bad ts"}
	}
	claimed, err := intField(shield, "rung")
	if err != nil {
		return Verdict{Params: params, Identity: "anonymous", Reason: "bad rung"}
	}
	var epoch *int64
	if _, present := shield["epoch"]; present {
		value, err := intField(shield, "epoch")
		if err != nil {
			return Verdict{Params: params, Identity: "anonymous", Reason: "bad epoch"}
		}
		epoch = &value
	}
	nonce := stringField(shield, "nonce")
	sig := stringField(shield, "sig")
	if nonce == "" || sig == "" {
		return Verdict{Params: params, Identity: "anonymous", Reason: "missing signature fields"}
	}

	signedAt := time.Unix(ts, 0)
	if delta := now.Sub(signedAt); delta > MaxSkew || delta < -MaxSkew {
		return Verdict{Params: params, Identity: "anonymous", Reason: "stale signature"}
	}

	// The epoch is a public coordinate, not a secret: it bounds how long a
	// captured signature is usable. Current and previous epoch are accepted.
	if epoch != nil && g.period > 0 {
		current := now.Unix() / int64(g.period.Seconds())
		if *epoch < current-1 || *epoch > current {
			return Verdict{Params: params, Identity: "anonymous", Reason: "stale epoch"}
		}
	}

	clean := stripShield(params)
	canonical, err := Canonical(clean)
	if err != nil {
		return Verdict{Params: params, Identity: "anonymous", Reason: "unencodable params"}
	}
	expected := Signature(key.Secret, method, canonical, ts, nonce, int(claimed), kid, epoch)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return Verdict{Params: params, Identity: "anonymous", Reason: "bad signature"}
	}

	// Spend the nonce only once the signature is known good: an attacker's
	// guesses must not be able to burn a legitimate caller's nonce. ONE
	// CRITICAL CLIENT RULE FOLLOWS FROM THIS: sign, then send; never retry a
	// rejected request with the same nonce, because a successful verification
	// on a request the network dropped has spent it.
	if !g.claimNonce(nonce, now) {
		return Verdict{Params: params, Identity: "anonymous", Reason: "replayed nonce"}
	}

	rung := key.Rung
	if claimed > 0 && int(claimed) < rung {
		rung = int(claimed)
	}
	return Verdict{
		Entitled: true,
		Rung:     rung,
		Params:   clean,
		Identity: "shield:" + kid,
	}
}

// Canonical renders params the way the signature covers them: sorted keys,
// compact separators, no HTML escaping. This is kb's `json.dumps(params or {},
// sort_keys=True, separators=(",", ":"), ensure_ascii=False)`.
func Canonical(params interface{}) (string, error) {
	if params == nil {
		return "{}", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(params); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// Signature is the HMAC a caller must present. canonical must be Canonical of
// the params without ShieldParam.
func Signature(secret, method, canonical string, ts int64, nonce string, rung int, kid string, epoch *int64) string {
	parts := []string{
		method,
		canonical,
		strconv.FormatInt(ts, 10),
		nonce,
		strconv.Itoa(rung),
	}
	// The default key has no id on the wire, matching kb's gate.
	if kid != "" && kid != "default" {
		parts = append(parts, kid)
	}
	if epoch != nil {
		parts = append(parts, strconv.FormatInt(*epoch, 10))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

// CurrentEpoch is the tumble epoch at now for a period. A caller signing a
// request uses it so the gate can refuse a captured signature after the tumble.
func CurrentEpoch(now time.Time, period time.Duration) int64 {
	if period <= 0 {
		period = DefaultPeriod
	}
	return now.Unix() / int64(period.Seconds())
}

func (g *Gate) claimNonce(nonce string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for seen, at := range g.nonces {
		if now.Sub(at) > nonceTTL {
			delete(g.nonces, seen)
		}
	}
	if _, spent := g.nonces[nonce]; spent {
		return false
	}
	g.nonces[nonce] = now
	return true
}

func stripShield(params interface{}) interface{} {
	m, ok := params.(map[string]interface{})
	if !ok {
		return params
	}
	if _, present := m[ShieldParam]; !present {
		return params
	}
	clean := make(map[string]interface{}, len(m))
	for key, value := range m {
		if key == ShieldParam {
			continue
		}
		clean[key] = value
	}
	return clean
}

func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

func stringField(m map[string]interface{}, key string) string {
	value, _ := m[key].(string)
	return value
}

func intField(m map[string]interface{}, key string) (int64, error) {
	value, present := m[key]
	if !present || value == nil {
		return 0, nil
	}
	switch v := value.(type) {
	case float64:
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case json.Number:
		return v.Int64()
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("%s is not a number", key)
	}
}
