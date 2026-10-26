// Package tearsheet models a proxy hop tear sheet: an ordered list of hops a
// caller carries and tears off one at a time.
//
// A negotiated path is reusable by any holder during its tick. A tear sheet is
// the single-use variant, because a dynamic skill is a tear-off ticket queue:
// tearing a hop mints a ticket, and redeeming that ticket twice is reuse rather
// than a retry. The sheet itself holds no server state — every hop is a
// self-contained segment, and the only state is the ledger of redeemed nonces,
// which exists to catch the replay.
//
// The sheet is validated against the public tick with the same one-tick grace
// as a path. A sheet is not a route-length contract: the caller decides how
// much of the map to carry, and a one-hop sheet is as valid as an eight-hop one.
package tearsheet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	// ErrEmpty means the sheet carries no hops.
	ErrEmpty = errors.New("tearsheet: sheet has no hops")
	// ErrInvalid means a hop or the sheet is missing a required field, or a
	// nonce is repeated.
	ErrInvalid = errors.New("tearsheet: invalid sheet")
	// ErrStale means the sheet's tick is older than the grace window.
	ErrStale = errors.New("tearsheet: sheet is stale")
	// ErrFuture means the sheet's tick has not arrived.
	ErrFuture = errors.New("tearsheet: sheet is not valid yet")
	// ErrReused means this ticket's nonce was already redeemed.
	ErrReused = errors.New("tearsheet: ticket already used")
)

// Hop is one proxy hop. Endpoint is the MCP endpoint to forward the call to;
// Nonce is the single-use capability that makes the tear a ticket.
type Hop struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to"`
	Endpoint string `json:"endpoint"`
	Feature  string `json:"feature,omitempty"`
	Nonce    string `json:"nonce"`
}

// Sheet is the perforated list. Index counts hops already torn so a ticket can
// report its absolute position even though it only ever sees the remainder.
type Sheet struct {
	ID    string `json:"id"`
	Tick  uint64 `json:"tick"`
	Index int    `json:"index,omitempty"`
	Hops  []Hop  `json:"hops"`
}

// Ticket is what a tear returns: the hop, and the nonce that will be spent.
type Ticket struct {
	Sheet string `json:"sheet"`
	Tick  uint64 `json:"tick"`
	Index int    `json:"index"`
	Hop   Hop    `json:"hop"`
	Nonce string `json:"nonce"`
	Last  bool   `json:"last"`
}

// Validate checks a sheet without consuming it.
func Validate(s Sheet) error {
	if s.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalid)
	}
	if s.Tick == 0 {
		return fmt.Errorf("%w: tick is required", ErrInvalid)
	}
	if s.Index < 0 {
		return fmt.Errorf("%w: index must be >= 0", ErrInvalid)
	}
	if len(s.Hops) == 0 {
		return ErrEmpty
	}
	seen := make(map[string]bool, len(s.Hops))
	for i, hop := range s.Hops {
		if hop.To == "" {
			return fmt.Errorf("%w: hop %d has no destination", ErrInvalid, i)
		}
		if hop.Endpoint == "" {
			return fmt.Errorf("%w: hop %d has no endpoint", ErrInvalid, i)
		}
		if hop.Nonce == "" {
			return fmt.Errorf("%w: hop %d has no nonce", ErrInvalid, i)
		}
		if seen[hop.Nonce] {
			return fmt.Errorf("%w: hop %d repeats nonce %q", ErrInvalid, i, hop.Nonce)
		}
		seen[hop.Nonce] = true
	}
	return nil
}

// Tear removes the head hop and returns the ticket for it plus the remainder.
// The remainder keeps the sheet id, tick and absolute index, so it can be
// passed to the next node unchanged.
func Tear(s Sheet, now, grace uint64) (Ticket, Sheet, error) {
	if err := Validate(s); err != nil {
		return Ticket{}, Sheet{}, err
	}
	if s.Tick > now {
		return Ticket{}, Sheet{}, fmt.Errorf("%w: tick %d, now %d", ErrFuture, s.Tick, now)
	}
	if now-s.Tick > grace {
		return Ticket{}, Sheet{}, fmt.Errorf("%w: tick %d, now %d, grace %d", ErrStale, s.Tick, now, grace)
	}

	hop := s.Hops[0]
	rest := Sheet{
		ID:    s.ID,
		Tick:  s.Tick,
		Index: s.Index + 1,
		Hops:  append([]Hop(nil), s.Hops[1:]...),
	}
	ticket := Ticket{
		Sheet: s.ID,
		Tick:  s.Tick,
		Index: s.Index,
		Hop:   hop,
		Nonce: hop.Nonce,
		Last:  len(rest.Hops) == 0,
	}
	return ticket, rest, nil
}

