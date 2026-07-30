package exec_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/concourse/concourse/agent/workflowwait"
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

type prApprovalVerifierFunc func(context.Context, publisher.PRApprovalRequest) (publisher.ApprovalEvidence, error)

func (function prApprovalVerifierFunc) Verify(ctx context.Context, request publisher.PRApprovalRequest) (publisher.ApprovalEvidence, error) {
	return function(ctx, request)
}

type prRevisionExecutorFunc func(context.Context, publisher.PRRevisionPublicationRequest) error

func (function prRevisionExecutorFunc) ExecutePRRevision(ctx context.Context, request publisher.PRRevisionPublicationRequest) error {
	return function(ctx, request)
}

type evidenceVerifierFunc func(context.Context, publisher.EvidenceRequest) (publisher.PublicationEvidence, error)

func (function evidenceVerifierFunc) Verify(ctx context.Context, request publisher.EvidenceRequest) (publisher.PublicationEvidence, error) {
	return function(ctx, request)
}

type prImpactVerifierFunc func(context.Context, publisher.PRImpactVerificationRequest) (contracts.PublishImpactBody, error)

func (function prImpactVerifierFunc) VerifyPRImpact(ctx context.Context, request publisher.PRImpactVerificationRequest) (contracts.PublishImpactBody, error) {
	return function(ctx, request)
}

func TestPublishSnapshotStepAuthorizesExactSealedInputAndPublishes(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	repository, state, delegates, delegate := loadSnapshotHarness()
	registerPublishArtifact(t, repository, manifest)
	plan := publishSnapshotPlan()
	content := configurePassingPublishValidation(t, repository, metadata, manifest, &plan)

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
		publishStepMetadata("alice"), delegates, metadata, executor, nil, exec.WithPublishSnapshotContentStore(content))

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
	require.GreaterOrEqual(t, metadata.GetAuthorizedCallCount(), 3)
	require.Equal(t, 1, delegate.FinishedCallCount())

	plan.Parameters["target_branch"] = "mutated"
	require.Equal(t, "main", captured.Parameters["target_branch"])
}

func TestPublishSnapshotValidationRequirementRejectsBeforeExecutor(t *testing.T) {
	manifest := publishSnapshotManifest()
	authority := publishValidationAuthority(t, "change")
	validation, _ := publishValidationManifest(t, *publishSnapshotRef(manifest), publishValidationBody(*publishSnapshotRef(manifest), authority))
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedCalls(func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		if id == manifest.ID {
			return manifest, true, nil
		}
		if id == validation.ID {
			return validation, true, nil
		}
		return snapshot.Snapshot{}, false, nil
	})
	repository, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, repository, manifest)
	validationRef := snapshot.SnapshotRef{ID: validation.ID, Type: validation.Type, Digest: validation.Digest}
	require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"validation": {Artifact: &loadArtifact{handle: "sealed-validation"}, Snapshot: &validationRef},
	}))
	plan := publishSnapshotPlan()
	plan.Validation = "validation"
	plan.PublishValidation = &atc.PublishValidationRequirement{Candidate: "change", Validation: "validation"}
	called := false
	step := exec.NewPublishSnapshotStep("publish", plan, publishStepMetadata("alice"), delegates, metadata, publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
		called = true
		return publisher.Publication{}, nil
	}), nil, exec.WithPublishSnapshotContentStore(new(snapshotfakes.FakeContentStore)))
	var ok bool
	var err error
	require.NotPanics(t, func() { ok, err = step.Run(context.Background(), state) })
	require.False(t, ok)
	require.ErrorContains(t, err, "authoritative validation plan is unavailable")
	require.False(t, called)
}

func TestPublishSnapshotStepRejectsPublicationWithoutStoreVerifiedWorkflowRun(t *testing.T) {
	manifest := publishSnapshotManifest()
	metadata := new(snapshotfakes.FakeMetadataStore)
	metadata.GetAuthorizedReturns(manifest, true, nil)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), manifest)
	plan := publishSnapshotPlan()
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, manifest, &plan)
	step := exec.NewPublishSnapshotStep("publish", plan,
		publishStepMetadata("alice"), delegates, metadata,
		publisherExecutorFunc(func(_ context.Context, request publisher.Request) (publisher.Publication, error) {
			key, err := request.OperationKey()
			require.NoError(t, err)
			return publisher.Publication{
				OperationKey: key, Request: request, Status: publisher.StatusSucceeded, Attempt: 1,
				Result: publisher.Result{Status: publisher.StatusSucceeded},
			}, nil
		}), nil, exec.WithPublishSnapshotContentStore(content))

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
	plan := publishSnapshotPlan()
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, manifest, &plan)
	step := exec.NewPublishSnapshotStep("publish", plan,
		publishStepMetadata("alice"), delegates, metadata,
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
		}), nil, exec.WithPublishSnapshotContentStore(content))

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
	plan := publishSnapshotPlan()
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, manifest, &plan)
	step := exec.NewPublishSnapshotStep("publish", plan,
		publishStepMetadata("alice"), delegates, metadata,
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
		}), nil, exec.WithPublishSnapshotContentStore(content))

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
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, manifest, &plan)
	called := false
	step := exec.NewPublishSnapshotStep("publish", plan,
		publishStepMetadata("alice"), delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			called = true
			return publisher.Publication{}, nil
		}), nil, exec.WithPublishSnapshotContentStore(content))
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
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, change, &plan, answer)
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
		publishStepMetadata("alice"), delegates, metadata, executor, verifier, exec.WithPublishSnapshotContentStore(content))

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
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, change, &plan)
	verified := false
	published := false
	step := exec.NewPublishSnapshotStep("publish", plan,
		publishStepMetadata("alice"), delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			published = true
			return publisher.Publication{}, nil
		}),
		mergeApprovalVerifierFunc(func(context.Context, publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error) {
			verified = true
			return publisher.ApprovalEvidence{}, nil
		}), exec.WithPublishSnapshotContentStore(content))

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
	content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, change, &plan)
	published := false
	step := exec.NewPublishSnapshotStep("publish", plan,
		publishStepMetadata("alice"), delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			published = true
			return publisher.Publication{}, nil
		}),
		mergeApprovalVerifierFunc(func(context.Context, publisher.MergeApprovalRequest) (publisher.ApprovalEvidence, error) {
			return publisher.ApprovalEvidence{}, nil
		}), exec.WithPublishSnapshotContentStore(content))

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
			plan := publishSnapshotPlan()
			content := configurePassingPublishValidation(t, state.ArtifactRepository(), metadata, manifest, &plan)
			step := exec.NewPublishSnapshotStep("publish", plan,
				publishStepMetadata("concourse"), delegates, metadata,
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
				}), nil, exec.WithPublishSnapshotContentStore(content))
			ok, err := step.Run(context.Background(), state)
			require.False(t, ok)
			require.EqualError(t, err, test.want)
			require.NotContains(t, err.Error(), "private")
		})
	}
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

