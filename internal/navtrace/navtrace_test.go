package navtrace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixedClock() time.Time {
	return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
}

func TestRecordsAScriptedWalk(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	rec.SetClock(fixedClock)

	steps := []Step{
		{
			Trace: "tr-1", Step: 1, Goal: "flag-skill-0", Follower: "scripted", Depth: 1,
			Instruction: "skill.check-working", Shown: true,
			Action: &Action{Method: "tools/call", Name: "adhd.skill.check-working.call", ArgKeys: []string{"id"}},
			Result: &Result{OK: true},
		},
		{
			Trace: "tr-1", Step: 2, Goal: "flag-skill-0", Follower: "scripted", Depth: 2,
			Instruction: "skill.ticket-02", Shown: true,
			Action: &Action{Method: "tools/call", Name: "adhd.skill.check-working.call", ArgKeys: []string{"id", "ticket"}},
			Result: &Result{OK: true, Flag: "flag{skill:0:2}"},
			Stop:   StopFlag,
		},
	}
	for _, step := range steps {
		if err := rec.Record(step); err != nil {
			t.Fatalf("record step %d: %v", step.Step, err)
		}
	}
	if got := rec.Dropped(); got != 0 {
		t.Fatalf("dropped %d steps", got)
	}

	lines := decodeLines(t, buf.Bytes())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0].Version != Version {
		t.Fatalf("version %d, want %d", lines[0].Version, Version)
	}
	if !lines[0].At.Equal(fixedClock()) {
		t.Fatalf("clock not applied: %v", lines[0].At)
	}
	if lines[1].Result == nil || lines[1].Result.Flag != "flag{skill:0:2}" {
		t.Fatalf("flag not recorded: %+v", lines[1].Result)
	}
	if lines[1].Stop != StopFlag {
		t.Fatalf("stop %q, want %q", lines[1].Stop, StopFlag)
	}
}

func TestRecordsANonAction(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	rec.SetClock(fixedClock)

	err := rec.Record(Step{
		Trace: "tr-2", Step: 1, Goal: "flag-skill-0", Follower: "model:local", Depth: 1,
		Instruction: "skill.check-working", Shown: true, Stop: StopNoCall,
	})
	if err != nil {
		t.Fatalf("a non-action with a reason is a valid step: %v", err)
	}
	lines := decodeLines(t, buf.Bytes())
	if lines[0].Action != nil || lines[0].Stop != StopNoCall {
		t.Fatalf("unexpected step: %+v", lines[0])
	}
}

func TestValidateRejectsIncompleteSteps(t *testing.T) {
	base := Step{
		Trace: "tr", Step: 1, Goal: "g", Follower: "scripted", Depth: 1,
		Action: &Action{Method: "tools/call"},
	}
	tests := []struct {
		name   string
		mutate func(*Step)
		want   string
	}{
		{"no trace", func(s *Step) { s.Trace = "" }, "trace id"},
		{"zero step", func(s *Step) { s.Step = 0 }, "step must be"},
		{"no goal", func(s *Step) { s.Goal = "" }, "goal"},
		{"no follower", func(s *Step) { s.Follower = "" }, "follower"},
		{"negative depth", func(s *Step) { s.Depth = -1 }, "depth"},
		{"shown without instruction", func(s *Step) { s.Shown = true }, "shown without"},
		{"unknown stop", func(s *Step) { s.Stop = "paused" }, "unknown stop"},
		{"no action no stop", func(s *Step) { s.Action = nil }, "neither an action"},
		{"action without method", func(s *Step) { s.Action.Method = "" }, "no method"},
		{"flag without a call", func(s *Step) {
			s.Action = nil
			s.Stop = StopNoCall
			s.Result = &Result{Flag: "flag{x}"}
		}, "claims no call"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			step := base
			action := *base.Action
			step.Action = &action
			tc.mutate(&step)
			err := step.Validate()
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestStepCarriesThe32BitSkillID(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	rec.SetClock(fixedClock)
	id := ID("adhd.skill.route-plan.call")
	if err := rec.Record(Step{
		Trace: "tr-skill", Step: 1, Goal: "g", Follower: "scripted", Depth: 1, Skill: id,
		Action: &Action{Method: "tools/call", Name: "adhd.skill.route-plan.call"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	lines := decodeLines(t, buf.Bytes())
	if lines[0].Skill != id || id == 0 {
		t.Fatalf("skill id %d, want %d", lines[0].Skill, id)
	}
}

func TestArgValuesAreNotRecorded(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)
	rec.SetClock(fixedClock)
	_ = rec.Record(Step{
		Trace: "tr-3", Step: 1, Goal: "g", Follower: "scripted", Depth: 1,
		Action: &Action{Method: "tools/call", Name: "adhd.skill.call", ArgKeys: []string{"id"}},
	})
	if strings.Contains(buf.String(), "working-03") {
		t.Fatalf("a trace must not carry argument values: %s", buf.String())
	}
}

func TestConcurrentRecordingsStayWholeLines(t *testing.T) {
	var buf bytes.Buffer
	rec := NewRecorder(&buf)

	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				err := rec.Record(Step{
					Trace: "tr", Step: j + 1, Goal: "g", Follower: "scripted", Depth: 1,
					Action: &Action{Method: "tools/call", Name: "adhd.skill.call"},
				})
				if err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	lines := decodeLines(t, buf.Bytes())
	if len(lines) != writers*perWriter {
		t.Fatalf("got %d lines, want %d", len(lines), writers*perWriter)
	}
}

func TestWriteFailureIsCountedNotSwallowed(t *testing.T) {
	rec := NewRecorder(failingWriter{})
	err := rec.Record(Step{
		Trace: "tr-4", Step: 1, Goal: "g", Follower: "scripted", Depth: 1,
		Action: &Action{Method: "tools/call"},
	})
	if err == nil {
		t.Fatal("want a write error")
	}
	if rec.Dropped() != 1 {
		t.Fatalf("dropped %d, want 1", rec.Dropped())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func decodeLines(t *testing.T, raw []byte) []Step {
	t.Helper()
	var out []Step
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var step Step
		if err := json.Unmarshal(sc.Bytes(), &step); err != nil {
			t.Fatalf("decode %q: %v", sc.Text(), err)
		}
		out = append(out, step)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}
