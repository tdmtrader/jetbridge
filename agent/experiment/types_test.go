package experiment_test

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
)

func TestDefinitionValidatesCompatiblePinnedMatrix(t *testing.T) {
	definition := validDefinition(t)
	if err := definition.Validate(); err != nil {
		t.Fatalf("valid definition: %v", err)
	}
	if definition.ExpectedCells() != 20 {
		t.Fatalf("expected cells = %d, want 20", definition.ExpectedCells())
	}
	if definition.CanMutate() != true {
		t.Fatal("draft experiment is not mutable")
	}
	definition.State = experiment.StateRunning
	if definition.CanMutate() {
		t.Fatal("started experiment remained mutable")
	}
}

func TestDefinitionBoundsEveryMatrixDimensionAndTheMaterializedCrossProduct(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*experiment.Definition)
		want   string
	}{
		{
			name: "variants",
			mutate: func(value *experiment.Definition) {
				template := value.Variants[1]
				for len(value.Variants) <= experiment.MaxVariants {
					next := template
					next.Label = fmt.Sprintf("variant-%d", len(value.Variants))
					value.Variants = append(value.Variants, next)
				}
			},
			want: "at most 16 variants",
		},
		{
			name: "fixtures",
			mutate: func(value *experiment.Definition) {
				template := value.Fixtures[0]
				for len(value.Fixtures) <= experiment.MaxFixtures {
					next := template
					next.Label = fmt.Sprintf("fixture-%d", len(value.Fixtures))
					value.Fixtures = append(value.Fixtures, next)
				}
			},
			want: "at most 256 fixtures",
		},
		{
			name: "materialized cells",
			mutate: func(value *experiment.Definition) {
				value.Repetitions = experiment.MaxRepetitions
				for len(value.Fixtures) < 6 {
					next := value.Fixtures[0]
					next.Label = fmt.Sprintf("fixture-%d", len(value.Fixtures))
					value.Fixtures = append(value.Fixtures, next)
				}
			},
			want: "at most 2000 materialized cells",
		},
		{
			name: "assertion metrics",
			mutate: func(value *experiment.Definition) {
				fixture := &value.Fixtures[1]
				fixture.Assertions = fixture.Assertions[:0]
				for index := 0; index <= experiment.MaxMeasurementsPerCell; index++ {
					fixture.Assertions = append(fixture.Assertions, experiment.Assertion{
						Metric: fmt.Sprintf("metric-%d", index), Comparator: experiment.ComparatorGTE,
						Thresholds: []float64{1},
					})
				}
			},
			want: "at most 32 assertion metrics",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition(t)
			test.mutate(&definition)
			if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDefinitionAllowsAStartableBudgetThatCanPartiallyAdmitTheMatrix(t *testing.T) {
	definition := validDefinition(t)
	definition.Budget = experiment.Budget{PerCellUSD: 2, TotalUSD: 3}
	if err := definition.ValidateStart(); err != nil {
		t.Fatalf("ValidateStart() = %v", err)
	}
	if !definition.Budget.Limited() {
		t.Fatal("nonzero budget was not reported as limited")
	}
}

func TestDefinitionFailsClosedWhenARequestedTokenCapCannotBeEnforced(t *testing.T) {
	definition := validDefinition(t)
	definition.Budget = experiment.Budget{MaxTokensPerCell: 10_000}
	err := definition.ValidateStart()
	if err == nil || !strings.Contains(err.Error(), "max_tokens_per_cell cannot be hard-enforced") {
		t.Fatalf("ValidateStart() = %v, want explicit unsupported token-cap error", err)
	}
}

func TestBudgetRejectsDollarAmountsThatPersistenceWouldRound(t *testing.T) {
	for _, value := range []float64{0.0000004, 0.0000006, 1.0000004} {
		budget := experiment.Budget{PerCellUSD: value}
		err := budget.Validate()
		if err == nil || !strings.Contains(err.Error(), "at most six decimal places") {
			t.Fatalf("Budget{%g}.Validate() = %v", value, err)
		}
	}
	if err := (experiment.Budget{PerCellUSD: 0.000001, TotalUSD: 1.234567}).Validate(); err != nil {
		t.Fatalf("six-decimal budget rejected: %v", err)
	}
	if err := (experiment.Budget{PerCellUSD: math.MaxFloat64}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "micro-USD integer") {
		t.Fatalf("unrepresentable micro-USD budget accepted: %v", err)
	}
	if err := (experiment.Budget{PerCellUSD: 1_000_000_000_000}).Validate(); err == nil ||
		!strings.Contains(err.Error(), "NUMERIC(18,6)") {
		t.Fatalf("durable numeric overflow accepted: %v", err)
	}
}

func TestDefinitionRejectsControlAndTargetDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*experiment.Definition)
		want   string
	}{
		{name: "no control", mutate: func(value *experiment.Definition) { value.Variants[0].Control = false }, want: "exactly one control"},
		{name: "two controls", mutate: func(value *experiment.Definition) { value.Variants[1].Control = true }, want: "exactly one control"},
		{name: "duplicate label", mutate: func(value *experiment.Definition) { value.Variants[1].Label = value.Variants[0].Label }, want: "duplicate variant label"},
		{name: "signature drift", mutate: func(value *experiment.Definition) { value.Variants[1].SignatureHash = strings.Repeat("f", 64) }, want: "incompatible signature"},
		{name: "workflow with function", mutate: func(value *experiment.Definition) { value.Variants[0].Target.FunctionID = "review" }, want: "workflow target must not set function_id"},
		{name: "function without function", mutate: func(value *experiment.Definition) { value.Variants[1].Target.FunctionID = "" }, want: "function target requires function_id"},
		{name: "unpinned definition", mutate: func(value *experiment.Definition) { value.Variants[1].Target.DefinitionID = 0 }, want: "definition_id must be positive"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition(t)
			test.mutate(&definition)
			if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDefinitionRejectsClientSuppliedFrozenTargetIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*experiment.Definition)
	}{
		{name: "variant", mutate: func(value *experiment.Definition) {
			value.Variants[0].TargetConfigHash = strings.Repeat("a", 64)
		}},
		{name: "evaluator", mutate: func(value *experiment.Definition) {
			value.Evaluator.TargetConfigHash = strings.Repeat("b", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition(t)
			test.mutate(&definition)
			if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), "server-derived at start") {
				t.Fatalf("Validate() = %v, want server-derived identity rejection", err)
			}
		})
	}
}

