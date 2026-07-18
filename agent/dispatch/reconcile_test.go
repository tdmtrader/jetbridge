package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func intp(i int) *int { return &i }

// reconcileScaffold: a MemoryStore holding one RUNNING ticket pinned to
// workflow smoke/3 (matching smokeDefinition) and dispatched as run 100.
func reconcileScaffold(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, int) {
	t.Helper()
	deps, store, _, _ := dispatchDeps(t)
	id := queuedTicket(t, store, "smoke")
	v := 3
	if err := store.Update(id, tickets.Update{WorkflowVersion: &v}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(id, tickets.StateQueued, tickets.StateRunning,
		tickets.TransitionMeta{PipelineRunID: intp(100)}); err != nil {
		t.Fatal(err)
	}
	return deps, store, id
}

func completedRun(id int, status db.PipelineRunStatus) *dbfakes.FakePipelineRun {
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(id)
	run.StatusReturns(status)
	run.CompletedAtReturns(time.Unix(1, 0), true)
	return run
}

// fakeRunReader is a HAND fake over the local RunReader seam:
// db.PipelineRunFactory.GetRunByID (Task 5) has not landed, so there is
// no counterfeiter method to stub on the factory fake.
type fakeRunReader struct{ run *dbfakes.FakePipelineRun }

func (f *fakeRunReader) GetRunByID(id int) (db.PipelineRun, bool, error) {
	if f.run != nil && f.run.ID() == id {
		return f.run, true, nil
	}
	return nil, false, nil
}

func runReaderFor(run *dbfakes.FakePipelineRun) dispatch.RunReader {
	return &fakeRunReader{run: run}
}

// --- checkpoint-free branches (live today) --------------------------------

func TestReconcileSucceededRunSafetyNet(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunSucceeded)),
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("succeeded-but-still-running => needs_review safety net, got %s", got.State)
	}
}

func TestReconcileFailedRunNoCheckpointsNeedsReview(t *testing.T) {
	for _, status := range []db.PipelineRunStatus{
		db.PipelineRunFailed, db.PipelineRunErrored, db.PipelineRunAborted,
	} {
		deps, store, id := reconcileScaffold(t)
		d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
			RunReader: runReaderFor(completedRun(100, status)),
		})
		if err := d.Run(context.Background()); err != nil {
			t.Fatalf("[%s] Run: %v", status, err)
		}
		got, _, _ := store.Get(id)
		if got.State != tickets.StateNeedsReview {
			t.Errorf("[%s] checkpoint-free failure => needs_review (human triage), got %s", status, got.State)
		}
	}
}

func TestReconcileIncompleteRunUntouched(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	run := new(dbfakes.FakePipelineRun)
	run.IDReturns(100)
	run.StatusReturns(db.PipelineRunRunning)
	run.CompletedAtReturns(time.Time{}, false)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{RunReader: runReaderFor(run)})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("incomplete run must leave the ticket running, got %s", got.State)
	}
}

func TestReconcileMissingRunRowTriagesToNeedsReview(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(nil), // run row gone
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("vanished run treated as errored => checkpoint-free triage (needs_review), got %s", got.State)
	}
}

func TestReconcileNilRunReaderSkipsPass(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{}) // no RunReader
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateRunning {
		t.Errorf("nil RunReader must skip reconciliation, got %s", got.State)
	}
}

func TestReconcileStaleTransitionBenign(t *testing.T) {
	deps, _, _ := reconcileScaffold(t)
	// Harvest races us: simulate by making every Transition stale.
	deps.Tickets = staleOnTransition{Store: deps.Tickets}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("stale transition must be benign, got: %v", err)
	}
}

type staleOnTransition struct{ tickets.Store }

func (s staleOnTransition) Transition(int, tickets.State, tickets.State, tickets.TransitionMeta) error {
	return tickets.ErrStaleTransition
}

// --- checkpoint branches (dormant in prod; exercised via the seam) --------

type fakeQuestions struct {
	rows     []dispatch.CheckpointRow
	released []int
}

func (q *fakeQuestions) ListByRun(int) ([]dispatch.CheckpointRow, error) { return q.rows, nil }
func (q *fakeQuestions) Answer(id int, answer, by string) error {
	q.released = append(q.released, id)
	return nil
}

// checkpointWorkflows: smokeDefinition plus a checkpoint step, registered
// so the pinned smoke/3 lookup resolves the on_reject policy.
func checkpointWorkflows(onReject string) *fakeWorkflows {
	def := smokeDefinition()
	def.Config.Steps = append(def.Config.Steps, workflow.Step{Checkpoint: "plan-approval", OnReject: onReject})
	return &fakeWorkflows{byName: map[string]*workflow.Definition{"smoke": def}}
}

func TestReconcileRejectedSendBackRequeues(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "reject"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs, MaxAttempts: 3,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	// The SAME Run pass listed queued tickets BEFORE reconciling, so the
	// requeued ticket is picked up next pass — assert queued now.
	if got.State != tickets.StateQueued {
		t.Errorf("rejected send_back must requeue, got %s", got.State)
	}
	if got.AttemptCount != 1 {
		t.Errorf("running->queued bumps attempt_count (§2.1), got %d", got.AttemptCount)
	}
}

func TestReconcileSendBackOverAttemptCapErrors(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	// Reach the cap via the edge that owns attempt_count.
	for i := 0; i < 3; i++ {
		if err := store.Transition(id, tickets.StateRunning, tickets.StateQueued, tickets.TransitionMeta{}); err != nil {
			t.Fatal(err)
		}
		if err := store.Transition(id, tickets.StateQueued, tickets.StateRunning,
			tickets.TransitionMeta{PipelineRunID: intp(100)}); err != nil {
			t.Fatal(err)
		}
	}
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "reject"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs, MaxAttempts: 3,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateErrored {
		t.Errorf("over-cap send_back => running->errored (legal edge), got %s", got.State)
	}
	if got.ErrorDetail == "" {
		t.Error("cap trip must record error_detail")
	}
}

func TestReconcileRejectedFailNeedsReview(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("fail")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "reject"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("rejected fail checkpoint => needs_review, got %s", got.State)
	}
}

func TestReconcileUnansweredCheckpointErrorsAndReleases(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 4, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: false},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunAborted)),
		Questions: qs,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateErrored {
		t.Errorf("unanswered checkpoint on dead run => errored, got %s", got.State)
	}
	if len(qs.released) != 1 || qs.released[0] != 4 {
		t.Errorf("orphan rows must be released via Answer(id, \"\", \"dispatcher\"), got %v", qs.released)
	}
}

func TestReconcileAllApprovedFallsThroughToTriage(t *testing.T) {
	deps, store, id := reconcileScaffold(t)
	deps.Workflows = checkpointWorkflows("send_back")
	qs := &fakeQuestions{rows: []dispatch.CheckpointRow{
		{ID: 1, StepName: "checkpoint-plan-approval", AskedAt: 10, Answered: true, Answer: "approve"},
	}}
	d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
		RunReader: runReaderFor(completedRun(100, db.PipelineRunFailed)),
		Questions: qs,
	})
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _, _ := store.Get(id)
	if got.State != tickets.StateNeedsReview {
		t.Errorf("approved checkpoints + failed run => b.3 triage, got %s", got.State)
	}
}
