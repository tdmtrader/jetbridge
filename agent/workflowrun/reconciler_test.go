package workflowrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/workflowprovenance"
)

func TestNewReconcilerValidatesConfiguration(t *testing.T) {
	store := &reconciliationStoreFake{}
	logger := lager.NewLogger("test")
	tests := []struct {
		name      string
		store     ReconciliationStore
		logger    lager.Logger
		timeout   time.Duration
		delay     time.Duration
		options   []ReconcilerOption
		wantError string
	}{
		{name: "nil store", logger: logger, timeout: 10 * time.Minute, delay: time.Minute, wantError: "store"},
		{name: "nil logger", store: store, timeout: 10 * time.Minute, delay: time.Minute, wantError: "logger"},
		{name: "nonpositive timeout", store: store, logger: logger, delay: time.Minute, wantError: "admission timeout"},
		{name: "nonpositive retry delay", store: store, logger: logger, timeout: 10 * time.Minute, wantError: "retry delay"},
		{name: "timeout below two delays", store: store, logger: logger, timeout: 90 * time.Second, delay: time.Minute, wantError: "twice"},
		{name: "two delays overflow duration", store: store, logger: logger, timeout: time.Duration(1<<63 - 1), delay: time.Duration(1 << 62), wantError: "twice"},
		{name: "nil clock", store: store, logger: logger, timeout: 10 * time.Minute, delay: time.Minute, options: []ReconcilerOption{WithReconcilerClock(nil)}, wantError: "clock"},
		{name: "nil wait completer", store: store, logger: logger, timeout: 10 * time.Minute, delay: time.Minute, options: []ReconcilerOption{WithWaitResolutionCompleter(nil)}, wantError: "wait resolution"},
		{name: "nil wait canceler", store: store, logger: logger, timeout: 10 * time.Minute, delay: time.Minute, options: []ReconcilerOption{WithWaitCanceler(nil)}, wantError: "wait canceler"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewReconciler(test.store, test.logger, test.timeout, test.delay, test.options...)
			if err == nil || !containsFold(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
		})
	}

	if _, err := NewReconciler(store, logger, 2*time.Minute, time.Minute); err != nil {
		t.Fatalf("minimum valid configuration: %v", err)
	}
}

func TestReconcilerClaimsAtMostOneHundredWithOneClockReading(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	runs := make([]db.AgentWorkflowRun, 101)
	claimedIDs := make([]snapshot.WorkflowRunID, len(runs))
	for index := range runs {
		claimedIDs[index] = snapshot.WorkflowRunID(index + 1)
		runs[index] = reconciliationRun(claimedIDs[index], db.AgentWorkflowRunStatusRunning, now.Add(-time.Hour))
	}
	store := &reconciliationStoreFake{
		claimFn: func(_ context.Context, gotNow time.Time, delay time.Duration, limit int) ([]snapshot.WorkflowRunID, error) {
			if !gotNow.Equal(now) || delay != time.Minute || limit != 100 {
				t.Fatalf("claim = (%s, %s, %d), want (%s, 1m, 100)", gotNow, delay, limit, now)
			}
			return claimedIDs, nil
		},
		inspectFn: func(_ context.Context, id snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
			return db.AgentWorkflowRunReconciliationView{
				Run: runs[int(id)-1], SelectedBuildExists: true, SelectedBuildStatus: db.BuildStatusPending,
			}, true, nil
		},
	}
	reporter := &reconciliationReporterFake{}
	clockCalls := 0
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, func() time.Time {
		clockCalls++
		return now
	})

	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if clockCalls != 1 {
		t.Fatalf("clock calls = %d, want 1", clockCalls)
	}
	if len(store.inspectIDs) != 100 {
		t.Fatalf("inspected rows = %d, want bounded 100", len(store.inspectIDs))
	}
	if got := reporter.rowCount(metric.WorkflowRunReconcilerRowDeferred); got != 100 {
		t.Fatalf("deferred metric = %d, want 100", got)
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassSuccess)
}

func TestReconcilerReturnsOnlyClaimErrors(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	claimErr := errors.New("claim unavailable")
	store := &reconciliationStoreFake{claimFn: func(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error) {
		return nil, claimErr
	}}
	reporter := &reconciliationReporterFake{}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil)

	err := reconciler.Run(context.Background())
	if !errors.Is(err, claimErr) {
		t.Fatalf("Run error = %v, want claim error", err)
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassError)
}

