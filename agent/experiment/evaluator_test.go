package experiment_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
)

type evaluationStore struct {
	claims      []experiment.EvaluationCell
	admitRun    func(context.Context, experiment.EvaluationCell, experiment.EvaluatorRunCreator) (experiment.EvaluatorRunAdmission, error)
	admit       func(context.Context, experiment.CellID) (experiment.BudgetReservation, error)
	check       func(context.Context, experiment.CellID) (experiment.BudgetUsage, error)
	recorded    map[experiment.CellID]snapshot.WorkflowRunID
	completed   map[experiment.CellID]experiment.CellStatus
	measurement map[experiment.CellID]snapshot.SnapshotID
	documents   map[experiment.CellID]contracts.MeasurementsDocument
}

func (store *evaluationStore) ClaimEvaluationCells(context.Context, int) ([]experiment.EvaluationCell, error) {
	return append([]experiment.EvaluationCell(nil), store.claims...), nil
}
func (store *evaluationStore) CreateAndRecordEvaluatorRun(
	ctx context.Context,
	cell experiment.EvaluationCell,
	create experiment.EvaluatorRunCreator,
) (experiment.EvaluatorRunAdmission, error) {
	if store.admitRun != nil {
		return store.admitRun(ctx, cell, create)
	}
	result, bindErr := create(ctx)
	admission := experiment.EvaluatorRunAdmission{Started: true, Result: result, BindError: bindErr}
	if bindErr != nil || result.WorkflowRunID.Validate() != nil ||
		result.WorkflowDefinitionID != cell.Evaluator.Target.DefinitionID ||
		result.WorkflowName != cell.Evaluator.Target.WorkflowName ||
		result.WorkflowVersion != cell.Evaluator.Target.Version ||
		result.FunctionID != cell.Evaluator.Target.FunctionID ||
		result.TargetConfigHash != cell.Evaluator.TargetConfigHash ||
		result.DevValidationProvenanceHash != cell.Evaluator.DevValidationProvenanceHash {
		return admission, nil
	}
	recorded, err := store.RecordEvaluatorRun(ctx, cell.ID, result.WorkflowRunID)
	admission.Recorded = recorded
	return admission, err
}
func (store *evaluationStore) ReserveCandidateBudget(context.Context, experiment.CellID) (experiment.BudgetReservation, error) {
	panic("evaluator must not reserve candidate budget")
}
func (store *evaluationStore) AdmitEvaluatorBudget(ctx context.Context, cell experiment.CellID) (experiment.BudgetReservation, error) {
	if store.admit == nil {
		return experiment.BudgetReservation{CellID: cell}, nil
	}
	return store.admit(ctx, cell)
}
func (store *evaluationStore) CheckCellBudget(ctx context.Context, cell experiment.CellID) (experiment.BudgetUsage, error) {
	if store.check == nil {
		return experiment.BudgetUsage{}, nil
	}
	return store.check(ctx, cell)
}
func (store *evaluationStore) RecordEvaluatorRun(_ context.Context, cell experiment.CellID, run snapshot.WorkflowRunID) (bool, error) {
	if store.recorded == nil {
		store.recorded = make(map[experiment.CellID]snapshot.WorkflowRunID)
	}
	if old, found := store.recorded[cell]; found {
		return old == run, nil
	}
	store.recorded[cell] = run
	return true, nil
}
func (store *evaluationStore) CompleteEvaluation(_ context.Context, cell experiment.CellID, status experiment.CellStatus, measurement *snapshot.SnapshotID) (bool, error) {
	if store.completed == nil {
		store.completed = make(map[experiment.CellID]experiment.CellStatus)
	}
	if old, found := store.completed[cell]; found {
		return old == status, nil
	}
	store.completed[cell] = status
	if measurement != nil {
		if store.measurement == nil {
			store.measurement = make(map[experiment.CellID]snapshot.SnapshotID)
		}
		store.measurement[cell] = *measurement
	}
	return true, nil
}
func (store *evaluationStore) RecordMeasurements(_ context.Context, cell experiment.CellID, document contracts.MeasurementsDocument) error {
	if store.documents == nil {
		store.documents = make(map[experiment.CellID]contracts.MeasurementsDocument)
	}
	store.documents[cell] = document
	return nil
}

type evaluationRuns struct {
	observations map[snapshot.WorkflowRunID]experiment.RunObservation
}

