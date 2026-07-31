package reviewgrade

import "sort"

// Report is the location-level scorecard for one case.
//
// Every match is a CANDIDATE: it proves the agent anchored at the right code,
// not that it described the right defect. Confirm candidates against the
// case's ground_truth/rubric.md before recording a result.
type Report struct {
	Case              string      `json:"case"`
	Tolerance         int         `json:"tolerance"`
	RequiredTotal     int         `json:"required_total"`
	RequiredMatched   int         `json:"required_matched"`
	Matches           []MatchPair `json:"matches"`
	MissedRequired    []string    `json:"missed_required"`
	MatchedOptional   []string    `json:"matched_optional"`
	UnmatchedProduced []string    `json:"unmatched_produced"`
	Conclusion        string      `json:"conclusion"`
}

// MatchPair records which produced finding claimed which expected finding.
type MatchPair struct {
	ExpectedID string `json:"expected_id"`
	ProducedID string `json:"produced_id"`
	Required   bool   `json:"required"`
}

// Recall is matched-required over total-required, 0 when nothing is required.
func (report Report) Recall() float64 {
	if report.RequiredTotal == 0 {
		return 0
	}
	return float64(report.RequiredMatched) / float64(report.RequiredTotal)
}

// Score pairs produced findings to expected findings greedily, at most one
// produced finding per expected finding and vice versa, in oracle order.
func Score(expected *Expected, review Review, tolerance int) Report {
	report := Report{Case: expected.Case, Tolerance: tolerance, Conclusion: review.Conclusion}
	claimed := make(map[int]bool, len(review.Findings))

	for _, target := range expected.Findings {
		if target.Required {
			report.RequiredTotal++
		}
		matchedIndex := -1
		for index, produced := range review.Findings {
			if claimed[index] {
				continue
			}
			if Matches(target, produced, tolerance) {
				matchedIndex = index
				break
			}
		}
		if matchedIndex < 0 {
			if target.Required {
				report.MissedRequired = append(report.MissedRequired, target.ID)
			}
			continue
		}
		claimed[matchedIndex] = true
		report.Matches = append(report.Matches, MatchPair{
			ExpectedID: target.ID, ProducedID: review.Findings[matchedIndex].ID, Required: target.Required,
		})
		if target.Required {
			report.RequiredMatched++
		} else {
			report.MatchedOptional = append(report.MatchedOptional, target.ID)
		}
	}

	for index, produced := range review.Findings {
		if !claimed[index] {
			report.UnmatchedProduced = append(report.UnmatchedProduced, produced.ID)
		}
	}
	sort.Strings(report.UnmatchedProduced)
	return report
}