func TestReconcilerAdmittingStateMachine(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		run          db.AgentWorkflowRun
		wantAdvance  bool
		wantTerminal *db.AgentWorkflowRunStatus
		wantMessage  string
		wantRow      metric.WorkflowRunReconcilerRowResult
	}{
		{name: "complete admission advances", run: completeAdmissionRun(1, now.Add(-time.Hour)), wantAdvance: true, wantRow: metric.WorkflowRunReconcilerRowAdvanced},
		{name: "fresh empty admission defers", run: reconciliationRun(1, db.AgentWorkflowRunStatusAdmitting, now.Add(-14*time.Minute)), wantRow: metric.WorkflowRunReconcilerRowDeferred},
		{name: "timed out empty admission errors", run: reconciliationRun(1, db.AgentWorkflowRunStatusAdmitting, now.Add(-15*time.Minute)), wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored), wantMessage: "admission interrupted", wantRow: metric.WorkflowRunReconcilerRowAdvanced},
		{name: "partial admission errors immediately", run: partialAdmissionRun(1, now.Add(-time.Minute)), wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored), wantMessage: "admission", wantRow: metric.WorkflowRunReconcilerRowAdvanced},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := storeForSingleRun(test.run)
			reporter := &reconciliationReporterFake{}
			reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil)

			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(store.advanceIDs); (got == 1) != test.wantAdvance {
				t.Fatalf("advance calls = %d, wantAdvance %t", got, test.wantAdvance)
			}
			if test.wantTerminal == nil {
				if len(store.finalizations) != 0 {
					t.Fatalf("unexpected finalization: %+v", store.finalizations)
				}
			} else {
				finalization := onlyFinalization(t, store)
				if finalization.ExpectedStatus != db.AgentWorkflowRunStatusAdmitting || finalization.TerminalStatus != *test.wantTerminal {
					t.Fatalf("finalization status = (%s -> %s)", finalization.ExpectedStatus, finalization.TerminalStatus)
				}
				if !containsFold(finalization.ErrorMessage, test.wantMessage) {
					t.Fatalf("error message = %q, want %q", finalization.ErrorMessage, test.wantMessage)
				}
			}
			if got := reporter.rowCount(test.wantRow); got != 1 {
				t.Fatalf("row metric %q = %d, want 1", test.wantRow, got)
			}
		})
	}
}

func TestReconcilerRechecksAndBoundsAFalseAdmissionCAS(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name         string
		after        db.AgentWorkflowRun
		wantDeferred bool
		wantTerminal bool
	}{
		{
			name: "concurrent advance is observed",
			after: func() db.AgentWorkflowRun {
				run := completeAdmissionRun(1, now)
				run.Status = db.AgentWorkflowRunStatusRunning
				return run
			}(),
		},
		{
			name: "fresh unchanged admission is deferred", after: completeAdmissionRun(1, now.Add(-14*time.Minute)),
			wantDeferred: true,
		},
		{
			name: "stuck unchanged admission is terminalized", after: completeAdmissionRun(1, now.Add(-15*time.Minute)),
			wantTerminal: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := test.after
			before.Status = db.AgentWorkflowRunStatusAdmitting
			calls := 0
			store := &reconciliationStoreFake{
				claimFn: func(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error) {
					return []snapshot.WorkflowRunID{before.ID}, nil
				},
				inspectFn: func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
					calls++
					if calls == 1 {
						return db.AgentWorkflowRunReconciliationView{Run: before}, true, nil
					}
					return db.AgentWorkflowRunReconciliationView{Run: test.after}, true, nil
				},
				advanceFn: func(context.Context, snapshot.WorkflowRunID) (bool, error) { return false, nil },
			}
			reporter := &reconciliationReporterFake{}
			reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil)

			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if calls != 2 {
				t.Fatalf("inspection calls = %d, want initial plus CAS recheck", calls)
			}
			if test.wantDeferred {
				if reporter.rowCount(metric.WorkflowRunReconcilerRowDeferred) != 1 || len(store.finalizations) != 0 {
					t.Fatalf("fresh false CAS was not deferred: rows=%v finalizations=%v", reporter.rows, store.finalizations)
				}
				return
			}
			if test.wantTerminal {
				finalization := onlyFinalization(t, store)
				if finalization.TerminalStatus != db.AgentWorkflowRunStatusErrored ||
					!containsFold(finalization.ErrorMessage, "could not advance") {
					t.Fatalf("finalization = %+v", finalization)
				}
				return
			}
			if reporter.rowCount(metric.WorkflowRunReconcilerRowAdvanced) != 1 || len(store.finalizations) != 0 {
				t.Fatalf("concurrent advance was not observed: rows=%v finalizations=%v", reporter.rows, store.finalizations)
			}
		})
	}
}

