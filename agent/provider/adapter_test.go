package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/provider"
)

func TestSafeAdapterBlocksAfterCompletedToolBatchBeforeNextModelRequest(t *testing.T) {
	release := make(chan struct{})
	boundaryCalled := make(chan provider.Boundary, 1)
	completedBatch := make(chan string, 1)
	nextRequest := make(chan struct{}, 1)
	control := boundaryControlFunc(func(_ context.Context, boundary provider.Boundary) error {
		if err := boundary.Validate(); err != nil {
			return err
		}
		boundaryCalled <- boundary.Clone()
		<-release
		return nil
	})
	adapter := &provider.FakeAdapter{
		IdentityValue:     provider.Identity{Name: "trusted", Version: "v1"},
		CapabilitiesValue: provider.Capabilities{SafeBoundary: true},
		StartFunc: func(_ context.Context, _ provider.StartRequest, got provider.BoundaryControl) (provider.RunningSession, error) {
			if got == nil {
				t.Fatal("safe adapter received nil boundary control")
			}
			return runningSessionFunc(func(ctx context.Context) (provider.Result, error) {
				// The completed batch precedes boundary evidence; the next model
				// request is after the blocking control call returns.
				completedBatch <- "tool-1"
				if err := got.AtSafeBoundary(ctx, provider.Boundary{SessionID: "session-1", TranscriptCursor: 12, CompletedToolCallIDs: []string{"tool-1"}}); err != nil {
					return provider.Result{}, err
				}
				nextRequest <- struct{}{}
				return provider.Result{SessionID: "session-1"}, nil
			}), nil
		},
	}
	if err := adapter.Identity().Validate(); err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Start(context.Background(), provider.StartRequest{}, control)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, waitErr := session.Wait(context.Background()); result <- waitErr }()
	select {
	case boundary := <-boundaryCalled:
		if boundary.CompletedToolCallIDs[0] != "tool-1" {
			t.Fatalf("boundary = %#v", boundary)
		}
	case <-time.After(time.Second):
		t.Fatal("adapter did not report completed tool batch")
	}
	if got := <-completedBatch; got != "tool-1" {
		t.Fatalf("completed batch = %q", got)
	}
	select {
	case <-nextRequest:
		t.Fatal("adapter issued the next model request before boundary control returned")
	default:
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

type boundaryControlFunc func(context.Context, provider.Boundary) error

func (fn boundaryControlFunc) AtSafeBoundary(ctx context.Context, boundary provider.Boundary) error {
	return fn(ctx, boundary)
}

type runningSessionFunc func(context.Context) (provider.Result, error)

func (fn runningSessionFunc) Wait(ctx context.Context) (provider.Result, error) { return fn(ctx) }
