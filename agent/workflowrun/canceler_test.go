package workflowrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

type waitCancelerStub struct {
	cancel func(context.Context, int, snapshot.WorkflowRunID, string, time.Time) (int, error)
}

func (stub *waitCancelerStub) CancelRun(ctx context.Context, teamID int, runID snapshot.WorkflowRunID, actor string, now time.Time) (int, error) {
	return stub.cancel(ctx, teamID, runID, actor, now)
}

type cancellationStoreStub struct {
	get              func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	transition       func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error)
	finalize         func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error)
	validateSelected func(context.Context, int, snapshot.WorkflowRunID, int64) (bool, error)
}

func (s *cancellationStoreStub) Get(ctx context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return s.get(ctx, teamID, id)
}

func (s *cancellationStoreStub) Transition(ctx context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
	return s.transition(ctx, id, from, to, message)
}

func (s *cancellationStoreStub) Finalize(ctx context.Context, request db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
	return s.finalize(ctx, request)
}

func (s *cancellationStoreStub) ValidateCancellationTarget(ctx context.Context, teamID int, id snapshot.WorkflowRunID, buildID int64) (bool, error) {
	if s.validateSelected == nil {
		return true, nil
	}
	return s.validateSelected(ctx, teamID, id, buildID)
}

func TestCancelerImmediatelyFinalizesCancellationWonBeforeExecutionAllocation(t *testing.T) {
	ctx := context.Background()
	runID := snapshot.WorkflowRunID(9007199254740993)
	admitting := cancellationRun(runID, 7, db.AgentWorkflowRunStatusAdmitting)
	canceling := cancellationRun(runID, 7, db.AgentWorkflowRunStatusCanceling)
	aborted := cancellationRun(runID, 7, db.AgentWorkflowRunStatusAborted)
	executionStatus := db.AgentWorkflowRunExecutionStatusAborted
	aborted.ExecutionStatus = &executionStatus

	var order []string
	gets := []db.AgentWorkflowRun{admitting, canceling, aborted}
	store := &cancellationStoreStub{
		get: func(got context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			order = append(order, "get")
			if got != ctx || teamID != 7 || id != runID {
				t.Fatalf("Get = (%v, %d, %s)", got, teamID, id.String())
			}
			run := gets[0]
			gets = gets[1:]
			return run, true, nil
		},
		transition: func(got context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			order = append(order, "transition")
			if got != ctx || id != runID || from != db.AgentWorkflowRunStatusAdmitting || to != db.AgentWorkflowRunStatusCanceling || message != "" {
				t.Fatalf("Transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return true, nil
		},
		finalize: func(got context.Context, request db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			order = append(order, "finalize")
			if got != ctx || request.WorkflowRunID != runID ||
				request.ExpectedStatus != db.AgentWorkflowRunStatusCanceling ||
				request.ExpectedExecutionStatus != nil || request.ExpectedActualPlanHash != nil ||
				request.TerminalStatus != db.AgentWorkflowRunStatusAborted || request.ErrorMessage != "" ||
				len(request.ExpectedOutputs) != 0 {
				t.Fatalf("Finalize = %+v", request)
			}
			return db.AgentWorkflowRunFinalizationResult{Status: db.AgentWorkflowRunStatusAborted}, true, nil
		},
	}
	canceler, err := NewCanceler(store, new(dbfakes.FakeBuildFactory))
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := canceler.Cancel(ctx, 7, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.Status != db.AgentWorkflowRunStatusAborted {
		t.Fatalf("Cancel = (%+v, %t)", got, found)
	}
	if want := []string{"get", "transition", "get", "finalize", "get"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
}

func TestCancelerDurablyCancelsOpenWaitsAfterRunCancellationWins(t *testing.T) {
	runID := snapshot.WorkflowRunID(9007199254740993)
	aborted := cancellationRun(runID, 7, db.AgentWorkflowRunStatusAborted)
	store := &cancellationStoreStub{
		get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return aborted, true, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			t.Fatal("terminal replay must not transition")
			return false, nil
		},
		finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			t.Fatal("terminal replay must not finalize")
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		},
	}
	called := 0
	waits := &waitCancelerStub{cancel: func(_ context.Context, teamID int, gotRunID snapshot.WorkflowRunID, actor string, now time.Time) (int, error) {
		called++
		if teamID != 7 || gotRunID != runID || actor != "system:workflow-run-cancel" || now.IsZero() {
			t.Fatalf("CancelRun = (%d, %s, %q, %s)", teamID, gotRunID.String(), actor, now)
		}
		return 2, nil
	}}
	canceler, err := NewCancelerWithWaits(store, new(dbfakes.FakeBuildFactory), waits)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := canceler.Cancel(context.Background(), 7, runID)
	if err != nil || !found || got.Status != db.AgentWorkflowRunStatusAborted || called != 1 {
		t.Fatalf("Cancel = (%+v, %t, %v), wait calls %d", got, found, err, called)
	}

	waits.cancel = func(context.Context, int, snapshot.WorkflowRunID, string, time.Time) (int, error) {
		return 0, errors.New("postgres password")
	}
	_, found, err = canceler.Cancel(context.Background(), 7, runID)
	if !found || !errors.Is(err, ErrCancelFailure) || strings.Contains(err.Error(), "password") {
		t.Fatalf("bounded wait cancellation = (found %t, err %v)", found, err)
	}
}

func TestCancelerAbortsOnlyExactSelectedBuild(t *testing.T) {
	ctx := context.Background()
	runID := snapshot.WorkflowRunID(71)
	running := cancellationRun(runID, 9, db.AgentWorkflowRunStatusRunning)
	buildID := int64(313)
	running.PlannedBuildID = &buildID
	canceling := running
	canceling.Status = db.AgentWorkflowRunStatusCanceling

	gets := []db.AgentWorkflowRun{running, canceling, canceling}
	store := &cancellationStoreStub{
		get: func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			if teamID != 9 || id != runID {
				t.Fatalf("Get scope = (%d, %s)", teamID, id.String())
			}
			run := gets[0]
			gets = gets[1:]
			return run, true, nil
		},
		transition: func(_ context.Context, id snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, message string) (bool, error) {
			if id != runID || from != db.AgentWorkflowRunStatusRunning || to != db.AgentWorkflowRunStatusCanceling || message != "" {
				t.Fatalf("Transition = (%s, %s, %s, %q)", id.String(), from, to, message)
			}
			return true, nil
		},
		finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			t.Fatal("selected execution must be finalized from build evidence")
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		},
		validateSelected: func(got context.Context, teamID int, id snapshot.WorkflowRunID, gotBuildID int64) (bool, error) {
			if got != ctx || teamID != 9 || id != runID || gotBuildID != buildID {
				t.Fatalf("ValidateCancellationTarget = (%d, %s, %d)", teamID, id.String(), gotBuildID)
			}
			return true, nil
		},
	}
	build := new(dbfakes.FakeBuildForAPI)
	build.IDReturns(int(buildID))
	build.TeamIDReturns(9)
	builds := new(dbfakes.FakeBuildFactory)
	builds.BuildForAPIReturns(build, true, nil)
	canceler, err := NewCanceler(store, builds)
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := canceler.Cancel(ctx, 9, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.Status != db.AgentWorkflowRunStatusCanceling {
		t.Fatalf("Cancel = (%+v, %t)", got, found)
	}
	if builds.BuildForAPICallCount() != 1 || builds.BuildForAPIArgsForCall(0) != int(buildID) {
		t.Fatalf("BuildForAPI calls = %d", builds.BuildForAPICallCount())
	}
	if build.MarkAsAbortedCallCount() != 1 {
		t.Fatalf("MarkAsAborted calls = %d", build.MarkAsAbortedCallCount())
	}
}

func TestCancelerRejectsSelectedBuildWithoutDurableInstanceAndJobLinkage(t *testing.T) {
	runID := snapshot.WorkflowRunID(72)
	run := cancellationRun(runID, 9, db.AgentWorkflowRunStatusCanceling)
	buildID := int64(314)
	run.PlannedBuildID = &buildID
	store := &cancellationStoreStub{
		get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return run, true, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			t.Fatal("canceling replay must not transition")
			return false, nil
		},
		finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			t.Fatal("selected execution must be finalized from build evidence")
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		},
		validateSelected: func(_ context.Context, teamID int, id snapshot.WorkflowRunID, gotBuildID int64) (bool, error) {
			if teamID != 9 || id != runID || gotBuildID != buildID {
				t.Fatalf("ValidateCancellationTarget = (%d, %s, %d)", teamID, id.String(), gotBuildID)
			}
			return false, nil
		},
	}
	build := new(dbfakes.FakeBuildForAPI)
	build.IDReturns(int(buildID))
	build.TeamIDReturns(9)
	builds := new(dbfakes.FakeBuildFactory)
	builds.BuildForAPIReturns(build, true, nil)
	canceler, err := NewCanceler(store, builds)
	if err != nil {
		t.Fatal(err)
	}

	_, found, err := canceler.Cancel(context.Background(), 9, runID)
	if !found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() {
		t.Fatalf("Cancel error = %v, found = %t", err, found)
	}
	if build.MarkAsAbortedCallCount() != 0 {
		t.Fatal("build without durable execution linkage was aborted")
	}
}