func TestReconcilerRunningOutcomeStateMachine(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		run           db.AgentWorkflowRun
		buildExists   bool
		buildStatus   db.BuildStatus
		wantTerminal  *db.AgentWorkflowRunStatus
		wantMessage   string
		wantDeferred  bool
		provenance    provenanceVerification
		provenanceErr error
		wantOutputs   bool
	}{
		{name: "pending build defers", run: runningRun(1, now, nil, true), buildExists: true, buildStatus: db.BuildStatusPending, wantDeferred: true},
		{name: "started build defers", run: runningRun(1, now, nil, true), buildExists: true, buildStatus: db.BuildStatusStarted, wantDeferred: true},
		{name: "missing build without outcome errors", run: runningRun(1, now, nil, true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored), wantMessage: "outcome"},
		{name: "terminal build without copied outcome errors", run: runningRun(1, now, nil, true), buildExists: true, buildStatus: db.BuildStatusFailed, wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored), wantMessage: "outcome"},
		{name: "copied outcome without plan triad errors", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusFailed), false), buildExists: true, buildStatus: db.BuildStatusFailed, wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored), wantMessage: "provenance"},
		{name: "failed maps truthfully", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusFailed), true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusFailed)},
		{name: "errored maps truthfully", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusErrored), true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored)},
		{name: "aborted maps truthfully", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusAborted), true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusAborted)},
		{name: "succeeded finalizes verified outputs", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusSucceeded), true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusSucceeded), provenance: validProvenanceVerification(), wantOutputs: true},
		{name: "contract mismatch fails workflow", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusSucceeded), true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusFailed), wantMessage: "contract", provenance: provenanceVerification{ContractMismatch: true}},
		{name: "provenance corruption errors workflow", run: runningRun(1, now, executionPointer(db.AgentWorkflowRunExecutionStatusSucceeded), true), wantTerminal: statusPointer(db.AgentWorkflowRunStatusErrored), wantMessage: "provenance", provenanceErr: errors.New("corrupt frozen bytes")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := storeForSingleRun(test.run)
			store.inspectFn = func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
				return db.AgentWorkflowRunReconciliationView{Run: test.run, SelectedBuildExists: test.buildExists, SelectedBuildStatus: test.buildStatus}, true, nil
			}
			verifier := &provenanceVerifierFake{verification: test.provenance, err: test.provenanceErr}
			reporter := &reconciliationReporterFake{}
			reconciler := mustReconcilerWithVerifier(t, store, now, 15*time.Minute, time.Minute, reporter, verifier)

			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if test.wantDeferred {
				if len(store.finalizations) != 0 || reporter.rowCount(metric.WorkflowRunReconcilerRowDeferred) != 1 {
					t.Fatalf("deferred row finalized=%d metrics=%+v", len(store.finalizations), reporter.rows)
				}
				return
			}
			finalization := onlyFinalization(t, store)
			if finalization.TerminalStatus != *test.wantTerminal {
				t.Fatalf("terminal status = %s, want %s", finalization.TerminalStatus, *test.wantTerminal)
			}
			if test.wantMessage != "" && !containsFold(finalization.ErrorMessage, test.wantMessage) {
				t.Fatalf("message = %q, want %q", finalization.ErrorMessage, test.wantMessage)
			}
			if test.wantOutputs != (len(finalization.ExpectedOutputs) > 0) {
				t.Fatalf("outputs = %+v, wantOutputs %t", finalization.ExpectedOutputs, test.wantOutputs)
			}
			if !reflect.DeepEqual(finalization.ExpectedExecutionStatus, test.run.ExecutionStatus) {
				t.Fatalf("expected execution status = %v, want %v", finalization.ExpectedExecutionStatus, test.run.ExecutionStatus)
			}
			if !reflect.DeepEqual(finalization.ExpectedActualPlanHash, test.run.ActualPlanHash) {
				t.Fatalf("expected plan hash = %v, want %v", finalization.ExpectedActualPlanHash, test.run.ActualPlanHash)
			}
		})
	}
}

