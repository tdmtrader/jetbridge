package publisher_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
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

// commentRequest is the shared explicit-comment fixture: one semantic work-item
// operation, reused by both the idempotency spec and the action-suppression
// spec so they exercise the same identity.
func commentRequest() publisher.Request {
	return publisher.Request{
		Publisher:   publisher.WorkItemPublisher,
		Input:       snapshot.SnapshotRef{ID: 8, Type: "review/v1", Digest: digest("b")},
		Destination: "JIRA-42", Mode: publisher.ModeComment,
		Parameters: map[string]string{"body": "review complete"}, ApprovalPolicyVersion: "comment/v1",
		Authority: publicationAuthority(),
	}
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
	store := publisher.NewMemoryStore(time.Now)
	credentials := &credentialsStub{credential: publisher.Credential{Reference: "secret/jira"}}
	backend := &workItemBackendStub{result: publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://jira.example/JIRA-42"}}
	values := validSnapshotValueInspector()
	service, err := publisher.NewWorkItemService(store, credentials, values, backend, activeActions(), time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := commentRequest()
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
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{err: context.DeadlineExceeded}
	service, err := publisher.NewWorkItemService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/jira"}}, validSnapshotValueInspector(), backend, activeActions(), time.Minute, time.Minute)
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
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{
		result:     publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://work.example/9"},
		crashAfter: true,
	}
	service, err := publisher.NewWorkItemService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}}, validSnapshotValueInspector(), backend, activeActions(), time.Minute, time.Minute)
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

func TestWorkItemServiceRefusesRecoveredResultWithoutExternalIdentity(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{found: true, lookup: publisher.WorkItemResult{}}
	service, err := publisher.NewWorkItemService(
		store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}},
		validSnapshotValueInspector(), backend, activeActions(), time.Minute, time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := commentRequest()
	_, err = service.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "external identity") {
		t.Fatalf("empty recovered result = %v, want a named refusal", err)
	}
	// The refusal must never be classifiable terminal: terminalGatewayFailure
	// completes an operation as failed PERMANENTLY, and it asks Retryable and
	// nothing else. A refusal that answered false here would be one wrapping
	// change away from burning a publication that a later attempt could land.
	if !publisher.Retryable(err) {
		t.Fatalf("Retryable(%v) = false, want true — the refusal must not be terminal", err)
	}
	key, _ := request.OperationKey()
	pending, found, err := store.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("refusal must stay retryable: (%+v, %t, %v)", pending, found, err)
	}

	// The refusal is retryable, not terminal: once the backend answers with a
	// real identity the same operation completes.
	now = now.Add(2 * time.Minute)
	backend.lookup = publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://work.example/9"}
	completed, err := service.Execute(context.Background(), request)
	if err != nil || completed.Status != publisher.StatusSucceeded || completed.Result.ExternalID != "comment-9" {
		t.Fatalf("retry after a usable identity = (%+v, %v)", completed, err)
	}
}

// TestWorkItemServiceRefusesFreshResultWithoutExternalIdentity is the second
// application point. It is a distinct code path with a distinct hazard: the
// external write has already happened, so the refusal cannot mean "nothing
// landed" — only "the provider did not tell us what landed". Staying pending is
// still the safe answer, because the next attempt reaches Lookup first and so
// cannot repeat the write.
func TestWorkItemServiceRefusesFreshResultWithoutExternalIdentity(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &workItemBackendStub{result: publisher.WorkItemResult{URL: "https://work.example/9"}}
	service, err := publisher.NewWorkItemService(
		store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}},
		validSnapshotValueInspector(), backend, activeActions(), time.Minute, time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := commentRequest()
	_, err = service.Execute(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "external identity") {
		t.Fatalf("fresh result without an external id = %v, want a named refusal", err)
	}
	if !publisher.Retryable(err) {
		t.Fatalf("Retryable(%v) = false, want true — the refusal must not be terminal", err)
	}
	key, _ := request.OperationKey()
	pending, found, err := store.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("refusal must stay retryable: (%+v, %t, %v)", pending, found, err)
	}

	now = now.Add(2 * time.Minute)
	backend.result = publisher.WorkItemResult{ExternalID: "comment-9", URL: "https://work.example/9"}
	completed, err := service.Execute(context.Background(), request)
	if err != nil || completed.Status != publisher.StatusSucceeded || completed.Result.ExternalID != "comment-9" {
		t.Fatalf("retry after a usable identity = (%+v, %v)", completed, err)
	}
}

func TestWorkItemServiceRefusesResultsWithUnusableIdentities(t *testing.T) {
	for name, result := range map[string]publisher.WorkItemResult{
		"empty external id":     {ExternalID: ""},
		"blank external id":     {ExternalID: "   "},
		"untrimmed external id": {ExternalID: "comment-9 "},
		"control character":     {ExternalID: "comment\x009"},
		"plaintext url":         {ExternalID: "comment-9", URL: "http://work.example/9"},
		"url with credentials":  {ExternalID: "comment-9", URL: "https://user:pass@work.example/9"},
		"url without a host":    {ExternalID: "comment-9", URL: "https:///9"},
		// net/url rejects DEL and every byte below 0x20 outright.
		"unparseable url": {ExternalID: "comment-9", URL: "https://work.example/\x7f"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, recovered := range []bool{false, true} {
				store := publisher.NewMemoryStore(time.Now)
				backend := &workItemBackendStub{found: recovered, lookup: result, result: result}
				service, err := publisher.NewWorkItemService(
					store, &credentialsStub{credential: publisher.Credential{Reference: "secret/work"}},
					validSnapshotValueInspector(), backend, activeActions(), time.Minute, time.Minute,
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.Execute(context.Background(), commentRequest()); err == nil ||
					!strings.Contains(err.Error(), "external identity") {
					t.Fatalf("recovered=%t result %+v = %v, want a named refusal", recovered, result, err)
				}
			}
		})
	}
}
