// Package mirror carries the primary node's traffic mirror to a backup over a
// host-local Unix datagram socket.
//
// The feed is deliberately metadata-only: tick, path identity, endpoints and
// caller, never a request or response body. The backup needs enough to
// reconstruct which map was traversed on which tick, not a second copy of the
// traffic it would have to process.
//
// Framing is per tick: the primary accumulates frames during a tick and flushes
// one datagram at the boundary. A full socket buffer is backpressure, not an
// unbounded queue — the writer reports it and the caller decides whether to
// drop the mirror or slow the primary.
package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// MaxDatagram bounds one flushed batch. macOS caps a Unix datagram at roughly
// 8 KiB, so batches are chunked well below that rather than relying on the
// larger Linux limit. Mirror frames are small metadata; 32 frames fit.
const MaxDatagram = 8 * 1024

// MaxFramesPerBatch is the Batcher default: 16 metadata frames stay inside one
// datagram (and inside a small send buffer) on every supported platform.
const MaxFramesPerBatch = 16

// maxSocketPath is the portable Unix socket path limit (macOS is 104 bytes).
const maxSocketPath = 100

// ErrBackpressure means the backup is not draining fast enough. The mirror is
// an observability side channel: callers should count and drop, not block the
// request path indefinitely.
var ErrBackpressure = errors.New("mirror: backup is not draining (backpressure)")

// Frame is one mirrored fact. It intentionally has no payload field.
type Frame struct {
	Tick     uint64    `json:"tick"`                // tick the route was negotiated for
	UsedTick uint64    `json:"used_tick,omitempty"` // tick the hop was actually used on
	Kind     string    `json:"kind"`
	Path     string    `json:"path,omitempty"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	Feature  string    `json:"feature,omitempty"`
	Skill    uint32    `json:"skill,omitempty"` // 32-bit capability id (0 = none)
	Caller   string    `json:"caller,omitempty"`
	At       time.Time `json:"at"`
}

// Batch is one tick's frames.
type Batch struct {
	Tick   uint64  `json:"tick"`
	Frames []Frame `json:"frames"`
}

// Writer sends tick batches to a backup's datagram socket.
type Writer struct {
	conn    *net.UnixConn
	timeout time.Duration
}

// Dial connects to the backup's socket. writeBuffer bounds how much mirroring
// the kernel will queue before Send reports backpressure.
func Dial(path string, writeBuffer int, timeout time.Duration) (*Writer, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("mirror: dial %s: %w", path, err)
	}
	if writeBuffer > 0 {
		if err := conn.SetWriteBuffer(writeBuffer); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("mirror: set write buffer: %w", err)
		}
	}
	if timeout <= 0 {
		timeout = 5 * time.Millisecond
	}
	return &Writer{conn: conn, timeout: timeout}, nil
}

func (w *Writer) Close() error { return w.conn.Close() }

// Send flushes one tick. It never blocks longer than the writer's timeout: a
// timeout or a full buffer is reported as ErrBackpressure so the primary can
// drop the mirror sample instead of stalling the request path.
func (w *Writer) Send(batch Batch) error {
	data, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("mirror: marshal: %w", err)
	}
	if len(data) > MaxDatagram {
		return fmt.Errorf("mirror: batch of %d frames is %d bytes (max %d); flush in chunks",
			len(batch.Frames), len(data), MaxDatagram)
	}

	if err := w.conn.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
		return fmt.Errorf("mirror: deadline: %w", err)
	}
	if _, err := w.conn.Write(data); err != nil {
		var netErr net.Error
		switch {
		case errors.As(err, &netErr) && netErr.Timeout():
			return ErrBackpressure
		case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.ENOBUFS):
			return ErrBackpressure
		default:
			return fmt.Errorf("mirror: write: %w", err)
		}
	}
	return nil
}

// Batcher accumulates frames for the current tick. Append returns a batch when
// the tick advances or the batch is full; Flush returns the tail at shutdown.
type Batcher struct {
	tick   uint64
	frames []Frame
	max    int
}

func NewBatcher(max int) *Batcher {
	if max <= 0 {
		max = MaxFramesPerBatch
	}
	return &Batcher{max: max}
}

// Append adds a frame and returns the previous batch when it is ready to send.
func checkPath(path string) error {
	if path == "" {
		return errors.New("mirror: empty socket path")
	}
	if len(path) > maxSocketPath {
		return fmt.Errorf("mirror: socket path is %d bytes (max %d): %s", len(path), maxSocketPath, path)
	}
	return nil
}

func (b *Batcher) Append(f Frame) *Batch {
	if len(b.frames) > 0 && (f.Tick != b.tick || len(b.frames) >= b.max) {
		ready := &Batch{Tick: b.tick, Frames: b.frames}
		b.frames = nil
		b.tick = f.Tick
		b.frames = append(b.frames, f)
		return ready
	}
	b.tick = f.Tick
	b.frames = append(b.frames, f)
	return nil
}

// Flush returns the remaining frames, if any.
func (b *Batcher) Flush() *Batch {
	if len(b.frames) == 0 {
		return nil
	}
	ready := &Batch{Tick: b.tick, Frames: b.frames}
	b.frames = nil
	return ready
}

// Reader receives batches on the backup node.
type Reader struct {
	conn *net.UnixConn
	path string
}

// Listen binds the backup's datagram socket at path.
func Listen(path string) (*Reader, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	addr := &net.UnixAddr{Name: path, Net: "unixgram"}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("mirror: listen %s: %w", path, err)
	}
	return &Reader{conn: conn, path: path}, nil
}

func (r *Reader) Close() error {
	err := r.conn.Close()
	_ = os.Remove(r.path)
	return err
}

// Receive blocks for the next batch or the deadline.
func (r *Reader) Receive(timeout time.Duration) (Batch, error) {
	var batch Batch
	if timeout > 0 {
		if err := r.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return batch, fmt.Errorf("mirror: read deadline: %w", err)
		}
	}
	buf := make([]byte, MaxDatagram)
	n, _, err := r.conn.ReadFromUnix(buf)
	if err != nil {
		return batch, err
	}
	if err := json.Unmarshal(buf[:n], &batch); err != nil {
		return batch, fmt.Errorf("mirror: decode batch: %w", err)
	}
	return batch, nil
}
