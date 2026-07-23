package publisher_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

type credentialsStub struct {
	requests   []publisher.Request
	credential publisher.Credential
	err        error
}

func (stub *credentialsStub) AuthorizeDestination(_ context.Context, request publisher.Request) (publisher.Credential, error) {
	stub.requests = append(stub.requests, request.Clone())
	return stub.credential, stub.err
}

type changeInspectorStub struct {
	change publisher.RepositoryChange
	err    error
}

func (stub changeInspectorStub) Inspect(context.Context, publisher.Request) (publisher.RepositoryChange, error) {
	return stub.change, stub.err
}

type gitBackendStub struct {
	base       string
	baseReads  int
	operations []publisher.GitOperation
	result     publisher.GitResult
	lookup     publisher.GitResult
	found      bool
	lookups    int
	crashAfter bool
	err        error
	block      bool
}

type concurrentGitBackend struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	writes  int
}

func (backend *concurrentGitBackend) Lookup(context.Context, publisher.Credential, string) (publisher.GitResult, bool, error) {
	return publisher.GitResult{}, false, nil
}

func (backend *concurrentGitBackend) CurrentBase(context.Context, publisher.Credential, string, string) (string, error) {
	return "base", nil
}

func (backend *concurrentGitBackend) Publish(
	ctx context.Context,
	_ publisher.Credential,
	_ publisher.GitOperation,
) (publisher.GitResult, error) {
	backend.mu.Lock()
	backend.writes++
	backend.mu.Unlock()
	backend.once.Do(func() { close(backend.started) })
	select {
	case <-ctx.Done():
		return publisher.GitResult{}, ctx.Err()
	case <-backend.release:
		return publisher.GitResult{ExternalID: "shared", HeadSHA: "head"}, nil
	}
}

func (stub *gitBackendStub) Lookup(context.Context, publisher.Credential, string) (publisher.GitResult, bool, error) {
	stub.lookups++
	return stub.lookup, stub.found, nil
}

func (stub *gitBackendStub) CurrentBase(context.Context, publisher.Credential, string, string) (string, error) {
	stub.baseReads++
	return stub.base, stub.err
}

func (stub *gitBackendStub) Publish(ctx context.Context, _ publisher.Credential, operation publisher.GitOperation) (publisher.GitResult, error) {
	stub.operations = append(stub.operations, operation)
	if stub.block {
		<-ctx.Done()
		return publisher.GitResult{}, ctx.Err()
	}
	if stub.crashAfter {
		stub.lookup = stub.result
		stub.found = true
		stub.crashAfter = false
		return publisher.GitResult{}, context.DeadlineExceeded
	}
	return stub.result, stub.err
}

func TestGitServicePublishesOnceWithScopedCredential(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	credentials := &credentialsStub{credential: publisher.Credential{Reference: "secret/git/team/repo"}}
	backend := &gitBackendStub{
		base:   "base-sha",
		result: publisher.GitResult{ExternalID: "refs/heads/agent/upgrade", URL: "https://github.example/pr/7", HeadSHA: "head-sha"},
	}
	service, err := publisher.NewGitService(store, credentials, changeInspectorStub{change: publisher.RepositoryChange{
		BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/workspace/change",
	}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := branchRequest()
	publication, err := service.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || len(backend.operations) != 1 {
		t.Fatalf("Execute = (%+v, %v), operations=%+v", publication, err, backend.operations)
	}
	if len(credentials.requests) != 1 || credentials.requests[0].Destination != request.Destination ||
		credentials.requests[0].Authority != request.Authority || backend.operations[0].MaterializedRoot != "/workspace/change" ||
		backend.operations[0].Mode != publisher.ModeBranch || backend.operations[0].Authority != request.Authority {
		t.Fatalf("scoping/operation = %+v/%+v", credentials.requests, backend.operations[0])
	}
	replayed, err := service.Execute(context.Background(), request)
	if err != nil || replayed.OperationKey != publication.OperationKey || len(backend.operations) != 1 {
		t.Fatalf("replay = (%+v, %v), operations=%d", replayed, err, len(backend.operations))
	}
}

func TestGitServiceWaitsForConcurrentSemanticOperationAndReturnsCurrentOccurrence(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &concurrentGitBackend{started: make(chan struct{}), release: make(chan struct{})}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base", ResultSHA: "head", MaterializedRoot: "/change",
		}},
		backend,
		5*time.Second,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := branchRequest()
	secondRequest := firstRequest.Clone()
	secondRequest.Authority.BuildID++
	secondRequest.Authority.WorkflowRunID++
	secondRequest.Authority.Actor = "bob"
	type response struct {
		publication publisher.Publication
		err         error
	}
	firstResult := make(chan response, 1)
	secondResult := make(chan response, 1)
	go func() {
		publication, err := service.Execute(context.Background(), firstRequest)
		firstResult <- response{publication: publication, err: err}
	}()
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("first provider write did not start")
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		publication, err := service.Execute(ctx, secondRequest)
		secondResult <- response{publication: publication, err: err}
	}()
	time.Sleep(150 * time.Millisecond)
	close(backend.release)

	first := <-firstResult
	second := <-secondResult
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent results = (%+v, %v) / (%+v, %v)", first.publication, first.err, second.publication, second.err)
	}
	if first.publication.Status != publisher.StatusSucceeded || second.publication.Status != publisher.StatusSucceeded {
		t.Fatalf("concurrent statuses = %s/%s", first.publication.Status, second.publication.Status)
	}
	if first.publication.OperationKey != second.publication.OperationKey || first.publication.ID == second.publication.ID {
		t.Fatalf("operation/occurrence identities = %+v / %+v", first.publication, second.publication)
	}
	if second.publication.Request.Authority != secondRequest.Authority {
		t.Fatalf("second occurrence authority = %+v, want %+v", second.publication.Request.Authority, secondRequest.Authority)
	}
	backend.mu.Lock()
	writes := backend.writes
	backend.mu.Unlock()
	if writes != 1 {
		t.Fatalf("provider writes = %d, want 1", writes)
	}
}

