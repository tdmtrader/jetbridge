package atccmd

import (
	"context"
	"errors"
	"testing"
)

func TestWorkflowResourceSourceTickReconcilesTheLifecycle(t *testing.T) {
	calls := 0
	runnable, err := newWorkflowResourceSourceRunnable(
		workflowResourceSourceReconcileFunc(func(context.Context) error {
			calls++
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("newWorkflowResourceSourceRunnable: %v", err)
	}
	if err := runnable.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lifecycle reconciliations = %d, want 1", calls)
	}
}

func TestWorkflowResourceSourceTickSurfacesLifecycleFailure(t *testing.T) {
	want := errors.New("lifecycle convergence failed")
	runnable, err := newWorkflowResourceSourceRunnable(
		workflowResourceSourceReconcileFunc(func(context.Context) error {
			return want
		}),
	)
	if err != nil {
		t.Fatalf("newWorkflowResourceSourceRunnable: %v", err)
	}
	if err := runnable.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
}

func TestWorkflowResourceSourceRunnableRequiresALifecycle(t *testing.T) {
	if _, err := newWorkflowResourceSourceRunnable(nil); err == nil {
		t.Fatal("newWorkflowResourceSourceRunnable accepted a missing lifecycle")
	}
}

type workflowResourceSourceReconcileFunc func(context.Context) error

func (function workflowResourceSourceReconcileFunc) Reconcile(
	ctx context.Context,
) error {
	return function(ctx)
}
