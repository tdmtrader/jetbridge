package confidence

import (
	"github.com/concourse/ci-agent/schema"
)

// CompletenessReport contains the completeness score, breakdown, and missing fields.
type CompletenessReport struct {
	Score     float64            `json:"score"`
	Breakdown map[string]float64 `json:"breakdown"`
	Missing   []string           `json:"missing"`
}

// ScoreCompleteness evaluates how complete a PlanningInput is.
// A bare-minimum input (title + description only) scores 0.3;
// richly annotated input can reach 1.0.
func ScoreCompleteness(input *schema.PlanningInput) *CompletenessReport {
	report := &CompletenessReport{
		Breakdown: make(map[string]float64),
	}

	// Base score for required fields (title + description)
	report.Breakdown["base"] = 0.3
	score := 0.3

	// Type: +0.05
	if input.Type != "" {
		report.Breakdown["type"] = 0.05
		score += 0.05
	} else {
		report.Missing = append(report.Missing, "type")
	}

	// Priority: +0.05
	if input.Priority != "" {
		report.Breakdown["priority"] = 0.05
		score += 0.05
	} else {
		report.Missing = append(report.Missing, "priority")
	}

	// Labels: +0.05
	if len(input.Labels) > 0 {
		report.Breakdown["labels"] = 0.05
		score += 0.05
	} else {
		report.Missing = append(report.Missing, "labels")
	}

	// Acceptance criteria: +0.2
	if len(input.AcceptanceCriteria) > 0 {
		report.Breakdown["acceptance_criteria"] = 0.2
		score += 0.2
	} else {
		report.Missing = append(report.Missing, "acceptance_criteria")
	}

	// Context fields
	if input.Context != nil {
		if input.Context.Repo != "" {
			report.Breakdown["context.repo"] = 0.05
			score += 0.05
		} else {
			report.Missing = append(report.Missing, "context.repo")
		}

		if input.Context.Language != "" {
			report.Breakdown["context.language"] = 0.05
			score += 0.05
		} else {
			report.Missing = append(report.Missing, "context.language")
		}

		if len(input.Context.RelatedFiles) > 0 {
			report.Breakdown["context.related_files"] = 0.15
			score += 0.15
		} else {
			report.Missing = append(report.Missing, "context.related_files")
		}
	} else {
		report.Missing = append(report.Missing, "context.repo")
		report.Missing = append(report.Missing, "context.language")
		report.Missing = append(report.Missing, "context.related_files")
	}

	// Description length bonus: > 200 chars → +0.1
	if len(input.Description) > 200 {
		report.Breakdown["description_detail"] = 0.1
		score += 0.1
	}

	// Cap at 1.0
	if score > 1.0 {
		score = 1.0
	}

	report.Score = score
	return report
}