func TestCancelerRetriesCASAgainstAdmissionAdvance(t *testing.T) {
	runID := snapshot.WorkflowRunID(73)
	admitting := cancellationRun(runID, 2, db.AgentWorkflowRunStatusAdmitting)
	running := cancellationRun(runID, 2, db.AgentWorkflowRunStatusRunning)
	canceling := cancellationRun(runID, 2, db.AgentWorkflowRunStatusCanceling)
	aborted := cancellationRun(runID, 2, db.AgentWorkflowRunStatusAborted)
	gets := []db.AgentWorkflowRun{admitting, running, canceling, aborted}
	var transitions [][2]db.AgentWorkflowRunStatus
	store := &cancellationStoreStub{
		get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			run := gets[0]
			gets = gets[1:]
			return run, true, nil
		},
		transition: func(_ context.Context, _ snapshot.WorkflowRunID, from, to db.AgentWorkflowRunStatus, _ string) (bool, error) {
			transitions = append(transitions, [2]db.AgentWorkflowRunStatus{from, to})
			return from == db.AgentWorkflowRunStatusRunning, nil
		},
		finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			return db.AgentWorkflowRunFinalizationResult{Status: db.AgentWorkflowRunStatusAborted}, true, nil
		},
	}
	canceler, err := NewCanceler(store, new(dbfakes.FakeBuildFactory))
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := canceler.Cancel(context.Background(), 2, runID)
	if err != nil || !found || got.Status != db.AgentWorkflowRunStatusAborted {
		t.Fatalf("Cancel = (%+v, %t, %v)", got, found, err)
	}
	want := [][2]db.AgentWorkflowRunStatus{
		{db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusCanceling},
		{db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusCanceling},
	}
	if !reflect.DeepEqual(transitions, want) {
		t.Fatalf("transitions = %#v, want %#v", transitions, want)
	}
}

