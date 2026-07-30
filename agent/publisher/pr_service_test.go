package publisher_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestPROperationKeysSeparateKindsAndPreserveDirectGitIdentity(t *testing.T) {
	branch := validBranchPublicationRequest()
	create := validPullRequestPublicationRequest()
	status := validStatusPublicationRequest()
	response := validResponsePublicationRequest()

	keys := make(map[string]string)
	for kind, operationKey := range map[string]func() (string, error){
		"publish_pr_branch": branch.OperationKey,
		"create_pr":         create.OperationKey,
		"publish_pr_status": status.OperationKey,
		"respond_to_review": response.OperationKey,
	} {
		key, err := operationKey()
		if err != nil {
			t.Fatalf("%s OperationKey: %v", kind, err)
		}
		if previous, duplicate := keys[key]; duplicate {
			t.Fatalf("%s and %s share operation key %q", previous, kind, key)
		}
		keys[key] = kind
	}
	branchKey, err := branch.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	retry := branch
	retry.Authority.BuildID = 9001
	retry.Authority.WorkflowRunID = 9002
	retry.Authority.Actor = "retry-worker"
	retryKey, err := retry.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if retryKey != branchKey {
		t.Fatalf("occurrence-only authority changed semantic key: %q != %q", retryKey, branchKey)
	}
	otherTeam := branch
	otherTeam.Authority.TeamID++
	otherTeamKey, err := otherTeam.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if otherTeamKey == branchKey {
		t.Fatalf("different team shared PR operation key %q", branchKey)
	}

	direct := publisher.Request{
		Publisher: publisher.GitPublisher,
		Input: snapshot.SnapshotRef{
			ID: 7, Type: "repository-change/v1", Digest: testDigest('a'),
		},
		Destination: "github.example/team/repo",
		Mode:        publisher.ModeBranch,
		Parameters: map[string]string{
			"source_branch": "agent/upgrade",
			"target_branch": "main",
		},
		ApprovalPolicyVersion: "engineering/v2",
		Authority: publisher.Authority{
			TeamID: 17, TeamName: "engineering", BuildID: 42,
			WorkflowRunID: 91, Actor: "alice",
		},
	}
	key, err := direct.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	const wantDirectKey = "sha256:9563d0a82ccf46ec1e8933de347e55bea3a332c50f5c98aabe5943d6bfc89bba"
	if key != wantDirectKey {
		t.Fatalf("direct-Git operation key changed: got %q, want %q", key, wantDirectKey)
	}
}

func TestBranchPublicationAllowsAnExistingPRButCreationRequiresMissingLocator(t *testing.T) {
	branch := validBranchPublicationRequest()
	branch.Locator.ExternalID = "42"
	if err := branch.Validate(); err != nil {
		t.Fatalf("existing PR branch request: %v", err)
	}

	create := validPullRequestPublicationRequest()
	create.Locator.ExternalID = "42"
	if err := create.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
		t.Fatalf("pre-create locator error = %v, want invalid request", err)
	}
}

func TestPRBranchAndCreationBindTheFinalValidatedImpactRevision(t *testing.T) {
	branch := validBranchPublicationRequest()
	if err := branch.Validate(); err != nil {
		t.Fatalf("valid final branch revision: %v", err)
	}
	create := validPullRequestPublicationRequest()
	if err := create.Validate(); err != nil {
		t.Fatalf("valid final PR revision: %v", err)
	}

	for name, mutate := range map[string]func(*publisher.BranchPublicationRequest){
		"pre-rebase candidate": func(request *publisher.BranchPublicationRequest) {
			request.Candidate.Type = "repository/v1"
		},
		"wrong validation": func(request *publisher.BranchPublicationRequest) {
			request.Validation.Type = "review/v1"
		},
		"wrong impact": func(request *publisher.BranchPublicationRequest) {
			request.Impact.Type = "review/v1"
		},
	} {
		t.Run("branch rejects "+name, func(t *testing.T) {
			request := validBranchPublicationRequest()
			mutate(&request)
			if err := request.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
				t.Fatalf("Validate error = %v, want invalid request", err)
			}
		})
	}

	for name, mutate := range map[string]func(*publisher.PullRequestPublicationRequest){
		"pre-rebase candidate": func(request *publisher.PullRequestPublicationRequest) {
			request.Candidate.Type = "repository/v1"
		},
		"wrong validation": func(request *publisher.PullRequestPublicationRequest) {
			request.Validation.Type = "review/v1"
		},
		"wrong impact": func(request *publisher.PullRequestPublicationRequest) {
			request.Impact.Type = "review/v1"
		},
	} {
		t.Run("creation rejects "+name, func(t *testing.T) {
			request := validPullRequestPublicationRequest()
			mutate(&request)
			if err := request.Validate(); !errors.Is(err, publisher.ErrInvalidRequest) {
				t.Fatalf("Validate error = %v, want invalid request", err)
			}
		})
	}

	branchKey, err := branch.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	changedImpact := branch
	changedImpact.Impact.Digest = testDigest('9')
	changedKey, err := changedImpact.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	if changedKey == branchKey {
		t.Fatal("final impact evidence did not change branch operation identity")
	}
}

