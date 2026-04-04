// Package report scans JSONL traffic logs and cross-references them against
// Gherkin feature files to produce per-scenario pass/fail results.
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Evidence holds observed call counts for a single MCP method.
type Evidence struct {
	Method    string
	Requests  int
	Responses int
	Errors    int // responses where the "error" field is present
	FirstSeen time.Time
	LastSeen  time.Time
}

// WatcherEvent holds one light-update entry from the smokelink watcher.
type WatcherEvent struct {
	TargetID  string
	Endpoint  string
	OldStatus string
	NewStatus string
	Via       string // "poll" or "sse"
	Timestamp time.Time
}

// WatcherEvidence aggregates watcher observations by endpoint.
type WatcherEvidence struct {
	Endpoints    map[string]int       // endpoint name → update count
	Transitions  int                  // total status changes (old != new)
	BySources    map[string]int       // "poll"→N, "sse"→N
	ByNewStatus  map[string]int       // "green"→N, "red"→N, etc.
	SeenStatuses map[string]bool      // all status values ever seen
	Events       []WatcherEvent
}

// CallLog is the parsed representation of one JSONL log file.
type CallLog struct {
	Path         string
	Evidence     map[string]*Evidence // keyed by method name
	Watcher      WatcherEvidence
	TotalLines   int
	ValidLines   int
	InvalidLines int
	ParseErrors  []string
}

// ScanLog reads a JSONL traffic log and returns call evidence.
func ScanLog(path string) (*CallLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	cl := &CallLog{
		Path:     path,
		Evidence: make(map[string]*Evidence),
		Watcher: WatcherEvidence{
			Endpoints:    make(map[string]int),
			BySources:    make(map[string]int),
			ByNewStatus:  make(map[string]int),
			SeenStatuses: make(map[string]bool),
		},
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		cl.TotalLines++

		var entry map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			cl.InvalidLines++
			cl.ParseErrors = append(cl.ParseErrors, fmt.Sprintf("line %d: %v", cl.TotalLines, err))
			continue
		}
		cl.ValidLines++

		method := jsonString(entry["method"])
		entryType := jsonString(entry["type"])
		hasError := entry["error"] != nil && string(entry["error"]) != "null"

		ts := time.Now()
		if raw, ok := entry["timestamp"]; ok {
			var t time.Time
			if err := json.Unmarshal(raw, &t); err == nil {
				ts = t
			}
		}

		// Handle light-update entries before the method guard — they carry no method field.
		if entryType == "light-update" {
			targetID := jsonString(entry["target_id"])
			endpoint := jsonString(entry["endpoint"])
			oldStatus := jsonString(entry["old_status"])
			newStatus := jsonString(entry["new_status"])
			via := jsonString(entry["via"])

			we := WatcherEvent{
				TargetID:  targetID,
				Endpoint:  endpoint,
				OldStatus: oldStatus,
				NewStatus: newStatus,
				Via:       via,
				Timestamp: ts,
			}
			cl.Watcher.Events = append(cl.Watcher.Events, we)
			if endpoint != "" {
				cl.Watcher.Endpoints[endpoint]++
			}
			if via != "" {
				cl.Watcher.BySources[via]++
			}
			if newStatus != "" {
				cl.Watcher.ByNewStatus[newStatus]++
				cl.Watcher.SeenStatuses[newStatus] = true
			}
			if oldStatus != "" {
				cl.Watcher.SeenStatuses[oldStatus] = true
			}
			if oldStatus != newStatus && oldStatus != "" && newStatus != "" {
				cl.Watcher.Transitions++
			}

			// Synthetic method counter so the method table can show watcher activity
			evW, exists := cl.Evidence["smokelink.light-update"]
			if !exists {
				evW = &Evidence{Method: "smokelink.light-update", FirstSeen: ts}
				cl.Evidence["smokelink.light-update"] = evW
			}
			evW.Requests++
			evW.LastSeen = ts
			continue
		}

		if method == "" {
			continue
		}

		ev, exists := cl.Evidence[method]
		if !exists {
			ev = &Evidence{Method: method, FirstSeen: ts}
			cl.Evidence[method] = ev
		}
		ev.LastSeen = ts

		switch entryType {
		case "request":
			ev.Requests++
		case "response":
			ev.Responses++
			if hasError {
				ev.Errors++
			}
		}
	}

	return cl, scanner.Err()
}

// Seen reports whether a method was called at all.
func (cl *CallLog) Seen(method string) bool {
	ev, ok := cl.Evidence[method]
	return ok && ev.Requests > 0
}

// Succeeded reports whether a method was called and returned at least one non-error response.
func (cl *CallLog) Succeeded(method string) bool {
	ev, ok := cl.Evidence[method]
	if !ok {
		return false
	}
	return ev.Responses-ev.Errors > 0
}

// Errored reports whether a method returned at least one error response.
func (cl *CallLog) Errored(method string) bool {
	ev, ok := cl.Evidence[method]
	return ok && ev.Errors > 0
}

func jsonString(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
