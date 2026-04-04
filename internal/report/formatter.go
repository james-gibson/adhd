package report

import (
	"fmt"
	"strings"
	"time"
)

// Format produces a human-readable test report from feature results and the raw call log.
func Format(results []FeatureResult, log *CallLog) string {
	var sb strings.Builder

	// Header
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	sb.WriteString(fmt.Sprintf("ADHD MCP Traffic Report  %s\n", now))
	sb.WriteString(strings.Repeat("━", 72) + "\n")
	sb.WriteString(fmt.Sprintf("Log:  %s\n", log.Path))
	sb.WriteString(fmt.Sprintf("Lines: %d total, %d valid, %d corrupt\n\n", log.TotalLines, log.ValidLines, log.InvalidLines))

	// Method coverage table
	sb.WriteString("Observed Methods\n")
	sb.WriteString(strings.Repeat("─", 52) + "\n")
	sb.WriteString(fmt.Sprintf("  %-35s  %6s  %6s  %6s\n", "method", "calls", "ok", "errors"))
	for _, method := range methodOrder(log) {
		ev := log.Evidence[method]
		ok := ev.Responses - ev.Errors
		sb.WriteString(fmt.Sprintf("  %-35s  %6d  %6d  %6d\n", method, ev.Requests, ok, ev.Errors))
	}
	sb.WriteString("\n")

	// Per-feature results
	totalPass, totalFail, totalNoCov, totalUnver := 0, 0, 0, 0

	for _, fr := range results {
		sb.WriteString(featureBlock(fr))
		totalPass += fr.PassCount
		totalFail += fr.FailCount
		totalNoCov += fr.NoCoverage
		totalUnver += fr.Unverifiable
	}

	// Summary
	total := totalPass + totalFail + totalNoCov + totalUnver
	sb.WriteString(strings.Repeat("━", 72) + "\n")
	sb.WriteString("Summary\n")
	sb.WriteString(fmt.Sprintf("  ✓ PASS            %d\n", totalPass))
	sb.WriteString(fmt.Sprintf("  ✗ FAIL            %d\n", totalFail))
	sb.WriteString(fmt.Sprintf("  ○ NO COVERAGE     %d\n", totalNoCov))
	sb.WriteString(fmt.Sprintf("  – NOT VERIFIABLE  %d\n", totalUnver))
	sb.WriteString(fmt.Sprintf("  Total scenarios:  %d\n", total))

	if total > 0 {
		verifiable := totalPass + totalFail + totalNoCov
		if verifiable > 0 {
			pct := float64(totalPass) / float64(verifiable) * 100
			sb.WriteString(fmt.Sprintf("  Coverage:         %.0f%% of verifiable scenarios passing\n", pct))
		}
	}

	return sb.String()
}

func featureBlock(fr FeatureResult) string {
	var sb strings.Builder

	// Feature header
	domainTag := ""
	if fr.Domain != "" {
		domainTag = "  @domain-" + fr.Domain
	}
	sb.WriteString(fmt.Sprintf("Feature: %s%s\n", fr.Name, domainTag))
	sb.WriteString(fmt.Sprintf("  %s\n", fr.FilePath))
	sb.WriteString(fmt.Sprintf("  ✓ %d  ✗ %d  ○ %d  – %d\n",
		fr.PassCount, fr.FailCount, fr.NoCoverage, fr.Unverifiable))
	sb.WriteString(strings.Repeat("─", 72) + "\n")

	for _, sc := range fr.Scenarios {
		sb.WriteString(scenarioLine(sc))
	}
	sb.WriteString("\n")

	return sb.String()
}

func scenarioLine(sc ScenarioResult) string {
	var sb strings.Builder

	symbol := sc.Status.Symbol()
	label := sc.Status.String()

	sb.WriteString(fmt.Sprintf("  %s  %-16s  %s\n", symbol, label, sc.ScenarioName))

	// Show evidence breakdown for non-trivial results
	if sc.Status == StatusPass || sc.Status == StatusFail || sc.Status == StatusNoCoverage {
		for _, ev := range sc.Evidence {
			indicator := "✓"
			detail := fmt.Sprintf("%d calls, %d ok, %d errors", ev.Calls, ev.Successes, ev.Errors)
			if ev.MustError {
				if ev.Errors > 0 {
					indicator = "✓"
					detail = fmt.Sprintf("%d calls, %d errors (expected)", ev.Calls, ev.Errors)
				} else {
					indicator = "✗"
					detail = fmt.Sprintf("%d calls, no errors (error expected)", ev.Calls)
				}
			} else if ev.Calls == 0 {
				indicator = "○"
				detail = "not called"
			} else if ev.Successes == 0 {
				indicator = "✗"
				detail = fmt.Sprintf("%d calls, all errored", ev.Calls)
			}
			sb.WriteString(fmt.Sprintf("       %s  %-35s  %s\n", indicator, ev.Method, detail))
		}
	} else if sc.Status == StatusNotVerifiable && sc.Note != "" {
		sb.WriteString(fmt.Sprintf("          note: %s\n", sc.Note))
	}

	return sb.String()
}

// methodOrder returns method names in a deterministic display order.
func methodOrder(log *CallLog) []string {
	priority := []string{
		"initialize",
		"tools/list",
		"adhd.status",
		"adhd.lights.list",
		"adhd.lights.get",
		"adhd.isotope.status",
		"adhd.isotope.peers",
		"ping",
		"smoke-alarm.isotope.push-logs",
	}

	seen := make(map[string]bool)
	var ordered []string

	// First: known methods in priority order
	for _, m := range priority {
		if _, ok := log.Evidence[m]; ok {
			ordered = append(ordered, m)
			seen[m] = true
		}
	}

	// Then: any remaining methods alphabetically
	var extra []string
	for m := range log.Evidence {
		if !seen[m] {
			extra = append(extra, m)
		}
	}
	// simple sort
	for i := 0; i < len(extra); i++ {
		for j := i + 1; j < len(extra); j++ {
			if extra[i] > extra[j] {
				extra[i], extra[j] = extra[j], extra[i]
			}
		}
	}

	return append(ordered, extra...)
}
