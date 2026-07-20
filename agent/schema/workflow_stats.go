package schema

// WorkflowVersionStats is one row of the per-workflow, per-version run
// aggregation over agent_run_metrics (S-6). The "run" unit is a distinct
// build_id: a dispatched ticket run is one build with many agent-step rows,
// so cost/turns are summed across the build's steps and averaged per build,
// and success is the build's own terminal status (joined from builds), never
// a single green step. Version is a pointer because pre-workflow / ad-hoc CI
// rows carry a NULL workflow_version and aggregate into their own bucket.
type WorkflowVersionStats struct {
	Version       *int    `json:"version"`
	Runs          int     `json:"runs"`
	Tickets       int     `json:"tickets"`
	SucceededRuns int     `json:"succeeded_runs"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	TotalTurns    int     `json:"total_turns"`

	// Derived, filled by WithDerived (0 when Runs == 0).
	SuccessRate float64 `json:"success_rate"`
	AvgCostUSD  float64 `json:"avg_cost_usd"`
	AvgTurns    float64 `json:"avg_turns"`
}

// WithDerived returns a copy with SuccessRate/AvgCostUSD/AvgTurns computed
// from the raw counters. Zero Runs derives every ratio to 0 (no divide-by-zero,
// no NaN on the wire).
func (s WorkflowVersionStats) WithDerived() WorkflowVersionStats {
	if s.Runs > 0 {
		s.SuccessRate = float64(s.SucceededRuns) / float64(s.Runs)
		s.AvgCostUSD = s.TotalCostUSD / float64(s.Runs)
		s.AvgTurns = float64(s.TotalTurns) / float64(s.Runs)
	}
	return s
}
