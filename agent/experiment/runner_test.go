package experiment_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
)

type experimentRunnerStore struct {
	claim    func(context.Context, int) ([]experiment.CandidateCell, error)
	reserve  func(context.Context, experiment.CellID) (experiment.BudgetReservation, error)
	admit    func(context.Context, experiment.CandidateCell, experiment.CandidateRunCreator) (experiment.CandidateRunAdmission, error)
	mu       sync.Mutex
	recorded map[experiment.CellID]snapshot.WorkflowRunID
	failures map[experiment.CellID]string
}

func (store *experimentRunnerStore) ClaimCandidateCells(ctx context.Context, limit int) ([]experiment.CandidateCell, error) {
	return store.claim(ctx, limit)
}
func (store *experimentRunnerStore) CreateAndRecordCandidateRun(
	ctx context.Context,
	cell experiment.CandidateCell,
	create experiment.CandidateRunCreator,
) (experiment.CandidateRunAdmission, error) {
	if store.admit != nil {
		return store.admit(ctx, cell, create)
	}
	result, bindErr := create(ctx)
	admission := experiment.CandidateRunAdmission{Started: true, Result: result, BindError: bindErr}
	if bindErr != nil || result.WorkflowRunID.Validate() != nil ||
		result.WorkflowDefinitionID != cell.Target.DefinitionID ||
		result.WorkflowName != cell.Target.WorkflowName ||
		result.WorkflowVersion != cell.Target.Version ||
		result.FunctionID != cell.Target.FunctionID ||
		result.TargetConfigHash != cell.TargetConfigHash {
		return admission, nil
	}
	recorded, err := store.RecordCandidateRun(ctx, cell.ID, result.WorkflowRunID)
	admission.Recorded = recorded
	return admission, err
}
func (store *experimentRunnerStore) ReserveCandidateBudget(ctx context.Context, cell experiment.CellID) (experiment.BudgetReservation, error) {
	if store.reserve == nil {
		return experiment.BudgetReservation{CellID: cell}, nil
	}
	return store.reserve(ctx, cell)
}
func (store *experimentRunnerStore) AdmitEvaluatorBudget(context.Context, experiment.CellID) (experiment.BudgetReservation, error) {
	panic("candidate runner must not admit an evaluator budget")
}
func (store *experimentRunnerStore) CheckCellBudget(context.Context, experiment.CellID) (experiment.BudgetUsage, error) {
	panic("candidate runner must not check terminal cell budget")
}
func (store *experimentRunnerStore) RecordCandidateRun(_ context.Context, cell experiment.CellID, run snapshot.WorkflowRunID) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.recorded == nil {
		store.recorded = make(map[experiment.CellID]snapshot.WorkflowRunID)
	}
	if existing, found := store.recorded[cell]; found {
		return existing == run, nil
	}
	store.recorded[cell] = run
	return true, nil
}
func (store *experimentRunnerStore) RecordCandidateFailure(_ context.Context, cell experiment.CellID, category string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failures == nil {
		store.failures = make(map[experiment.CellID]string)
	}
	store.failures[cell] = category
	return nil
}

type experimentBinder struct {
	bind func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error)
}

func (binder *experimentBinder) BindAndCreate(ctx context.Context, admission experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
	return binder.bind(ctx, admission, request)
}

