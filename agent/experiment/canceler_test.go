package experiment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
)

type cancellationStore struct {
	work      []experiment.CancellationWork
	finalized []experiment.ID
}

func (store *cancellationStore) ClaimCancellations(context.Context, int) ([]experiment.CancellationWork, error) {
	return append([]experiment.CancellationWork(nil), store.work...), nil
}

func (store *cancellationStore) FinalizeCancellation(_ context.Context, teamID int, id experiment.ID) (bool, error) {
	for _, work := range store.work {
		if work.TeamID == teamID && work.ExperimentID == id {
			store.finalized = append(store.finalized, id)
			return true, nil
		}
	}
	return false, nil
}

type experimentRunCanceler struct {
	fail     map[snapshot.WorkflowRunID]error
	attempts []snapshot.WorkflowRunID
}

func (canceler *experimentRunCanceler) CancelRun(_ context.Context, _ int, id snapshot.WorkflowRunID) error {
	canceler.attempts = append(canceler.attempts, id)
	return canceler.fail[id]
}

func TestCancellationReconcilerIsRestartSafeAndDoesNotStarveLaterExperiments(t *testing.T) {
	store := &cancellationStore{work: []experiment.CancellationWork{
		{ExperimentID: 1, TeamID: 7, WorkflowRunIDs: []snapshot.WorkflowRunID{11, 12}},
		{ExperimentID: 2, TeamID: 8, WorkflowRunIDs: []snapshot.WorkflowRunID{21}},
	}}
	canceler := &experimentRunCanceler{fail: map[snapshot.WorkflowRunID]error{12: errors.New("abort unavailable")}}
	reconciler, err := experiment.NewCancellationReconciler(store, canceler, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Run(context.Background()); err == nil {
		t.Fatal("cancellation failure was hidden")
	}
	if got := canceler.attempts; len(got) != 3 || got[0] != 11 || got[1] != 12 || got[2] != 21 {
		t.Fatalf("attempts = %v", got)
	}
	if len(store.finalized) != 1 || store.finalized[0] != 2 {
		t.Fatalf("finalized despite partial failure = %v", store.finalized)
	}

	delete(canceler.fail, 12)
	store.finalized = nil
	canceler.attempts = nil
	if err := reconciler.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.finalized) != 2 {
		t.Fatalf("restart did not finalize both experiments: %v", store.finalized)
	}
}

func TestCancellationReconcilerValidatesDependenciesAndWork(t *testing.T) {
	store := &cancellationStore{work: []experiment.CancellationWork{{ExperimentID: 0, TeamID: 7}}}
	canceler := &experimentRunCanceler{}
	if _, err := experiment.NewCancellationReconciler(nil, canceler, 1); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := experiment.NewCancellationReconciler(store, nil, 1); err == nil {
		t.Fatal("nil canceler accepted")
	}
	if _, err := experiment.NewCancellationReconciler(store, canceler, 0); err == nil {
		t.Fatal("zero limit accepted")
	}
	reconciler, err := experiment.NewCancellationReconciler(store, canceler, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Run(context.Background()); err == nil {
		t.Fatal("corrupt cancellation work returned nil")
	}
	if len(store.finalized) != 0 {
		t.Fatalf("corrupt work finalized: %v", store.finalized)
	}
}
