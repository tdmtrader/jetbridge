package workflowrun

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
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

type buildLookupStub struct {
	lookup func(int) (db.BuildForAPI, bool, error)
}

func (stub buildLookupStub) BuildForAPI(id int) (db.BuildForAPI, bool, error) {
	return stub.lookup(id)
}

type wrongIdentityBuild struct {
	db.BuildForAPI
	id int
}

func (build wrongIdentityBuild) ID() int { return build.id }

func buildLookupMustNotRun(t *testing.T) BuildLookup {
	t.Helper()
	return buildLookupStub{lookup: func(int) (db.BuildForAPI, bool, error) {
		t.Fatal("selected-build lookup must not run")
		return nil, false, nil
	}}
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
	canceler, err := NewCanceler(store, buildLookupMustNotRun(t))
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
	canceler, err := NewCancelerWithWaits(store, buildLookupMustNotRun(t), waits)
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
	fixture := useRealWorkflowRunDB(t)
	running, selected := createLinkedExecution(t, fixture)
	canceler, err := NewCanceler(fixture.Runs, fixture.Builds)
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := canceler.Cancel(ctx, fixture.Team.ID(), running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.ID != running.ID || got.TeamID != fixture.Team.ID() || got.Status != db.AgentWorkflowRunStatusCanceling {
		t.Fatalf("Cancel = (%+v, %t)", got, found)
	}
	var aborted bool
	if err := fixture.Conn.QueryRow(`SELECT aborted FROM builds WHERE id = $1`, selected.ID()).Scan(&aborted); err != nil {
		t.Fatal(err)
	}
	if !aborted {
		t.Fatal("persisted selected build was not aborted")
	}
}

func TestCancelerRejectsSelectedBuildWithoutDurableInstanceAndJobLinkage(t *testing.T) {
	ctx := context.Background()
	fixture := useRealWorkflowRunDB(t)
	durable, _ := createDurableRun(t, fixture)
	build, err := fixture.Team.CreateStartedBuild(atc.Plan{ID: "unlinked-cancellation-target"})
	if err != nil {
		t.Fatal(err)
	}
	buildID := int64(build.ID())
	result, err := fixture.Conn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(durable.ID), buildID)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("set planned build: affected %d, err %v", affected, err)
	}
	run, found, err := fixture.Runs.Get(ctx, fixture.Team.ID(), durable.ID)
	if err != nil || !found || run.PlannedBuildID == nil || *run.PlannedBuildID != buildID {
		t.Fatalf("reload unlinked run = (%+v, %t, %v)", run, found, err)
	}
	changed, err := fixture.Runs.Transition(
		ctx,
		durable.ID,
		db.AgentWorkflowRunStatusAdmitting,
		db.AgentWorkflowRunStatusCanceling,
		"",
	)
	if err != nil || !changed {
		t.Fatalf("transition unlinked run = (%t, %v)", changed, err)
	}
	run, found, err = fixture.Runs.Get(ctx, fixture.Team.ID(), durable.ID)
	if err != nil || !found || run.Status != db.AgentWorkflowRunStatusCanceling {
		t.Fatalf("reload canceling run = (%+v, %t, %v)", run, found, err)
	}
	linked, err := fixture.Runs.ValidateCancellationTarget(ctx, fixture.Team.ID(), durable.ID, buildID)
	if err != nil || linked {
		t.Fatalf("ValidateCancellationTarget = (%t, %v)", linked, err)
	}
	canceler, err := NewCanceler(fixture.Runs, fixture.Builds)
	if err != nil {
		t.Fatal(err)
	}

	_, found, err = canceler.Cancel(ctx, fixture.Team.ID(), durable.ID)
	var aborted bool
	if err := fixture.Conn.QueryRow(`SELECT aborted FROM builds WHERE id = $1`, build.ID()).Scan(&aborted); err != nil {
		t.Fatal(err)
	}
	if aborted {
		t.Fatal("build without durable execution linkage was aborted")
	}
	if !found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() {
		t.Fatalf("Cancel error = %v, found = %t", err, found)
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
	canceler, err := NewCanceler(store, buildLookupMustNotRun(t))
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
			canceler, err := NewCanceler(store, buildLookupMustNotRun(t))
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
			canceler, err := NewCanceler(store, buildLookupMustNotRun(t))
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
	ctx := context.Background()
	fixture := useRealWorkflowRunDB(t)
	running, selected := createLinkedExecution(t, fixture)
	wrong := wrongIdentityBuild{BuildForAPI: selected, id: selected.ID() + 1}
	builds := buildLookupStub{lookup: func(id int) (db.BuildForAPI, bool, error) {
		if id != selected.ID() {
			t.Fatalf("BuildForAPI id = %d, want %d", id, selected.ID())
		}
		return wrong, true, nil
	}}
	canceler, err := NewCanceler(fixture.Runs, builds)
	if err != nil {
		t.Fatal(err)
	}

	_, found, err := canceler.Cancel(ctx, fixture.Team.ID(), running.ID)
	if !found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() {
		t.Fatalf("Cancel error = %v, found = %t", err, found)
	}
	var aborted bool
	if err := fixture.Conn.QueryRow(`SELECT aborted FROM builds WHERE id = $1`, selected.ID()).Scan(&aborted); err != nil {
		t.Fatal(err)
	}
	if aborted {
		t.Fatal("wrong build identity was aborted")
	}

	lookupSecret := errors.New("build lookup password: swordfish")
	errorBuilds := buildLookupStub{lookup: func(id int) (db.BuildForAPI, bool, error) {
		if id != selected.ID() {
			t.Fatalf("BuildForAPI id = %d, want %d", id, selected.ID())
		}
		return nil, true, lookupSecret
	}}
	canceler, err = NewCanceler(fixture.Runs, errorBuilds)
	if err != nil {
		t.Fatal(err)
	}
	_, found, err = canceler.Cancel(ctx, fixture.Team.ID(), running.ID)
	if !found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() || errors.Is(err, lookupSecret) || strings.Contains(err.Error(), lookupSecret.Error()) {
		t.Fatalf("bounded build lookup error = %v, found = %t", err, found)
	}
	if err := fixture.Conn.QueryRow(`SELECT aborted FROM builds WHERE id = $1`, selected.ID()).Scan(&aborted); err != nil {
		t.Fatal(err)
	}
	if aborted {
		t.Fatal("selected build was aborted after lookup failure")
	}

	secret := errors.New("postgres password: swordfish")
	store := &cancellationStoreStub{
		get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, secret
		},
	}
	canceler, err = NewCanceler(store, buildLookupMustNotRun(t))
	if err != nil {
		t.Fatal(err)
	}
	_, found, err = canceler.Cancel(ctx, fixture.Team.ID(), running.ID)
	if found || !errors.Is(err, ErrCancelFailure) || err.Error() != ErrCancelFailure.Error() || errors.Is(err, secret) {
		t.Fatalf("bounded dependency error = %v, found = %t", err, found)
	}
}