// configurePassingPublishValidation adds the same sealed, exact validation
// evidence production plans supply before a repository change can publish.
// Keeping legacy publisher assertions behind real evidence prevents test-only
// execution paths from weakening the fail-closed gate.
func configurePassingPublishValidation(t *testing.T, repository *build.Repository, metadata *snapshotfakes.FakeMetadataStore, candidate snapshot.Snapshot, plan *atc.PublishSnapshotPlan, extra ...snapshot.Snapshot) *snapshotfakes.FakeContentStore {
	t.Helper()
	authority := publishValidationAuthority(t, plan.Input)
	plan.Validation = "validation"
	plan.PublishValidation = &atc.PublishValidationRequirement{Candidate: plan.Input, Validation: "validation", Authority: &authority}
	validation, archive := publishValidationManifest(t, *publishSnapshotRef(candidate), publishValidationBody(*publishSnapshotRef(candidate), authority))
	validationRef := snapshot.SnapshotRef{ID: validation.ID, Type: validation.Type, Digest: validation.Digest}
	require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		"validation": {Artifact: &loadArtifact{handle: "sealed-validation"}, Snapshot: &validationRef},
	}))
	manifests := map[snapshot.SnapshotID]snapshot.Snapshot{candidate.ID: candidate, validation.ID: validation}
	for _, manifest := range extra {
		manifests[manifest.ID] = manifest
	}
	metadata.GetAuthorizedCalls(func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		manifest, found := manifests[id]
		return manifest, found, nil
	})
	content := new(snapshotfakes.FakeContentStore)
	content.OpenCalls(func(_ context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
		if manifest.ID != validation.ID {
			return nil, fmt.Errorf("unexpected validation content snapshot")
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	})
	return content
}

func publishValidationAuthority(t *testing.T, candidate string) atc.DevValidationAuthority {
	t.Helper()
	profile := []byte("schema_version: 1\nname: exact-gates\nchecks:\n  - id: tests\n    operation: test\n    scope: full\n    timeout: 20m\n    retries: 0\n")
	config := []byte("schema_version: 1\nrepo:\n  test: {cmd: [\"go\", \"test\", \"./...\"]}\ncomponents:\n  - id: repository\n    description: repository\n    paths: [\"src/\"]\n    kind: other\n")
	imageDigest := publishValidationDigest([]byte("image"))
	authority := atc.DevValidationAuthority{CandidateInput: candidate, ProfileName: "exact-gates", Profile: profile, ProfileDigest: publishValidationDigest(profile), ProtectedConfig: config, ProtectedConfigDigest: publishValidationDigest(config), CapabilityImage: "registry.example/dev-capability@" + imageDigest.String(), CapabilityImageDigest: imageDigest, WorkflowDefinitionID: 73, WorkflowVersion: 5}
	require.NoError(t, authority.Validate())
	return authority
}

func publishValidationBody(candidate snapshot.SnapshotRef, authority atc.DevValidationAuthority) contracts.ValidationBody {
	return contracts.ValidationBody{Conclusion: "passed", Summary: "authoritative validation", Attestation: contracts.ValidationAttestation{CandidateDigest: candidate.Digest, ProfileDigest: authority.ProfileDigest, ProtectedConfigDigest: authority.ProtectedConfigDigest, CapabilityImage: authority.CapabilityImage, CapabilityImageDigest: authority.CapabilityImageDigest, WorkflowDefinitionID: authority.WorkflowDefinitionID, WorkflowVersion: authority.WorkflowVersion, Toolchain: "dev-capability/" + authority.CapabilityImageDigest.String()}, Checks: []contracts.ValidationCheck{{ID: "tests", Kind: "test", Name: "tests", Status: "passed", Attempts: []contracts.ValidationAttempt{{Number: 1, Status: "passed", Duration: "1s", Log: contracts.ValidationLog{Path: "content/logs/tests/attempt-1.log", Digest: publishValidationDigest(nil), MediaType: "text/plain"}}}}}}
}

