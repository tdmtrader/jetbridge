package publisher_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/publishertest"
	"github.com/concourse/concourse/agent/snapshot"
)

type workItemBackendStub struct {
	requests   []publisher.WorkItemOperation
	result     publisher.WorkItemResult
	lookup     publisher.WorkItemResult
	found      bool
	lookups    int
	crashAfter bool
	err        error
}

type snapshotValueInspectorStub struct {
	requests []publisher.Request
	value    publisher.SnapshotValue
	err      error
}

func (stub *snapshotValueInspectorStub) InspectValue(_ context.Context, request publisher.Request) (publisher.SnapshotValue, error) {
	stub.requests = append(stub.requests, request.Clone())
	return stub.value, stub.err
}

func validSnapshotValueInspector() *snapshotValueInspectorStub {
	return &snapshotValueInspectorStub{value: publisher.SnapshotValue{CanonicalArchivePath: "/canonical/input.tar"}}
}

func (stub *workItemBackendStub) Lookup(context.Context, publisher.Credential, string) (publisher.WorkItemResult, bool, error) {
	stub.lookups++
	return stub.lookup, stub.found, nil
}

func (stub *workItemBackendStub) Publish(_ context.Context, credential publisher.Credential, operation publisher.WorkItemOperation) (publisher.WorkItemResult, error) {
	stub.requests = append(stub.requests, operation)
	if stub.crashAfter {
		stub.lookup = stub.result
		stub.found = true
		stub.crashAfter = false
		return publisher.WorkItemResult{}, context.DeadlineExceeded
	}
	return stub.result, stub.err
}

func TestWorkItemServicePublishesExplicitCommentIdempotently(t *testing.T) {
	store := publishertest.NewMemoryStore(time.Now)
	credentials := &credentialsStub{credential: publisher.Credential{Reference: "secret/jira"}}
	backend := &workItemBackendStub{result: publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://jira.example/JIRA-42"}}
	values := validSnapshotValueInspector()
	service, err := publisher.NewWorkItemService(store, credentials, values, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := publisher.Request{
		Publisher:   publisher.WorkItemPublisher,
		Input:       snapshot.SnapshotRef{ID: 8, Type: "review/v1", Digest: digest("b")},
		Destination: "JIRA-42", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "review complete"}, ApprovalPolicyVersion: "comment/v1",
		Authority: publicationAuthority(),
	}
	publication, err := service.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || len(backend.requests) != 1 {
		t.Fatalf("Execute = (%+v, %v), requests=%+v", publication, err, backend.requests)
	}
	if backend.requests[0].Input.ID != 8 || backend.requests[0].CanonicalArchivePath != "/canonical/input.tar" ||
		backend.requests[0].Parameters["body"] != "review complete" || backend.requests[0].Authority != request.Authority {
		t.Fatalf("operation = %+v", backend.requests[0])
	}
	if _, err := service.Execute(context.Background(), request); err != nil || len(backend.requests) != 1 {
		t.Fatalf("replay err/requests = %v/%d", err, len(backend.requests))
	}
}

func TestWorkItemServiceSupportsStateModeAndLeavesExternalErrorsRetryable(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publishertest.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{err: context.DeadlineExceeded}
	service, err := publisher.NewWorkItemService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/jira"}}, validSnapshotValueInspector(), backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := publisher.Request{
		Publisher:   publisher.WorkItemPublisher,
		Input:       snapshot.SnapshotRef{ID: 8, Type: "review/v1", Digest: digest("b")},
		Destination: "JIRA-42", Mode: publisher.ModeState,
		Parameters: map[string]string{"state": "done"}, ApprovalPolicyVersion: "state/v1",
		Authority: publicationAuthority(),
	}
	if _, err := service.Execute(context.Background(), request); err == nil {
		t.Fatal("external error was hidden")
	}
	key, _ := request.OperationKey()
	pending, found, _ := store.Get(context.Background(), key)
	if !found || pending.Status != publisher.StatusPending {
		t.Fatalf("pending = %+v/%t", pending, found)
	}
	now = now.Add(2 * time.Minute)
	backend.err = nil
	backend.result = publisher.WorkItemResult{ExternalID: "transition-1"}
	completed, err := service.Execute(context.Background(), request)
	if err != nil || completed.Status != publisher.StatusSucceeded || completed.Attempt != 2 {
		t.Fatalf("retry = (%+v, %v)", completed, err)
	}
}

func TestWorkItemServiceReconcilesCrashAfterProviderSuccessWithoutRepeatingWrite(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publishertest.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{
		result:     publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://work.example/9"},
		crashAfter: true,
	}
	service, err := publisher.NewWorkItemService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}}, validSnapshotValueInspector(), backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := publisher.Request{
		Publisher:   publisher.WorkItemPublisher,
		Input:       snapshot.SnapshotRef{ID: 8, Type: "review/v1", Digest: digest("b")},
		Destination: "WORK-9", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "ready"}, ApprovalPolicyVersion: "comment/v1",
		Authority: publicationAuthority(),
	}
	if _, err := service.Execute(context.Background(), request); err == nil {
		t.Fatal("crash-after-success was hidden")
	}
	now = now.Add(2 * time.Minute)
	publication, err := service.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || publication.Result.ExternalID != "comment-9" {
		t.Fatalf("reconciled = (%+v, %v)", publication, err)
	}
	if len(backend.requests) != 1 || backend.lookups != 2 {
		t.Fatalf("writes/lookups = %d/%d, want 1/2", len(backend.requests), backend.lookups)
	}
}