func (runs *evaluationRuns) Inspect(_ context.Context, _ int, id snapshot.WorkflowRunID) (experiment.RunObservation, bool, error) {
	value, found := runs.observations[id]
	return value, found, nil
}

type measurementReader struct {
	documents map[snapshot.SnapshotID]contracts.MeasurementsDocument
}

func (reader *measurementReader) ReadMeasurements(_ context.Context, _ int, id snapshot.SnapshotID) (contracts.MeasurementsDocument, bool, error) {
	value, found := reader.documents[id]
	return value, found, nil
}

func TestEvaluatorBindsPinnedEvaluatorFromFixtureAndCandidateOutputs(t *testing.T) {
	cell := evaluationCell()
	cell.Evaluator.DevValidationProvenanceHash = strings.Repeat("d", 64)
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	var gotAdmission experiment.AdmissionContext
	var gotRequest experiment.BindRequest
	binder := &experimentBinder{bind: func(_ context.Context, admission experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		gotAdmission, gotRequest = admission, request
		return successfulBind(request, 301), nil
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, binder, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotRequest.WorkflowName != "review-evaluator" || gotRequest.Version == nil || *gotRequest.Version != 2 ||
		gotRequest.Inputs["candidate"] != 201 || gotRequest.Inputs["repo"] != 101 ||
		gotRequest.IdempotencyKey != "experiment:50:cell:1:evaluator" ||
		gotRequest.ExpectedTargetConfigHash != cell.Evaluator.TargetConfigHash ||
		gotRequest.ExpectedDevValidationProvenanceHash != cell.Evaluator.DevValidationProvenanceHash ||
		gotRequest.AdmissionGate != (experiment.AdmissionGate{
			ExperimentID: cell.ExperimentID,
			CellID:       cell.ID,
			Phase:        experiment.AdmissionEvaluator,
		}) {
		t.Fatalf("evaluator request = %+v", gotRequest)
	}
	if gotAdmission.Origin.Kind != "experiment" || gotAdmission.Origin.Reference != "experiment:50:cell:1:evaluator" || gotAdmission.TeamID != 7 {
		t.Fatalf("evaluator admission = %+v", gotAdmission)
	}
	if store.recorded[1] != 301 || len(store.completed) != 0 {
		t.Fatalf("recorded/completed = %#v / %#v", store.recorded, store.completed)
	}
}

func TestEvaluatorDoesNotCreateAChildWhenTheParentCancellationFenceIsClosed(t *testing.T) {
	cell := evaluationCell()
	store := &evaluationStore{
		claims: []experiment.EvaluationCell{cell},
		admitRun: func(context.Context, experiment.EvaluationCell, experiment.EvaluatorRunCreator) (experiment.EvaluatorRunAdmission, error) {
			return experiment.EvaluatorRunAdmission{}, nil
		},
	}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, &experimentBinder{
		bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
			t.Fatal("closed parent fence invoked the evaluator binder")
			return experiment.BindResult{}, nil
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 0 || len(store.completed) != 0 {
		t.Fatalf("closed fence changed cell state: recorded=%v completed=%v", store.recorded, store.completed)
	}
}

func TestEvaluatorMarksTheCellBudgetSkippedBeforeBindingWhenTheBudgetIsExhausted(t *testing.T) {
	cell := evaluationCell()
	cell.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 5, MaxTokensPerCell: 1000}
	store := &evaluationStore{
		claims: []experiment.EvaluationCell{cell},
		admit: func(context.Context, experiment.CellID) (experiment.BudgetReservation, error) {
			return experiment.BudgetReservation{}, experiment.ErrBudgetExhausted
		},
	}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, &experimentBinder{bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
		t.Fatal("exhausted cell reached evaluator binder")
		return experiment.BindResult{}, nil
	}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[cell.ID] != experiment.CellSkippedBudget {
		t.Fatalf("completion = %q, want budget skipped", store.completed[cell.ID])
	}
}

func TestEvaluatorMarksBinderBudgetDenialAsBudgetSkipped(t *testing.T) {
	cell := evaluationCell()
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, &experimentBinder{
		bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
			return experiment.BindResult{}, experiment.ErrBindBudgetDenied
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[cell.ID] != experiment.CellSkippedBudget {
		t.Fatalf("completion = %q, want budget skipped", store.completed[cell.ID])
	}
}

func TestEvaluatorLeavesTheCellRetryableWhenBudgetAccountingFails(t *testing.T) {
	cell := evaluationCell()
	cell.Budget = experiment.Budget{PerCellUSD: 1}
	store := &evaluationStore{
		claims: []experiment.EvaluationCell{cell},
		admit: func(context.Context, experiment.CellID) (experiment.BudgetReservation, error) {
			return experiment.BudgetReservation{}, errors.New("ledger unavailable")
		},
	}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, &experimentBinder{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err == nil {
		t.Fatal("budget accounting failure was hidden")
	}
	if len(store.completed) != 0 || len(store.recorded) != 0 {
		t.Fatalf("accounting failure terminalized cell: completed=%v recorded=%v", store.completed, store.recorded)
	}
}

func TestEvaluatorRejectsAnOverBudgetMeasurementAfterTheRun(t *testing.T) {
	cell := evaluationCell()
	cell.Budget = experiment.Budget{PerCellUSD: 1, MaxTokensPerCell: 1000}
	evaluatorRunID := snapshot.WorkflowRunID(301)
	cell.EvaluatorWorkflowRunID = &evaluatorRunID
	store := &evaluationStore{
		claims: []experiment.EvaluationCell{cell},
		check: func(context.Context, experiment.CellID) (experiment.BudgetUsage, error) {
			return experiment.BudgetUsage{CostUSD: 1.01, Tokens: 1001}, experiment.ErrBudgetExhausted
		},
	}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		evaluatorRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"measurements": {ID: 401, Type: "measurements/v1", Digest: digestFor('b')},
		}},
	}}
	reader := &measurementReader{documents: map[snapshot.SnapshotID]contracts.MeasurementsDocument{401: {
		Conclusion: "measured",
		Metrics:    []contracts.Measurement{{ID: "score", Value: 8, Unit: "score", Direction: "higher-is-better"}},
	}}}
	evaluator, err := experiment.NewEvaluator(store, runs, reader, &experimentBinder{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[cell.ID] != experiment.CellSkippedBudget || len(store.documents) != 0 {
		t.Fatalf("over-budget measurement accepted: completed=%v documents=%v", store.completed, store.documents)
	}
}

func TestEvaluatorRejectsMeasurementsBeyondTheScorecardMetricBound(t *testing.T) {
	cell := evaluationCell()
	evaluatorRunID := snapshot.WorkflowRunID(301)
	cell.EvaluatorWorkflowRunID = &evaluatorRunID
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		evaluatorRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"measurements": {ID: 401, Type: "measurements/v1", Digest: digestFor('b')},
		}},
	}}
	metrics := make([]contracts.Measurement, experiment.MaxMeasurementsPerCell+1)
	for index := range metrics {
		metrics[index] = contracts.Measurement{
			ID: fmt.Sprintf("metric-%d", index), Value: float64(index), Unit: "score", Direction: "higher-is-better",
		}
	}
	reader := &measurementReader{documents: map[snapshot.SnapshotID]contracts.MeasurementsDocument{401: {
		Conclusion: "measured", Metrics: metrics,
	}}}
	evaluator, err := experiment.NewEvaluator(store, runs, reader, &experimentBinder{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[cell.ID] != experiment.CellEvaluatorFailure || len(store.documents) != 0 {
		t.Fatalf("oversized measurements accepted: completed=%v documents=%v", store.completed, store.documents)
	}
}

func TestEvaluatorRejectsAWorkflowRunForTheWrongFrozenTarget(t *testing.T) {
	cell := evaluationCell()
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		result := successfulBind(request, 301)
		result.FunctionID = "different-function"
		return result, nil
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, binder, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[cell.ID] != experiment.CellEvaluatorFailure || len(store.recorded) != 0 {
		t.Fatalf("mismatched evaluator target accepted: completed=%v recorded=%v", store.completed, store.recorded)
	}
}

func TestEvaluatorRejectsAWorkflowRunForDifferentFrozenRuntimeDependencies(t *testing.T) {
	cell := evaluationCell()
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		result := successfulBind(request, 301)
		result.TargetConfigHash = stringsRepeat('f', 64)
		return result, nil
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, binder, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[cell.ID] != experiment.CellEvaluatorFailure || len(store.recorded) != 0 {
		t.Fatalf("mismatched evaluator runtime dependencies accepted: completed=%v recorded=%v", store.completed, store.recorded)
	}
}

func TestEvaluatorCollectsStrictMeasurementsAndNegativeControlAssertions(t *testing.T) {
	cell := evaluationCell()
	evaluatorRunID := snapshot.WorkflowRunID(301)
	cell.EvaluatorWorkflowRunID = &evaluatorRunID
	cell.Role = experiment.FixtureNegativeControl
	cell.Assertions = []experiment.Assertion{
		{Metric: "defects", Comparator: experiment.ComparatorGTE, Thresholds: []float64{1}},
		{Metric: "score", Comparator: experiment.ComparatorBetween, Thresholds: []float64{0, 10}},
	}
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		evaluatorRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"measurements": {ID: 401, Type: "measurements/v1", Digest: digestFor('b')},
		}},
	}}
	reader := &measurementReader{documents: map[snapshot.SnapshotID]contracts.MeasurementsDocument{401: {
		Conclusion: "measured",
		Metrics: []contracts.Measurement{
			{ID: "defects", Value: 2, Unit: "count", Direction: "lower-is-better"},
			{ID: "score", Value: 8, Unit: "score", Direction: "higher-is-better"},
		},
	}}}
	evaluator, err := experiment.NewEvaluator(store, runs, reader, &experimentBinder{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[1] != experiment.CellValidMeasurement || store.measurement[1] != 401 {
		t.Fatalf("completion = %#v / %#v", store.completed, store.measurement)
	}
	if got := store.documents[1]; got.Conclusion != "measured" || len(got.Metrics) != 2 {
		t.Fatalf("persisted measurements = %#v", got)
	}

	cell.Assertions[0].Thresholds = []float64{3}
	store = &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	evaluator, _ = experiment.NewEvaluator(store, runs, reader, &experimentBinder{}, 10)
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.completed[1] != experiment.CellNegativeControlFailure || store.measurement[1] != 401 {
		t.Fatalf("negative failure = %#v / %#v", store.completed, store.measurement)
	}
}

func TestEvaluatorClassifiesCandidateAndEvaluatorFailures(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*experiment.EvaluationCell, map[snapshot.WorkflowRunID]experiment.RunObservation)
		want    experiment.CellStatus
	}{
		{name: "candidate contract", prepare: func(cell *experiment.EvaluationCell, observations map[snapshot.WorkflowRunID]experiment.RunObservation) {
			observations[cell.CandidateWorkflowRunID] = experiment.RunObservation{Status: experiment.ObservedRunFailed, Failure: experiment.ObservedContractFailure}
		}, want: experiment.CellCandidateContractFailure},
		{name: "candidate platform", prepare: func(cell *experiment.EvaluationCell, observations map[snapshot.WorkflowRunID]experiment.RunObservation) {
			observations[cell.CandidateWorkflowRunID] = experiment.RunObservation{Status: experiment.ObservedRunFailed, Failure: experiment.ObservedPlatformFailure}
		}, want: experiment.CellCandidatePlatformFailure},
		{name: "missing candidate output", prepare: func(cell *experiment.EvaluationCell, observations map[snapshot.WorkflowRunID]experiment.RunObservation) {
			observations[cell.CandidateWorkflowRunID] = experiment.RunObservation{Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{}}
		}, want: experiment.CellCandidateContractFailure},
		{name: "evaluator failed", prepare: func(cell *experiment.EvaluationCell, observations map[snapshot.WorkflowRunID]experiment.RunObservation) {
			id := snapshot.WorkflowRunID(301)
			cell.EvaluatorWorkflowRunID = &id
			observations[id] = experiment.RunObservation{Status: experiment.ObservedRunFailed, Failure: experiment.ObservedPlatformFailure}
		}, want: experiment.CellEvaluatorFailure},
		{name: "missing measurements", prepare: func(cell *experiment.EvaluationCell, observations map[snapshot.WorkflowRunID]experiment.RunObservation) {
			id := snapshot.WorkflowRunID(301)
			cell.EvaluatorWorkflowRunID = &id
			observations[id] = experiment.RunObservation{Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{}}
		}, want: experiment.CellEvaluatorFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cell := evaluationCell()
			observations := make(map[snapshot.WorkflowRunID]experiment.RunObservation)
			test.prepare(&cell, observations)
			store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
			evaluator, err := experiment.NewEvaluator(store, &evaluationRuns{observations: observations}, &measurementReader{}, &experimentBinder{bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
				t.Fatal("terminal failure reached binder")
				return experiment.BindResult{}, nil
			}}, 10)
			if err != nil {
				t.Fatal(err)
			}
			if err := evaluator.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.completed[1] != test.want {
				t.Fatalf("status = %q, want %q", store.completed[1], test.want)
			}
		})
	}
}