// Ledger remembers redeemed nonces until their tick can no longer be valid, so
// a second redemption of the same ticket is reported as reuse rather than as an
// unknown ticket.
//
// A ledger opened on a path is also the handoff seed: the parent writes it on
// every redeem, and a child launched by rotation opens the same path and
// inherits every live nonce. A redeem that misses memory re-reads the file
// before accepting, so a nonce spent by the parent during the handoff window is
// caught rather than forgotten.
type Ledger struct {
	mu   sync.Mutex
	used map[string]uint64
	path string
}

// NewLedger returns an empty in-memory ledger.
func NewLedger() *Ledger {
	return &Ledger{used: make(map[string]uint64)}
}

// ledgerDoc is the on-disk form. The version is checked so a future change to
// the meaning of an entry is refused rather than misread.
type ledgerDoc struct {
	Version int               `json:"version"`
	Used    map[string]uint64 `json:"used"`
}

const ledgerVersion = 1

// OpenLedger loads the ledger at path, or starts an empty one if the file does
// not exist yet. A corrupt or unknown-version file is an error: replay
// protection that silently starts empty is worse than no ledger, because it
// looks like protection.
func OpenLedger(path string) (*Ledger, error) {
	l := &Ledger{used: make(map[string]uint64), path: path}
	if err := l.reload(); err != nil {
		return nil, err
	}
	return l, nil
}

// Path reports the backing file, or "" for an in-memory ledger.
func (l *Ledger) Path() string { return l.path }

// reload merges the file into memory. Callers hold mu.
func (l *Ledger) reload() error {
	if l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path) //nolint:gosec // operator-supplied state path
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("tearsheet: read ledger %s: %w", l.path, err)
	}
	var doc ledgerDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("tearsheet: decode ledger %s: %w", l.path, err)
	}
	if doc.Version != ledgerVersion {
		return fmt.Errorf("tearsheet: ledger %s has version %d, want %d", l.path, doc.Version, ledgerVersion)
	}
	for nonce, tick := range doc.Used {
		if have, ok := l.used[nonce]; !ok || tick > have {
			l.used[nonce] = tick
		}
	}
	return nil
}

// persist writes the ledger atomically: a sibling temporary file is renamed
// over the target, so a reader never sees a half-written document and the
// handoff cannot pick up a truncated seed.
func (l *Ledger) persist() error {
	if l.path == "" {
		return nil
	}
	doc := ledgerDoc{Version: ledgerVersion, Used: l.used}
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("tearsheet: encode ledger: %w", err)
	}
	dir := filepath.Dir(l.path)
	tmp, err := os.CreateTemp(dir, ".tear-ledger-*")
	if err != nil {
		return fmt.Errorf("tearsheet: create ledger temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tearsheet: chmod ledger temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("tearsheet: write ledger temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("tearsheet: close ledger temp: %w", err)
	}
	if err := os.Rename(tmpName, l.path); err != nil {
		return fmt.Errorf("tearsheet: replace ledger %s: %w", l.path, err)
	}
	return nil
}

// Redeem spends a ticket nonce. It fails closed: a nonce is spent even if the
// hop that follows fails, because letting a caller retry a hop until it
// succeeds is the retry loop the nonce exists to prevent.
func (l *Ledger) Redeem(nonce string, tick, now, grace uint64) error {
	if nonce == "" {
		return fmt.Errorf("%w: empty nonce", ErrInvalid)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, ok := l.used[nonce]; ok {
		return ErrReused
	}
	// A child launched by rotation starts from the parent's file. Check it
	// again on a miss so a nonce the parent spent after the child loaded is
	// still refused.
	if err := l.reload(); err != nil {
		return err
	}
	if _, ok := l.used[nonce]; ok {
		return ErrReused
	}

	l.used[nonce] = tick
	l.prune(now, grace)
	if err := l.persist(); err != nil {
		return err
	}
	return nil
}

// Len reports how many nonces are retained.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.used)
}

// prune drops nonces whose tick can no longer pass Tear. Callers hold mu.
func (l *Ledger) prune(now, grace uint64) {
	for nonce, tick := range l.used {
		if tick <= now && now-tick > grace {
			delete(l.used, nonce)
		}
	}
}