func TestReconcilerCompletesDurableWaitIntentsBeforeInspectingRunningOutcome(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	run := runningRun(1, now, nil, true)
	run.TeamID = 7
	run.TeamName = "main"
	store := storeForSingleRun(run)
	store.inspectFn = func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
		return db.AgentWorkflowRunReconciliationView{
			Run: run, SelectedBuildExists: true, SelectedBuildStatus: db.BuildStatusStarted,
		}, true, nil
	}
	completer := &waitResolutionCompleterFake{}
	reporter := &reconciliationReporterFake{}
	reconciler := mustReconciler(
		t,
		store,
		now,
		15*time.Minute,
		time.Minute,
		reporter,
		nil,
		WithWaitResolutionCompleter(completer),
	)
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(completer.calls, []waitResolutionCall{{
		teamID: 7, teamName: "main", runID: run.ID,
	}}) {
		t.Fatalf("completion calls = %+v", completer.calls)
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowDeferred) != 1 {
		t.Fatalf("rows = %+v", reporter.rows)
	}

	completer.err = errors.New("snapshot storage unavailable")
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowFailed) != 1 {
		t.Fatalf("completion failure was not isolated for retry: %+v", reporter.rows)
	}
}

func TestReconcilerRepairsOpenWaitsForCancelingAndAbortedRuns(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, status := range []db.AgentWorkflowRunStatus{
		db.AgentWorkflowRunStatusCanceling,
		db.AgentWorkflowRunStatusAborted,
	} {
		t.Run(string(status), func(t *testing.T) {
			run := reconciliationRun(42, status, now)
			run.TeamID = 7
			run.TeamName = "main"
			store := storeForSingleRun(run)
			if status == db.AgentWorkflowRunStatusCanceling {
				store.inspectFn = func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
					return db.AgentWorkflowRunReconciliationView{
						Run: run, SelectedBuildExists: true, SelectedBuildStatus: db.BuildStatusStarted,
					}, true, nil
				}
			}
			waits := &reconciliationWaitCancelerFake{cancel: func(
				_ context.Context,
				teamID int,
				runID snapshot.WorkflowRunID,
				actor string,
				at time.Time,
			) (int, error) {
				if teamID != 7 || runID != run.ID || actor != "system:workflow-run-cancel" || !at.Equal(now) {
					t.Fatalf("CancelRun = (%d, %s, %q, %s)", teamID, runID, actor, at)
				}
				return 1, nil
			}}
			reporter := &reconciliationReporterFake{}
			reconciler := mustReconciler(
				t,
				store,
				now,
				15*time.Minute,
				time.Minute,
				reporter,
				nil,
				WithWaitCanceler(waits),
			)
			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := metric.WorkflowRunReconcilerRowAdvanced
			if status == db.AgentWorkflowRunStatusCanceling {
				want = metric.WorkflowRunReconcilerRowDeferred
			}
			if reporter.rowCount(want) != 1 {
				t.Fatalf("rows = %+v", reporter.rows)
			}
		})
	}
}

func TestReconcilerRetriesAbortedRunWaitCleanupAfterDependencyFailure(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	run := reconciliationRun(42, db.AgentWorkflowRunStatusAborted, now)
	run.TeamID = 7
	run.TeamName = "main"
	store := storeForSingleRun(run)
	calls := 0
	waits := &reconciliationWaitCancelerFake{cancel: func(
		context.Context,
		int,
		snapshot.WorkflowRunID,
		string,
		time.Time,
	) (int, error) {
		calls++
		if calls == 1 {
			return 0, errors.New("temporary database failure")
		}
		return 1, nil
	}}
	reporter := &reconciliationReporterFake{}
	reconciler := mustReconciler(
		t,
		store,
		now,
		15*time.Minute,
		time.Minute,
		reporter,
		nil,
		WithWaitCanceler(waits),
	)

	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowFailed) != 1 {
		t.Fatalf("first cleanup failure was not retained for retry: %+v", reporter.rows)
	}
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || reporter.rowCount(metric.WorkflowRunReconcilerRowAdvanced) != 1 {
		t.Fatalf("cleanup calls/rows = %d/%+v", calls, reporter.rows)
	}
}

