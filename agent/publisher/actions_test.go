package publisher_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

type actionsReaderStub struct {
	mode  string
	found bool
	err   error
	reads int
}

func (stub *actionsReaderStub) GetActionsMode() (string, bool, error) {
	stub.reads++
	return stub.mode, stub.found, stub.err
}

func TestEffectiveActionsModeFailsSafe(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  string
		found bool
		err   error
		want  string
	}{
		{name: "read error suppresses", err: errors.New("connection refused"), want: publisher.ActionsModeSuppressed},
		{name: "read error beats a stored active", mode: "active", found: true, err: errors.New("boom"), want: publisher.ActionsModeSuppressed},
		{name: "unset is active", want: publisher.ActionsModeActive},
		{name: "stored active is active", mode: "active", found: true, want: publisher.ActionsModeActive},
		{name: "stored suppressed is suppressed", mode: "suppressed", found: true, want: publisher.ActionsModeSuppressed},
		{name: "unrecognized value fails closed", mode: "halt", found: true, want: publisher.ActionsModeSuppressed},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := publisher.EffectiveActionsMode(test.mode, test.found, test.err); got != test.want {
				t.Fatalf("EffectiveActionsMode(%q, %t, %v) = %q, want %q", test.mode, test.found, test.err, got, test.want)
			}
		})
	}
}

func TestGitServiceMakesNoExternalCallWhileActionsAreSuppressed(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &gitBackendStub{base: "base-sha", result: publisher.GitResult{ExternalID: "pr-1", HeadSHA: "head-sha"}}
	actions := &actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/change",
		}},
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(actions),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := branchRequest()
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("Execute error = %v, want ErrActionsSuppressed", err)
	}
	if backend.lookups != 0 || len(backend.operations) != 0 || backend.baseReads != 0 {
		t.Fatalf("suppressed publish touched the provider: lookups=%d bases=%d operations=%d",
			backend.lookups, backend.baseReads, len(backend.operations))
	}

	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	publication, found, err := store.Get(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("Get = (%+v, %t, %v)", publication, found, err)
	}
	if publication.Status != publisher.StatusPending {
		t.Fatalf("operation status = %q, want pending so the run can be retried after resume", publication.Status)
	}
}

func TestGitServiceSuppressesWhenTheSwitchCannotBeRead(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &gitBackendStub{base: "base-sha", result: publisher.GitResult{ExternalID: "pr-1", HeadSHA: "head-sha"}}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/change",
		}},
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(&actionsReaderStub{err: errors.New("connection refused")}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), branchRequest()); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("Execute error = %v, want ErrActionsSuppressed", err)
	}
	if len(backend.operations) != 0 {
		t.Fatalf("unreadable switch permitted %d provider writes", len(backend.operations))
	}
}

// The WS3 acceptance property: suppression must not corrupt idempotency.
// One semantic operation, refused once, then executed exactly once on retry.
func TestGitServicePublishesExactlyOnceAfterActionsResume(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	backend := &gitBackendStub{base: "base-sha", result: publisher.GitResult{
		ExternalID: "refs/heads/agent/upgrade", URL: "https://github.example/pr/7", HeadSHA: "head-sha",
	}}
	actions := &actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}
	service, err := publisher.NewGitService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/git"}},
		changeInspectorStub{change: publisher.RepositoryChange{
			BaseSHA: "base-sha", ResultSHA: "head-sha", MaterializedRoot: "/change",
		}},
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(actions),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := branchRequest()
	if _, err := service.Execute(context.Background(), request); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("suppressed Execute error = %v, want ErrActionsSuppressed", err)
	}

	// Resume, and let the refused attempt's lease expire so the retry reclaims
	// execution instead of waiting behind it.
	actions.mode = publisher.ActionsModeActive
	now = now.Add(2 * time.Minute)

	publication, err := service.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("resumed Execute: %v", err)
	}
	if publication.Status != publisher.StatusSucceeded {
		t.Fatalf("resumed status = %q, want succeeded", publication.Status)
	}
	if len(backend.operations) != 1 {
		t.Fatalf("provider writes = %d, want exactly 1 across suppression and retry", len(backend.operations))
	}
	key, err := request.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if publication.OperationKey != key {
		t.Fatalf("operation key = %q, want the unchanged semantic identity %q", publication.OperationKey, key)
	}
	if publication.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (the refusal consumed attempt 1)", publication.Attempt)
	}
}

func TestWorkItemServiceMakesNoExternalCallWhileActionsAreSuppressed(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	backend := &workItemBackendStub{result: publisher.WorkItemResult{ExternalID: "comment-9"}}
	service, err := publisher.NewWorkItemService(
		store,
		&credentialsStub{credential: publisher.Credential{Reference: "secret/jira"}},
		validSnapshotValueInspector(),
		backend, time.Minute, time.Minute,
		publisher.WithActionsGate(&actionsReaderStub{mode: publisher.ActionsModeSuppressed, found: true}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), commentRequest()); !errors.Is(err, publisher.ErrActionsSuppressed) {
		t.Fatalf("Execute error = %v, want ErrActionsSuppressed", err)
	}
	if backend.lookups != 0 || len(backend.requests) != 0 {
		t.Fatalf("suppressed work-item publish touched the provider: lookups=%d writes=%d", backend.lookups, len(backend.requests))
	}
}
