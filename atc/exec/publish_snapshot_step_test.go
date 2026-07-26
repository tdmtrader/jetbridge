package exec_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/stretchr/testify/require"
)

type publisherExecutorFunc func(context.Context, publisher.Request) (publisher.Publication, error)

func (function publisherExecutorFunc) Execute(ctx context.Context, request publisher.Request) (publisher.Publication, error) {
	return function(ctx, request)
}

type mergeApprovalVerifierFunc func(context.Context, publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error)

func (function mergeApprovalVerifierFunc) Verify(ctx context.Context, request publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error) {
	return function(ctx, request)
}

func TestPublishSnapshotStepAuthorizesExactSealedInputAndPublishes(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	repository, state, delegates, delegate := loadSnapshotHarness()
	registerPublishArtifact(t, repository, manifest)
	plan := publishSnapshotPlan()

	var captured publisher.Request
	executor := publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
		captured = request.Clone()
		request.Authority.WorkflowRunID = 91
		key, err := request.OperationKey()
		require.NoError(t, err)
		return publisher.Publication{
			ID: 17, OperationKey: key, Request: request, Status: publisher.StatusSucceeded, Attempt: 1,
			Result:    publisher.Result{Status: publisher.StatusSucceeded, ExternalID: "pr-17"},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}, nil
	})
	step := exec.NewPublishSnapshotStep("publish", plan,
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, CreatedBy: "build-starter", SnapshotCreatedBy: "alice"}, delegates, metadata, executor, nil)

	ok, err := step.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, publisher.Request{
		Publisher:   plan.Publisher,
		Input:       snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest},
		Destination: plan.Destination, Mode: plan.Mode, Parameters: plan.Parameters,
		ApprovalPolicyVersion: plan.ApprovalPolicyVersion,
		Authority: publisher.Authority{
			TeamID: 17, TeamName: "main", BuildID: 42, Actor: "alice",
		},
	}, captured)
	require.Equal(t, 1, metadata.GetAuthorizedCallCount())
	_, teamID, snapshotID := metadata.GetAuthorizedArgsForCall(0)
	require.Equal(t, 17, teamID)
	require.Equal(t, manifest.ID, snapshotID)
	require.Equal(t, 1, delegate.FinishedCallCount())

	plan.Parameters["target_branch"] = "mutated"
	require.Equal(t, "main", captured.Parameters["target_branch"])
}

func TestPublishSnapshotStepRejectsPublicationWithoutStoreVerifiedWorkflowRun(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), manifest)
	step := exec.NewPublishSnapshotStep("publish", publishSnapshotPlan(),
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"}, delegates, metadata,
		publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
			key, err := request.OperationKey()
			require.NoError(t, err)
			return publisher.Publication{
				OperationKey: key, Request: request, Status: publisher.StatusSucceeded, Attempt: 1,
				Result: publisher.Result{Status: publisher.StatusSucceeded},
			}, nil
		}), nil)

	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "publish_snapshot: publication service returned an invalid response")
}

func TestPublishSnapshotStepRejectsSucceededResponseWithoutDurablePublicationIdentity(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), manifest)
	step := exec.NewPublishSnapshotStep("publish", publishSnapshotPlan(),
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"}, delegates, metadata,
		publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
			request.Authority.WorkflowRunID = 91
			key, err := request.OperationKey()
			require.NoError(t, err)
			now := time.Now().UTC()
			return publisher.Publication{
				OperationKey: key, Request: request, Status: publisher.StatusSucceeded, Attempt: 1,
				Result:    publisher.Result{Status: publisher.StatusSucceeded},
				CreatedAt: now, UpdatedAt: now,
			}, nil
		}), nil)

	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "publish_snapshot: publication service returned an invalid response")
}

func TestPublishSnapshotStepRejectsDurableResponseForDifferentSnapshotEvidence(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), manifest)
	step := exec.NewPublishSnapshotStep("publish", publishSnapshotPlan(),
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"}, delegates, metadata,
		publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
			request.Authority.WorkflowRunID = 91
			key, err := request.OperationKey()
			require.NoError(t, err)
			request.Input.Digest = snapshot.Digest("sha256:" + strings.Repeat("f", 64))
			now := time.Now().UTC()
			return publisher.Publication{
				ID: 17, OperationKey: key, Request: request, Status: publisher.StatusSucceeded, Attempt: 1,
				Result:    publisher.Result{Status: publisher.StatusSucceeded},
				CreatedAt: now, UpdatedAt: now,
			}, nil
		}), nil)

	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "publish_snapshot: publication service returned an invalid response")
}

