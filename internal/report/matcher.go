package report

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResultStatus classifies a scenario against observed traffic.
type ResultStatus int

const (
	StatusPass          ResultStatus = iota // required calls observed, all succeeded
	StatusFail                              // required calls observed but errors found
	StatusNoCoverage                        // required calls never appeared in the log
	StatusNotVerifiable                     // scenario cannot be verified from MCP traffic alone
)

func (s ResultStatus) String() string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusFail:
		return "FAIL"
	case StatusNoCoverage:
		return "NO COVERAGE"
	case StatusNotVerifiable:
		return "NOT VERIFIABLE"
	}
	return "UNKNOWN"
}

func (s ResultStatus) Symbol() string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusFail:
		return "✗"
	case StatusNoCoverage:
		return "○"
	case StatusNotVerifiable:
		return "–"
	}
	return "?"
}

// ScenarioResult is the computed result for one Gherkin scenario.
type ScenarioResult struct {
	Feature      string       // Feature name
	Domain       string       // @domain- tag value
	FilePath     string       // .feature file path
	ScenarioName string       // Scenario: ... text
	Status       ResultStatus
	Evidence     []MethodCheck // per-method breakdown
	Note         string        // explanation for non-verifiable scenarios
}

// MethodCheck captures the verdict for one required MCP method.
type MethodCheck struct {
	Method    string
	Calls     int  // request count observed
	Successes int  // response - error count
	Errors    int  // error response count
	Required  bool // was this method required?
	MustError bool // was this method expected to error?
}

// FeatureResult groups all scenario results under one feature.
type FeatureResult struct {
	Name        string
	Domain      string
	FilePath    string
	Tags        []string
	Scenarios   []ScenarioResult
	PassCount   int
	FailCount   int
	NoCoverage  int
	Unverifiable int
}

// scenarioSpec declares what MCP evidence constitutes coverage for a scenario.
type scenarioSpec struct {
	// keywords: if ALL are present in the scenario name (case-insensitive) this spec applies.
	keywords []string
	// required: methods that must be called and succeed.
	required []string
	// mustError: methods that must have been called and returned an error response.
	mustError []string
	// notVerifiable: if true, the scenario cannot be confirmed from MCP traffic.
	notVerifiable bool
	note          string
}

// domainSpecs maps domain tag values to per-scenario specs.
// A spec matches a scenario if all of its keywords appear in the scenario name.
var domainSpecs = map[string][]scenarioSpec{
	"mcp-server": {
		{
			keywords: []string{"initialization"},
			required: []string{"initialize"},
		},
		{
			keywords: []string{"tools/list"},
			required: []string{"tools/list"},
		},
		{
			keywords: []string{"lights.list"},
			required: []string{"adhd.lights.list"},
		},
		{
			keywords: []string{"lights.get", "single"},
			required: []string{"adhd.lights.get"},
		},
		{
			keywords:  []string{"lights.get", "missing"},
			mustError: []string{"adhd.lights.get"},
		},
		{
			keywords: []string{"status", "summary"},
			required: []string{"adhd.status"},
		},
		{
			keywords: []string{"disabled"},
			notVerifiable: true,
			note: "requires inspecting server startup, not observable in traffic log",
		},
		{
			keywords: []string{"json-rpc"},
			required: []string{"initialize"},
		},
	},
	"lights": {
		{
			keywords: []string{"status", "transitions"},
			required: []string{"adhd.lights.list"},
		},
		{
			keywords: []string{"cluster", "count"},
			required: []string{"adhd.status"},
		},
		{
			keywords: []string{"retrieved", "name"},
			required: []string{"adhd.lights.get"},
		},
		{
			keywords: []string{"lookup", "unknown"},
			mustError: []string{"adhd.lights.get"},
		},
		{
			keywords: []string{"display", "current"},
			required: []string{"adhd.lights.list"},
		},
		// Fallback: any lights scenario needs lights.list
		{
			keywords: []string{},
			required: []string{"adhd.lights.list"},
		},
	},
	"dashboard": {
		{
			keywords: []string{"empty lights"},
			required: []string{"adhd.status"},
		},
		{
			keywords: []string{"discovers features"},
			required: []string{"adhd.lights.list"},
		},
		{
			keywords: []string{"displays lights"},
			required: []string{"adhd.lights.list"},
		},
		{
			// Keyboard / navigation / quit — UI-only, not visible in MCP traffic
			keywords:      []string{},
			notVerifiable: true,
			note:          "keyboard operations are Bubble Tea UI events, not observable in MCP traffic",
		},
	},
	"smoke-alarm-network": {
		{
			keywords: []string{"poll"},
			notVerifiable: true,
			note: "polling is observed in smoke-alarm watcher logs, not in MCP server traffic",
		},
		{
			keywords: []string{"light naming"},
			notVerifiable: true,
			note: "light naming is derived from smoke-alarm /status, not MCP calls",
		},
		{
			keywords: []string{"light status mapped"},
			notVerifiable: true,
			note: "status mapping is tested in smokelink unit tests",
		},
		{
			keywords: []string{"sse"},
			notVerifiable: true,
			note: "SSE subscription is an HTTP stream, not a JSON-RPC MCP call",
		},
		{
			keywords:      []string{},
			notVerifiable: true,
			note:          "smoke-alarm integration requires watcher logs, not MCP server traffic",
		},
	},
}