func TestReconcilerCancelingStateMachine(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, execution := range []db.AgentWorkflowRunExecutionStatus{
		db.AgentWorkflowRunExecutionStatusSucceeded,
		db.AgentWorkflowRunExecutionStatusFailed,
		db.AgentWorkflowRunExecutionStatusErrored,
		db.AgentWorkflowRunExecutionStatusAborted,
	} {
		t.Run(string(execution)+" maps truthfully", func(t *testing.T) {
			run := runningRun(1, now, &execution, execution == db.AgentWorkflowRunExecutionStatusSucceeded)
			run.Status = db.AgentWorkflowRunStatusCanceling
			store := storeForSingleRun(run)
			store.inspectFn = func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
				return db.AgentWorkflowRunReconciliationView{Run: run}, true, nil
			}
			reconciler := mustReconcilerWithVerifier(t, store, now, 15*time.Minute, time.Minute, &reconciliationReporterFake{}, &provenanceVerifierFake{verification: validProvenanceVerification()})

			if err := reconciler.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := onlyFinalization(t, store).TerminalStatus; string(got) != string(execution) {
				t.Fatalf("terminal status = %s, want %s", got, execution)
			}
		})
	}

	t.Run("active selected build defers", func(t *testing.T) {
		run := completeAdmissionRun(1, now.Add(-time.Hour))
		run.Status = db.AgentWorkflowRunStatusCanceling
		store := storeForSingleRun(run)
		store.inspectFn = func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
			return db.AgentWorkflowRunReconciliationView{Run: run, SelectedBuildExists: true, SelectedBuildStatus: db.BuildStatusStarted}, true, nil
		}
		reporter := &reconciliationReporterFake{}
		reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil)
		if err := reconciler.Run(context.Background()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(store.finalizations) != 0 || reporter.rowCount(metric.WorkflowRunReconcilerRowDeferred) != 1 {
			t.Fatalf("active cancellation was not deferred")
		}
	})

	t.Run("missing selected build defers then errors at timeout", func(t *testing.T) {
		for _, test := range []struct {
			name         string
			updatedAt    time.Time
			wantTerminal bool
		}{{"fresh", now.Add(-14 * time.Minute), false}, {"timed out", now.Add(-15 * time.Minute), true}} {
			t.Run(test.name, func(t *testing.T) {
				run := reconciliationRun(1, db.AgentWorkflowRunStatusCanceling, test.updatedAt)
				store := storeForSingleRun(run)
				reporter := &reconciliationReporterFake{}
				reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil)
				if err := reconciler.Run(context.Background()); err != nil {
					t.Fatalf("Run: %v", err)
				}
				if test.wantTerminal {
					finalization := onlyFinalization(t, store)
					if finalization.TerminalStatus != db.AgentWorkflowRunStatusErrored || finalization.ErrorMessage != "cancellation incomplete" {
						t.Fatalf("finalization = %+v", finalization)
					}
				} else if len(store.finalizations) != 0 || reporter.rowCount(metric.WorkflowRunReconcilerRowDeferred) != 1 {
					t.Fatal("fresh missing cancellation was not deferred")
				}
			})
		}
	})
}

func TestReconcilerContinuesAfterPoisonRowAndTreatsFalseCASAsBenign(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	first := reconciliationRun(1, db.AgentWorkflowRunStatusRunning, now)
	second := runningRun(2, now, executionPointer(db.AgentWorkflowRunExecutionStatusFailed), true)
	store := &reconciliationStoreFake{
		claimFn: func(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error) {
			return []snapshot.WorkflowRunID{first.ID, second.ID}, nil
		},
		inspectFn: func(_ context.Context, id snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
			if id == first.ID {
				return db.AgentWorkflowRunReconciliationView{}, false, errors.New("poison row detail")
			}
			return db.AgentWorkflowRunReconciliationView{Run: second}, true, nil
		},
		finalizeFn: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		},
	}
	reporter := &reconciliationReporterFake{}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, reporter, nil)

	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatalf("per-row errors must not escape Run: %v", err)
	}
	if len(store.inspectIDs) != 2 || len(store.finalizations) != 1 {
		t.Fatalf("inspect=%v finalizations=%d, want poison isolation", store.inspectIDs, len(store.finalizations))
	}
	if reporter.rowCount(metric.WorkflowRunReconcilerRowFailed) != 1 || reporter.rowCount(metric.WorkflowRunReconcilerRowAdvanced) != 1 {
		t.Fatalf("rows = %+v", reporter.rows)
	}
	reporter.requirePasses(t, metric.WorkflowRunReconcilerPassDegraded)
}

