package experiment

import (
	"fmt"
	"math"

	"github.com/concourse/concourse/atc"
)

// ValidateExecutionBudgets proves that every possible agent process in a
// candidate/evaluator cell has a hard CLI cap and that the sum of those caps
// fits inside the durable cell reservation. Retry and across are rejected for
// budgeted experiments because they multiply an authored slice dynamically.
func ValidateExecutionBudgets(budget Budget, expectedCells int, candidates []atc.Config, evaluator atc.Config) error {
	return validateExecutionBudgets(budget, expectedCells, candidates, evaluator, false)
}

// ValidateExecutionBudgetsForGlobalCap applies the ordinary per-cell proof and
// additionally forbids an unlimited dollar budget when any candidate or
// evaluator can start an agent. Experiment child runs deliberately consume a
// shared cell reservation instead of reserving independently, so allowing an
// agent-bearing zero-dollar experiment while the deployment cap is enabled
// would leave those children outside every global liability row.
func ValidateExecutionBudgetsForGlobalCap(
	budget Budget,
	expectedCells int,
	candidates []atc.Config,
	evaluator atc.Config,
	globalCapEnabled bool,
) error {
	return validateExecutionBudgets(budget, expectedCells, candidates, evaluator, globalCapEnabled)
}

func validateExecutionBudgets(
	budget Budget,
	expectedCells int,
	candidates []atc.Config,
	evaluator atc.Config,
	globalCapEnabled bool,
) error {
	if err := validateEffectFreeExperimentConfigs(candidates, evaluator); err != nil {
		return err
	}
	if err := budget.Validate(); err != nil {
		return err
	}
	if budget.PerCellUSD == 0 && budget.TotalUSD == 0 {
		if globalCapEnabled {
			hasAgents, err := experimentConfigsHaveAgents(candidates, evaluator)
			if err != nil {
				return err
			}
			if hasAgents {
				return fmt.Errorf("experiment: per_cell_usd or total_usd is required for agent executions while the global daily cap is enabled")
			}
		}
		return nil
	}
	var limit float64
	if budget.PerCellUSD > 0 {
		limit = math.Floor(budget.PerCellUSD*1_000_000) / 1_000_000
	} else {
		if expectedCells <= 0 {
			return fmt.Errorf("experiment: cannot allocate total budget without cells")
		}
		limit = math.Floor((budget.TotalUSD/float64(expectedCells))*1_000_000) / 1_000_000
	}
	if limit <= 0 {
		return fmt.Errorf("experiment: budget is too small to allocate a positive cell reservation")
	}
	limitMicroUSD := int64(math.Round(limit * 1_000_000))

	evaluatorSlice, err := configAgentBudgetSlice("evaluator", evaluator)
	if err != nil {
		return err
	}
	for index, candidate := range candidates {
		candidateSlice, err := configAgentBudgetSlice(fmt.Sprintf("candidate %d", index+1), candidate)
		if err != nil {
			return err
		}
		if candidateSlice > math.MaxInt64-evaluatorSlice {
			return fmt.Errorf("experiment: candidate %d and evaluator agent budget slices overflow", index+1)
		}
		combined := candidateSlice + evaluatorSlice
		if combined > limitMicroUSD {
			return fmt.Errorf(
				"experiment: candidate %d and evaluator agent slices total %.6f USD, which exceeds the %.6f USD cell reservation",
				index+1, float64(combined)/1_000_000, limit,
			)
		}
	}
	return nil
}

// Experiments compare transformations over frozen snapshots. Publication is a
// live-system write boundary, so allowing it inside a repeated candidate or
// evaluator would turn a measurement run into N real external mutations.
// Sandboxed publishers can be introduced later as an explicit, separately
// authorized experiment capability; ordinary publishers fail closed here.
func validateEffectFreeExperimentConfigs(candidates []atc.Config, evaluator atc.Config) error {
	configs := append(append([]atc.Config(nil), candidates...), evaluator)
	for configIndex, config := range configs {
		label := "evaluator"
		if configIndex < len(candidates) {
			label = fmt.Sprintf("candidate %d", configIndex+1)
		}
		recursor := atc.StepRecursor{OnPublishSnapshot: func(step *atc.PublishSnapshotStep) error {
			return fmt.Errorf(
				"experiment: %s contains publish_snapshot %q; experiments must remain effect-free",
				label, step.Name,
			)
		}}
		for _, job := range config.Jobs {
			step := job.StepConfig()
			if step == nil {
				return fmt.Errorf("experiment: %s workflow plan contains an empty step", label)
			}
			if err := step.Visit(recursor); err != nil {
				return err
			}
		}
	}
	return nil
}

func experimentConfigsHaveAgents(candidates []atc.Config, evaluator atc.Config) (bool, error) {
	configs := append(append([]atc.Config(nil), candidates...), evaluator)
	for _, config := range configs {
		hasAgents := false
		recursor := atc.StepRecursor{OnAgent: func(*atc.AgentStep) error {
			hasAgents = true
			return nil
		}}
		for _, job := range config.Jobs {
			step := job.StepConfig()
			if step == nil {
				return false, fmt.Errorf("experiment: workflow plan contains an empty step")
			}
			if err := step.Visit(recursor); err != nil {
				return false, err
			}
		}
		if hasAgents {
			return true, nil
		}
	}
	return false, nil
}

func configAgentBudgetSlice(label string, config atc.Config) (int64, error) {
	var total int64
	recursor := atc.StepRecursor{
		OnAgent: func(step *atc.AgentStep) error {
			if math.IsNaN(step.BudgetSliceUSD) || math.IsInf(step.BudgetSliceUSD, 0) || step.BudgetSliceUSD <= 0 {
				return fmt.Errorf("experiment: %s agent %q requires a finite positive budget_slice_usd", label, step.Name)
			}
			scaled := step.BudgetSliceUSD * 1_000_000
			if math.Abs(scaled-math.Round(scaled)) > 0.0000001 {
				return fmt.Errorf("experiment: %s agent %q budget_slice_usd supports at most six decimal places", label, step.Name)
			}
			microUSD := int64(math.Round(scaled))
			if microUSD <= 0 || total > math.MaxInt64-microUSD {
				return fmt.Errorf("experiment: %s agent budget slices overflow", label)
			}
			total += microUSD
			return nil
		},
		OnRetry: func(*atc.RetryStep) error {
			return fmt.Errorf("experiment: %s uses attempts, which cannot be hard-bounded by a static budget slice", label)
		},
		OnAcross: func(*atc.AcrossStep) error {
			return fmt.Errorf("experiment: %s uses across, which cannot be hard-bounded by a static budget slice", label)
		},
	}
	for _, job := range config.Jobs {
		if err := job.StepConfig().Visit(recursor); err != nil {
			return 0, err
		}
	}
	return total, nil
}
