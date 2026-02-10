package scoring

import (
	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/schema"
)

const baseScore = 10.0

// ComputeScore calculates the review score from proven issues and severity weights.
// The score starts at 10.0 and deducts points per issue based on severity.
// The score floors at 0.0.
func ComputeScore(issues []schema.ProvenIssue, weights config.SeverityWeights) schema.Score {
	deductions := make([]schema.ScoreDeduction, 0, len(issues))
	totalDeducted := 0.0

	for _, issue := range issues {
		points := weightForSeverity(issue.Severity, weights)
		deductions = append(deductions, schema.ScoreDeduction{
			IssueID:  issue.ID,
			Severity: issue.Severity,
			Points:   points,
		})
		totalDeducted += points
	}

	value := baseScore - totalDeducted
	if value < 0 {
		value = 0
	}

	return schema.Score{
		Value:      value,
		Max:        baseScore,
		Deductions: deductions,
	}
}

// EvaluatePass determines whether the review passes based on score, threshold,
// and whether critical issues should cause an automatic failure.
func EvaluatePass(score schema.Score, threshold float64, failOnCritical bool) bool {
	if failOnCritical {
		for _, d := range score.Deductions {
			if d.Severity == schema.SeverityCritical {
				return false
			}
		}
	}
	return score.Value >= threshold
}

func weightForSeverity(s schema.Severity, w config.SeverityWeights) float64 {
	switch s {
	case schema.SeverityCritical:
		return w.Critical
	case schema.SeverityHigh:
		return w.High
	case schema.SeverityMedium:
		return w.Medium
	case schema.SeverityLow:
		return w.Low
	default:
		return 0
	}
}