func TestReconcilerStopsPromptlyWhenContextIsCanceledBetweenRows(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	store := &reconciliationStoreFake{
		claimFn: func(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error) {
			return []snapshot.WorkflowRunID{1, 2}, nil
		},
		inspectFn: func(_ context.Context, id snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
			cancel()
			return db.AgentWorkflowRunReconciliationView{}, false, context.Canceled
		},
	}
	reconciler := mustReconciler(t, store, now, 15*time.Minute, time.Minute, &reconciliationReporterFake{}, nil)

	if err := reconciler.Run(ctx); err != nil {
		t.Fatalf("row cancellation must stop without becoming a processing error: %v", err)
	}
	if !reflect.DeepEqual(store.inspectIDs, []snapshot.WorkflowRunID{1}) {
		t.Fatalf("inspected IDs = %v, want prompt stop after first", store.inspectIDs)
	}
}

func TestFrozenProvenanceVerifierReconstructsAndClassifiesStoredEvidence(t *testing.T) {
	run := frozenProvenanceRun(t)
	verification, err := (frozenProvenanceVerifier{}).Verify(run)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verification.ContractMismatch || len(verification.ExpectedOutputs) != 1 {
		t.Fatalf("verification = %+v", verification)
	}
	output := verification.ExpectedOutputs[0]
	if output.Port != "review" || output.WorkflowRunID != run.ID || output.WorkflowDefinitionID != run.WorkflowDefinitionID ||
		output.Type != snapshot.TypeRef("review/v1") || !output.Optional || len(output.Producers) != 1 ||
		output.Producers[0] != (db.AgentWorkflowRunExpectedProducer{
			PlanID: "agent-plan", StepKind: "agent", StepName: "review", LocalOutputPort: "finding",
		}) {
		t.Fatalf("output = %+v", output)
	}

	t.Run("hash mismatch is corruption", func(t *testing.T) {
		corrupt := run
		wrong := "wrong-hash"
		corrupt.ActualPlanHash = &wrong
		if _, err := (frozenProvenanceVerifier{}).Verify(corrupt); err == nil {
			t.Fatal("expected plan hash corruption")
		}
	})

	t.Run("dependency mismatch is corruption", func(t *testing.T) {
		corrupt := run
		corrupt.ResolvedDependencies = json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":["unexpected"]}`)
		if _, err := (frozenProvenanceVerifier{}).Verify(corrupt); err == nil {
			t.Fatal("expected dependency corruption")
		}
	})

	t.Run("plan versus concrete config disagreement is a contract failure", func(t *testing.T) {
		mismatch := run
		config := frozenConcreteConfig(run.ID, run.WorkflowDefinitionID, "other/v1")
		mismatch.ConcreteConfig = mustCanonicalJSON(t, config)
		verification, err := (frozenProvenanceVerifier{}).Verify(mismatch)
		if err != nil {
			t.Fatalf("Verify mismatch: %v", err)
		}
		if !verification.ContractMismatch {
			t.Fatal("contract mismatch was not classified separately")
		}
	})
}

type reconciliationStoreFake struct {
	claimFn    func(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error)
	inspectFn  func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error)
	advanceFn  func(context.Context, snapshot.WorkflowRunID) (bool, error)
	finalizeFn func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error)

	inspectIDs    []snapshot.WorkflowRunID
	advanceIDs    []snapshot.WorkflowRunID
	finalizations []db.AgentWorkflowRunFinalization
}

func (fake *reconciliationStoreFake) ClaimForReconciliation(ctx context.Context, now time.Time, delay time.Duration, limit int) ([]snapshot.WorkflowRunID, error) {
	if fake.claimFn == nil {
		return nil, nil
	}
	return fake.claimFn(ctx, now, delay, limit)
}

func (fake *reconciliationStoreFake) InspectForReconciliation(ctx context.Context, id snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
	fake.inspectIDs = append(fake.inspectIDs, id)
	if fake.inspectFn == nil {
		return db.AgentWorkflowRunReconciliationView{}, false, fmt.Errorf("unexpected InspectForReconciliation(%d)", id)
	}
	return fake.inspectFn(ctx, id)
}

func (fake *reconciliationStoreFake) AdvanceAdmission(ctx context.Context, id snapshot.WorkflowRunID) (bool, error) {
	fake.advanceIDs = append(fake.advanceIDs, id)
	if fake.advanceFn == nil {
		return true, nil
	}
	return fake.advanceFn(ctx, id)
}

func (fake *reconciliationStoreFake) Finalize(ctx context.Context, finalization db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
	fake.finalizations = append(fake.finalizations, finalization)
	if fake.finalizeFn == nil {
		return db.AgentWorkflowRunFinalizationResult{Status: finalization.TerminalStatus, ErrorMessage: finalization.ErrorMessage}, true, nil
	}
	return fake.finalizeFn(ctx, finalization)
}

