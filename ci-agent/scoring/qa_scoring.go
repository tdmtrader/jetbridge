package scoring

import (
	"github.com/concourse/ci-agent/schema"
)

// ComputeQAScore computes the QA coverage score from requirement results.
func ComputeQAScore(results []schema.RequirementResult, threshold float64) schema.QAScore {
	if len(results) == 0 {
		return schema.QAScore{Value: 0, Max: 10.0, Pass: false, Threshold: threshold}
	}

	totalPoints := 0.0
	for _, r := range results {
		totalPoints += r.CoveragePoints
	}

	value := (totalPoints / float64(len(results))) * 10.0
	if value > 10.0 {
		value = 10.0
	}

	return schema.QAScore{
		Value:     value,
		Max:       10.0,
		Pass:      value >= threshold,
		Threshold: threshold,
	}
}

// ExtractGaps returns gaps for uncovered_broken and failing requirements.
func ExtractGaps(results []schema.RequirementResult) []schema.Gap {
	var gaps []schema.Gap
	for _, r := range results {
		switch r.Status {
		case schema.CoverageUncoveredBroken, schema.CoverageFailing:
			gaps = append(gaps, schema.Gap{
				RequirementID: r.ID,
				Severity:      "medium",
				Description:   r.Text,
			})
		}
	}
	return gaps
}
