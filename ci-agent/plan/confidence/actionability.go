package confidence

import (
	"github.com/concourse/ci-agent/plan/adapter"
)

// ActionabilityReport describes how actionable a plan is.
type ActionabilityReport struct {
	Score   float64 `json:"score"`
	Details string  `json:"details"`
}

// ScoreActionability evaluates how actionable a plan is.
func ScoreActionability(plan *adapter.PlanOutput) *ActionabilityReport {
	if len(plan.Phases) == 0 {
		return &ActionabilityReport{Score: 0.0, Details: "empty plan"}
	}

	score := 0.6 // base for having phases and tasks

	// Tasks with file references: up to +0.3
	totalTasks := 0
	tasksWithFiles := 0
	for _, phase := range plan.Phases {
		for _, task := range phase.Tasks {
			totalTasks++
			if len(task.Files) > 0 {
				tasksWithFiles++
			}
		}
	}
	if totalTasks > 0 {
		fileRatio := float64(tasksWithFiles) / float64(totalTasks)
		score += fileRatio * 0.3
	}

	// Non-empty key files: +0.1
	if len(plan.KeyFiles) > 0 {
		score += 0.1
	}

	// All phases have >= 2 tasks: +0.1 (not trivially shallow)
	allDeep := true
	for _, phase := range plan.Phases {
		if len(phase.Tasks) < 2 {
			allDeep = false
			break
		}
	}
	if allDeep {
		score += 0.1
	}

	if score > 1.0 {
		score = 1.0
	}

	return &ActionabilityReport{Score: score, Details: "actionability assessment"}
}
