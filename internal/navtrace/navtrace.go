// Package navtrace records how an agent navigates an advertised instruction
// surface towards a flag.
//
// It is deliberately separate from the headless MCPTrafficLog. That log is
// transport-level — method, spec score, latency — and cannot answer which
// instruction was shown, what the agent chose to do, or where it stopped. A
// navigation step is the unit that carries those answers, so it gets its own
// schema and its own writer; the traffic log stays a transport log.
//
// A step exists to measure the instruction-following gap. "Shown" says an
// instruction was presented, "action" says the agent acted, and "stop" records
// the non-action. A model that never calls a tool is a data point, not a
// missing row: the traces are training data for weighting traffic precisely
// because the failures to follow are visible in them.
//
// Nothing here infers a weight. A weight is an aggregate over many steps
// (reach, follow-through, capture) computed offline and recorded in the prior
// metadata, so a change to it is a deliberate baseline change rather than a
// side effect of a run.
package navtrace

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Version is the on-disk schema version. Bump it when a field changes meaning;
// readers may refuse a version they do not know.
const Version = 1

// Stop is why a trace ended without the agent taking the next action. It is
// required on any step that has no action, so an abandoned trace cannot look
// like a truncated file.
type Stop string

const (
	StopFinal       Stop = "final"        // the agent answered in text
	StopNoCall      Stop = "no_call"      // produced neither an action nor an answer
	StopInvalidCall Stop = "invalid_call" // emitted something that did not parse as a tool call
	StopRefused     Stop = "refused"      // the cluster refused the action
	StopBudget      Stop = "budget"       // the step budget was exhausted
	StopFlag        Stop = "flag"         // the flag was captured
)

// valid reports whether s is a known stop reason.
func (s Stop) valid() bool {
	switch s {
	case StopFinal, StopNoCall, StopInvalidCall, StopRefused, StopBudget, StopFlag:
		return true
	}
	return false
}

// Action is what the agent chose to do. It carries the method and the name it
// dialled, plus the argument keys but never the argument values: a ticket id or
// a flag can hide in a value, and a trace is not a body log. The keys are
// enough to distinguish a path call from a skill call from a catalogue draw.
type Action struct {
	Method  string   `json:"method"`
	Name    string   `json:"name,omitempty"`
	ArgKeys []string `json:"arg_keys,omitempty"`
}

// Result is the cluster's answer as far as navigation needs it. Bodies are not
// recorded; the flag is the one value that is, because its presence is the
// capture.
type Result struct {
	OK      bool   `json:"ok"`
	Code    int    `json:"code,omitempty"`
	Alerted bool   `json:"alerted"`
	Flag    string `json:"flag,omitempty"`
}

// Step is one line of a trace.
type Step struct {
	Version     int       `json:"v"`
	Trace       string    `json:"trace"`
	Step        int       `json:"step"`
	At          time.Time `json:"at"`
	Goal        string    `json:"goal"`
	Follower    string    `json:"follower"`        // "scripted" or "model:<id>"
	Depth       int       `json:"depth"`           // position in the instruction chain
	Skill       uint32    `json:"skill,omitempty"` // 32-bit capability id (0 = ambient/unattributed)
	Instruction string    `json:"instruction,omitempty"`
	Shown       bool      `json:"shown"`
	Action      *Action   `json:"action,omitempty"`
	Result      *Result   `json:"result,omitempty"`
	Stop        Stop      `json:"stop,omitempty"`
	LatencyMS   float64   `json:"latency_ms,omitempty"`
}

// Validate enforces the shape a reader can rely on. It fails closed: a step
// that says nothing about how it ended is rejected rather than written.
func (s Step) Validate() error {
	if s.Trace == "" {
		return fmt.Errorf("navtrace: trace id is required")
	}
	if s.Step < 1 {
		return fmt.Errorf("navtrace: step must be >= 1, got %d", s.Step)
	}
	if s.Goal == "" {
		return fmt.Errorf("navtrace: goal is required")
	}
	if s.Follower == "" {
		return fmt.Errorf("navtrace: follower is required")
	}
	if s.Depth < 0 {
		return fmt.Errorf("navtrace: depth must be >= 0, got %d", s.Depth)
	}
	if s.Shown && s.Instruction == "" {
		return fmt.Errorf("navtrace: step %d shown without an instruction", s.Step)
	}
	if s.Stop != "" && !s.Stop.valid() {
		return fmt.Errorf("navtrace: unknown stop reason %q", s.Stop)
	}
	// A step with no action must say why it took none. A step with an action
	// may continue (no stop) or end the trace (refused, flag, final, budget).
	if s.Action == nil && s.Stop == "" {
		return fmt.Errorf("navtrace: step %d has neither an action nor a stop reason", s.Step)
	}
	if s.Action != nil && s.Action.Method == "" {
		return fmt.Errorf("navtrace: step %d action has no method", s.Step)
	}
	if s.Result != nil && s.Result.Flag != "" && s.Stop == StopNoCall {
		return fmt.Errorf("navtrace: step %d captured a flag but claims no call", s.Step)
	}
	return nil
}

// Recorder appends steps as JSONL.
//
// It never drops silently: a write failure is returned and counted, and the
// trace stays incomplete on disk rather than pretending the step happened.
// Concurrent recorders are safe; lines are written whole under a mutex.
type Recorder struct {
	mu      sync.Mutex
	enc     *json.Encoder
	now     func() time.Time
	dropped atomic.Int64
}

// NewRecorder writes JSONL steps to w. If now is nil, time.Now is used.
func NewRecorder(w io.Writer) *Recorder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Recorder{enc: enc, now: time.Now}
}

// SetClock replaces the clock, for deterministic fixtures. Call it before the
// first Record.
func (r *Recorder) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// Record validates and appends one step. Version and At are stamped when the
// caller leaves them zero.
func (r *Recorder) Record(step Step) error {
	if step.Version == 0 {
		step.Version = Version
	}
	if step.At.IsZero() {
		step.At = r.now().UTC()
	}
	if err := step.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.enc.Encode(step); err != nil {
		r.dropped.Add(1)
		return fmt.Errorf("navtrace: write step %d of %s: %w", step.Step, step.Trace, err)
	}
	return nil
}

// Dropped reports steps that failed to write.
func (r *Recorder) Dropped() int64 { return r.dropped.Load() }