func TestGitServiceRejectsStaleBaseBeforeSideEffect(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &gitBackendStub{base: "new-base"}
	service, err := publisher.NewGitService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/git"}}, changeInspectorStub{change: publisher.RepositoryChange{
		BaseSHA: "old-base", ResultSHA: "head", MaterializedRoot: "/change",
	}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := branchRequest()
	publication, err := service.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusRebaseRequired || len(backend.operations) != 0 {
		t.Fatalf("branch stale base = (%+v, %v), operations=%d", publication, err, len(backend.operations))
	}

	merge := request
	merge.Mode = publisher.ModeMerge
	merge.ApprovedBy = "alice"
	merge.Approval = approvalEvidence("alice", 11)
	merge.Parameters = map[string]string{"target_branch": "main", "expected_base_sha": "old-base"}
	publication, err = service.Execute(context.Background(), merge)
	if err != nil || publication.Status != publisher.StatusStaleBase || len(backend.operations) != 0 {
		t.Fatalf("merge stale base = (%+v, %v), operations=%d", publication, err, len(backend.operations))
	}
}

func TestGitServiceTimeoutLeavesOperationRetryable(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &gitBackendStub{base: "base", block: true}
	service, err := publisher.NewGitService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/git"}}, changeInspectorStub{change: publisher.RepositoryChange{
		BaseSHA: "base", ResultSHA: "head", MaterializedRoot: "/change",
	}}, backend, 5*time.Millisecond, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := branchRequest()
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout = %v", err)
	}
	key, _ := request.OperationKey()
	pending, found, err := store.Get(context.Background(), key)
	if err != nil || !found || pending.Status != publisher.StatusPending {
		t.Fatalf("pending = (%+v, %t, %v)", pending, found, err)
	}
	now = now.Add(2 * time.Minute)
	backend.block = false
	backend.result = publisher.GitResult{HeadSHA: "head"}
	retried, err := service.Execute(context.Background(), request)
	if err != nil || retried.Status != publisher.StatusSucceeded || retried.Attempt != 2 {
		t.Fatalf("retry = (%+v, %v)", retried, err)
	}
}