func TestRunnerBindsClaimedCellsThroughOrdinaryWorkflowRuns(t *testing.T) {
	cells := []experiment.CandidateCell{candidateCell(3), candidateCell(1), candidateCell(2)}
	for index := range cells {
		cells[index].DevValidationProvenanceHash = strings.Repeat(string(rune('a'+index)), 64)
	}
	store := &experimentRunnerStore{claim: func(_ context.Context, limit int) ([]experiment.CandidateCell, error) {
		if limit != 3 {
			t.Fatalf("claim limit = %d, want 3", limit)
		}
		return cells, nil
	}}
	var mu sync.Mutex
	requests := make(map[experiment.CellID]experiment.BindRequest)
	admissions := make(map[experiment.CellID]experiment.AdmissionContext)
	binder := &experimentBinder{bind: func(_ context.Context, admission experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		cellID := cellIDFromRequest(t, request.IdempotencyKey)
		mu.Lock()
		requests[cellID], admissions[cellID] = request, admission
		mu.Unlock()
		return successfulBind(request, snapshot.WorkflowRunID(100+cellID)), nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, cell := range cells {
		request := requests[cell.ID]
		version := cell.Target.Version
		wantKey := fmt.Sprintf("experiment:%s:fixture:%d:variant:%d:rep:%d:candidate", cell.ExperimentID.String(), cell.FixtureID, cell.VariantID, cell.Repetition)
		if request.WorkflowName != cell.Target.WorkflowName || request.Version == nil || *request.Version != version ||
			request.FunctionID != cell.Target.FunctionID || request.IdempotencyKey != wantKey ||
			request.Inputs["repo"] != cell.Inputs["repo"] ||
			request.ExpectedTargetConfigHash != cell.TargetConfigHash ||
			request.ExpectedDevValidationProvenanceHash != cell.DevValidationProvenanceHash ||
			request.AdmissionGate != (experiment.AdmissionGate{
				ExperimentID: cell.ExperimentID,
				CellID:       cell.ID,
				Phase:        experiment.AdmissionCandidate,
			}) {
			t.Fatalf("cell %s request = %+v", cell.ID.String(), request)
		}
		admission := admissions[cell.ID]
		if admission.TeamID != cell.TeamID || admission.TeamName != cell.TeamName || admission.CreatedBy != cell.CreatedBy ||
			admission.Origin.Kind != "experiment" || admission.Origin.Reference != fmt.Sprintf("experiment:%s:cell:%s", cell.ExperimentID.String(), cell.ID.String()) {
			t.Fatalf("cell %s admission = %+v", cell.ID.String(), admission)
		}
		if store.recorded[cell.ID] != snapshot.WorkflowRunID(100+cell.ID) {
			t.Fatalf("cell %s recorded run = %s", cell.ID.String(), store.recorded[cell.ID].String())
		}
	}
}

func TestRunnerBoundsConcurrency(t *testing.T) {
	var cells []experiment.CandidateCell
	for index := 1; index <= 8; index++ {
		cells = append(cells, candidateCell(experiment.CellID(index)))
	}
	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) { return cells, nil }}
	var active, maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, len(cells))
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return successfulBind(request, snapshot.WorkflowRunID(100+cellIDFromRequest(t, request.IdempotencyKey))), nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background()) }()
	for range 2 {
		<-started
	}
	select {
	case <-started:
		t.Fatal("runner exceeded configured concurrency before a slot was released")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
	}
}

func TestRunnerDoesNotCreateAChildWhenTheParentCancellationFenceIsClosed(t *testing.T) {
	cell := candidateCell(1)
	store := &experimentRunnerStore{
		claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
			return []experiment.CandidateCell{cell}, nil
		},
		admit: func(context.Context, experiment.CandidateCell, experiment.CandidateRunCreator) (experiment.CandidateRunAdmission, error) {
			return experiment.CandidateRunAdmission{}, nil
		},
	}
	binder := &experimentBinder{bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
		t.Fatal("closed parent fence invoked the workflow binder")
		return experiment.BindResult{}, nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.recorded) != 0 || len(store.failures) != 0 {
		t.Fatalf("closed fence changed cell state: recorded=%v failures=%v", store.recorded, store.failures)
	}
}

func TestRunnerRecordsStableFailureCategoriesAndContinues(t *testing.T) {
	cells := []experiment.CandidateCell{candidateCell(1), candidateCell(2), candidateCell(3)}
	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) { return cells, nil }}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		switch cellIDFromRequest(t, request.IdempotencyKey) {
		case 1:
			return experiment.BindResult{}, experiment.ErrBindBudgetDenied
		case 2:
			return experiment.BindResult{}, experiment.ErrBindInvalidRequest
		default:
			return successfulBind(request, 103), nil
		}
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failures[1] != "budget_denied" || store.failures[2] != "invalid_admission" || store.recorded[3] != 103 {
		t.Fatalf("failures/runs = %#v / %#v", store.failures, store.recorded)
	}
}

func TestRunnerLeavesPlatformBindingFailuresRetryable(t *testing.T) {
	cell := candidateCell(1)
	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
		return []experiment.CandidateCell{cell}, nil
	}}
	binder := &experimentBinder{bind: func(
		context.Context,
		experiment.AdmissionContext,
		experiment.BindRequest,
	) (experiment.BindResult, error) {
		return experiment.BindResult{}, errors.New("secret backend unavailable")
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "secret backend unavailable") {
		t.Fatalf("Run() = %v, want retryable platform error", err)
	}
	if len(store.recorded) != 0 || len(store.failures) != 0 {
		t.Fatalf("retryable failure mutated cell: recorded=%v failures=%v", store.recorded, store.failures)
	}
}