func publishValidationManifest(t *testing.T, candidate snapshot.SnapshotRef, body contracts.ValidationBody) (snapshot.Snapshot, []byte) {
	t.Helper()
	record, err := contracts.NewRecord(snapshot.TypeRef("validation/v1"), []contracts.Subject{contracts.SubjectFromInput("primary", contracts.SubjectRolePrimary, "change", candidate)}, body)
	require.NoError(t, err)
	document, err := json.Marshal(record)
	require.NoError(t, err)
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, file := range []struct {
		name string
		data []byte
	}{{"content/logs/tests/attempt-1.log", nil}, {"record.json", document}} {
		require.NoError(t, writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.data)), Typeflag: tar.TypeReg}))
		_, err := writer.Write(file.data)
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	raw := archive.Bytes()
	return snapshot.Snapshot{ID: 88, Type: snapshot.TypeRef("validation/v1"), Digest: publishValidationDigest(raw), ByteSize: int64(len(raw)), FileCount: 2, Representation: "application/x-tar", ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC()}, append([]byte(nil), raw...)
}

func publishValidationDigest(raw []byte) snapshot.Digest {
	sum := sha256.Sum256(raw)
	return snapshot.Digest(fmt.Sprintf("sha256:%x", sum))
}

func publishStepMetadata(actor string) exec.StepMetadata {
	definitionID, version := 73, 5
	return exec.StepMetadata{TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: actor, WorkflowDefinitionID: &definitionID, WorkflowVersion: &version}
}

