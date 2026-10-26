package mirror

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func socketPath(t *testing.T) string {
	t.Helper()
	// Unix socket paths are capped (~104 bytes on macOS); t.TempDir() is too long.
	dir, err := os.MkdirTemp("/tmp", "mir")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "m.sock")
}

// TestBatchesAreMetadataOnly is the cost boundary: the backup mirrors the map
// traversal, not a second copy of the traffic.
func TestBatchesAreMetadataOnly(t *testing.T) {
	frame := Frame{
		Tick: 88, Kind: "path.hop", Path: "4", From: "node-a",
		To: "http://node-b/mcp", Feature: "gh", Caller: "10.0.0.7",
		At: time.Unix(1700000000, 0).UTC(),
	}
	data, err := json.Marshal(Batch{Tick: 88, Frames: []Frame{frame}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"body", "payload", "params", "result", "response"} {
		if strings.Contains(string(data), `"`+forbidden+`"`) {
			t.Fatalf("mirror batch carries a %q field: %s", forbidden, data)
		}
	}
	var roundTrip Batch
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Tick != 88 || len(roundTrip.Frames) != 1 || roundTrip.Frames[0].Path != "4" {
		t.Fatalf("round trip lost data: %+v", roundTrip)
	}
}

func TestReaderReceivesTickBatchesInOrder(t *testing.T) {
	path := socketPath(t)
	reader, err := Listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = reader.Close() }()

	writer, err := Dial(path, 0, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = writer.Close() }()

	for _, tick := range []uint64{88, 89} {
		if err := writer.Send(Batch{Tick: tick, Frames: []Frame{{Tick: tick, Kind: "path.hop", Path: "4"}}}); err != nil {
			t.Fatalf("send tick %d: %v", tick, err)
		}
	}

	for _, want := range []uint64{88, 89} {
		batch, err := reader.Receive(time.Second)
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if batch.Tick != want {
			t.Fatalf("batch tick = %d, want %d", batch.Tick, want)
		}
		if len(batch.Frames) != 1 || batch.Frames[0].Tick != want {
			t.Fatalf("batch frames = %+v", batch.Frames)
		}
	}
}

// TestBackpressureIsSignalled fills the backup's socket buffer and requires the
// writer to report backpressure instead of blocking or growing an unbounded
// queue.
func TestBackpressureIsSignalled(t *testing.T) {
	path := socketPath(t)
	reader, err := Listen(path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = reader.Close() }()

	writer, err := Dial(path, 8192, 2*time.Millisecond)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = writer.Close() }()

	frames := make([]Frame, MaxFramesPerBatch)
	for i := range frames {
		frames[i] = Frame{Tick: 88, Kind: "path.hop", Path: "4", From: "node-a", To: "http://node-b/mcp", Caller: "10.0.0.7"}
	}

	var sawBackpressure bool
	for i := 0; i < 500; i++ {
		err := writer.Send(Batch{Tick: 88, Frames: frames})
		if err == nil {
			continue
		}
		if errors.Is(err, ErrBackpressure) {
			sawBackpressure = true
			break
		}
		t.Fatalf("unexpected send error: %v", err)
	}
	if !sawBackpressure {
		t.Fatal("never reported backpressure while the backup was not draining")
	}

	// Draining releases it: one read is enough for the next send to fit.
	if _, err := reader.Receive(time.Second); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := writer.Send(Batch{Tick: 89, Frames: frames[:1]}); err != nil {
		t.Fatalf("send after drain: %v", err)
	}
}

func TestBatcherSplitsOnTickChange(t *testing.T) {
	b := NewBatcher(2)

	if ready := b.Append(Frame{Tick: 88}); ready != nil {
		t.Fatalf("first frame produced a batch: %+v", ready)
	}
	ready := b.Append(Frame{Tick: 89})
	if ready == nil || ready.Tick != 88 || len(ready.Frames) != 1 {
		t.Fatalf("tick change did not flush tick 88: %+v", ready)
	}
	if tail := b.Flush(); tail == nil || tail.Tick != 89 || len(tail.Frames) != 1 {
		t.Fatalf("flush did not return tick 89: %+v", tail)
	}

	full := NewBatcher(2)
	_ = full.Append(Frame{Tick: 90})
	if ready := full.Append(Frame{Tick: 90}); ready != nil {
		t.Fatalf("batch flushed before it was full: %+v", ready)
	}
	if ready := full.Append(Frame{Tick: 90}); ready == nil || len(ready.Frames) != 2 {
		t.Fatalf("full batch did not flush: %+v", ready)
	}
}