func TestPRServiceKeepsBranchSuccessSeparateFromRecoverablePRCreation(t *testing.T) {
	now := time.Date(2026, 7, 29, 13, 0, 0, 0, time.UTC)
	store := newPRMemoryStore(func() time.Time { return now })
	mutator := &recoveringPRMutator{}
	service, err := publisher.NewPRService(
		store,
		staticPRMutatorResolver{mutator: mutator},
		time.Second,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	branch, err := service.PublishBranch(context.Background(), validBranchPublicationRequest())
	if err != nil || branch.Status != publisher.StatusSucceeded || branch.Result.HeadSHA != objectID('c') {
		t.Fatalf("PublishBranch = (%+v, %v)", branch, err)
	}
	createRequest := validPullRequestPublicationRequest()
	if _, err := service.FindOrCreate(context.Background(), createRequest); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first FindOrCreate error = %v, want timeout", err)
	}

	now = now.Add(2 * time.Minute)
	created, err := service.FindOrCreate(context.Background(), createRequest)
	if err != nil || created.Status != publisher.StatusSucceeded ||
		created.Result.ExternalID != "42" || created.Result.URL != "https://github.example/acme/widget/pull/42" {
		t.Fatalf("recovered FindOrCreate = (%+v, %v)", created, err)
	}
	if branch.OperationKey == created.OperationKey {
		t.Fatalf("branch and PR creation shared operation key %q", branch.OperationKey)
	}
	if mutator.branchWrites != 1 || mutator.createAttempts != 2 {
		t.Fatalf("provider branch/create attempts = %d/%d, want 1/2", mutator.branchWrites, mutator.createAttempts)
	}
}

type staticPRMutatorResolver struct {
	mutator publisher.PRMutator
}

func (resolver staticPRMutatorResolver) ResolvePRMutator(context.Context, publisher.PRAction) (publisher.PRMutator, error) {
	return resolver.mutator, nil
}

type recoveringPRMutator struct {
	branchWrites   int
	createAttempts int
	created        *publisher.ExternalPullRequest
}

func (mutator *recoveringPRMutator) CompareAndSwapBranch(_ context.Context, mutation publisher.BranchMutation) (publisher.BranchResult, error) {
	mutator.branchWrites++
	return publisher.BranchResult{HeadSHA: mutation.NewSourceSHA, Applied: true}, nil
}

func (mutator *recoveringPRMutator) FindOrCreatePullRequest(_ context.Context, request publisher.CreatePRMutation) (publisher.ExternalPullRequest, error) {
	mutator.createAttempts++
	if mutator.created == nil {
		value := publisher.ExternalPullRequest{
			Locator:   publisher.PRLocator{Provider: publisher.PRProviderGitHub, Repository: request.Locator.Repository, ExternalID: "42"},
			URL:       "https://github.example/acme/widget/pull/42",
			State:     contracts.PullRequestActive,
			SourceRef: request.SourceRef,
			SourceSHA: request.SourceSHA,
			TargetRef: request.TargetRef,
			TargetSHA: request.TargetSHA,
		}
		mutator.created = &value
		return publisher.ExternalPullRequest{}, context.DeadlineExceeded
	}
	return *mutator.created, nil
}