func TestAwaitAndPublishSnapshotStepsBindExactPRReapprovalContext(t *testing.T) {
	change := publishSnapshotManifest()
	observation, observationArchive := publishPRObservationFixture(t, 91)
	response, responseArchive := publishPRResponseFixture(t, 93, observation)
	answer := publishOpaqueManifest(94, "human-answer/v1", []byte("approval answer"))
	question := publishOpaqueManifest(95, "question/v1", []byte("server question"))
	review := publishOpaqueManifest(96, "review/v1", []byte("accepted review"))
	acceptedCandidate := publishOpaqueManifest(97, "repository/v1", []byte("accepted candidate"))
	acceptedValidation := publishOpaqueManifest(98, "validation/v1", []byte("accepted validation"))
	impact, impactArchive := publishImpactFixture(t, 92, acceptedCandidate.Digest, change.Digest, true)

	metadata := new(snapshotfakes.FakeMetadataStore)
	content := new(snapshotfakes.FakeContentStore)
	repository, state, delegates := awaitHarness(t, change, content)
	registerNamedSnapshotArtifact(t, repository, "change", change)
	registerNamedSnapshotArtifact(t, repository, "pull-request", observation)
	registerNamedSnapshotArtifact(t, repository, "publish-impact", impact)
	registerNamedSnapshotArtifact(t, repository, "response", response)
	registerNamedSnapshotArtifact(t, repository, "accepted-review", review)
	registerNamedSnapshotArtifact(t, repository, "accepted-candidate", acceptedCandidate)
	registerNamedSnapshotArtifact(t, repository, "accepted-validation", acceptedValidation)

	publishPlan := publishSnapshotPlan()
	validationContent := configurePassingPublishValidation(
		t, repository, metadata, change, &publishPlan,
		observation, impact, response, answer, review, acceptedCandidate, acceptedValidation,
	)
	validationManifest, found, err := metadata.GetAuthorized(context.Background(), 17, 88)
	require.NoError(t, err)
	require.True(t, found)
	validationOpen := validationContent.OpenStub
	content.OpenCalls(func(ctx context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
		switch manifest.ID {
		case observation.ID:
			return io.NopCloser(bytes.NewReader(observationArchive)), nil
		case impact.ID:
			return io.NopCloser(bytes.NewReader(impactArchive)), nil
		case response.ID:
			return io.NopCloser(bytes.NewReader(responseArchive)), nil
		default:
			return validationOpen(ctx, manifest)
		}
	})

	plan := atc.AwaitSnapshotPlan{
		Name: "reapproval", Type: "human-answer/v1", OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
		PRApproval: &atc.PRApprovalIntent{
			BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
			Observation: "pull-request", Candidate: "change",
			Impact: "publish-impact", Response: "response",
			Destination:           "github.example/team/repo",
			ApprovalPolicyVersion: "engineering/v3", Prompt: "Approve this exact revision?",
			AcceptedReview: &atc.PRAcceptedReviewIntent{
				Review: "accepted-review", Candidate: "accepted-candidate", Validation: "accepted-validation",
				ReviewWorkflowRunID: "7", OutcomeRevision: 2,
			},
		},
		Validation:           "validation",
		PRApprovalValidation: publishPlan.PublishValidation.Clone(),
		WorkflowDefinitionID: 7,
		WorkflowRunID:        "19",
	}
	var sealedRequest snapshot.SealRequest
	sealer := &recordingOutputSealer{stub: func(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
		sealedRequest = request.Clone()
		return map[string]snapshot.SealedOutput{
			"question": {Port: snapshot.Port{Name: "question", Type: "question/v1"}, Snapshot: awaitRef(question)},
		}, nil
	}}
	var waitRequest workflowwait.CreateRequest
	store := &waitStoreStub{}
	store.create = func(_ context.Context, request workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
		waitRequest = request
		answerRef := awaitRef(answer)
		return waitFromRequest(31, request, workflowwait.StatusResolved, &answerRef), true, nil
	}
	store.expire = func(context.Context, workflowwait.ExecutionKey, time.Time) (workflowwait.Wait, bool, error) {
		return workflowwait.Wait{}, false, errors.New("resolved wait must not poll")
	}
	acceptedEvidence := publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review: awaitRef(review), Candidate: awaitRef(acceptedCandidate), Validation: awaitRef(acceptedValidation),
			ReviewWorkflowRunID: 7, OutcomeRevision: 2, AcceptedBy: "reviewer",
			AcceptedAt: time.Date(2026, time.July, 30, 6, 7, 8, 0, time.UTC),
		},
	}
	evidenceVerifier := evidenceVerifierFunc(func(_ context.Context, request publisher.EvidenceRequest) (publisher.PublicationEvidence, error) {
		require.Equal(t, 17, request.TeamID)
		require.NotNil(t, request.AcceptedReview)
		require.Equal(t, publisher.AcceptedReviewEvidenceRequest{
			Review: awaitRef(review), Candidate: awaitRef(acceptedCandidate), Validation: awaitRef(acceptedValidation),
			ReviewWorkflowRunID: 7, OutcomeRevision: 2,
		}, *request.AcceptedReview)
		return acceptedEvidence, nil
	})
	var impactRequests []publisher.PRImpactVerificationRequest
	impactVerifier := prImpactVerifierFunc(func(_ context.Context, request publisher.PRImpactVerificationRequest) (contracts.PublishImpactBody, error) {
		require.NoError(t, request.Validate())
		impactRequests = append(impactRequests, request)
		return request.Body, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	waitStep := exec.NewAwaitSnapshotStep("plan-pr", []int{2}, plan, exec.StepMetadata{
		TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "build:42",
		WorkflowDefinitionID: intPointer(73), WorkflowVersion: intPointer(5),
	}, delegates, store, sealer, metadata, content, time.Millisecond,
		exec.WithAwaitSnapshotPREvidenceVerifier(evidenceVerifier),
		exec.WithAwaitSnapshotPRImpactVerifier(impactVerifier),
	)
	ok, err := waitStep.Run(ctx, state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "reapproval-pr-question", waitRequest.QuestionName)
	require.Equal(t, []string{
		"pull-request", "change", "validation", "publish-impact", "response",
		"accepted-review", "accepted-candidate", "accepted-validation",
	}, sealedRequest.InputOrder)

	stream, err := sealedRequest.Outputs[0].OpenTar(context.Background())
	require.NoError(t, err)
	defer stream.Close()
	archive := tar.NewReader(stream)
	header, err := archive.Next()
	require.NoError(t, err)
	require.Equal(t, "question.json", header.Name)
	var document contracts.QuestionDocument
	require.NoError(t, json.NewDecoder(archive).Decode(&document))
	var contextEnvelope publisher.PRApprovalContext
	require.NoError(t, json.Unmarshal([]byte(document.Context), &contextEnvelope))
	require.Equal(t, int64(41), contextEnvelope.BindingID)
	require.Equal(t, awaitRef(observation).ID, contextEnvelope.ObservationSnapshotID)
	require.Equal(t, observation.Digest, contextEnvelope.ObservationDigest)
	require.Equal(t, awaitRef(change).ID, contextEnvelope.CandidateSnapshotID)
	require.Equal(t, change.Digest, contextEnvelope.CandidateDigest)
	require.Equal(t, strings.Repeat("a", 40), contextEnvelope.SourceHead)
	require.Equal(t, strings.Repeat("b", 40), contextEnvelope.TargetHead)
	require.Equal(t, publishPlan.Destination, contextEnvelope.Destination)
	require.Equal(t, awaitRef(response).ID, contextEnvelope.ResponseSnapshotID)
	require.Equal(t, response.Digest, contextEnvelope.ResponseDigest)
	require.Equal(t, awaitRef(validationManifest).ID, contextEnvelope.ValidationSnapshotID)
	require.Equal(t, impact.Digest, contextEnvelope.ImpactDigest)

	publishPlan.Approval = "reapproval"
	publishPlan.WorkflowRunID = "19"
	publishPlan.ApprovalPolicyVersion = "engineering/v3"
	publishPlan.PRApproval = &atc.PRApprovalPublicationIntent{
		BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
		Observation: "pull-request", Impact: "publish-impact", Response: "response",
		AcceptedReview: &atc.PRAcceptedReviewIntent{
			Review: "accepted-review", Candidate: "accepted-candidate", Validation: "accepted-validation",
			ReviewWorkflowRunID: "7", OutcomeRevision: 2,
		},
	}
	var verified publisher.PRApprovalRequest
	approvalEvidence := publisher.ApprovalEvidence{
		WaitID: 31, Question: awaitRef(question), Answer: awaitRef(answer),
		ResolvedBy: "alice", ResolvedAt: time.Now().UTC(),
	}
	verifier := prApprovalVerifierFunc(func(_ context.Context, request publisher.PRApprovalRequest) (publisher.ApprovalEvidence, error) {
		verified = request
		return approvalEvidence, nil
	})
	legacyCalled := false
	legacy := publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
		legacyCalled = true
		return publisher.Publication{}, errors.New("legacy publisher must not receive provider-native PR revisions")
	})

	missingHandoff := exec.NewPublishSnapshotStep(
		"publish-pr", publishPlan, publishStepMetadata("alice"), delegates, metadata, legacy, nil,
		exec.WithPublishSnapshotContentStore(content),
		exec.WithPublishSnapshotPRApprovalVerifier(verifier),
		exec.WithPublishSnapshotEvidenceVerifier(evidenceVerifier),
		exec.WithPublishSnapshotPRImpactVerifier(impactVerifier),
	)
	ok, err = missingHandoff.Run(context.Background(), state)
	require.False(t, ok)
	require.ErrorContains(t, err, "provider-native PR revision execution is unavailable")
	require.False(t, legacyCalled)

	var handedOff publisher.PRRevisionPublicationRequest
	prExecutor := prRevisionExecutorFunc(func(_ context.Context, request publisher.PRRevisionPublicationRequest) error {
		handedOff = request
		return nil
	})
	publishStep := exec.NewPublishSnapshotStep(
		"publish-pr", publishPlan, publishStepMetadata("alice"), delegates, metadata, nil, nil,
		exec.WithPublishSnapshotContentStore(content),
		exec.WithPublishSnapshotPRApprovalVerifier(verifier),
		exec.WithPublishSnapshotEvidenceVerifier(evidenceVerifier),
		exec.WithPublishSnapshotPRImpactVerifier(impactVerifier),
		exec.WithPublishSnapshotPRRevisionExecutor(prExecutor),
	)
	ok, err = publishStep.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, legacyCalled)
	require.Equal(t, publisher.PRApprovalRequest{
		TeamID: 17, WorkflowRunID: 19, BuildID: 42, Approval: awaitRef(answer),
		BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
		Observation: awaitRef(observation), Candidate: awaitRef(change),
		SourceHead: strings.Repeat("a", 40), TargetHead: strings.Repeat("b", 40),
		Destination: publishPlan.Destination,
		Response:    awaitRef(response), Validation: awaitRef(validationManifest), Impact: awaitRef(impact),
		ApprovalPolicyVersion: "engineering/v3",
	}, verified)
	require.Equal(t, publisher.Authority{
		TeamID: 17, TeamName: "main", BuildID: 42, WorkflowRunID: 19, Actor: "alice",
	}, handedOff.Authority)
	require.Equal(t, awaitRef(observation), handedOff.Observation)
	require.Equal(t, awaitRef(change), handedOff.Candidate)
	require.Equal(t, awaitRef(validationManifest), handedOff.Validation)
	require.Equal(t, awaitRef(impact), handedOff.Impact)
	require.Equal(t, awaitRef(response), handedOff.Response)
	require.Equal(t, publishPlan.Destination, handedOff.Destination)
	require.Equal(t, publisher.PublicationEvidence{
		Kind: publisher.EvidenceHumanWait, HumanWait: &approvalEvidence,
	}, handedOff.Evidence)
	require.NotNil(t, handedOff.ApprovalContext)
	require.Equal(t, contextEnvelope, *handedOff.ApprovalContext)
	require.Len(t, impactRequests, 2)
	for _, request := range impactRequests {
		require.Equal(t, awaitRef(acceptedCandidate), request.Baseline)
		require.Equal(t, awaitRef(change), request.Candidate)
		require.Equal(t, int64(41), request.BindingID)
		require.Equal(t, snapshot.Digest("sha256:"+strings.Repeat("1", 64)), request.ActionDigest)
		require.Equal(t, awaitRef(observation), request.Observation)
		require.Equal(t, awaitRef(validationManifest), request.Validation)
		require.Equal(t, awaitRef(impact), request.Impact)
		require.Equal(t, awaitRef(response), request.Response)
		require.Equal(t, acceptedEvidence, request.AcceptedReview)
		require.Equal(t, acceptedCandidate.Digest.String(), request.Body.BaselineDigest)
	}
}

