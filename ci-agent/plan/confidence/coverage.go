package confidence

import (
	"strings"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

// CoverageReport describes how well a spec covers the acceptance criteria.
type CoverageReport struct {
	Score   float64 `json:"score"`
	Details string  `json:"details"`
}

// ScoreCoverage evaluates how well the spec addresses acceptance criteria.
func ScoreCoverage(input *schema.PlanningInput, spec *adapter.SpecOutput) *CoverageReport {
	if len(input.AcceptanceCriteria) == 0 {
		return &CoverageReport{
			Score:   0.8,
			Details: "no acceptance criteria in input; assumed adequate",
		}
	}

	specLower := strings.ToLower(spec.SpecMarkdown)
	covered := 0
	for _, ac := range input.AcceptanceCriteria {
		// Simple keyword check: does the spec mention key words from AC?
		words := strings.Fields(strings.ToLower(ac))
		matchCount := 0
		for _, w := range words {
			if len(w) > 3 && strings.Contains(specLower, w) {
				matchCount++
			}
		}
		if len(words) > 0 && matchCount > 0 {
			covered++
		}
	}

	coverage := float64(covered) / float64(len(input.AcceptanceCriteria))

	// Unresolved questions penalty
	penalty := 0.0
	if len(spec.UnresolvedQuestions) >= 3 {
		penalty = 0.2
	} else if len(spec.UnresolvedQuestions) >= 1 {
		penalty = 0.1
	}

	score := coverage - penalty
	if score < 0.0 {
		score = 0.0
	}
	if score > 1.0 {
		score = 1.0
	}

	return &CoverageReport{
		Score:   score,
		Details: "keyword-based coverage check",
	}
}