func TestEvaluatorLeavesNonterminalRunsPendingAndDoesNotHideStoreErrors(t *testing.T) {
	cell := evaluationCell()
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		cell.CandidateWorkflowRunID: {Status: experiment.ObservedRunRunning},
	}}
	binder := &experimentBinder{bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
		t.Fatal("pending candidate reached binder")
		return experiment.BindResult{}, nil
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, binder, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 0 || len(store.completed) != 0 {
		t.Fatalf("pending cell mutated: %#v %#v", store.recorded, store.completed)
	}

	runs.observations[cell.CandidateWorkflowRunID] = experiment.RunObservation{Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')}}}
	binder.bind = func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
		return experiment.BindResult{}, errors.New("binder offline")
	}
	if err := evaluator.Run(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "binder offline") {
		t.Fatalf("binder platform failure was not left retryable: %v", err)
	}
	if len(store.completed) != 0 || len(store.recorded) != 0 {
		t.Fatalf("retryable binder failure mutated cell: completed=%v recorded=%v", store.completed, store.recorded)
	}
}

func TestEvaluatorTerminalizesPoisonClaimsAndContinuesLaterCells(t *testing.T) {
	invalid := evaluationCell()
	invalid.TeamName = ""
	valid := evaluationCell()
	valid.ID = 2
	valid.CandidateWorkflowRunID = 202
	store := &evaluationStore{claims: []experiment.EvaluationCell{invalid, valid}}
	runs := &evaluationRuns{observations: map[snapshot.WorkflowRunID]experiment.RunObservation{
		valid.CandidateWorkflowRunID: {Status: experiment.ObservedRunSucceeded, Outputs: map[string]snapshot.SnapshotRef{
			"review": {ID: 201, Type: "review/v1", Digest: digestFor('a')},
		}},
	}}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		return successfulBind(request, 302), nil
	}}
	evaluator, err := experiment.NewEvaluator(store, runs, &measurementReader{}, binder, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err == nil {
		t.Fatal("poison claim validation error was hidden")
	}
	if store.completed[invalid.ID] != experiment.CellEvaluatorFailure || store.recorded[valid.ID] != 302 {
		t.Fatalf("poison claim starved work: completed=%v recorded=%v", store.completed, store.recorded)
	}
}