func TestPublishSnapshotStepMergeFailsClosedWithoutDurableWaitApprovalVerifier(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	plan := publishSnapshotPlan()
	plan.Mode = publisher.ModeMerge
	plan.Approval = "approval"
	plan.WorkflowRunID = "91"
	plan.Parameters = map[string]string{"target_branch": "main"}

	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), manifest)
	called := false
	step := exec.NewPublishSnapshotStep("publish", plan,
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"}, delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			called = true
			return publisher.Publication{}, nil
		}), nil)
	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "publish_snapshot: durable merge approval verification is unavailable")
	require.False(t, called)
}

func TestPublishSnapshotStepBindsMergeToExactDurableApproval(t *testing.T) {
	change := publishSnapshotManifest()
	answer := snapshot.Snapshot{
		ID: 78, Type: snapshot.TypeRef("human-answer/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64)), ByteSize: 128, FileCount: 1,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		CreatedAt: time.Now().UTC(),
	}
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedCalls(func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		switch id {
		case change.ID:
			return change, true, nil
		case answer.ID:
			return answer, true, nil
		default:
			return snapshot.Snapshot{}, false, nil
		}
	})
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), change)
	answerRef := snapshot.SnapshotRef{ID: answer.ID, Type: answer.Type, Digest: answer.Digest}
	require.NoError(t, state.ArtifactRepository().RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"approval": {Artifact: &loadArtifact{handle: "sealed-approval"}, Snapshot: &answerRef},
	}))
	plan := publishSnapshotPlan()
	plan.Mode = publisher.ModeMerge
	plan.Approval = "approval"
	plan.WorkflowRunID = "91"
	plan.Parameters = map[string]string{"target_branch": "main"}
	resolvedAt := time.Now().UTC()
	question := snapshot.SnapshotRef{
		ID: 79, Type: snapshot.TypeRef("question/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("d", 64)),
	}
	var verified publisher.MergeApprovalRequest
	verifier := mergeApprovalVerifierFunc(func(_ context.Context, request publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error) {
		verified = request
		return publisher.ApprovalEvidence{
			WaitID: 12, Question: question, Answer: answerRef,
			ResolvedBy: "reviewer", ResolvedAt: resolvedAt,
		}, nil
	})
	var published publisher.Request
	executor := publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
		published = request.Clone()
		key, err := request.OperationKey()
		require.NoError(t, err)
		now := time.Now().UTC()
		return publisher.Publication{
			ID: 17, OperationKey: key, Request: request, Status: publisher.StatusSucceeded, Attempt: 1,
			Result:    publisher.Result{Status: publisher.StatusSucceeded, ExternalID: "merge-17"},
			CreatedAt: now, UpdatedAt: now,
		}, nil
	})
	step := exec.NewPublishSnapshotStep("publish", plan,
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"},
		delegates, metadata, executor, verifier)

	ok, err := step.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, publisher.MergeApprovalRequest{
		TeamID: 17, WorkflowRunID: 91, BuildID: 42,
		Input:    snapshot.SnapshotRef{ID: change.ID, Type: change.Type, Digest: change.Digest},
		Approval: answerRef, Publisher: plan.Publisher, Mode: plan.Mode, Destination: plan.Destination,
		Parameters: map[string]string{
			"target_branch": "main", "expected_base_sha": publishMergeBaseFixture,
		},
		ExpectedBaseSHA: publishMergeBaseFixture, ApprovalPolicyVersion: plan.ApprovalPolicyVersion,
	}, verified)
	require.Equal(t, snapshot.WorkflowRunID(91), published.Authority.WorkflowRunID)
	require.Equal(t, "reviewer", published.ApprovedBy)
	require.Equal(t, &publisher.ApprovalEvidence{
		WaitID: 12, Question: question, Answer: answerRef,
		ResolvedBy: "reviewer", ResolvedAt: resolvedAt,
	}, published.Approval)
	// The publication asserts the same server-derived base the approval was
	// verified against, and the plan itself never carried one.
	require.Equal(t, publishMergeBaseFixture, published.Parameters["expected_base_sha"])
	require.NotContains(t, plan.Parameters, "expected_base_sha")
}

