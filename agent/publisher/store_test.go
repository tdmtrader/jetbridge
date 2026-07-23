package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestPublicationMarshalsDatabaseIdentityAsQuotedDecimal(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(publisher.Publication{
		ID: 9007199254740993, OperationKey: "sha256:" + strings.Repeat("a", 64),
		Request: branchRequest(), Status: publisher.StatusPending, Attempt: 1,
		LeaseUntil: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"id":"9007199254740993"`) {
		t.Fatalf("publication wire identity is not exact: %s", payload)
	}
	var decoded struct {
		ID snapshot.DatabaseID `json:"id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.ID != 9007199254740993 {
		t.Fatalf("decode exact publication ID: %#v, %v", decoded, err)
	}
}

func TestMemoryStoreAcquiresOnceReclaimsExpiredAndCompletesByAttempt(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := publisher.NewMemoryStore(func() time.Time { return now })
	request := branchRequest()
	first, execute, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil || !execute || first.Attempt != 1 || first.Status != publisher.StatusPending {
		t.Fatalf("first Acquire = (%+v, %t, %v)", first, execute, err)
	}
	duplicate, execute, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil || execute || duplicate.Attempt != first.Attempt {
		t.Fatalf("duplicate Acquire = (%+v, %t, %v)", duplicate, execute, err)
	}

	now = now.Add(2 * time.Minute)
	reclaimed, execute, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil || !execute || reclaimed.Attempt != 2 {
		t.Fatalf("reclaimed Acquire = (%+v, %t, %v)", reclaimed, execute, err)
	}
	if _, err := store.Complete(context.Background(), reclaimed.OperationKey, 1, publisher.Result{Status: publisher.StatusSucceeded}); !errors.Is(err, publisher.ErrOperationConflict) {
		t.Fatalf("stale Complete = %v", err)
	}
	completed, err := store.Complete(context.Background(), reclaimed.OperationKey, 2, publisher.Result{
		Status: publisher.StatusSucceeded, ExternalID: "refs/heads/agent/upgrade", HeadSHA: "abc",
	})
	if err != nil || completed.Status != publisher.StatusSucceeded || completed.Result.HeadSHA != "abc" {
		t.Fatalf("Complete = (%+v, %v)", completed, err)
	}
	replayed, execute, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil || execute || replayed.Attempt != 2 || replayed.Result.HeadSHA != "abc" {
		t.Fatalf("terminal replay = (%+v, %t, %v)", replayed, execute, err)
	}
}

func TestMemoryStoreRejectsOperationKeyCollisionAndMismatchedCompletion(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	request := branchRequest()
	publication, _, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), publication.OperationKey, publication.Attempt, publisher.Result{Status: publisher.StatusPending}); !errors.Is(err, publisher.ErrInvalidResult) {
		t.Fatalf("pending Complete = %v", err)
	}
	if _, err := store.Complete(context.Background(), "sha256:"+strings.Repeat("0", 64), 1, publisher.Result{Status: publisher.StatusSucceeded}); !errors.Is(err, publisher.ErrOperationNotFound) {
		t.Fatalf("unknown Complete = %v", err)
	}
}

func TestMemoryStoreReturnsOccurrenceAuthorityForSemanticReplay(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	firstRequest := branchRequest()
	first, execute, err := store.Acquire(context.Background(), firstRequest, time.Minute)
	if err != nil || !execute {
		t.Fatalf("first Acquire = (%+v, %t, %v)", first, execute, err)
	}
	result := publisher.Result{Status: publisher.StatusSucceeded, ExternalID: "shared"}
	first, err = store.Complete(context.Background(), first.OperationKey, first.Attempt, result)
	if err != nil {
		t.Fatal(err)
	}

	secondRequest := firstRequest.Clone()
	secondRequest.Authority.BuildID++
	secondRequest.Authority.WorkflowRunID++
	secondRequest.Authority.Actor = "bob"
	second, execute, err := store.Acquire(context.Background(), secondRequest, time.Minute)
	if err != nil || execute {
		t.Fatalf("semantic replay Acquire = (%+v, %t, %v)", second, execute, err)
	}
	if second.ID == first.ID || second.OperationKey != first.OperationKey {
		t.Fatalf("occurrence/provider identities = %s/%s and %q/%q", first.ID, second.ID, first.OperationKey, second.OperationKey)
	}
	if second.Request.Authority != secondRequest.Authority || second.Result != result {
		t.Fatalf("semantic replay projection = %+v", second)
	}
}

func TestMemoryStoreRejectsUnverifiedWorkflowRunAuthority(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	request := branchRequest()
	request.Authority.WorkflowRunID = 0
	if _, _, err := store.Acquire(context.Background(), request, time.Minute); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("Acquire without verified workflow run = %v", err)
	}
}

func TestMemoryStorePreservesCancellationAndClonesRequests(t *testing.T) {
	store := publisher.NewMemoryStore(time.Now)
	request := branchRequest()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Acquire(canceled, request, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire cancellation = %v", err)
	}
	publication, _, err := store.Acquire(context.Background(), request, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.Parameters["source_branch"] = "mutated"
	if publication.Request.Parameters["source_branch"] != "agent/upgrade" {
		t.Fatalf("stored request was aliased: %+v", publication.Request)
	}
}

func branchRequest() publisher.Request {
	return publisher.Request{
		Publisher: publisher.GitPublisher, Input: changeRef(), Destination: "github.example/team/repo",
		Mode:                  publisher.ModeBranch,
		Parameters:            map[string]string{"source_branch": "agent/upgrade", "target_branch": "main"},
		ApprovalPolicyVersion: "engineering/v2",
		Authority:             publicationAuthority(),
	}
}