func TestAwaitSnapshotStepSkipsHumanWaitWhenExactImpactDoesNotRequireReapproval(t *testing.T) {
	change := publishSnapshotManifest()
	observation, observationArchive := publishPRObservationFixture(t, 91)
	response, responseArchive := publishPRResponseFixture(t, 93, observation)
	review := publishOpaqueManifest(94, "review/v1", []byte("accepted review"))
	acceptedCandidate := publishOpaqueManifest(95, "repository/v1", []byte("accepted candidate"))
	acceptedValidation := publishOpaqueManifest(96, "validation/v1", []byte("accepted validation"))
	impact, impactArchive := publishImpactFixture(t, 92, acceptedCandidate.Digest, change.Digest, false)

	metadata := new(snapshotfakes.FakeMetadataStore)
	content := new(snapshotfakes.FakeContentStore)
	repository, state, delegates := awaitHarness(t, change, content)
	registerNamedSnapshotArtifact(t, repository, "change", change)
	registerNamedSnapshotArtifact(t, repository, "pull-request", observation)
	registerNamedSnapshotArtifact(t, repository, "publish-impact", impact)
	registerNamedSnapshotArtifact(t, repository, "response", response)
	registerNamedSnapshotArtifact(t, repository, "accepted-review", review)
	registerNamedSnapshotArtifact(t, repository, "accepted-candidate", acceptedCandidate)
	registerNamedSnapshotArtifact(t, repository, "accepted-validation", acceptedValidation)
	publishPlan := publishSnapshotPlan()
	validationContent := configurePassingPublishValidation(
		t, repository, metadata, change, &publishPlan, observation, impact, response,
		review, acceptedCandidate, acceptedValidation,
	)
	validationOpen := validationContent.OpenStub
	content.OpenCalls(func(ctx context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
		switch manifest.ID {
		case observation.ID:
			return io.NopCloser(bytes.NewReader(observationArchive)), nil
		case impact.ID:
			return io.NopCloser(bytes.NewReader(impactArchive)), nil
		case response.ID:
			return io.NopCloser(bytes.NewReader(responseArchive)), nil
		default:
			return validationOpen(ctx, manifest)
		}
	})
	plan := atc.AwaitSnapshotPlan{
		Name: "reapproval", Type: "human-answer/v1", OnTimeout: atc.AwaitSnapshotOnTimeoutFail,
		PRApproval: &atc.PRApprovalIntent{
			BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
			Observation: "pull-request", Candidate: "change",
			Impact: "publish-impact", Response: "response",
			Destination:           publishPlan.Destination,
			ApprovalPolicyVersion: "engineering/v3", Prompt: "Approve this exact revision?",
			AcceptedReview: &atc.PRAcceptedReviewIntent{
				Review: "accepted-review", Candidate: "accepted-candidate", Validation: "accepted-validation",
				ReviewWorkflowRunID: "7", OutcomeRevision: 2,
			},
		},
		Validation:           "validation",
		PRApprovalValidation: publishPlan.PublishValidation.Clone(),
		WorkflowDefinitionID: 7,
		WorkflowRunID:        "19",
	}
	store := &waitStoreStub{
		create: func(context.Context, workflowwait.CreateRequest) (workflowwait.Wait, bool, error) {
			return workflowwait.Wait{}, false, errors.New("no human wait should be created")
		},
		expire: func(context.Context, workflowwait.ExecutionKey, time.Time) (workflowwait.Wait, bool, error) {
			return workflowwait.Wait{}, false, errors.New("no human wait should be polled")
		},
	}
	sealer := &recordingOutputSealer{err: errors.New("no approval question should be sealed")}
	acceptedEvidence := publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review: awaitRef(review), Candidate: awaitRef(acceptedCandidate), Validation: awaitRef(acceptedValidation),
			ReviewWorkflowRunID: 7, OutcomeRevision: 2, AcceptedBy: "reviewer",
			AcceptedAt: time.Date(2026, time.July, 30, 6, 7, 8, 0, time.UTC),
		},
	}
	var impactRequest publisher.PRImpactVerificationRequest
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	step := exec.NewAwaitSnapshotStep(
		"plan-pr", nil, plan,
		exec.StepMetadata{
			TeamID: 17, TeamName: "main", BuildID: 42, SnapshotCreatedBy: "build:42",
			WorkflowDefinitionID: intPointer(73), WorkflowVersion: intPointer(5),
		},
		delegates, store, sealer, metadata, content, time.Millisecond,
		exec.WithAwaitSnapshotPREvidenceVerifier(evidenceVerifierFunc(func(_ context.Context, request publisher.EvidenceRequest) (publisher.PublicationEvidence, error) {
			require.Equal(t, 17, request.TeamID)
			require.NotNil(t, request.AcceptedReview)
			return acceptedEvidence, nil
		})),
		exec.WithAwaitSnapshotPRImpactVerifier(prImpactVerifierFunc(func(_ context.Context, request publisher.PRImpactVerificationRequest) (contracts.PublishImpactBody, error) {
			require.NoError(t, request.Validate())
			impactRequest = request
			return request.Body, nil
		})),
	)

	ok, err := step.Run(ctx, state)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, store.createCall)
	require.Zero(t, store.expireCall)
	require.Empty(t, sealer.calls)
	_, present := repository.ArtifactEntryFor("reapproval")
	require.False(t, present)
	require.Equal(t, int64(41), impactRequest.BindingID)
	require.Equal(t, awaitRef(observation), impactRequest.Observation)
	require.Equal(t, awaitRef(response), impactRequest.Response)
	require.Equal(t, awaitRef(acceptedCandidate), impactRequest.Baseline)
	require.Equal(
		t,
		awaitRef(acceptedValidation),
		impactRequest.BaselineValidation,
	)
	require.Equal(t, acceptedCandidate.Digest.String(), impactRequest.Body.BaselineDigest)
}