func TestCancelerLeavesCancelingRunForReconciliationWhenSelectedBuildIsGone(t *testing.T) {
	ctx := context.Background()
	fixture := useRealWorkflowRunDB(t)
	durable, _ := createDurableRun(t, fixture)
	missingBuildID := int64(9007199254740991)
	if _, found, err := fixture.Builds.BuildForAPI(int(missingBuildID)); err != nil || found {
		t.Fatalf("missing build precondition = (found %t, %v)", found, err)
	}
	result, err := fixture.Conn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(durable.ID), missingBuildID)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("set missing planned build: affected %d, err %v", affected, err)
	}
	changed, err := fixture.Runs.Transition(
		ctx,
		durable.ID,
		db.AgentWorkflowRunStatusAdmitting,
		db.AgentWorkflowRunStatusCanceling,
		"",
	)
	if err != nil || !changed {
		t.Fatalf("transition missing-build run = (%t, %v)", changed, err)
	}
	run, found, err := fixture.Runs.Get(ctx, fixture.Team.ID(), durable.ID)
	if err != nil || !found || run.Status != db.AgentWorkflowRunStatusCanceling || run.PlannedBuildID == nil || *run.PlannedBuildID != missingBuildID {
		t.Fatalf("reload missing-build run = (%+v, %t, %v)", run, found, err)
	}
	canceler, err := NewCanceler(fixture.Runs, fixture.Builds)
	if err != nil {
		t.Fatal(err)
	}

	got, found, err := canceler.Cancel(ctx, fixture.Team.ID(), durable.ID)
	if err != nil || !found || got.ID != durable.ID || got.TeamID != fixture.Team.ID() || got.Status != db.AgentWorkflowRunStatusCanceling {
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
	canceler, err := NewCanceler(store, buildLookupMustNotRun(t))
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