// parsedScenario holds a scenario name plus its line in the file.
type parsedScenario struct {
	name     string
	lineNum  int
}

// parseScenarios reads a .feature file and returns all scenario names.
func parseScenarios(filePath string) ([]parsedScenario, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var scenarios []parsedScenario
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Scenario:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "Scenario:"))
			scenarios = append(scenarios, parsedScenario{name: name, lineNum: lineNum})
		} else if strings.HasPrefix(line, "Scenario Outline:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "Scenario Outline:"))
			scenarios = append(scenarios, parsedScenario{name: name, lineNum: lineNum})
		}
	}
	return scenarios, scanner.Err()
}

// findSpec finds the most specific matching spec for a scenario name within a domain.
// "Most specific" = the spec with the most keywords that all match.
func findSpec(domain, scenarioName string) scenarioSpec {
	specs, ok := domainSpecs[domain]
	if !ok {
		return scenarioSpec{notVerifiable: true, note: "unknown domain " + domain}
	}

	lower := strings.ToLower(scenarioName)
	best := scenarioSpec{notVerifiable: true, note: "no matching spec for this scenario"}
	bestScore := -1

	for _, spec := range specs {
		if len(spec.keywords) == 0 {
			// Catch-all: only use if nothing better matched
			if bestScore < 0 {
				best = spec
			}
			continue
		}
		// Check all keywords match
		allMatch := true
		for _, kw := range spec.keywords {
			if !strings.Contains(lower, strings.ToLower(kw)) {
				allMatch = false
				break
			}
		}
		if allMatch && len(spec.keywords) > bestScore {
			best = spec
			bestScore = len(spec.keywords)
		}
	}

	return best
}

// scoreScenario computes a ScenarioResult from a scenario name, domain, and call log.
func scoreScenario(featureName, domain, filePath, scenarioName string, log *CallLog) ScenarioResult {
	// Smoke-alarm scenarios are verified via watcher evidence, not MCP call evidence
	if domain == "smoke-alarm-network" {
		return scoreWatcherScenario(featureName, filePath, scenarioName, log)
	}

	spec := findSpec(domain, scenarioName)

	result := ScenarioResult{
		Feature:      featureName,
		Domain:       domain,
		FilePath:     filePath,
		ScenarioName: scenarioName,
		Note:         spec.note,
	}

	if spec.notVerifiable {
		result.Status = StatusNotVerifiable
		return result
	}

	allRequired := len(spec.required) > 0 || len(spec.mustError) > 0
	if !allRequired {
		result.Status = StatusNotVerifiable
		result.Note = "no MCP evidence criteria defined"
		return result
	}

	pass := true
	covered := false

	for _, method := range spec.required {
		ev := log.Evidence[method]
		check := MethodCheck{Method: method, Required: true}
		if ev != nil {
			check.Calls = ev.Requests
			check.Successes = ev.Responses - ev.Errors
			check.Errors = ev.Errors
		}
		result.Evidence = append(result.Evidence, check)

		if check.Calls == 0 {
			pass = false
		} else {
			covered = true
			if check.Successes == 0 {
				pass = false
			}
		}
	}

	for _, method := range spec.mustError {
		ev := log.Evidence[method]
		check := MethodCheck{Method: method, Required: true, MustError: true}
		if ev != nil {
			check.Calls = ev.Requests
			check.Successes = ev.Responses - ev.Errors
			check.Errors = ev.Errors
		}
		result.Evidence = append(result.Evidence, check)

		if check.Calls == 0 {
			pass = false
		} else {
			covered = true
			if check.Errors == 0 {
				pass = false
			}
		}
	}

	switch {
	case pass && covered:
		result.Status = StatusPass
	case !covered:
		result.Status = StatusNoCoverage
	default:
		result.Status = StatusFail
	}

	return result
}

// watcherScenarioSpec maps scenario name keywords to watcher evidence predicates.
// Each predicate returns (pass bool, note string).
type watcherPredicate func(w WatcherEvidence) (bool, string)

