package features

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Feature represents a parsed Gherkin feature
type Feature struct {
	Name         string   // Feature name (from "Feature: ..." line)
	FilePath     string   // Path to the .feature file
	Tags         []string // Feature tags (e.g., @adhd, @z0-physical)
	Scenarios    int      // Number of scenarios in feature
	Description  string   // Feature description (lines after "Feature:")
	ZAxis        ZAxis    // 42i z-axis level (0=physical, 1=relational, 2=epistemic, 3=analytical)
	Domain       string   // Domain/subject area (from @domain- tag)
	Seed         string   // Seed/origin (from @seed- tag)
	Magnitude    string   // 42i magnitude (from @42i-magnitude- tag)
	Dependencies []string // Other .feature files this depends on
}

// Loader discovers and parses Gherkin features
type Loader struct {
	searchPaths []string
}

// NewLoader creates a feature loader with search paths
func NewLoader(paths []string) *Loader {
	return &Loader{
		searchPaths: paths,
	}
}

// LoadFeatures discovers all .feature files and parses them
func (l *Loader) LoadFeatures() ([]Feature, error) {
	var features []Feature

	for _, path := range l.searchPaths {
		// Skip non-existent paths
		if _, err := os.Stat(path); os.IsNotExist(err) {
			slog.Debug("search path does not exist", "path", path)
			continue
		}

		// Walk directory for .feature files
		err := filepath.Walk(path, func(filePath string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			if !info.IsDir() && strings.HasSuffix(filePath, ".feature") {
				feature, err := parseFeature(filePath)
				if err != nil {
					slog.Warn("failed to parse feature", "path", filePath, "error", err)
					return nil // Continue walking
				}

				features = append(features, feature)
			}

			return nil
		})

		if err != nil {
			slog.Warn("error walking search path", "path", path, "error", err)
		}
	}

	slog.Debug("loaded features", "count", len(features))
	return features, nil
}

// LoadFeatureFile loads and parses a single .feature file
func LoadFeatureFile(filePath string) (Feature, error) {
	return parseFeature(filePath)
}

// LoadFeatureFiles loads and parses multiple .feature files, supporting glob patterns
func LoadFeatureFiles(patterns []string) ([]Feature, error) {
	var features []Feature

	for _, pattern := range patterns {
		// Handle glob patterns
		matches, err := filepath.Glob(pattern)
		if err != nil {
			slog.Warn("invalid glob pattern", "pattern", pattern, "error", err)
			continue
		}

		// No matches is not an error, just skip
		if len(matches) == 0 {
			slog.Debug("no files matched pattern", "pattern", pattern)
			continue
		}

		// Load each matched file
		for _, filePath := range matches {
			feature, err := parseFeature(filePath)
			if err != nil {
				slog.Warn("failed to parse feature file", "path", filePath, "error", err)
				continue
			}
			features = append(features, feature)
		}
	}

	return features, nil
}

// parseFeature reads and parses a single .feature file
func parseFeature(filePath string) (Feature, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return Feature{}, err
	}
	defer func() { _ = file.Close() }()

	feature := Feature{
		FilePath: filePath,
		ZAxis:    ZAxisUnknown,
	}

	scanner := bufio.NewScanner(file)
	lineNum := 0
	foundFeature := false
	var description []string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lineNum++

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse tags
		if strings.HasPrefix(line, "@") {
			tags := strings.Fields(line)
			feature.Tags = append(feature.Tags, tags...)
			continue
		}

		// Parse feature name
		if strings.HasPrefix(line, "Feature:") {
			feature.Name = strings.TrimSpace(strings.TrimPrefix(line, "Feature:"))
			foundFeature = true
			continue
		}

		// Capture description (lines between "Feature:" and "Background:" or "Scenario:")
		if foundFeature && !strings.HasPrefix(line, "Background:") && !strings.HasPrefix(line, "Scenario:") {
			description = append(description, line)
			continue
		}

		// Count scenarios
		if strings.HasPrefix(line, "Scenario:") || strings.HasPrefix(line, "Scenario Outline:") {
			feature.Scenarios++
		}
	}

	// Join description lines and clean up
	if len(description) > 0 {
		feature.Description = strings.TrimSpace(strings.Join(description, " "))
	}

	if err := scanner.Err(); err != nil {
		return Feature{}, err
	}

	// Fallback: use filename if no feature name was parsed
	if feature.Name == "" {
		feature.Name = strings.TrimSuffix(filepath.Base(filePath), ".feature")
	}

	// Extract 42i metadata from tags
	Extract42iMetadata(&feature)

	return feature, nil
}
