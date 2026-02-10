package implement

import "math"

// ConfidenceResult captures the confidence assessment.
type ConfidenceResult struct {
	Score     float64            `json:"score"`
	Status    string             `json:"status"`
	Breakdown map[string]float64 `json:"breakdown"`
}

// ScoreConfidence computes implementation confidence from tracker state.
func ScoreConfidence(tracker *TaskTracker, suitePass bool) *ConfidenceResult {
	summary := tracker.Summary()
	total := float64(summary.Total)
	if total == 0 {
		return &ConfidenceResult{Score: 0.0, Status: "abstain"}
	}

	// If final suite fails, override to 0.
	if !suitePass {
		return &ConfidenceResult{
			Score:  0.0,
			Status: "fail",
			Breakdown: map[string]float64{
				"committed": float64(summary.Committed) / total,
				"skipped":   float64(summary.Skipped) / total,
				"failed":    float64(summary.Failed) / total,
				"suite":     0.0,
			},
		}
	}

	// Base score: committed tasks count fully, skipped count as 0.9 each.
	committedScore := float64(summary.Committed) / total
	skippedScore := float64(summary.Skipped) * 0.9 / total
	baseScore := committedScore + skippedScore

	// Suite pass bonus: +0.1, capped at 1.0.
	bonus := 0.1
	score := math.Min(baseScore+bonus, 1.0)

	status := "pass"
	if score < 0.5 {
		status = "fail"
	}

	return &ConfidenceResult{
		Score:  score,
		Status: status,
		Breakdown: map[string]float64{
			"committed": committedScore,
			"skipped":   skippedScore,
			"failed":    float64(summary.Failed) / total,
			"suite":     bonus,
		},
	}
}