func TestPublishSnapshotStepUsesAcceptedReviewWhenImpactDoesNotRequireReapproval(t *testing.T) {
	change := publishSnapshotManifest()
	observation, observationArchive := publishPRObservationFixture(t, 91)
	response, responseArchive := publishPRResponseFixture(t, 93, observation)
	review := publishOpaqueManifest(94, "review/v1", []byte("accepted review"))
	acceptedCandidate := publishOpaqueManifest(95, "repository/v1", []byte("accepted candidate"))
	acceptedValidation := publishOpaqueManifest(96, "validation/v1", []byte("accepted validation"))
	impact, impactArchive := publishImpactFixture(t, 92, acceptedCandidate.Digest, change.Digest, false)
	metadata := new(snapshotfakes.FakeMetadataStore)
	_, state, delegates, _ := loadSnapshotHarness()
	registerPublishArtifact(t, state.ArtifactRepository(), change)
	for name, manifest := range map[string]snapshot.Snapshot{
		"pull-request": observation, "publish-impact": impact, "response": response,
		"accepted-review": review, "accepted-candidate": acceptedCandidate, "accepted-validation": acceptedValidation,
	} {
		registerNamedSnapshotArtifact(t, state.ArtifactRepository(), name, manifest)
	}
	plan := publishSnapshotPlan()
	content := configurePassingPublishValidation(
		t, state.ArtifactRepository(), metadata, change, &plan,
		observation, impact, response, review, acceptedCandidate, acceptedValidation,
	)
	validationOpen := content.OpenStub
	content.OpenCalls(func(ctx context.Context, manifest snapshot.Snapshot) (io.ReadCloser, error) {
		switch manifest.ID {
		case observation.ID:
			return io.NopCloser(bytes.NewReader(observationArchive)), nil
		case impact.ID:
			return io.NopCloser(bytes.NewReader(impactArchive)), nil
		case response.ID:
			return io.NopCloser(bytes.NewReader(responseArchive)), nil
		default:
			return validationOpen(ctx, manifest)
		}
	})
	plan.Approval = "reapproval"
	plan.WorkflowRunID = "19"
	plan.PRApproval = &atc.PRApprovalPublicationIntent{
		BindingID: 41, ActionDigest: "sha256:" + strings.Repeat("1", 64),
		Observation: "pull-request", Impact: "publish-impact", Response: "response",
		AcceptedReview: &atc.PRAcceptedReviewIntent{
			Review: "accepted-review", Candidate: "accepted-candidate", Validation: "accepted-validation",
			ReviewWorkflowRunID: "7", OutcomeRevision: 2,
		},
	}
	acceptedEvidence := publisher.AcceptedReviewEvidence{
		Review: awaitRef(review), Candidate: awaitRef(acceptedCandidate), Validation: awaitRef(acceptedValidation),
		ReviewWorkflowRunID: 7, OutcomeRevision: 2, AcceptedBy: "reviewer",
		AcceptedAt: time.Date(2026, time.July, 30, 6, 7, 8, 0, time.UTC),
	}
	humanVerified, acceptedVerified, legacyPublished, prPublished := false, false, false, false
	exactEvidenceVerifier := evidenceVerifierFunc(func(_ context.Context, request publisher.EvidenceRequest) (publisher.PublicationEvidence, error) {
		acceptedVerified = true
		require.Equal(t, 17, request.TeamID)
		require.NotNil(t, request.AcceptedReview)
		require.Equal(t, publisher.AcceptedReviewEvidenceRequest{
			Review: awaitRef(review), Candidate: awaitRef(acceptedCandidate), Validation: awaitRef(acceptedValidation),
			ReviewWorkflowRunID: 7, OutcomeRevision: 2,
		}, *request.AcceptedReview)
		return publisher.PublicationEvidence{
			Kind: publisher.EvidenceAcceptedReview, AcceptedReview: &acceptedEvidence,
		}, nil
	})
	missingLegacyCalled, missingPRCalled := false, false
	missingVerifier := exec.NewPublishSnapshotStep(
		"publish-pr", plan, publishStepMetadata("alice"), delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			missingLegacyCalled = true
			return publisher.Publication{}, nil
		}),
		nil,
		exec.WithPublishSnapshotContentStore(content),
		exec.WithPublishSnapshotEvidenceVerifier(exactEvidenceVerifier),
		exec.WithPublishSnapshotPRRevisionExecutor(prRevisionExecutorFunc(func(context.Context, publisher.PRRevisionPublicationRequest) error {
			missingPRCalled = true
			return nil
		})),
	)
	ok, err := missingVerifier.Run(context.Background(), state)
	require.False(t, ok)
	require.ErrorContains(t, err, "exact PR impact verification is unavailable")
	require.False(t, missingLegacyCalled)
	require.False(t, missingPRCalled)

	mismatchedPRCalled := false
	mismatchedImpact := exec.NewPublishSnapshotStep(
		"publish-pr", plan, publishStepMetadata("alice"), delegates, metadata, nil, nil,
		exec.WithPublishSnapshotContentStore(content),
		exec.WithPublishSnapshotEvidenceVerifier(exactEvidenceVerifier),
		exec.WithPublishSnapshotPRImpactVerifier(prImpactVerifierFunc(func(_ context.Context, request publisher.PRImpactVerificationRequest) (contracts.PublishImpactBody, error) {
			require.NoError(t, request.Validate())
			body := request.Body
			body.ReapprovalRequired = true
			body.Reasons = []string{"The impact verifier returned a different decision."}
			return body, nil
		})),
		exec.WithPublishSnapshotPRRevisionExecutor(prRevisionExecutorFunc(func(context.Context, publisher.PRRevisionPublicationRequest) error {
			mismatchedPRCalled = true
			return nil
		})),
	)
	ok, err = mismatchedImpact.Run(context.Background(), state)
	require.False(t, ok)
	require.ErrorContains(t, err, "exact PR impact verification failed")
	require.False(t, mismatchedPRCalled)

	var impactRequest publisher.PRImpactVerificationRequest
	var handedOff publisher.PRRevisionPublicationRequest
	step := exec.NewPublishSnapshotStep(
		"publish-pr", plan, publishStepMetadata("alice"), delegates, metadata,
		publisherExecutorFunc(func(context.Context, publisher.Request) (publisher.Publication, error) {
			legacyPublished = true
			return publisher.Publication{}, nil
		}),
		nil,
		exec.WithPublishSnapshotContentStore(content),
		exec.WithPublishSnapshotPRApprovalVerifier(prApprovalVerifierFunc(func(context.Context, publisher.PRApprovalRequest) (publisher.ApprovalEvidence, error) {
			humanVerified = true
			return publisher.ApprovalEvidence{}, nil
		})),
		exec.WithPublishSnapshotEvidenceVerifier(exactEvidenceVerifier),
		exec.WithPublishSnapshotPRImpactVerifier(prImpactVerifierFunc(func(_ context.Context, request publisher.PRImpactVerificationRequest) (contracts.PublishImpactBody, error) {
			require.NoError(t, request.Validate())
			impactRequest = request
			return request.Body, nil
		})),
		exec.WithPublishSnapshotPRRevisionExecutor(prRevisionExecutorFunc(func(_ context.Context, request publisher.PRRevisionPublicationRequest) error {
			prPublished = true
			handedOff = request
			return nil
		})),
	)
	ok, err = step.Run(context.Background(), state)
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, humanVerified)
	require.True(t, acceptedVerified)
	require.False(t, legacyPublished)
	require.True(t, prPublished)
	require.Nil(t, handedOff.ApprovalContext)
	require.NotNil(t, handedOff.AcceptedReview)
	require.Equal(t, publisher.EvidenceAcceptedReview, handedOff.Evidence.Kind)
	require.Equal(t, int64(41), impactRequest.BindingID)
	require.Equal(t, awaitRef(observation), impactRequest.Observation)
	require.Equal(t, handedOff.Validation, impactRequest.Validation)
	require.Equal(t, awaitRef(response), impactRequest.Response)
	require.Equal(t, awaitRef(acceptedCandidate), impactRequest.Baseline)
	require.Equal(
		t,
		awaitRef(acceptedValidation),
		impactRequest.BaselineValidation,
	)
	require.Equal(t, acceptedCandidate.Digest.String(), impactRequest.Body.BaselineDigest)
}

