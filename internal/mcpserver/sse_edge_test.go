package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/james-gibson/adhd/internal/config"
	"github.com/james-gibson/adhd/internal/lights"
)

// This file exercises the edge contract: GET /mcp opens an unbuffered SSE
// stream, counts what leaves the process, and reports how it ended. The point
// of the record is that a stream which ends without a reason cannot be costed.

func newEdgeTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(config.DefaultConfig().MCPServer, lights.NewCluster())
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mcp", s.handleMCPSSE)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts
}

func TestEdgeStreamHandshakeAndClientAbort(t *testing.T) {
	s, ts := newEdgeTestServer(t)

	records := make(chan StreamRecord, 1)
	s.SetStreamObserver(func(rec StreamRecord) { records <- rec })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type: got %q, want text/event-stream", got)
	}
	// The header is part of the contract: an intermediary that re-buffers the
	// stream would silently turn TTFB back into total duration.
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("x-accel-buffering: got %q, want no", got)
	}

	// One frame must arrive before anything else, so a connected client knows
	// it is connected.
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read event line: %v", err)
	}
	if line != "event: endpoint\n" {
		t.Fatalf("first frame event: got %q, want %q", line, "event: endpoint\n")
	}
	line, err = br.ReadString('\n')
	if err != nil {
		t.Fatalf("read data line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, "listening") {
		t.Fatalf("handshake data: got %q, want a data line carrying \"listening\"", line)
	}

	// Hang up. The server sees the request context done and must say so.
	cancel()

	select {
	case rec := <-records:
		if rec.ClosedBy != ClosedByClientAbort {
			t.Errorf("closed_by: got %q, want %q", rec.ClosedBy, ClosedByClientAbort)
		}
		if rec.EventCount != 1 {
			t.Errorf("event_count: got %d, want 1", rec.EventCount)
		}
		if rec.Bytes <= 0 {
			t.Errorf("bytes_streamed: got %d, want > 0", rec.Bytes)
		}
		if rec.TTFB <= 0 {
			t.Errorf("ttfb: got %v, want > 0", rec.TTFB)
		}
		if rec.Duration <= 0 {
			t.Errorf("duration: got %v, want > 0", rec.Duration)
		}
		if rec.UserAgent == "" {
			t.Error("user_agent: empty, want the caller's")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no stream record after client abort")
	}
}

func TestEdgeStreamRequiresFlusher(t *testing.T) {
	s := NewServer(config.DefaultConfig().MCPServer, lights.NewCluster())

	w := &flushlessWriter{}
	s.handleMCPSSE(w, httptest.NewRequest(http.MethodGet, "/mcp", nil))

	if w.code != http.StatusInternalServerError {
		t.Fatalf("status: got %d, want %d", w.code, http.StatusInternalServerError)
	}
	if !strings.Contains(w.body.String(), "Streaming not supported") {
		t.Fatalf("body: got %q, want the streaming refusal", w.body.String())
	}
}

func TestSSEEdgeCountsWhatItWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	edge := newSSEEdge(rec, rec)

	if err := edge.send("endpoint", map[string]string{"status": "listening"}); err != nil {
		t.Fatalf("send handshake: %v", err)
	}
	if err := edge.send("", map[string]int{"n": 1}); err != nil {
		t.Fatalf("send unnamed frame: %v", err)
	}

	got := edge.record(ClosedByServerClose, nil)

	if got.EventCount != 2 {
		t.Errorf("event_count: got %d, want 2", got.EventCount)
	}
	if got.Bytes != int64(rec.Body.Len()) {
		t.Errorf("bytes_streamed: got %d, want %d", got.Bytes, rec.Body.Len())
	}
	if n := strings.Count(rec.Body.String(), "\n\n"); n != 2 {
		t.Errorf("frame terminators: got %d, want 2", n)
	}
	if !strings.HasPrefix(rec.Body.String(), "event: endpoint\ndata: ") {
		t.Errorf("frame shape: got %q", rec.Body.String())
	}
	if got.TTFB <= 0 {
		t.Errorf("ttfb: got %v, want > 0", got.TTFB)
	}
	if got.ClosedBy != ClosedByServerClose {
		t.Errorf("closed_by: got %q, want %q", got.ClosedBy, ClosedByServerClose)
	}
	if got.Duration <= 0 {
		t.Errorf("duration: got %v, want > 0", got.Duration)
	}
}

func TestSSEEdgeReportsWriteAndEncodeFailures(t *testing.T) {
	// A frame that cannot be encoded never reaches the wire, so it must fail
	// rather than count as sent.
	edge := newSSEEdge(httptest.NewRecorder(), httptest.NewRecorder())
	if err := edge.send("bad", make(chan int)); err == nil {
		t.Fatal("send of an unencodable payload: got nil error, want a marshal error")
	}

	// A write that fails is an error close, not a normal one.
	failing := newSSEEdge(&failingWriter{}, nil)
	if err := failing.send("endpoint", map[string]string{"status": "listening"}); err == nil {
		t.Fatal("send over a failing writer: got nil error, want a write error")
	}
	if got := failing.events; got != 0 {
		t.Errorf("event_count: got %d, want 0 — a failed write is not a sent frame", got)
	}
}

// flushlessWriter is an http.ResponseWriter that is deliberately not an
// http.Flusher, so the handler's capability check can be exercised.
type flushlessWriter struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (w *flushlessWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *flushlessWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *flushlessWriter) WriteHeader(code int) { w.code = code }

// failingWriter accepts nothing.
type failingWriter struct{ header http.Header }

func (w *failingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *failingWriter) Write([]byte) (int, error) { return 0, errors.New("write refused") }

func (w *failingWriter) WriteHeader(int) {}
