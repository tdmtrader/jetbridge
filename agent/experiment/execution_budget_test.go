package experiment_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/atc"
)

func budgetConfig(steps ...atc.StepConfig) atc.Config {
	plan := make([]atc.Step, len(steps))
	for index, step := range steps {
		plan[index] = atc.Step{Config: step}
	}
	return atc.Config{Jobs: atc.JobConfigs{{Name: "run", PlanSequence: plan}}}
}

func TestValidateExecutionBudgetsBoundsEveryPossibleAgentExecution(t *testing.T) {
	candidate := budgetConfig(&atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.6})
	evaluator := budgetConfig(&atc.AgentStep{Name: "evaluator", BudgetSliceUSD: 0.4})
	if err := experiment.ValidateExecutionBudgets(
		experiment.Budget{PerCellUSD: 1}, 2, []atc.Config{candidate, candidate}, evaluator,
	); err != nil {
		t.Fatalf("ValidateExecutionBudgets() = %v", err)
	}
}

func TestValidateExecutionBudgetsFailsClosedForUnboundedOrMultiplicativeAgentExecution(t *testing.T) {
	tests := []struct {
		name      string
		candidate atc.Config
		want      string
	}{
		{
			name:      "missing slice",
			candidate: budgetConfig(&atc.AgentStep{Name: "candidate"}),
			want:      "positive budget_slice_usd",
		},
		{
			name: "retry",
			candidate: budgetConfig(&atc.RetryStep{
				Attempts: 2,
				Step:     &atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.5},
			}),
			want: "attempts",
		},
		{
			name: "across",
			candidate: budgetConfig(&atc.AcrossStep{
				Vars: []atc.AcrossVarConfig{{Var: "version", Values: []string{"a", "b"}}},
				Step: &atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.5},
			}),
			want: "across",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := experiment.ValidateExecutionBudgets(
				experiment.Budget{PerCellUSD: 1}, 1, []atc.Config{test.candidate}, atc.Config{},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateExecutionBudgets() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExecutionBudgetsRejectsACombinedCandidateAndEvaluatorSliceOverTheCellReservation(t *testing.T) {
	candidate := budgetConfig(&atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.7})
	evaluator := budgetConfig(&atc.AgentStep{Name: "evaluator", BudgetSliceUSD: 0.4})
	err := experiment.ValidateExecutionBudgets(
		experiment.Budget{TotalUSD: 2}, 2, []atc.Config{candidate}, evaluator,
	)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 1.000000 USD cell reservation") {
		t.Fatalf("ValidateExecutionBudgets() = %v, want combined-slice error", err)
	}
}

func TestValidateExecutionBudgetsRejectsCombinedSliceIntegerOverflow(t *testing.T) {
	candidate := budgetConfig(&atc.AgentStep{Name: "candidate", BudgetSliceUSD: 5_000_000_000_000})
	evaluator := budgetConfig(&atc.AgentStep{Name: "evaluator", BudgetSliceUSD: 5_000_000_000_000})
	err := experiment.ValidateExecutionBudgets(
		experiment.Budget{PerCellUSD: 1}, 1, []atc.Config{candidate}, evaluator,
	)
	if err == nil || !strings.Contains(err.Error(), "budget slices overflow") {
		t.Fatalf("ValidateExecutionBudgets() = %v, want combined-slice overflow", err)
	}
}

func TestValidateExecutionBudgetsRejectsSlicesBelowTheDurableMicrodollarPrecision(t *testing.T) {
	candidate := budgetConfig(&atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.0000015})
	err := experiment.ValidateExecutionBudgets(
		experiment.Budget{PerCellUSD: 0.000002}, 1, []atc.Config{candidate}, atc.Config{},
	)
	if err == nil || !strings.Contains(err.Error(), "at most six decimal places") {
		t.Fatalf("ValidateExecutionBudgets() = %v, want slice precision error", err)
	}
}

func TestValidateExecutionBudgetsLeavesZeroDollarBudgetsUnlimited(t *testing.T) {
	unbounded := budgetConfig(&atc.RetryStep{
		Attempts: 3,
		Step:     &atc.AgentStep{Name: "candidate"},
	})
	if err := experiment.ValidateExecutionBudgets(experiment.Budget{}, 1, []atc.Config{unbounded}, atc.Config{}); err != nil {
		t.Fatalf("ValidateExecutionBudgets() = %v", err)
	}
}

func TestValidateExecutionBudgetsRejectsOutboundPublishersEvenWithoutABudget(t *testing.T) {
	publisher := budgetConfig(&atc.PublishSnapshotStep{Name: "open-pr"})
	err := experiment.ValidateExecutionBudgets(
		experiment.Budget{}, 1, []atc.Config{publisher}, atc.Config{},
	)
	if err == nil || !strings.Contains(err.Error(), "publish_snapshot") || !strings.Contains(err.Error(), "effect-free") {
		t.Fatalf("ValidateExecutionBudgets() = %v, want effect-free publisher error", err)
	}
}

func TestValidateExecutionBudgetsForGlobalCapRequiresDollarLiabilityForAgents(t *testing.T) {
	agent := budgetConfig(&atc.AgentStep{Name: "candidate", BudgetSliceUSD: 0.25})
	err := experiment.ValidateExecutionBudgetsForGlobalCap(
		experiment.Budget{}, 1, []atc.Config{agent}, atc.Config{}, true,
	)
	if err == nil || !strings.Contains(err.Error(), "per_cell_usd or total_usd is required") {
		t.Fatalf("ValidateExecutionBudgetsForGlobalCap() = %v, want explicit dollar budget error", err)
	}

	deterministic := budgetConfig(&atc.TaskStep{Name: "test"})
	if err := experiment.ValidateExecutionBudgetsForGlobalCap(
		experiment.Budget{}, 1, []atc.Config{deterministic}, atc.Config{}, true,
	); err != nil {
		t.Fatalf("deterministic ValidateExecutionBudgetsForGlobalCap() = %v", err)
	}
}