// A plan that authors the server-owned base assertion is refused outright: the
// step must never quietly replace a value a reviewer might have been shown.
func TestPublishSnapshotStepRefusesAuthoredMergeBase(t *testing.T) {
	change := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(change, true, nil)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), change)
	plan := publishSnapshotPlan()
	plan.Mode = publisher.ModeMerge
	plan.Approval = "approval"
	plan.WorkflowRunID = "91"
	plan.Parameters = map[string]string{
		"target_branch": "main", "expected_base_sha": strings.Repeat("b", 40),
	}
	verified := false
	published := false
	step := exec.NewPublishSnapshotStep("publish", plan,
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"}, delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			published = true
			return publisher.Publication{}, nil
		}),
		mergeApprovalVerifierFunc(func(context.Context, publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error) {
			verified = true
			return publisher.ApprovalEvidence{}, nil
		}))

	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "publish_snapshot: invalid publication plan")
	require.False(t, verified)
	require.False(t, published)
}

// Without a sealed base_sha there is nothing to assert, so the merge fails
// closed before any approval verification or side effect.
func TestPublishSnapshotStepFailsClosedWithoutSealedMergeBase(t *testing.T) {
	change := publishSnapshotManifest()
	change.IntrinsicMetadata = nil
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(change, true, nil)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), change)
	plan := publishSnapshotPlan()
	plan.Mode = publisher.ModeMerge
	plan.Approval = "approval"
	plan.WorkflowRunID = "91"
	plan.Parameters = map[string]string{"target_branch": "main"}
	published := false
	step := exec.NewPublishSnapshotStep("publish", plan,
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "alice"}, delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			published = true
			return publisher.Publication{}, nil
		}),
		mergeApprovalVerifierFunc(func(context.Context, publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error) {
			return publisher.ApprovalEvidence{}, nil
		}))

	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.EqualError(t, err, "publish_snapshot: merge base is unavailable for the published change")
	require.False(t, published)
}

func TestPublishSnapshotStepFailsClosedForUnsealedUnauthorizedOrMismatchedInputs(t *testing.T) {
	valid := publishSnapshotManifest()
	tests := []struct {
		name     string
		entry    *snapshot.SnapshotRef
		manifest snapshot.Snapshot
		found    bool
	}{
		{name: "untyped artifact", entry: nil, manifest: valid, found: true},
		{name: "unauthorized", entry: publishSnapshotRef(valid), manifest: snapshot.Snapshot{}, found: false},
		{name: "expired", entry: publishSnapshotRef(valid), manifest: func() snapshot.Snapshot {
			value := valid
			value.ContentState = snapshot.ContentStateExpired
			return value
		}(), found: true},
		{name: "manifest digest mismatch", entry: publishSnapshotRef(valid), manifest: func() snapshot.Snapshot {
			value := valid
			value.Digest = snapshot.Digest("sha256:" + strings.Repeat("c", 64))
			return value
		}(), found: true},
		{name: "plan type mismatch", entry: publishSnapshotRef(valid), manifest: valid, found: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := new(snapshotfakes.FakeMetadataStore)
			metadata.GetAuthorizedReturns(test.manifest, test.found, nil)
			repository, state, delegates, _ := loadSnapshotHarness()
			entry := build.ArtifactEntry{Artifact: &loadArtifact{handle: "sealed"}, Snapshot: test.entry}
			require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{"change": entry}))
			plan := publishSnapshotPlan()
			if test.name == "plan type mismatch" {
				plan.InputType = snapshot.TypeRef("review/v1")
			}
			called := false
			step := exec.NewPublishSnapshotStep("publish", plan,
				exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, CreatedBy: "alice", SnapshotCreatedBy: "alice"}, delegates, metadata,
				publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
					called = true
					return publisher.Publication{}, nil
				}), nil)
			ok, err := step.Run(context.Background(), state)
			require.False(t, ok)
			require.Error(t, err)
			require.False(t, called)
			require.NotContains(t, err.Error(), valid.ID.String())
		})
	}
}