func TestDefinitionRejectsIncompleteFixturesAndInvalidControls(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*experiment.Definition)
		want   string
	}{
		{name: "missing required port", mutate: func(value *experiment.Definition) { delete(value.Fixtures[0].Inputs, "repo") }, want: "missing required input"},
		{name: "unknown port", mutate: func(value *experiment.Definition) { value.Fixtures[0].Inputs["mystery"] = 99 }, want: "unknown input"},
		{name: "invalid snapshot", mutate: func(value *experiment.Definition) { value.Fixtures[0].Inputs["repo"] = 0 }, want: "snapshot ID"},
		{name: "normal assertions", mutate: func(value *experiment.Definition) { value.Fixtures[0].Assertions = value.Fixtures[1].Assertions }, want: "normal fixture must not have assertions"},
		{name: "negative without assertions", mutate: func(value *experiment.Definition) { value.Fixtures[1].Assertions = nil }, want: "negative_control fixture requires"},
		{name: "duplicate assertion metric", mutate: func(value *experiment.Definition) {
			value.Fixtures[1].Assertions = append(value.Fixtures[1].Assertions, value.Fixtures[1].Assertions[0])
		}, want: "duplicate assertion metric"},
		{name: "between arity", mutate: func(value *experiment.Definition) {
			value.Fixtures[1].Assertions[0] = experiment.Assertion{Metric: "defects", Comparator: experiment.ComparatorBetween, Thresholds: []float64{1}}
		}, want: "exactly two"},
		{name: "between order", mutate: func(value *experiment.Definition) {
			value.Fixtures[1].Assertions[0] = experiment.Assertion{Metric: "defects", Comparator: experiment.ComparatorBetween, Thresholds: []float64{2, 1}}
		}, want: "ascending"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition(t)
			test.mutate(&definition)
			if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDefinitionValidatesPinnedEvaluatorMappingByExactType(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*experiment.Definition)
		want   string
	}{
		{name: "missing evaluator input", mutate: func(value *experiment.Definition) { value.Evaluator.Mappings = value.Evaluator.Mappings[:1] }, want: "missing required evaluator input"},
		{name: "duplicate evaluator input", mutate: func(value *experiment.Definition) {
			value.Evaluator.Mappings[1].EvaluatorPort = value.Evaluator.Mappings[0].EvaluatorPort
		}, want: "duplicate evaluator mapping"},
		{name: "unknown candidate output", mutate: func(value *experiment.Definition) { value.Evaluator.Mappings[0].SourcePort = "unknown" }, want: "unknown candidate output"},
		{name: "type mismatch", mutate: func(value *experiment.Definition) {
			value.Evaluator.Mappings[0].EvaluatorPort = "repo"
			value.Evaluator.Mappings[1].EvaluatorPort = "candidate"
		}, want: "type mismatch"},
		{name: "missing measurements", mutate: func(value *experiment.Definition) { value.Evaluator.MeasurementsPort = "" }, want: "measurements_port is required"},
		{name: "wrong measurements type", mutate: func(value *experiment.Definition) { value.Evaluator.Signature.Outputs[0].Type = "review/v1" }, want: "must have exact type measurements/v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := validDefinition(t)
			test.mutate(&definition)
			if err := definition.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSignatureHashIsDeterministicAndOrderSensitive(t *testing.T) {
	signature := candidateSignature()
	first, err := experiment.HashSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	second, err := experiment.HashSignature(signature)
	if err != nil || second != first {
		t.Fatalf("second hash = %q, %v, want %q", second, err, first)
	}
	reordered := signature
	reordered.Inputs = append([]workflow.SignaturePort(nil), signature.Inputs...)
	reordered.Inputs[0], reordered.Inputs[1] = reordered.Inputs[1], reordered.Inputs[0]
	third, err := experiment.HashSignature(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("ordered signature ports produced the same hash after reordering")
	}
}

func TestAssertionEvaluatesEveryFrozenComparator(t *testing.T) {
	tests := []struct {
		comparator experiment.Comparator
		thresholds []float64
		value      float64
		want       bool
	}{
		{experiment.ComparatorLT, []float64{2}, 1, true},
		{experiment.ComparatorLT, []float64{2}, 2, false},
		{experiment.ComparatorLTE, []float64{2}, 2, true},
		{experiment.ComparatorGT, []float64{2}, 3, true},
		{experiment.ComparatorGT, []float64{2}, 2, false},
		{experiment.ComparatorGTE, []float64{2}, 2, true},
		{experiment.ComparatorBetween, []float64{2, 4}, 2, true},
		{experiment.ComparatorBetween, []float64{2, 4}, 4, true},
		{experiment.ComparatorBetween, []float64{2, 4}, 5, false},
	}
	for index, test := range tests {
		assertion := experiment.Assertion{Metric: "metric", Comparator: test.comparator, Thresholds: test.thresholds}
		if got := assertion.Evaluate(test.value); got != test.want {
			t.Fatalf("case %d Evaluate(%g) = %t, want %t", index, test.value, got, test.want)
		}
	}
}

func TestExperimentAndCellIDsUseQuotedDecimalJSON(t *testing.T) {
	value := struct {
		Experiment experiment.ID     `json:"experiment_id"`
		Cell       experiment.CellID `json:"cell_id"`
	}{Experiment: experiment.ID(1<<53 + 11), Cell: experiment.CellID(1<<53 + 13)}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"experiment_id":"9007199254741003","cell_id":"9007199254741005"}` {
		t.Fatalf("JSON = %s", payload)
	}
	var decoded struct {
		Experiment experiment.ID     `json:"experiment_id"`
		Cell       experiment.CellID `json:"cell_id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded != value {
		t.Fatalf("decode = %#v, %v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`{"experiment_id":1,"cell_id":"2"}`), &decoded); err == nil {
		t.Fatal("numeric experiment ID was accepted")
	}
}

func validDefinition(t *testing.T) experiment.Definition {
	t.Helper()
	signature := candidateSignature()
	hash, err := experiment.HashSignature(signature)
	if err != nil {
		t.Fatal(err)
	}
	return experiment.Definition{
		Name: "review-prompts", State: experiment.StateDraft, Repetitions: 5,
		Signature: signature,
		Budget:    experiment.Budget{PerCellUSD: 1.5, TotalUSD: 40, MaxTokensPerCell: 100_000},
		Variants: []experiment.Variant{
			{Label: "control", Control: true, SignatureHash: hash, Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "review", DefinitionID: 41, Version: 3}},
			{Label: "candidate", SignatureHash: hash, Target: experiment.Target{Kind: experiment.TargetFunction, WorkflowName: "review", DefinitionID: 42, Version: 4, FunctionID: "review"}},
		},
		Fixtures: []experiment.Fixture{
			{Label: "normal", Role: experiment.FixtureNormal, Inputs: map[string]snapshot.SnapshotID{"repo": 101}},
			{Label: "bad-change", Role: experiment.FixtureNegativeControl, Inputs: map[string]snapshot.SnapshotID{"repo": 102}, Assertions: []experiment.Assertion{{Metric: "defects", Comparator: experiment.ComparatorGTE, Thresholds: []float64{1}}}},
		},
		Evaluator: experiment.Evaluator{
			Target: experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "review-evaluator", DefinitionID: 51, Version: 2},
			Signature: workflow.PublicSignature{
				Inputs:  []workflow.SignaturePort{{Name: "candidate", Type: "review/v1"}, {Name: "repo", Type: "repository/v1"}},
				Outputs: []workflow.SignaturePort{{Name: "measurements", Type: "measurements/v1"}},
			},
			Mappings: []experiment.EvaluatorMapping{
				{EvaluatorPort: "candidate", SourceDirection: experiment.SourceCandidateOutput, SourcePort: "review"},
				{EvaluatorPort: "repo", SourceDirection: experiment.SourceFixtureInput, SourcePort: "repo"},
			},
			MeasurementsPort: "measurements",
		},
	}
}

func candidateSignature() workflow.PublicSignature {
	return workflow.PublicSignature{
		Inputs:  []workflow.SignaturePort{{Name: "repo", Type: "repository/v1"}, {Name: "context", Type: "log-bundle/v1", Optional: true}},
		Outputs: []workflow.SignaturePort{{Name: "review", Type: "review/v1"}},
	}
}