func publishPRObservationFixture(t *testing.T, id snapshot.SnapshotID) (snapshot.Snapshot, []byte) {
	t.Helper()
	body := contracts.PullRequestBody{
		Provider: "github", Repository: "acme/widget", ExternalID: "42",
		URL:   "https://github.example/acme/widget/pull/42",
		State: contracts.PullRequestActive, Mergeability: contracts.PullRequestMergeable,
		SourceRef: "refs/heads/feature", SourceSHA: strings.Repeat("a", 40),
		TargetRef: "refs/heads/main", TargetSHA: strings.Repeat("b", 40),
		Iteration: "42", Trigger: contracts.PullRequestReviewBatchTrigger,
		ReviewBatches: []contracts.PullRequestReviewBatch{{
			ID: "batch-1", ReviewID: "review-1", CommitSHA: strings.Repeat("a", 40),
			Reviewer: "reviewer", Ready: true, ThreadIDs: []string{},
		}},
	}
	record, err := contracts.NewRecord(snapshot.TypeRef("pull-request/v1"), nil, body)
	require.NoError(t, err)
	return publishRecordManifest(t, id, snapshot.TypeRef("pull-request/v1"), record)
}

func publishPRResponseFixture(t *testing.T, id snapshot.SnapshotID, observation snapshot.Snapshot) (snapshot.Snapshot, []byte) {
	t.Helper()
	body := contracts.PullRequestResponseBody{
		Kind: contracts.PullRequestResponseNoResponse,
	}
	subject := contracts.SubjectFromInput(
		"pull-request", contracts.SubjectRolePrimary, "pull-request",
		snapshot.SnapshotRef{ID: observation.ID, Type: observation.Type, Digest: observation.Digest},
	)
	record, err := contracts.NewRecord(snapshot.TypeRef("pull-request-response/v1"), []contracts.Subject{subject}, body)
	require.NoError(t, err)
	return publishRecordManifest(t, id, snapshot.TypeRef("pull-request-response/v1"), record)
}

