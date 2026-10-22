package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// This file defines the local edge contract for the server→client SSE stream on
// GET /mcp.
//
// The edge is deliberately unbuffered. A stream that waits for a buffer before
// it writes is not a stream: it pays the whole response's latency before the
// first byte, and it grows with the response. Frames are written and flushed as
// they are produced, and the cost is counted on the way out rather than
// inspected afterwards.
//
// The interior of the cluster does not use this path. A peer call is one POST
// that answers application/json, which is what makes per-edge byte counts,
// attribution by JSON-RPC id, and retry possible. SSE is the last inch to the
// client, not the transport between isotopes. Retiring the dual-endpoint
// HTTP+SSE *transport* does not retire the encoding: Streamable HTTP still
// answers a POST with an SSE stream when the client asks for one.
//
// Unbuffered has one consequence the router must live with: the first flushed
// frame commits the response. A route cannot be changed after TTFB, so every
// input a routing decision needs — health, hop count, rung, expected response
// size — has to be known before the stream opens. There is no observation of
// the current call to route on.

// ClosedBy says how an edge stream ended. A stream that ends without saying why
// cannot be costed: the bytes may have been produced and never delivered, and a
// router that cannot tell the difference will prefer whichever route lets
// callers hang up earliest.
type ClosedBy string

const (
	// ClosedByClientAbort is the client hanging up. Tokens were produced; bytes
	// never arrived. This is the case that understates cost if it is not
	// recorded separately.
	ClosedByClientAbort ClosedBy = "client_abort"
	// ClosedByServerClose is the server ending a stream the client is still
	// entitled to receive.
	ClosedByServerClose ClosedBy = "server_close"
	// ClosedByError is a failed write, or a frame that could not be encoded.
	ClosedByError ClosedBy = "error"
)

// StreamRecord is the cost of one edge stream.
//
// TTFB and Duration are separate because they answer different questions. TTFB
// is what a caller feels and what a router can compare across routes; Duration
// is a property of the payload, which the route did not choose. A single
// latency float cannot carry both, and a mean over both ranks a fast route with
// a big answer as slow.
type StreamRecord struct {
	RemoteAddr string        `json:"remote_addr,omitempty"`
	UserAgent  string        `json:"user_agent,omitempty"`
	Started    time.Time     `json:"started"`
	TTFB       time.Duration `json:"ttfb_ns"`
	Duration   time.Duration `json:"duration_ns"`
	EventCount int           `json:"event_count"`
	Bytes      int64         `json:"bytes_streamed"`
	ClosedBy   ClosedBy      `json:"closed_by"`
}

// StreamObserver receives a record when an edge stream closes. It is called
// on the stream's own goroutine, after the last frame, and only once per
// stream.
type StreamObserver func(StreamRecord)

// SetStreamObserver registers the sink for edge stream cost records, typically
// the traffic log. With no observer set, records are logged at debug level
// rather than dropped: a stream whose cost is invisible is a stream the router
// cannot account for.
func (s *Server) SetStreamObserver(obs StreamObserver) {
	s.streamObserver = obs
}

// reportStream hands one closed edge stream to the observer.
func (s *Server) reportStream(rec StreamRecord) {
	if s.streamObserver != nil {
		s.streamObserver(rec)
		return
	}
	slog.Debug("edge stream closed",
		"closed_by", rec.ClosedBy,
		"events", rec.EventCount,
		"bytes", rec.Bytes,
		"ttfb", rec.TTFB,
		"duration", rec.Duration,
	)
}

// sseEdge is an unbuffered, counting http.ResponseWriter for one edge stream.
//
// It implements http.Flusher so it can stand in for the wrapped writer anywhere
// the handler would otherwise type-assert. Ownership is single-goroutine: one
// handler writes one stream, so the counters need no lock.
type sseEdge struct {
	w       http.ResponseWriter
	flusher http.Flusher

	started time.Time
	ttfb    time.Duration
	bytes   int64
	events  int
}

// newSSEEdge wraps w. A nil flusher is allowed: the frames still count, they
// just are not pushed out.
func newSSEEdge(w http.ResponseWriter, flusher http.Flusher) *sseEdge {
	return &sseEdge{w: w, flusher: flusher, started: time.Now()}
}

func (e *sseEdge) Header() http.Header  { return e.w.Header() }
func (e *sseEdge) WriteHeader(code int) { e.w.WriteHeader(code) }
func (e *sseEdge) Flush() {
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

// Write forwards immediately and counts what actually left the process. There
// is no buffer to measure afterwards, so this is the only place bytes can be
// observed; the first successful write is also the only honest TTFB.
func (e *sseEdge) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if n > 0 {
		e.bytes += int64(n)
		if e.ttfb == 0 {
			e.ttfb = time.Since(e.started)
		}
	}
	return n, err
}

// send writes one SSE frame: an optional event name, one data line, and the
// blank line that terminates it. The frame is flushed before send returns, so a
// caller that has seen send succeed knows the frame is committed.
func (e *sseEdge) send(event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sse edge: marshal %s: %w", event, err)
	}

	var frame bytes.Buffer
	if event != "" {
		fmt.Fprintf(&frame, "event: %s\n", event)
	}
	frame.WriteString("data: ")
	frame.Write(data)
	frame.WriteString("\n\n")

	if _, err := e.Write(frame.Bytes()); err != nil {
		return fmt.Errorf("sse edge: write %s: %w", event, err)
	}
	e.events++
	e.Flush()
	return nil
}

// record snapshots the stream's cost. Duration includes the time the stream sat
// idle waiting for a message, which is why it is not a latency figure on its own.
func (e *sseEdge) record(closedBy ClosedBy, r *http.Request) StreamRecord {
	rec := StreamRecord{
		Started:    e.started,
		TTFB:       e.ttfb,
		Duration:   time.Since(e.started),
		EventCount: e.events,
		Bytes:      e.bytes,
		ClosedBy:   closedBy,
	}
	if r != nil {
		rec.RemoteAddr = r.RemoteAddr
		rec.UserAgent = r.UserAgent()
	}
	return rec
}