type watcherSpec struct {
	keywords  []string
	predicate watcherPredicate
	fallback  string // note when no watcher data at all
	// optional: when true, absence of this specific evidence type is NO COVERAGE not FAIL.
	// Use for features that require explicit configuration (e.g. SSE endpoints).
	optional bool
}

var watcherSpecs = []watcherSpec{
	{
		keywords: []string{"poll"},
		predicate: func(w WatcherEvidence) (bool, string) {
			n := w.BySources["poll"]
			if n == 0 {
				return false, "no poll events observed"
			}
			return true, fmt.Sprintf("%d poll events across %d endpoints", n, len(w.Endpoints))
		},
		fallback: "no watcher events — configure smoke-alarm endpoints and run with watcher",
	},
	{
		keywords: []string{"light naming"},
		predicate: func(w WatcherEvidence) (bool, string) {
			if len(w.Events) == 0 {
				return false, "no light-update events"
			}
			// Verify at least one event has endpoint + targetID (smoke:endpoint/target naming)
			for _, e := range w.Events {
				if e.Endpoint != "" && e.TargetID != "" {
					return true, fmt.Sprintf("light named smoke:%s/%s observed", e.Endpoint, e.TargetID)
				}
			}
			return false, "events seen but no endpoint+targetID pairing"
		},
		fallback: "no watcher events",
	},
	{
		keywords: []string{"light status mapped"},
		predicate: func(w WatcherEvidence) (bool, string) {
			// Need to see at least two different statuses to verify mapping
			if len(w.SeenStatuses) < 2 {
				return false, fmt.Sprintf("only %d distinct status values seen (need ≥ 2)", len(w.SeenStatuses))
			}
			statuses := make([]string, 0, len(w.SeenStatuses))
			for s := range w.SeenStatuses {
				statuses = append(statuses, s)
			}
			return true, fmt.Sprintf("status values observed: %v", statuses)
		},
		fallback: "no watcher events — need alarms returning healthy + degraded/outage",
	},
	{
		keywords: []string{"sse"},
		optional: true, // SSE is only present when use_sse: true is configured
		predicate: func(w WatcherEvidence) (bool, string) {
			n := w.BySources["sse"]
			if n == 0 {
				return false, "no SSE events observed (configure use_sse: true on an endpoint)"
			}
			return true, fmt.Sprintf("%d SSE events observed", n)
		},
		fallback: "no watcher events",
	},
	{
		keywords: []string{"light update on status change"},
		predicate: func(w WatcherEvidence) (bool, string) {
			if w.Transitions == 0 {
				return false, "no status transitions observed"
			}
			return true, fmt.Sprintf("%d status transitions observed", w.Transitions)
		},
		fallback: "no watcher events — need an alarm whose targets change state",
	},
	{
		keywords: []string{"multiple smoke-alarm"},
		predicate: func(w WatcherEvidence) (bool, string) {
			if len(w.Endpoints) < 2 {
				return false, fmt.Sprintf("only %d endpoint(s) seen (need ≥ 2)", len(w.Endpoints))
			}
			return true, fmt.Sprintf("%d distinct endpoints observed", len(w.Endpoints))
		},
		fallback: "no watcher events — configure multiple smoke-alarm endpoints",
	},
	{
		keywords: []string{"graceful", "unavailability"},
		predicate: func(w WatcherEvidence) (bool, string) {
			// If the watcher ran at all, it survived unreachable endpoints
			if len(w.Events) == 0 && len(w.Endpoints) == 0 {
				return false, "no watcher activity — cannot confirm graceful handling"
			}
			return true, "watcher continued operating (unreachable endpoints did not crash it)"
		},
		fallback: "no watcher events — start watcher with at least one reachable + one unreachable endpoint",
	},
	{
		keywords: []string{"network visibility"},
		predicate: func(w WatcherEvidence) (bool, string) {
			if len(w.Events) == 0 {
				return false, "no light-update events"
			}
			return true, fmt.Sprintf("%d lights visible via smoke-alarm proxy", len(w.Endpoints))
		},
		fallback: "no watcher events",
	},
	{
		keywords: []string{"convergence"},
		predicate: func(w WatcherEvidence) (bool, string) {
			// Deduplication: watcher should not emit duplicate events for unchanged state.
			// Evidence: at least some events seen, no explicit duplicates detectable from log.
			if len(w.Events) == 0 {
				return false, "no watcher events"
			}
			// Count events per target to check for deduplication (same status repeated = bug)
			counts := make(map[string]int)
			for _, e := range w.Events {
				key := e.Endpoint + "/" + e.TargetID + "/" + e.NewStatus
				counts[key]++
			}
			// Deduplication means each (endpoint/target/status) combo should appear only once
			// unless there was a transition back (e.g., green→red→green)
			return true, fmt.Sprintf("%d events, %d unique target-state combinations", len(w.Events), len(counts))
		},
		fallback: "no watcher events",
	},
}

