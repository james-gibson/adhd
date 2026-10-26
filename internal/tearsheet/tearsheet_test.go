package tearsheet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTearRemovesOneHopAndKeepsTheRest(t *testing.T) {
	sheet := Sheet{
		ID: "sheet-1", Tick: 100,
		Hops: []Hop{
			{From: "a", To: "b", Endpoint: "http://b/mcp", Nonce: "n1"},
			{From: "b", To: "c", Endpoint: "http://c/mcp", Nonce: "n2"},
			{From: "c", To: "d", Endpoint: "http://d/mcp", Nonce: "n3"},
		},
	}

	ticket, rest, err := Tear(sheet, 100, 1)
	if err != nil {
		t.Fatalf("tear: %v", err)
	}
	if ticket.Nonce != "n1" || ticket.Hop.To != "b" || ticket.Index != 0 || ticket.Last {
		t.Fatalf("unexpected ticket: %+v", ticket)
	}
	if rest.Index != 1 || len(rest.Hops) != 2 || rest.Hops[0].Nonce != "n2" {
		t.Fatalf("unexpected remainder: %+v", rest)
	}
	if rest.ID != sheet.ID || rest.Tick != sheet.Tick {
		t.Fatalf("remainder lost sheet identity: %+v", rest)
	}

	// The original sheet is not mutated: the caller's copy still has three.
	if len(sheet.Hops) != 3 {
		t.Fatalf("tear mutated the input sheet: %d hops", len(sheet.Hops))
	}

	last, empty, err := Tear(rest, 100, 1)
	if err != nil {
		t.Fatalf("tear second: %v", err)
	}
	if last.Index != 1 || last.Nonce != "n2" || last.Last {
		t.Fatalf("unexpected second ticket: %+v", last)
	}
	ticket3, empty2, err := Tear(empty, 100, 1)
	if err != nil {
		t.Fatalf("tear third: %v", err)
	}
	if !ticket3.Last || ticket3.Index != 2 || len(empty2.Hops) != 0 {
		t.Fatalf("unexpected last ticket: %+v rest=%+v", ticket3, empty2)
	}
}

func TestTearHonoursTheOneTickGrace(t *testing.T) {
	sheet := Sheet{ID: "s", Tick: 100, Hops: []Hop{{To: "b", Endpoint: "http://b", Nonce: "n"}}}

	if _, _, err := Tear(sheet, 100, 1); err != nil {
		t.Fatalf("same tick should tear: %v", err)
	}
	if _, _, err := Tear(sheet, 101, 1); err != nil {
		t.Fatalf("one tick late should tear: %v", err)
	}
	if _, _, err := Tear(sheet, 102, 1); !errors.Is(err, ErrStale) {
		t.Fatalf("two ticks late: got %v, want ErrStale", err)
	}
	if _, _, err := Tear(sheet, 99, 1); !errors.Is(err, ErrFuture) {
		t.Fatalf("future tick: got %v, want ErrFuture", err)
	}
}

func TestValidateRejectsMalformedSheets(t *testing.T) {
	base := Sheet{
		ID: "s", Tick: 1,
		Hops: []Hop{{To: "b", Endpoint: "http://b", Nonce: "n1"}},
	}
	tests := []struct {
		name   string
		mutate func(*Sheet)
		want   error
	}{
		{"no id", func(s *Sheet) { s.ID = "" }, ErrInvalid},
		{"no tick", func(s *Sheet) { s.Tick = 0 }, ErrInvalid},
		{"negative index", func(s *Sheet) { s.Index = -1 }, ErrInvalid},
		{"no hops", func(s *Sheet) { s.Hops = nil }, ErrEmpty},
		{"no destination", func(s *Sheet) { s.Hops[0].To = "" }, ErrInvalid},
		{"no endpoint", func(s *Sheet) { s.Hops[0].Endpoint = "" }, ErrInvalid},
		{"no nonce", func(s *Sheet) { s.Hops[0].Nonce = "" }, ErrInvalid},
		{"duplicate nonce", func(s *Sheet) { s.Hops = append(s.Hops, s.Hops[0]) }, ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sheet := base
			sheet.Hops = append([]Hop(nil), base.Hops...)
			tc.mutate(&sheet)
			if err := Validate(sheet); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLedgerRefusesAReplayedTicket(t *testing.T) {
	l := NewLedger()
	if err := l.Redeem("n1", 100, 100, 1); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if err := l.Redeem("n1", 100, 100, 1); !errors.Is(err, ErrReused) {
		t.Fatalf("second redeem: got %v, want ErrReused", err)
	}
	if err := l.Redeem("n2", 100, 100, 1); err != nil {
		t.Fatalf("distinct nonce: %v", err)
	}
	if l.Len() != 2 {
		t.Fatalf("ledger holds %d, want 2", l.Len())
	}
}

func TestLedgerPrunesOnlyExpiredTickets(t *testing.T) {
	l := NewLedger()
	_ = l.Redeem("old", 100, 100, 1)
	_ = l.Redeem("live", 101, 101, 1)

	// At tick 102 the tick-100 nonce can no longer pass Tear; the tick-101
	// nonce is still inside its grace, so its replay must still be caught.
	_ = l.Redeem("new", 102, 102, 1)
	if l.Len() != 2 {
		t.Fatalf("ledger holds %d, want old pruned and two live", l.Len())
	}
	if err := l.Redeem("live", 101, 102, 1); !errors.Is(err, ErrReused) {
		t.Fatalf("live ticket replay: got %v, want ErrReused", err)
	}
}

func TestLedgerSeedsAHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tear-ledger.json")

	parent, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	if err := parent.Redeem("n1", 100, 100, 1); err != nil {
		t.Fatalf("parent redeem: %v", err)
	}

	// The child is launched by rotation and opens the same path.
	child, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := child.Redeem("n1", 100, 100, 1); !errors.Is(err, ErrReused) {
		t.Fatalf("child replay: got %v, want ErrReused", err)
	}
	if err := child.Redeem("n2", 100, 100, 1); err != nil {
		t.Fatalf("child fresh ticket: %v", err)
	}
}

func TestLedgerCatchesAWriteFromTheOtherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tear-ledger.json")

	parent, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("open parent: %v", err)
	}
	child, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}

	// The parent spends after the child loaded; the child's memory is stale,
	// and the reload-on-miss is what closes the handoff window.
	if err := parent.Redeem("n1", 100, 100, 1); err != nil {
		t.Fatalf("parent redeem: %v", err)
	}
	if err := child.Redeem("n1", 100, 100, 1); !errors.Is(err, ErrReused) {
		t.Fatalf("child replay after parent write: got %v, want ErrReused", err)
	}
}

func TestOpenLedgerRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tear-ledger.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLedger(path); err == nil {
		t.Fatal("a corrupt ledger must not silently start empty")
	}
}

func TestLedgerWritesAJSONSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tear-ledger.json")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.Redeem("n1", 100, 100, 1); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if len(data) == 0 || data[0] != '{' {
		t.Fatalf("seed is not JSON: %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("seed mode %o, want 0600", perm)
	}
}
