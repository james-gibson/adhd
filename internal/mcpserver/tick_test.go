package mcpserver

import (
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

func TestPublicTickBuckets(t *testing.T) {
	// Unix seconds are UTC; the tick is floor(unix / 5). 0..4 share tick 0.
	cases := []struct {
		unix int64
		want uint64
	}{
		{0, 0}, {1, 0}, {4, 0},
		{5, 1}, {6, 1}, {9, 1},
		{10, 2},
		{1700000000, 340000000},
	}
	for _, tc := range cases {
		if got := PublicTick(time.Unix(tc.unix, 0)); got != tc.want {
			t.Errorf("PublicTick(%d) = %d, want %d", tc.unix, got, tc.want)
		}
	}

	// A tick is a 5-second window: the start and the last second agree, the
	// next second does not.
	start := time.Unix(1700000000, 0)
	if PublicTick(start) != PublicTick(start.Add(4*time.Second)) {
		t.Error("tick changed inside its five-second window")
	}
	if PublicTick(start) == PublicTick(start.Add(5*time.Second)) {
		t.Error("tick did not advance at the five-second boundary")
	}
}

func TestDefaultTickSourceIsPublicTick(t *testing.T) {
	s := NewServer(config.MCPServerConfig{}, lights.NewCluster())

	before := PublicTick(time.Now())
	got, ok := s.nowTick()
	after := PublicTick(time.Now())
	if !ok {
		t.Fatal("default server has no tick source")
	}
	if got < before || got > after {
		t.Fatalf("default tick %d outside [%d,%d]", got, before, after)
	}
}

func TestSetTickSourceOverridesPublicTick(t *testing.T) {
	s := NewServer(config.MCPServerConfig{}, lights.NewCluster())
	s.SetTickSource(func() uint64 { return 88 })

	got, ok := s.nowTick()
	if !ok || got != 88 {
		t.Fatalf("override tick = %d (ok=%v), want 88", got, ok)
	}
}

func TestTickWithinGrace(t *testing.T) {
	// Negotiated on 88: usable on 88 and 89, not on 87 (future) or 90 (two late).
	cases := []struct {
		negotiated, now uint64
		want            bool
	}{
		{88, 88, true},
		{88, 89, true},
		{88, 90, false},
		{88, 87, false},
		{88, 0, false},
	}
	for _, tc := range cases {
		if got := tickWithinGrace(tc.negotiated, tc.now); got != tc.want {
			t.Errorf("tickWithinGrace(%d, %d) = %v, want %v", tc.negotiated, tc.now, got, tc.want)
		}
	}
}
