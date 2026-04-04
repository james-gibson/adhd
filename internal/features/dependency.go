package features

import (
	"regexp"
	"strings"
)

// ZAxis represents the 42i z-axis level (physical → relational → epistemic → analytical)
type ZAxis int

const (
	ZAxisUnknown    ZAxis = -1
	ZAxisPhysical   ZAxis = 0
	ZAxisRelational ZAxis = 1
	ZAxisEpistemic  ZAxis = 2
	ZAxisAnalytical ZAxis = 3
)

// Extract42iMetadata extracts structured information from Gherkin tags
func Extract42iMetadata(feature *Feature) {
	if feature.Tags == nil {
		return
	}

	// Extract z-axis from tags
	for _, tag := range feature.Tags {
		if strings.HasPrefix(tag, "@z0-") {
			feature.ZAxis = ZAxisPhysical
		} else if strings.HasPrefix(tag, "@z1-") {
			feature.ZAxis = ZAxisRelational
		} else if strings.HasPrefix(tag, "@z2-") {
			feature.ZAxis = ZAxisEpistemic
		} else if strings.HasPrefix(tag, "@z3-") {
			feature.ZAxis = ZAxisAnalytical
		}

		// Extract domain
		if strings.HasPrefix(tag, "@domain-") {
			feature.Domain = strings.TrimPrefix(tag, "@domain-")
		}

		// Extract seed
		if strings.HasPrefix(tag, "@seed-") {
			feature.Seed = strings.TrimPrefix(tag, "@seed-")
		}

		// Extract 42i magnitude
		if strings.HasPrefix(tag, "@42i-magnitude-") {
			feature.Magnitude = strings.TrimPrefix(tag, "@42i-magnitude-")
		}
	}

	// Extract dependencies from "declared in X.feature" patterns
	feature.Dependencies = extractDependencies(feature)
}

// extractDependencies finds feature file references in the feature description
func extractDependencies(feature *Feature) []string {
	var deps []string

	// Look for patterns like "... declared in some-feature.feature"
	declaredPattern := regexp.MustCompile(`declared in\s+([a-zA-Z0-9\-_.]+\.feature)`)
	matches := declaredPattern.FindAllStringSubmatch(feature.Description, -1)

	for _, match := range matches {
		if len(match) > 1 {
			deps = append(deps, match[1])
		}
	}

	return deps
}

// ZAxisLabel returns a human-readable label for the z-axis
func (z ZAxis) Label() string {
	switch z {
	case ZAxisPhysical:
		return "Physical"
	case ZAxisRelational:
		return "Relational"
	case ZAxisEpistemic:
		return "Epistemic"
	case ZAxisAnalytical:
		return "Analytical"
	default:
		return "Unknown"
	}
}

// ZAxisSymbol returns a visual symbol for the z-axis
func (z ZAxis) Symbol() string {
	switch z {
	case ZAxisPhysical:
		return "⚙️"  // gear/structure
	case ZAxisRelational:
		return "🔗" // chain/connections
	case ZAxisEpistemic:
		return "🧠" // knowledge
	case ZAxisAnalytical:
		return "🔍" // analysis
	default:
		return "?"
	}
}