func TestGitServiceReconcilesCrashAfterProviderSuccessWithoutRepeatingWrite(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &gitBackendStub{
		base: "base", result: publisher.GitResult{ExternalID: "pr-9", URL: "https://git.example/pr/9", HeadSHA: "head"},
		crashAfter: true,
	}
	service, err := publisher.NewGitService(store, &credentialsStub{credential: publisher.Credential{Reference: "secret/git"}}, changeInspectorStub{change: publisher.RepositoryChange{
		BaseSHA: "base", ResultSHA: "head", MaterializedRoot: "/change",
	}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := branchRequest()
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("crash-after-success = %v", err)
	}
	now = now.Add(2 * time.Minute)
	publication, err := service.Execute(context.Background(), request)
	if err != nil || publication.Status != publisher.StatusSucceeded || publication.Result.ExternalID != "pr-9" {
		t.Fatalf("reconciled = (%+v, %v)", publication, err)
	}
	if len(backend.operations) != 1 || backend.lookups != 2 {
		t.Fatalf("writes/lookups = %d/%d, want 1/2", len(backend.operations), backend.lookups)
	}
}

func TestGitServiceRejectsProviderResultForAnyCommitOtherThanTheExactSnapshotResult(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "reconcile"}[recovered], func(t *testing.T) {
			store := publisher.NewMemoryStore(time.Now)
			backend := &gitBackendStub{
				base: "base", result: publisher.GitResult{HeadSHA: "different-head"},
				found: recovered, lookup: publisher.GitResult{HeadSHA: "different-head"},
			}
			service, err := publisher.NewGitService(store,
				&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
				changeInspectorStub{change: publisher.RepositoryChange{
					BaseSHA: "base", ResultSHA: "exact-head", MaterializedRoot: "/change",
				}}, backend, time.Minute, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			request := branchRequest()
			if _, err := service.Execute(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Fatalf("mismatched provider result error = %v", err)
			}
			key, _ := request.OperationKey()
			pending, found, err := store.Get(context.Background(), key)
			if err != nil || !found || pending.Status != publisher.StatusPending {
				t.Fatalf("mismatch must remain retryable: %+v/%t/%v", pending, found, err)
			}
		})
	}
}

func TestGitServiceFailsBeforeAcquireForUnapprovedMerge(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	service, err := publisher.NewGitService(store, &credentialsStub{}, changeInspectorStub{}, &gitBackendStub{}, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := branchRequest()
	request.Mode = publisher.ModeMerge
	request.Parameters = map[string]string{"target_branch": "main", "expected_base_sha": "base"}
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("unapproved merge = %v", err)
	}
}

func TestGitServiceFailsClosedWhenCredentialProviderReturnsEmptyHandle(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &gitBackendStub{base: "base"}
	service, err := publisher.NewGitService(store, &credentialsStub{}, changeInspectorStub{change: publisher.RepositoryChange{
		BaseSHA: "base", ResultSHA: "head", MaterializedRoot: "/change",
	}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), branchRequest()); err == nil {
		t.Fatal("empty credential handle was accepted")
	}
	if backend.baseReads != 0 || len(backend.operations) != 0 {
		t.Fatalf("backend reached without destination authorization: base reads %d, operations %d", backend.baseReads, len(backend.operations))
	}
}

func TestGitServiceUsesReclaimingOccurrenceApprovalAndAuthority(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	credentials := &credentialsStub{err: errors.New("vault unavailable")}
	backend := &gitBackendStub{base: "base", result: publisher.GitResult{HeadSHA: "head"}}
	service, err := publisher.NewGitService(store, credentials, changeInspectorStub{change: publisher.RepositoryChange{
		BaseSHA: "base", ResultSHA: "head", MaterializedRoot: "/change",
	}}, backend, time.Minute, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first := branchRequest()
	first.Mode = publisher.ModeMerge
	first.Parameters = map[string]string{"target_branch": "main", "expected_base_sha": "base"}
	first.ApprovedBy = "alice"
	first.Approval = approvalEvidence("alice", 11)
	if _, err := service.Execute(context.Background(), first); err == nil {
		t.Fatal("credential failure was hidden")
	}

	now = now.Add(2 * time.Minute)
	credentials.err = nil
	credentials.credential = publisher.Credential{Reference: "secret/git"}
	retry := first.Clone()
	retry.ApprovedBy = "bob"
	retry.Approval = approvalEvidence("bob", 12)
	retry.Authority.BuildID++
	retry.Authority.WorkflowRunID++
	retry.Authority.Actor = "bob"
	publication, err := service.Execute(context.Background(), retry)
	if err != nil || publication.Status != publisher.StatusSucceeded || len(backend.operations) != 1 {
		t.Fatalf("retry = (%+v, %v), operations=%+v", publication, err, backend.operations)
	}
	operation := backend.operations[0]
	if operation.ApprovedBy != "bob" || operation.Authority != retry.Authority {
		t.Fatalf("reclaimed operation authority = %+v, want current %+v", operation, retry.Authority)
	}
}
