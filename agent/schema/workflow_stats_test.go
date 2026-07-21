package schema

import (
	"math"
	"testing"
)

func TestWorkflowVersionStatsWithDerived(t *testing.T) {
	v := 3
	s := WorkflowVersionStats{
		Version:       &v,
		Runs:          4,
		Tickets:       3,
		SucceededRuns: 3,
		TotalCostUSD:  8.00,
		TotalTurns:    40,
	}.WithDerived()

	if math.Abs(s.SuccessRate-0.75) > 1e-9 {
		t.Errorf("SuccessRate = %v, want 0.75", s.SuccessRate)
	}
	if math.Abs(s.AvgCostUSD-2.00) > 1e-9 {
		t.Errorf("AvgCostUSD = %v, want 2.00", s.AvgCostUSD)
	}
	if math.Abs(s.AvgTurns-10.0) > 1e-9 {
		t.Errorf("AvgTurns = %v, want 10.0", s.AvgTurns)
	}
}

func TestWorkflowVersionStatsZeroRunsIsSafe(t *testing.T) {
	s := WorkflowVersionStats{Runs: 0, SucceededRuns: 0, TotalCostUSD: 0, TotalTurns: 0}.WithDerived()
	if s.SuccessRate != 0 || s.AvgCostUSD != 0 || s.AvgTurns != 0 {
		t.Errorf("zero-run stats must derive to 0, got %+v", s)
	}
}