func TestPublishSnapshotStepReturnsSafeTerminalAndDependencyErrors(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)

	for _, test := range []struct {
		name     string
		response publisher.Publication
		err      error
		want     string
	}{
		{name: "rebase required", response: publisher.Publication{
			Status: publisher.StatusRebaseRequired, Attempt: 1,
			Result: publisher.Result{Status: publisher.StatusRebaseRequired, Detail: "base changed"},
		}, want: "publish_snapshot: publication ended with status rebase_required"},
		{name: "pending", response: publisher.Publication{Status: publisher.StatusPending, Attempt: 1}, want: "publish_snapshot: publication is still pending"},
		{name: "dependency failure", err: errors.New("token secret at https://private.example"), want: "publish_snapshot: publication execution failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, state, delegates, _ := loadSnapshotHarness()
			registerPublishArtifact(t, state.ArtifactRepository(), manifest)
			step := exec.NewPublishSnapshotStep("publish", publishSnapshotPlan(),
				exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "concourse"}, delegates, metadata,
				publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
					response := test.response
					if test.err == nil {
						request.Authority.WorkflowRunID = 91
						response.OperationKey, _ = request.OperationKey()
						response.Request = request
						response.ID = 17
						response.CreatedAt = time.Now().UTC()
						response.UpdatedAt = response.CreatedAt
						if response.Status == publisher.StatusPending {
							response.LeaseUntil = response.CreatedAt.Add(time.Minute)
						}
					}
					return response, test.err
				}), nil)
			ok, err := step.Run(context.Background(), state)
			require.False(t, ok)
			require.EqualError(t, err, test.want)
			require.NotContains(t, err.Error(), "private")
		})
	}
}

// The action switch is the ONE executor error a build log must explain rather
// than flatten: nothing was published because an operator engaged a
// cluster-wide brake, and the message names the command that releases it. The
// executor's suppression error folds the raw database error in with %v when the
// switch cannot be read, so the step must surface the sentinel's own canned
// text and nothing else.
func TestPublishSnapshotStepSurfacesTheActionSuppressionRefusal(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	_, state, delegates, delegate := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), manifest)

	wrapped := fmt.Errorf("%w (the switch could not be read: %v)", publisher.ErrActionsSuppressed,
		errors.New("dial tcp 10.9.9.9:5432: connect: connection refused"))
	step := exec.NewPublishSnapshotStep("publish", publishSnapshotPlan(),
		exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "concourse"}, delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			return publisher.Publication{}, wrapped
		}), nil)

	ok, err := step.Run(context.Background(), state)
	require.False(t, ok)
	require.ErrorIs(t, err, publisher.ErrActionsSuppressed)
	require.Contains(t, err.Error(), publisher.ErrActionsSuppressed.Error())
	require.Contains(t, err.Error(), "fly agent actions resume")
	// The refusal is neither anonymized into the generic failure nor allowed to
	// carry the DB connection detail the wrapped variant appends.
	require.NotContains(t, err.Error(), "publication execution failed")
	require.NotContains(t, err.Error(), "10.9.9.9")
	require.NotContains(t, err.Error(), "connection refused")
	require.Equal(t, 0, delegate.FinishedCallCount())
}

func publishSnapshotPlan() atc.PublishSnapshotPlan {
	return atc.PublishSnapshotPlan{
		Name: "publish-change", Publisher: publisher.GitPublisher, Input: "change",
		InputType: snapshot.TypeRef("repository-change/v1"), Destination: "github.example/team/repo",
		Mode: publisher.ModePullRequest, Parameters: map[string]string{
			"source_branch": "agent/change", "target_branch": "main",
		},
		ApprovalPolicyVersion: "engineering/v2",
	}
}

// publishMergeBaseFixture is the base_sha the repository-change contract
// validator sealed into the fixture's intrinsic metadata. Nothing authors it;
// the step reads it back out of the snapshot it publishes.
const publishMergeBaseFixture = "b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0"

func publishSnapshotManifest() snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: 77, Type: snapshot.TypeRef("repository-change/v1"),
		Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)), ByteSize: 1024, FileCount: 3,
		Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable,
		IntrinsicMetadata: repositoryChangeIntrinsicMetadata(publishMergeBaseFixture),
		CreatedAt:         time.Now().UTC(),
	}
}

func publishSnapshotRef(manifest snapshot.Snapshot) *snapshot.SnapshotRef {
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	return &ref
}

func registerPublishArtifact(t *testing.T, repository *build.Repository, manifest snapshot.Snapshot) {
	t.Helper()
	require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"change": {Artifact: &loadArtifact{handle: "sealed-change"}, Snapshot: publishSnapshotRef(manifest)},
	}))
}
