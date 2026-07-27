package dispatch_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/dispatch"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
)

func intp(i int) *int { return &i }

// reconcileScaffold holds one running ticket and only the dependency needed
// by reconciliation.
func reconcileScaffold(t *testing.T) (dispatch.Deps, *tickets.MemoryStore, int) {
	t.Helper()
	store := tickets.NewMemoryStore()
	deps := dispatch.Deps{Tickets: store}
	id := queuedTicket(t, store, "smoke")
	v := 7
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

func TestReconcileTerminalSubordinateRunNeedsReview(t *testing.T) {
	for _, status := range []db.PipelineRunStatus{
		db.PipelineRunFailed,
		db.PipelineRunErrored,
		db.PipelineRunAborted,
	} {
		t.Run(string(status), func(t *testing.T) {
			deps, store, id := reconcileScaffold(t)
			recorder := &transitionRecorder{Store: deps.Tickets}
			deps.Tickets = recorder
			d := dispatch.NewDispatcher(deps, dispatch.LoopConfig{
				RunReader: runReaderFor(completedRun(100, status)),
			})

			if err := d.Run(context.Background()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			got, _, _ := store.Get(id)
			if got.State != tickets.StateNeedsReview {
				t.Fatalf("terminal subordinate run => needs_review, got %s", got.State)
			}
			if len(recorder.transitions) != 1 {
				t.Fatalf("expected one transition, got %d", len(recorder.transitions))
			}
			transition := recorder.transitions[0]
			if transition.to != tickets.StateNeedsReview {
				t.Errorf("transition destination = %s, want needs_review", transition.to)
			}
			if transition.meta != (tickets.TransitionMeta{}) {
				t.Errorf("transition metadata = %#v, want empty", transition.meta)
			}
		})
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
	// Another writer races us: simulate by making every Transition stale.
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

type recordedTransition struct {
	to   tickets.State
	meta tickets.TransitionMeta
}

type transitionRecorder struct {
	tickets.Store
	transitions []recordedTransition
}

func (r *transitionRecorder) Transition(id int, from, to tickets.State, meta tickets.TransitionMeta) error {
	r.transitions = append(r.transitions, recordedTransition{to: to, meta: meta})
	return r.Store.Transition(id, from, to, meta)
}

func TestLoopConfigRemovesCheckpointAuthority(t *testing.T) {
	typ := reflect.TypeOf(dispatch.LoopConfig{})
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	if want := []string{"Mode", "RunReader", "MaxAttempts"}; !reflect.DeepEqual(fields, want) {
		t.Errorf("LoopConfig fields = %v, want %v", fields, want)
	}
}
