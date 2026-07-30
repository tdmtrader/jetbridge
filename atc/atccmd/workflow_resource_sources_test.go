package atccmd

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestWorkflowResourceSourceTickConvergesMonitorsBeforeLifecycle(t *testing.T) {
	var order []string
	runnable, err := newWorkflowResourceSourceRunnable(
		workflowResourceSourceReconcileFunc(func(context.Context) error {
			order = append(order, "monitor")
			return nil
		}),
		workflowResourceSourceReconcileFunc(func(context.Context) error {
			order = append(order, "lifecycle")
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("newWorkflowResourceSourceRunnable: %v", err)
	}
	if err := runnable.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"monitor", "lifecycle"}) {
		t.Fatalf("reconciliation order = %v", order)
	}
}

func TestWorkflowResourceSourceTickDoesNotActivateAfterMonitorFailure(t *testing.T) {
	want := errors.New("monitor convergence failed")
	lifecycleCalled := false
	runnable, err := newWorkflowResourceSourceRunnable(
		workflowResourceSourceReconcileFunc(func(context.Context) error {
			return want
		}),
		workflowResourceSourceReconcileFunc(func(context.Context) error {
			lifecycleCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("newWorkflowResourceSourceRunnable: %v", err)
	}
	if err := runnable.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
	if lifecycleCalled {
		t.Fatal("lifecycle activated a pipeline after monitor convergence failed")
	}
}

type workflowResourceSourceReconcileFunc func(context.Context) error

func (function workflowResourceSourceReconcileFunc) Reconcile(
	ctx context.Context,
) error {
	return function(ctx)
}