type reconciliationReporterFake struct {
	passes []metric.WorkflowRunReconcilerPassResult
	rows   []metric.WorkflowRunReconcilerRowResult
}

func (fake *reconciliationReporterFake) RecordPass(_ context.Context, result metric.WorkflowRunReconcilerPassResult, _ time.Time) {
	fake.passes = append(fake.passes, result)
}

func (fake *reconciliationReporterFake) RecordRow(_ context.Context, result metric.WorkflowRunReconcilerRowResult) {
	fake.rows = append(fake.rows, result)
}

func (fake *reconciliationReporterFake) rowCount(result metric.WorkflowRunReconcilerRowResult) int {
	count := 0
	for _, actual := range fake.rows {
		if actual == result {
			count++
		}
	}
	return count
}

func (fake *reconciliationReporterFake) requirePasses(t *testing.T, expected ...metric.WorkflowRunReconcilerPassResult) {
	t.Helper()
	if !reflect.DeepEqual(fake.passes, expected) {
		t.Fatalf("pass metrics = %v, want %v", fake.passes, expected)
	}
}

type provenanceVerifierFake struct {
	verification provenanceVerification
	err          error
	calls        []snapshot.WorkflowRunID
}

type waitResolutionCall struct {
	teamID   int
	teamName string
	runID    snapshot.WorkflowRunID
}

type waitResolutionCompleterFake struct {
	calls []waitResolutionCall
	err   error
}

type reconciliationWaitCancelerFake struct {
	cancel func(context.Context, int, snapshot.WorkflowRunID, string, time.Time) (int, error)
}

func (fake *reconciliationWaitCancelerFake) CancelRun(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	actor string,
	now time.Time,
) (int, error) {
	return fake.cancel(ctx, teamID, runID, actor, now)
}

func (fake *waitResolutionCompleterFake) ReconcilePending(
	_ context.Context,
	teamID int,
	teamName string,
	runID snapshot.WorkflowRunID,
) error {
	fake.calls = append(fake.calls, waitResolutionCall{teamID: teamID, teamName: teamName, runID: runID})
	return fake.err
}

func (fake *provenanceVerifierFake) Verify(run db.AgentWorkflowRun) (provenanceVerification, error) {
	fake.calls = append(fake.calls, run.ID)
	return fake.verification, fake.err
}

func mustReconciler(
	t *testing.T,
	store ReconciliationStore,
	now time.Time,
	timeout,
	delay time.Duration,
	reporter reconciliationReporter,
	clock func() time.Time,
	extra ...ReconcilerOption,
) *Reconciler {
	t.Helper()
	options := []ReconcilerOption{withReconciliationReporter(reporter)}
	if clock == nil {
		clock = func() time.Time { return now }
	}
	options = append(options, WithReconcilerClock(clock))
	options = append(options, extra...)
	reconciler, err := NewReconciler(store, lager.NewLogger("test"), timeout, delay, options...)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return reconciler
}

func mustReconcilerWithVerifier(t *testing.T, store ReconciliationStore, now time.Time, timeout, delay time.Duration, reporter reconciliationReporter, verifier provenanceVerifier) *Reconciler {
	t.Helper()
	reconciler := mustReconciler(t, store, now, timeout, delay, reporter, nil)
	reconciler.provenance = verifier
	return reconciler
}

func storeForSingleRun(run db.AgentWorkflowRun) *reconciliationStoreFake {
	store := &reconciliationStoreFake{}
	store.claimFn = func(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error) {
		return []snapshot.WorkflowRunID{run.ID}, nil
	}
	store.inspectFn = func(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error) {
		return db.AgentWorkflowRunReconciliationView{Run: run}, true, nil
	}
	return store
}

func reconciliationRun(id snapshot.WorkflowRunID, status db.AgentWorkflowRunStatus, updatedAt time.Time) db.AgentWorkflowRun {
	return db.AgentWorkflowRun{ID: id, Status: status, UpdatedAt: updatedAt}
}

func completeAdmissionRun(id snapshot.WorkflowRunID, updatedAt time.Time) db.AgentWorkflowRun {
	run := reconciliationRun(id, db.AgentWorkflowRunStatusAdmitting, updatedAt)
	pipelineRunID, templateID, instanceID := 11, 12, 13
	buildID := int64(14)
	hash := "concrete-hash"
	run.PipelineRunID = &pipelineRunID
	run.TemplatePipelineID = &templateID
	run.InstancePipelineID = &instanceID
	run.PlannedBuildID = &buildID
	run.ConcreteConfig = json.RawMessage(`{"jobs":[]}`)
	run.ConcreteConfigHash = &hash
	return run
}