func (*recoveringPRMutator) PublishValidationStatus(context.Context, publisher.StatusMutation) (publisher.ExternalResult, error) {
	return publisher.ExternalResult{}, fmt.Errorf("unexpected status publication")
}

func (*recoveringPRMutator) PublishReviewResponse(context.Context, publisher.ResponseMutation) (publisher.ExternalResult, error) {
	return publisher.ExternalResult{}, fmt.Errorf("unexpected response publication")
}

type prMemoryStore struct {
	mu         sync.Mutex
	now        func() time.Time
	nextID     snapshot.DatabaseID
	operations map[string]*prMemoryOperation
}

type prMemoryOperation struct {
	action     publisher.PRAction
	status     publisher.Status
	attempt    int
	leaseUntil time.Time
	result     publisher.Result
	id         snapshot.DatabaseID
	createdAt  time.Time
	updatedAt  time.Time
}

func newPRMemoryStore(now func() time.Time) *prMemoryStore {
	return &prMemoryStore{now: now, operations: make(map[string]*prMemoryOperation)}
}

func (store *prMemoryStore) AcquirePR(_ context.Context, action publisher.PRAction, lease time.Duration) (publisher.Publication, bool, error) {
	if err := action.ValidatePersisted(); err != nil {
		return publisher.Publication{}, false, err
	}
	key, err := action.OperationKey()
	if err != nil {
		return publisher.Publication{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	if operation, found := store.operations[key]; found {
		if operation.status != publisher.StatusPending || now.Before(operation.leaseUntil) {
			return operation.publication(key), false, nil
		}
		operation.attempt++
		operation.leaseUntil = now.Add(lease)
		operation.updatedAt = now
		operation.action = action.Clone()
		return operation.publication(key), true, nil
	}
	store.nextID++
	operation := &prMemoryOperation{
		action: action.Clone(), status: publisher.StatusPending, attempt: 1,
		leaseUntil: now.Add(lease), id: store.nextID, createdAt: now, updatedAt: now,
	}
	store.operations[key] = operation
	return operation.publication(key), true, nil
}

func (store *prMemoryStore) CompletePR(_ context.Context, operationKey string, attempt int, result publisher.Result) (publisher.Publication, error) {
	if err := result.Validate(); err != nil {
		return publisher.Publication{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.operations[operationKey]
	if !found || operation.attempt != attempt || operation.status != publisher.StatusPending {
		return publisher.Publication{}, publisher.ErrOperationConflict
	}
	operation.status = result.Status
	operation.result = result
	operation.leaseUntil = time.Time{}
	operation.updatedAt = store.now().UTC()
	return operation.publication(operationKey), nil
}

func (operation *prMemoryOperation) publication(key string) publisher.Publication {
	action := operation.action.Clone()
	return publisher.Publication{
		ID: operation.id, OperationKey: key, OperationKind: action.Kind, PRAction: &action,
		Status: operation.status, Attempt: operation.attempt, LeaseUntil: operation.leaseUntil,
		Result: operation.result, CreatedAt: operation.createdAt, UpdatedAt: operation.updatedAt,
	}
}

func validBranchPublicationRequest() publisher.BranchPublicationRequest {
	return publisher.BranchPublicationRequest{
		Authority:             prAuthority(),
		Observation:           snapshot.SnapshotRef{ID: 21, Type: "pull-request/v1", Digest: testDigest('1')},
		Candidate:             snapshot.SnapshotRef{ID: 22, Type: "repository-change/v1", Digest: testDigest('2')},
		Validation:            snapshot.SnapshotRef{ID: 23, Type: "validation/v1", Digest: testDigest('3')},
		Impact:                snapshot.SnapshotRef{ID: 24, Type: "publish-impact/v1", Digest: testDigest('4')},
		Evidence:              prEvidence(),
		Destination:           "github.example/acme/widget",
		ApprovalPolicyVersion: "engineering/v3",
		Locator:               publisher.PRLocator{Provider: publisher.PRProviderGitHub, Repository: "acme/widget"},
		SourceRef:             "refs/heads/agent/upgrade",
		TargetRef:             "refs/heads/main",
		ExpectedSource:        contracts.PullRequestHeadExpectation{Exists: true, SHA: objectID('a')},
		ExpectedTargetSHA:     objectID('b'),
		NewSourceSHA:          objectID('c'),
	}
}

func validPullRequestPublicationRequest() publisher.PullRequestPublicationRequest {
	branch := validBranchPublicationRequest()
	return publisher.PullRequestPublicationRequest{
		Authority:             branch.Authority,
		Observation:           branch.Observation,
		Candidate:             branch.Candidate,
		Validation:            branch.Validation,
		Impact:                branch.Impact,
		Evidence:              branch.Evidence,
		Destination:           branch.Destination,
		ApprovalPolicyVersion: branch.ApprovalPolicyVersion,
		Locator:               branch.Locator,
		SourceRef:             branch.SourceRef,
		SourceSHA:             branch.NewSourceSHA,
		TargetRef:             branch.TargetRef,
		TargetSHA:             branch.ExpectedTargetSHA,
		Title:                 "Upgrade widget",
		Body:                  "Validated and ready for review.",
	}
}

func validStatusPublicationRequest() publisher.StatusPublicationRequest {
	create := validPullRequestPublicationRequest()
	return publisher.StatusPublicationRequest{
		Authority:             create.Authority,
		Observation:           snapshot.SnapshotRef{ID: 23, Type: "pull-request/v1", Digest: testDigest('3')},
		Validation:            snapshot.SnapshotRef{ID: 24, Type: "validation/v1", Digest: testDigest('4')},
		Evidence:              create.Evidence,
		Destination:           create.Destination,
		ApprovalPolicyVersion: create.ApprovalPolicyVersion,
		Locator: publisher.PRLocator{
			Provider: publisher.PRProviderGitHub, Repository: "acme/widget", ExternalID: "42",
		},
		SourceSHA:   create.SourceSHA,
		State:       "success",
		Description: "Jetbridge validation passed",
		TargetURL:   "https://ci.example/runs/91",
	}
}

func validResponsePublicationRequest() publisher.ResponsePublicationRequest {
	status := validStatusPublicationRequest()
	return publisher.ResponsePublicationRequest{
		Authority:             status.Authority,
		Observation:           status.Observation,
		ResponseSnapshot:      snapshot.SnapshotRef{ID: 25, Type: "pull-request-response/v1", Digest: testDigest('5')},
		Evidence:              status.Evidence,
		Destination:           status.Destination,
		ApprovalPolicyVersion: status.ApprovalPolicyVersion,
		Locator:               status.Locator,
		Batch: publisher.PRReviewBatch{
			ID: "review-17", ReviewID: "17", CommitSHA: status.SourceSHA,
			Reviewer: "github-user-9", Ready: true, ThreadIDs: []string{"thread-101"},
		},
		Response: contracts.PullRequestResponseBody{
			BatchID: "review-17",
			Summary: "Addressed the requested changes.",
			Replies: []contracts.PullRequestThreadResponse{{
				ThreadID: "thread-101", Body: "Updated in the new revision.",
			}},
		},
	}
}

func prAuthority() publisher.Authority {
	return publisher.Authority{
		TeamID: 17, TeamName: "engineering", BuildID: 42,
		WorkflowRunID: 91, Actor: "alice",
	}
}

func prEvidence() publisher.PublicationEvidence {
	return publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review:              snapshot.SnapshotRef{ID: 11, Type: "review/v1", Digest: testDigest('a')},
			Candidate:           snapshot.SnapshotRef{ID: 22, Type: "repository/v1", Digest: testDigest('2')},
			Validation:          snapshot.SnapshotRef{ID: 12, Type: "validation/v1", Digest: testDigest('b')},
			ReviewWorkflowRunID: 81,
			OutcomeRevision:     3,
			AcceptedBy:          "alice",
			AcceptedAt:          time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		},
	}
}

func objectID(character byte) string {
	return strings.Repeat(string(character), 40)
}

func testDigest(character byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(character), 64))
}