func TestRunnerReservesEachBudgetedCellBeforeBinding(t *testing.T) {
	cell := candidateCell(1)
	cell.Budget = experiment.Budget{PerCellUSD: 2.5, TotalUSD: 10, MaxTokensPerCell: 20_000}
	reserved := make(chan experiment.CellID, 1)
	store := &experimentRunnerStore{
		claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
			return []experiment.CandidateCell{cell}, nil
		},
		reserve: func(_ context.Context, id experiment.CellID) (experiment.BudgetReservation, error) {
			reserved <- id
			return experiment.BudgetReservation{CellID: id, LimitUSD: 2.5, MaxTokens: 20_000}, nil
		},
	}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		select {
		case id := <-reserved:
			if id != cell.ID {
				t.Fatalf("reserved cell = %s, want %s", id.String(), cell.ID.String())
			}
		default:
			t.Fatal("binder ran before durable budget reservation")
		}
		return successfulBind(request, 101), nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.recorded[cell.ID] != 101 {
		t.Fatalf("recorded run = %s", store.recorded[cell.ID].String())
	}
}

func TestRunnerReservesBudgetedCellsInStableOrderBeforeConcurrentBinding(t *testing.T) {
	cells := []experiment.CandidateCell{candidateCell(3), candidateCell(1), candidateCell(2)}
	for index := range cells {
		cells[index].Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 3}
	}
	var reserveMu sync.Mutex
	var reserveOrder []experiment.CellID
	store := &experimentRunnerStore{
		claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
			return cells, nil
		},
		reserve: func(_ context.Context, id experiment.CellID) (experiment.BudgetReservation, error) {
			reserveMu.Lock()
			reserveOrder = append(reserveOrder, id)
			reserveMu.Unlock()
			return experiment.BudgetReservation{CellID: id, LimitUSD: 1}, nil
		},
	}
	var boundBeforeAdmission atomic.Bool
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		reserveMu.Lock()
		admitted := len(reserveOrder)
		reserveMu.Unlock()
		if admitted != len(cells) {
			boundBeforeAdmission.Store(true)
		}
		return successfulBind(request, snapshot.WorkflowRunID(100+cellIDFromRequest(t, request.IdempotencyKey))), nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if boundBeforeAdmission.Load() {
		t.Fatal("binding began before the complete claimed batch had deterministic budget decisions")
	}
	want := []experiment.CellID{1, 2, 3}
	if !reflect.DeepEqual(reserveOrder, want) {
		t.Fatalf("reservation order = %v, want %v", reserveOrder, want)
	}
}

func TestRunnerDoesNotBindAnExhaustedOrUnreservableCell(t *testing.T) {
	tests := []struct {
		name        string
		reserveErr  error
		wantRunErr  bool
		wantFailure string
	}{
		{name: "exhausted", reserveErr: experiment.ErrBudgetExhausted, wantFailure: "budget_denied"},
		{name: "accounting unavailable", reserveErr: errors.New("ledger unavailable"), wantRunErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cell := candidateCell(1)
			cell.Budget = experiment.Budget{PerCellUSD: 1, TotalUSD: 1}
			store := &experimentRunnerStore{
				claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
					return []experiment.CandidateCell{cell}, nil
				},
				reserve: func(context.Context, experiment.CellID) (experiment.BudgetReservation, error) {
					return experiment.BudgetReservation{}, test.reserveErr
				},
			}
			binder := &experimentBinder{bind: func(context.Context, experiment.AdmissionContext, experiment.BindRequest) (experiment.BindResult, error) {
				t.Fatal("unreserved cell reached workflow binder")
				return experiment.BindResult{}, nil
			}}
			runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
			if err != nil {
				t.Fatal(err)
			}
			runErr := runner.Run(context.Background())
			if (runErr != nil) != test.wantRunErr {
				t.Fatalf("Run() = %v, want error=%t", runErr, test.wantRunErr)
			}
			if store.failures[cell.ID] != test.wantFailure {
				t.Fatalf("failure = %q, want %q", store.failures[cell.ID], test.wantFailure)
			}
		})
	}
}

func TestRunnerCancellationLeavesUnstartedCellsForRestart(t *testing.T) {
	cells := []experiment.CandidateCell{candidateCell(1), candidateCell(2)}
	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) { return cells, nil }}
	started := make(chan struct{})
	binder := &experimentBinder{bind: func(ctx context.Context, _ experiment.AdmissionContext, _ experiment.BindRequest) (experiment.BindResult, error) {
		close(started)
		<-ctx.Done()
		return experiment.BindResult{}, ctx.Err()
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want canceled", err)
	}
	if len(store.failures) != 0 || len(store.recorded) != 0 {
		t.Fatalf("cancellation terminalized cells: failures=%#v runs=%#v", store.failures, store.recorded)
	}
}

