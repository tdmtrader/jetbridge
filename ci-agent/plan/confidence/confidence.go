package confidence

// ConfidenceWeights configures the weight of each sub-score.
type ConfidenceWeights struct {
	Completeness  float64 `json:"completeness"`
	Coverage      float64 `json:"coverage"`
	Actionability float64 `json:"actionability"`
}

// DefaultWeights returns the default confidence weights.
func DefaultWeights() ConfidenceWeights {
	return ConfidenceWeights{
		Completeness:  0.25,
		Coverage:      0.35,
		Actionability: 0.40,
	}
}

// ConfidenceReport contains the composite confidence score.
type ConfidenceReport struct {
	Score     float64            `json:"score"`
	SubScores map[string]float64 `json:"sub_scores"`
}

// PassesThreshold returns true if the score meets or exceeds the threshold.
func (r *ConfidenceReport) PassesThreshold(threshold float64) bool {
	return r.Score >= threshold
}

// ComputeConfidence calculates a weighted average of sub-scores.
func ComputeConfidence(completeness, coverage, actionability float64, weights ConfidenceWeights) *ConfidenceReport {
	score := completeness*weights.Completeness +
		coverage*weights.Coverage +
		actionability*weights.Actionability

	if score > 1.0 {
		score = 1.0
	}
	if score < 0.0 {
		score = 0.0
	}

	return &ConfidenceReport{
		Score: score,
		SubScores: map[string]float64{
			"completeness":  completeness,
			"coverage":      coverage,
			"actionability": actionability,
		},
	}
}