func partialAdmissionRun(id snapshot.WorkflowRunID, updatedAt time.Time) db.AgentWorkflowRun {
	run := reconciliationRun(id, db.AgentWorkflowRunStatusAdmitting, updatedAt)
	pipelineRunID := 11
	run.PipelineRunID = &pipelineRunID
	return run
}

func runningRun(id snapshot.WorkflowRunID, updatedAt time.Time, execution *db.AgentWorkflowRunExecutionStatus, withPlan bool) db.AgentWorkflowRun {
	run := completeAdmissionRun(id, updatedAt)
	run.Status = db.AgentWorkflowRunStatusRunning
	run.ExecutionStatus = execution
	if withPlan {
		hash := "actual-plan-hash"
		run.ActualPlan = json.RawMessage(`{"id":"root"}`)
		run.ActualPlanHash = &hash
		run.ResolvedDependencies = json.RawMessage(`{"version":1,"resources":[],"images":[],"platform_resource_types":[]}`)
	}
	return run
}

func validProvenanceVerification() provenanceVerification {
	return provenanceVerification{ExpectedOutputs: []db.AgentWorkflowRunExpectedOutput{{
		Port: "review", Type: snapshot.TypeRef("review/v1"), WorkflowDefinitionID: 41, WorkflowRunID: 1,
		Producers: []db.AgentWorkflowRunExpectedProducer{{PlanID: "agent-plan", StepKind: "agent", StepName: "review", LocalOutputPort: "finding"}},
	}}}
}

func frozenProvenanceRun(t *testing.T) db.AgentWorkflowRun {
	t.Helper()
	runID := snapshot.WorkflowRunID(9)
	definitionID := 41
	declaration := atc.SnapshotOutputConfig{
		Type: snapshot.TypeRef("review/v1"), Retention: snapshot.RetentionClassWorkflow, Optional: true,
		WorkflowPort: "review", WorkflowDefinitionID: definitionID, WorkflowRunID: runID.String(),
	}
	plan := atc.Plan{ID: "agent-plan", Agent: &atc.AgentPlan{
		Name: "review", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": declaration},
	}}
	config := frozenConcreteConfig(runID, definitionID, "review/v1")
	concrete := mustCanonicalJSON(t, config)
	capture, err := workflowprovenance.FromPlan(workflowprovenance.Input{
		WorkflowRunID: runID, WorkflowDefinitionID: definitionID, ConcreteConfig: concrete,
	}, plan)
	if err != nil {
		t.Fatalf("FromPlan: %v", err)
	}
	planHash := capture.PlanHash
	return db.AgentWorkflowRun{
		ID: runID, WorkflowDefinitionID: definitionID, ConcreteConfig: concrete,
		ActualPlan: capture.CanonicalPlan, ActualPlanHash: &planHash, ResolvedDependencies: capture.ResolvedDependencies,
	}
}

func frozenConcreteConfig(runID snapshot.WorkflowRunID, definitionID int, outputType string) atc.Config {
	return atc.Config{Jobs: atc.JobConfigs{{
		Name: "run", PlanSequence: []atc.Step{{Config: &atc.AgentStep{
			Name: "review", SnapshotOutputs: map[string]atc.SnapshotOutputConfig{"finding": {
				Type: snapshot.TypeRef(outputType), Retention: snapshot.RetentionClassWorkflow,
				Optional: true, WorkflowPort: "review", WorkflowDefinitionID: definitionID, WorkflowRunID: runID.String(),
			}},
		}}},
	}}}
}

func mustCanonicalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := atc.CanonicalJSON(value)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	return payload
}

func onlyFinalization(t *testing.T, store *reconciliationStoreFake) db.AgentWorkflowRunFinalization {
	t.Helper()
	if len(store.finalizations) != 1 {
		t.Fatalf("finalizations = %d, want 1", len(store.finalizations))
	}
	return store.finalizations[0]
}

func statusPointer(value db.AgentWorkflowRunStatus) *db.AgentWorkflowRunStatus { return &value }

func executionPointer(value db.AgentWorkflowRunExecutionStatus) *db.AgentWorkflowRunExecutionStatus {
	return &value
}

func containsFold(value, substring string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(substring))
}