func TestCancelerReplaysCancelingAndAbortedIdempotently(t *testing.T) {
	for _, status := range []db.AgentWorkflowRunStatus{db.AgentWorkflowRunStatusCanceling, db.AgentWorkflowRunStatusAborted} {
		t.Run(string(status), func(t *testing.T) {
			run := cancellationRun(81, 3, status)
			gets := 0
			store := &cancellationStoreStub{
				get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
					gets++
					return run, true, nil
				},
				transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
					t.Fatal("idempotent replay must not transition")
					return false, nil
				},
				finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
					if status == db.AgentWorkflowRunStatusAborted {
						t.Fatal("aborted replay must not finalize")
					}
					return db.AgentWorkflowRunFinalizationResult{Status: db.AgentWorkflowRunStatusAborted}, false, nil
				},
			}
			if status == db.AgentWorkflowRunStatusCanceling {
				// The locked finalizer lost an idempotent race; the next scoped
				// reload observes the terminal winner.
				store.get = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
					gets++
					if gets == 1 {
						return run, true, nil
					}
					terminal := run
					terminal.Status = db.AgentWorkflowRunStatusAborted
					return terminal, true, nil
				}
			}
			canceler, err := NewCanceler(store, new(dbfakes.FakeBuildFactory))
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := canceler.Cancel(context.Background(), 3, 81)
			if err != nil || !found || got.Status != db.AgentWorkflowRunStatusAborted {
				t.Fatalf("Cancel = (%+v, %t, %v)", got, found, err)
			}
		})
	}
}