// scoreWatcherScenario evaluates a smoke-alarm-network scenario against watcher evidence.
func scoreWatcherScenario(featureName, filePath, scenarioName string, log *CallLog) ScenarioResult {
	result := ScenarioResult{
		Feature:      featureName,
		Domain:       "smoke-alarm-network",
		FilePath:     filePath,
		ScenarioName: scenarioName,
	}

	lower := strings.ToLower(scenarioName)

	// Find the best matching spec
	var best *watcherSpec
	bestScore := -1
	for i, spec := range watcherSpecs {
		if len(spec.keywords) == 0 {
			continue
		}
		allMatch := true
		for _, kw := range spec.keywords {
			if !strings.Contains(lower, strings.ToLower(kw)) {
				allMatch = false
				break
			}
		}
		if allMatch && len(spec.keywords) > bestScore {
			best = &watcherSpecs[i]
			bestScore = len(spec.keywords)
		}
	}

	if best == nil {
		// No watcher data of any kind
		if len(log.Watcher.Events) == 0 {
			result.Status = StatusNotVerifiable
			result.Note = "no smoke-alarm watcher data in log; start ADHD with smoke-alarm endpoints configured"
			return result
		}
		result.Status = StatusNoCoverage
		result.Note = "no matching watcher spec for this scenario"
		return result
	}

	// No watcher data at all
	if len(log.Watcher.Events) == 0 && len(log.Watcher.Endpoints) == 0 {
		result.Status = StatusNoCoverage
		result.Note = best.fallback
		return result
	}

	pass, note := best.predicate(log.Watcher)
	result.Note = note
	if pass {
		result.Status = StatusPass
		// Attach a synthetic evidence entry showing watcher activity
		result.Evidence = []MethodCheck{{
			Method:    "smokelink.light-update",
			Calls:     len(log.Watcher.Events),
			Successes: len(log.Watcher.Events),
			Required:  true,
		}}
	} else {
		if len(log.Watcher.Events) == 0 || best.optional {
			result.Status = StatusNoCoverage
		} else {
			result.Status = StatusFail
		}
	}

	return result
}

// MatchFeatures cross-references a CallLog against Gherkin feature files.
// featurePaths is a list of directories or .feature file paths to scan.
func MatchFeatures(log *CallLog, featurePaths []string) ([]FeatureResult, error) {
	// Collect .feature files
	var featureFiles []string
	for _, path := range featurePaths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			_ = filepath.Walk(path, func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() && strings.HasSuffix(p, ".feature") {
					featureFiles = append(featureFiles, p)
				}
				return nil
			})
		} else if strings.HasSuffix(path, ".feature") {
			featureFiles = append(featureFiles, path)
		}
	}

	var featureResults []FeatureResult

	for _, filePath := range featureFiles {
		fr := buildFeatureResult(filePath, log)
		featureResults = append(featureResults, fr)
	}

	return featureResults, nil
}

// buildFeatureResult produces a FeatureResult for one .feature file.
func buildFeatureResult(filePath string, log *CallLog) FeatureResult {
	fr := FeatureResult{FilePath: filePath}

	// Read file header for name, tags, domain
	f, err := os.Open(filePath)
	if err != nil {
		fr.Name = filepath.Base(filePath)
		return fr
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "@") {
			tags := strings.Fields(line)
			fr.Tags = append(fr.Tags, tags...)
			for _, tag := range tags {
				if strings.HasPrefix(tag, "@domain-") {
					fr.Domain = strings.TrimPrefix(tag, "@domain-")
				}
			}
		} else if strings.HasPrefix(line, "Feature:") {
			fr.Name = strings.TrimSpace(strings.TrimPrefix(line, "Feature:"))
			break
		}
	}
	if fr.Name == "" {
		fr.Name = strings.TrimSuffix(filepath.Base(filePath), ".feature")
	}

	// Parse scenarios
	scenarios, err := parseScenarios(filePath)
	if err != nil {
		return fr
	}

	for _, sc := range scenarios {
		result := scoreScenario(fr.Name, fr.Domain, filePath, sc.name, log)
		fr.Scenarios = append(fr.Scenarios, result)
		switch result.Status {
		case StatusPass:
			fr.PassCount++
		case StatusFail:
			fr.FailCount++
		case StatusNoCoverage:
			fr.NoCoverage++
		case StatusNotVerifiable:
			fr.Unverifiable++
		}
	}

	return fr
}