func publishImpactFixture(t *testing.T, id snapshot.SnapshotID, baseline, candidate snapshot.Digest, required bool) (snapshot.Snapshot, []byte) {
	t.Helper()
	body := contracts.PublishImpactBody{
		BaselineDigest:  baseline.String(),
		CandidateDigest: candidate.String(),
		ChangedFiles:    []contracts.PublishChangedFile{{Path: "main.go", AddedLines: 1}},
		ChangedLines:    1, ReapprovalRequired: required,
		AgentAssessment: &contracts.AgentImpactAssessment{
			ReapprovalRequired: required,
			Rationale:          "The exact candidate impact was assessed.",
		},
	}
	if required {
		body.Reasons = []string{"Agent assessment requires reapproval: The exact candidate impact was assessed."}
	}
	record, err := contracts.NewRecord(snapshot.TypeRef("publish-impact/v1"), nil, body)
	require.NoError(t, err)
	return publishRecordManifest(t, id, snapshot.TypeRef("publish-impact/v1"), record)
}

func publishRecordManifest(t *testing.T, id snapshot.SnapshotID, typ snapshot.TypeRef, record any) (snapshot.Snapshot, []byte) {
	t.Helper()
	document, err := json.Marshal(record)
	require.NoError(t, err)
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	require.NoError(t, writer.WriteHeader(&tar.Header{Name: "record.json", Mode: 0o600, Size: int64(len(document)), Typeflag: tar.TypeReg}))
	_, err = writer.Write(document)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	raw := append([]byte(nil), archive.Bytes()...)
	return snapshot.Snapshot{
		ID: id, Type: typ, Digest: publishValidationDigest(raw),
		ByteSize: int64(len(raw)), FileCount: 1, Representation: "application/x-tar",
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}, raw
}

func publishOpaqueManifest(id snapshot.SnapshotID, typ snapshot.TypeRef, content []byte) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID: id, Type: typ, Digest: publishValidationDigest(content),
		ByteSize: int64(len(content)), FileCount: 1, Representation: "application/x-tar",
		ContentState: snapshot.ContentStateAvailable, CreatedAt: time.Now().UTC(),
	}
}

func registerNamedSnapshotArtifact(t *testing.T, repository *build.Repository, name string, manifest snapshot.Snapshot) {
	t.Helper()
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	require.NoError(t, repository.RegisterArtifacts(map[build.ArtifactName]build.ArtifactEntry{
		build.ArtifactName(name): {Artifact: &loadArtifact{handle: "sealed-" + name}, Snapshot: &ref},
	}))
}