func TestCancelerConflictsWithNonAbortedTerminalHistory(t *testing.T) {
	for _, status := range []db.AgentWorkflowRunStatus{
		db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored,
	} {
		t.Run(string(status), func(t *testing.T) {
			store := &cancellationStoreStub{
				get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
					return cancellationRun(91, 4, status), true, nil
				},
				transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
					t.Fatal("terminal history must not transition")
					return false, nil
				},
				finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
					t.Fatal("terminal history must not finalize")
					return db.AgentWorkflowRunFinalizationResult{}, false, nil
				},
			}
			canceler, err := NewCanceler(store, new(dbfakes.FakeBuildFactory))
			if err != nil {
				t.Fatal(err)
			}
			_, found, err := canceler.Cancel(context.Background(), 4, 91)
			if !found || !errors.Is(err, ErrCancelConflict) || err.Error() != ErrCancelConflict.Error() {
				t.Fatalf("Cancel error = %v, found = %t", err, found)
			}
		})
	}
}

func TestCancelerBoundsDependencyErrorsAndRejectsWrongBuildIdentity(t *testing.T) {
	run := cancellationRun(101, 5, db.AgentWorkflowRunStatusCanceling)
	buildID := int64(611)
	run.PlannedBuildID = &buildID
	store := &cancellationStoreStub{
		get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return run, true, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			t.Fatal("canceling replay must not transition")
			return false, nil
		},
		finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			t.Fatal("selected build must not finalize directly")
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		},
	}
	wrong := new(dbfakes.FakeBuildForAPI)
	wrong.IDReturns(int(buildID) + 1)
	wrong.TeamIDReturns(5)
	builds := new(dbfakes.FakeBuildFactory)
	builds.BuildForAPIReturns(wrong, true, nil)
	canceler, err := NewCanceler(store, builds)
	if err != nil {
		t.Fatal(err)
	}

	_, found, err := canceler.Cancel(context.Background(), 5, 101)
	if !found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() {
		t.Fatalf("Cancel error = %v, found = %t", err, found)
	}
	if wrong.MarkAsAbortedCallCount() != 0 {
		t.Fatal("wrong build identity was aborted")
	}

	secret := errors.New("postgres password: swordfish")
	store.get = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{}, false, secret
	}
	_, found, err = canceler.Cancel(context.Background(), 5, 101)
	if found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() || errors.Is(err, secret) {
		t.Fatalf("bounded dependency error = %v, found = %t", err, found)
	}
}

func TestCancelerLeavesCancelingRunForReconciliationWhenSelectedBuildIsGone(t *testing.T) {
	run := cancellationRun(107, 5, db.AgentWorkflowRunStatusCanceling)
	buildID := int64(613)
	run.PlannedBuildID = &buildID
	store := &cancellationStoreStub{
		get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return run, true, nil
		},
		transition: func(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error) {
			t.Fatal("canceling replay must not transition")
			return false, nil
		},
		finalize: func(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error) {
			t.Fatal("a once-selected execution needs reconciliation evidence")
			return db.AgentWorkflowRunFinalizationResult{}, false, nil
		},
	}
	builds := new(dbfakes.FakeBuildFactory)
	builds.BuildForAPIReturns(nil, false, nil)
	canceler, err := NewCanceler(store, builds)
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := canceler.Cancel(context.Background(), 5, 107)
	if err != nil || !found || got.Status != db.AgentWorkflowRunStatusCanceling {
		t.Fatalf("Cancel = (%+v, %t, %v)", got, found, err)
	}
}

func TestCancelerReturnsTeamScopedAbsenceAndContextCancellation(t *testing.T) {
	store := &cancellationStoreStub{
		get: func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			if teamID != 17 || id != 111 {
				t.Fatalf("Get scope = (%d, %s)", teamID, id.String())
			}
			return db.AgentWorkflowRun{}, false, nil
		},
	}
	canceler, err := NewCanceler(store, new(dbfakes.FakeBuildFactory))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := canceler.Cancel(context.Background(), 17, 111); err != nil || found {
		t.Fatalf("Cancel absent = (found %t, %v)", found, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found, err := canceler.Cancel(canceled, 17, 111); !errors.Is(err, context.Canceled) || found {
		t.Fatalf("Cancel canceled = (found %t, %v)", found, err)
	}
}

func cancellationRun(id snapshot.WorkflowRunID, teamID int, status db.AgentWorkflowRunStatus) db.AgentWorkflowRun {
	return db.AgentWorkflowRun{ID: id, TeamID: teamID, TeamName: "main", Status: status}
}
