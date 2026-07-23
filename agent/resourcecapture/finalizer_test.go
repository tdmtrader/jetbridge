package resourcecapture_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

type pendingCaptureOutputsStub struct {
	requests []db.ResourceCaptureOutput
	actor    string
	limit    int
	err      error
}

func (stub *pendingCaptureOutputsStub) ListPendingResourceCaptureOutputs(_ context.Context, actor string, limit int) ([]db.ResourceCaptureOutput, error) {
	stub.actor, stub.limit = actor, limit
	return append([]db.ResourceCaptureOutput(nil), stub.requests...), stub.err
}

type finalizerOutputStoreStub struct {
	requests []resourcecapture.OutputRequest
	errors   []error
}

func (stub *finalizerOutputStoreStub) Finalize(_ context.Context, request resourcecapture.OutputRequest) (snapshot.Snapshot, bool, error) {
	stub.requests = append(stub.requests, request)
	index := len(stub.requests) - 1
	if index < len(stub.errors) {
		return snapshot.Snapshot{}, false, stub.errors[index]
	}
	return snapshot.Snapshot{ID: 1}, true, nil
}

func TestFinalizerPinsEveryDurablePendingCaptureWithServerAuthority(t *testing.T) {
	pending := &pendingCaptureOutputsStub{requests: []db.ResourceCaptureOutput{
		{TeamID: 7, TeamName: "main", PipelineRunID: 51, OperationKey: "first", OutputPort: "snapshot", ExpectedType: "repository/v1"},
		{TeamID: 7, TeamName: "main", PipelineRunID: 52, OperationKey: "second", OutputPort: "snapshot", ExpectedType: "repository/v1"},
	}}
	outputs := &finalizerOutputStoreStub{}
	finalizer, err := resourcecapture.NewFinalizer(pending, outputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pending.actor != resourcecapture.FinalizerActor || pending.limit != resourcecapture.FinalizerBatchSize || len(outputs.requests) != 2 {
		t.Fatalf("pass = actor %q limit %d requests %#v", pending.actor, pending.limit, outputs.requests)
	}
	for _, request := range outputs.requests {
		if request.Actor != resourcecapture.FinalizerActor {
			t.Fatalf("finalizer actor = %q", request.Actor)
		}
	}
}

func TestFinalizerContinuesPastOneOutputFailureAndHonorsCancellation(t *testing.T) {
	pending := &pendingCaptureOutputsStub{requests: []db.ResourceCaptureOutput{{PipelineRunID: 1}, {PipelineRunID: 2}}}
	outputs := &finalizerOutputStoreStub{errors: []error{errors.New("first failed")}}
	finalizer, err := resourcecapture.NewFinalizer(pending, outputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Run(context.Background()); !errors.Is(err, resourcecapture.ErrFinalizerPass) {
		t.Fatalf("pass error = %v", err)
	}
	if len(outputs.requests) != 2 {
		t.Fatalf("one failure blocked later outputs: %#v", outputs.requests)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := finalizer.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pass error = %v", err)
	}
}