func TestRunnerRejectsInvalidDependenciesAndClaims(t *testing.T) {
	if _, err := experiment.NewRunner(nil, &experimentBinder{}, experiment.RunnerConfig{MaxConcurrency: 1}); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := experiment.NewRunner(&experimentRunnerStore{}, nil, experiment.RunnerConfig{MaxConcurrency: 1}); err == nil {
		t.Fatal("nil binder accepted")
	}
	if _, err := experiment.NewRunner(&experimentRunnerStore{}, &experimentBinder{}, experiment.RunnerConfig{}); err == nil {
		t.Fatal("zero concurrency accepted")
	}

	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
		invalid := candidateCell(1)
		invalid.TeamID = 0
		return []experiment.CandidateCell{invalid, candidateCell(2)}, nil
	}}
	runner, err := experiment.NewRunner(store, &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		return successfulBind(request, 102), nil
	}}, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err == nil {
		t.Fatal("invalid claimed cell returned nil")
	}
	if store.failures[1] != "invalid_admission" || store.recorded[2] != 102 {
		t.Fatalf("poison claim starved later work: failures=%v recorded=%v", store.failures, store.recorded)
	}
}

func TestRunnerRejectsAWorkflowRunForTheWrongFrozenTarget(t *testing.T) {
	cell := candidateCell(1)
	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
		return []experiment.CandidateCell{cell}, nil
	}}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		result := successfulBind(request, 101)
		result.WorkflowDefinitionID++
		return result, nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failures[cell.ID] != "invalid_admission" || len(store.recorded) != 0 {
		t.Fatalf("mismatched target accepted: failures=%v recorded=%v", store.failures, store.recorded)
	}
}

func TestRunnerRejectsAWorkflowRunForDifferentFrozenRuntimeDependencies(t *testing.T) {
	cell := candidateCell(1)
	store := &experimentRunnerStore{claim: func(context.Context, int) ([]experiment.CandidateCell, error) {
		return []experiment.CandidateCell{cell}, nil
	}}
	binder := &experimentBinder{bind: func(_ context.Context, _ experiment.AdmissionContext, request experiment.BindRequest) (experiment.BindResult, error) {
		result := successfulBind(request, 101)
		result.TargetConfigHash = fmt.Sprintf("%064d", 2)
		return result, nil
	}}
	runner, err := experiment.NewRunner(store, binder, experiment.RunnerConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failures[cell.ID] != "invalid_admission" || len(store.recorded) != 0 {
		t.Fatalf("mismatched runtime dependencies accepted: failures=%v recorded=%v", store.failures, store.recorded)
	}
}

func candidateCell(id experiment.CellID) experiment.CandidateCell {
	return experiment.CandidateCell{
		ID: id, ExperimentID: 50, FixtureID: 60 + int64(id), VariantID: 70 + int64(id),
		TeamID: 7, TeamName: "main", CreatedBy: "alice", Repetition: int(id),
		Target:           experiment.Target{Kind: experiment.TargetFunction, WorkflowName: "review", DefinitionID: 41, Version: 3, FunctionID: "review"},
		TargetConfigHash: fmt.Sprintf("%064d", 1),
		Inputs:           map[string]snapshot.SnapshotID{"repo": snapshot.SnapshotID(80 + id)},
	}
}

func cellIDFromRequest(t *testing.T, key string) experiment.CellID {
	t.Helper()
	var experimentID, fixtureID, variantID int64
	var repetition int
	if _, err := fmt.Sscanf(key, "experiment:%d:fixture:%d:variant:%d:rep:%d:candidate", &experimentID, &fixtureID, &variantID, &repetition); err != nil {
		t.Fatalf("parse idempotency key %q: %v", key, err)
	}
	return experiment.CellID(variantID - 70)
}

func successfulBind(request experiment.BindRequest, id snapshot.WorkflowRunID) experiment.BindResult {
	version := 0
	if request.Version != nil {
		version = *request.Version
	}
	return experiment.BindResult{
		WorkflowRunID: id, WorkflowDefinitionID: request.DefinitionID,
		WorkflowName: request.WorkflowName, WorkflowVersion: version, FunctionID: request.FunctionID,
		TargetConfigHash:            request.ExpectedTargetConfigHash,
		DevValidationProvenanceHash: request.ExpectedDevValidationProvenanceHash,
	}
}