func TestEvaluatorQuarantinesAClaimWithoutFrozenRuntimeDependencies(t *testing.T) {
	cell := evaluationCell()
	cell.Evaluator.TargetConfigHash = ""
	store := &evaluationStore{claims: []experiment.EvaluationCell{cell}}
	evaluator, err := experiment.NewEvaluator(store, &evaluationRuns{}, &measurementReader{}, &experimentBinder{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Run(context.Background()); err == nil {
		t.Fatal("missing frozen evaluator config was accepted")
	}
	if store.completed[cell.ID] != experiment.CellEvaluatorFailure {
		t.Fatalf("completion = %q, want evaluator failure", store.completed[cell.ID])
	}
}

func evaluationCell() experiment.EvaluationCell {
	signature := candidateSignature()
	return experiment.EvaluationCell{
		ID: 1, ExperimentID: 50, TeamID: 7, TeamName: "main", CreatedBy: "alice",
		CandidateWorkflowRunID: 200, CandidateSignature: signature,
		FixtureInputs: map[string]snapshot.SnapshotID{"repo": 101},
		Evaluator: experiment.Evaluator{
			Target:           experiment.Target{Kind: experiment.TargetWorkflow, WorkflowName: "review-evaluator", DefinitionID: 51, Version: 2},
			TargetConfigHash: stringsRepeat('e', 64),
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
		Role: experiment.FixtureNormal,
	}
}

func digestFor(character byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + stringsRepeat(character, 64))
}

func stringsRepeat(character byte, count int) string {
	bytes := make([]byte, count)
	for index := range bytes {
		bytes[index] = character
	}
	return string(bytes)
}
