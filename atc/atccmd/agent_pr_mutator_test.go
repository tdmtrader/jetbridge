package atccmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/publisher/directgit"
	"github.com/concourse/concourse/agent/publisher/gittransport"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc/db"
)

func TestAgentPRMutatorResolverSelectsProviderSpecificVerifiedBranchAuthentication(t *testing.T) {
	for _, test := range []struct {
		name           string
		provider       publisher.PRProvider
		repository     string
		apiBaseURL     string
		repositoryURL  string
		authentication gittransport.AuthenticationMode
	}{
		{
			name: "github", provider: publisher.PRProviderGitHub,
			repository: "acme/widget", apiBaseURL: "https://api.github.example",
			repositoryURL:  "https://github.example/acme/widget.git",
			authentication: gittransport.AuthenticationAskpass,
		},
		{
			name: "azure devops", provider: publisher.PRProviderAzureDevOps,
			repository: "project/widget", apiBaseURL: "https://dev.azure.example/acme",
			repositoryURL:  "https://dev.azure.example/acme/project/_git/widget",
			authentication: gittransport.AuthenticationBearer,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			action := validBridgeBranchAction(test.provider, test.repository)
			resolver := newBridgeResolver(t, test.provider, test.repository, test.apiBaseURL, test.repositoryURL)

			mutator, err := resolver.ResolvePRMutator(context.Background(), action)
			if err != nil {
				t.Fatalf("resolve PR mutator: %v", err)
			}
			bound, ok := mutator.(*boundPRMutator)
			if !ok {
				t.Fatalf("mutator = %T, want action-bound adapter", mutator)
			}
			if bound.authentication != test.authentication {
				t.Fatalf("branch authentication = %q, want %q", bound.authentication, test.authentication)
			}

			wrong := branchMutationForAction(t, action)
			wrong.NewSourceSHA = objectIDForBridge('d')
			if _, err := mutator.CompareAndSwapBranch(context.Background(), wrong); err == nil ||
				!strings.Contains(err.Error(), "does not match acquired PR action") {
				t.Fatalf("wrong branch action error = %v", err)
			}
		})
	}
}

func TestAgentPRMutatorResolverFailsClosedForMismatchedProviderActionWithoutCredentialLeak(t *testing.T) {
	resolver := newBridgeResolver(
		t,
		publisher.PRProviderGitHub,
		"acme/widget",
		"https://api.github.example",
		"https://github.example/acme/widget.git",
	)
	action := validBridgeBranchAction(publisher.PRProviderAzureDevOps, "project/widget")

	_, err := resolver.ResolvePRMutator(context.Background(), action)
	if err == nil {
		t.Fatal("accepted an Azure action through a GitHub policy")
	}
	for _, value := range []string{err.Error(), fmt.Sprintf("%+v", resolver)} {
		if strings.Contains(value, bridgeSecret) {
			t.Fatalf("credential leaked from resolver failure or value: %q", value)
		}
	}
}

func TestAgentPRMutatorResolverMapsExactPublisherMutations(t *testing.T) {
	actions := []publisher.PRAction{
		validBridgeBranchAction(publisher.PRProviderGitHub, "acme/widget"),
		validBridgeCreateAction(),
		validBridgeStatusAction(),
		validBridgeResponseAction(),
	}
	for _, action := range actions {
		t.Run(string(action.Kind), func(t *testing.T) {
			delegate := &capturingPullRequestMutator{}
			mutator, err := newBoundPRMutator(action, delegate, gittransport.AuthenticationAskpass)
			if err != nil {
				t.Fatalf("bind mutator: %v", err)
			}
			switch action.Kind {
			case publisher.OperationPublishPRBranch:
				mutation := branchMutationForAction(t, action)
				result, err := mutator.CompareAndSwapBranch(context.Background(), mutation)
				if err != nil {
					t.Fatalf("branch mutation: %v", err)
				}
				if result.HeadSHA != mutation.NewSourceSHA || delegate.branch.NewSourceSHA != mutation.NewSourceSHA ||
					delegate.branch.Locator.Provider != pullrequest.ProviderGitHub {
					t.Fatalf("branch mapping = %#v / %#v", result, delegate.branch)
				}
			case publisher.OperationCreatePR:
				mutation := createMutationForAction(t, action)
				result, err := mutator.FindOrCreatePullRequest(context.Background(), mutation)
				if err != nil {
					t.Fatalf("create mutation: %v", err)
				}
				if result.Locator != mutation.Locator || delegate.create.SourceSHA != mutation.SourceSHA || result.URL == "" {
					t.Fatalf("create mapping = %#v / %#v", result, delegate.create)
				}
			case publisher.OperationPublishPRStatus:
				mutation := statusMutationForAction(t, action)
				result, err := mutator.PublishValidationStatus(context.Background(), mutation)
				if err != nil {
					t.Fatalf("status mutation: %v", err)
				}
				if result.OperationKey != mutation.OperationKey || delegate.status.TargetRef != mutation.TargetRef {
					t.Fatalf("status mapping = %#v / %#v", result, delegate.status)
				}
			case publisher.OperationRespondToReview:
				mutation := responseMutationForAction(t, action)
				result, err := mutator.PublishReviewResponse(context.Background(), mutation)
				if err != nil {
					t.Fatalf("response mutation: %v", err)
				}
				if result.OperationKey != mutation.OperationKey || delegate.response.Batch.ID != mutation.Batch.ID {
					t.Fatalf("response mapping = %#v / %#v", result, delegate.response)
				}
			}
		})
	}
}

func TestAgentPRMutatorResolverRejectsInconsistentAzurePolicyRepositoryURL(t *testing.T) {
	resolver := newBridgeResolver(
		t,
		publisher.PRProviderAzureDevOps,
		"project/widget",
		"https://dev.azure.example/acme",
		"https://dev.azure.example/acme/other/_git/widget",
	)
	_, err := resolver.ResolvePRMutator(
		context.Background(),
		validBridgeBranchAction(publisher.PRProviderAzureDevOps, "project/widget"),
	)
	if err == nil || !strings.Contains(err.Error(), "repository URL") {
		t.Fatalf("inconsistent Azure repository URL error = %v", err)
	}
	if strings.Contains(fmt.Sprint(err), bridgeSecret) {
		t.Fatalf("credential leaked in Azure consistency error: %v", err)
	}
}

func TestAzureRepositoryIdentityRequiresTwoCanonicalSegments(t *testing.T) {
	for _, repository := range []string{
		"", "project", "/widget", "project/", "project/widget/extra", "project//widget",
		"project/../widget", "project/widget?query", "project/widget\n",
	} {
		t.Run(strings.ReplaceAll(repository, "/", "_"), func(t *testing.T) {
			if _, _, err := azureRepositoryIdentity(repository); err == nil {
				t.Fatalf("accepted malformed Azure repository %q", repository)
			}
		})
	}
}

func TestAgentPRMutatorResolverPropagatesCancellation(t *testing.T) {
	resolver := newBridgeResolver(
		t,
		publisher.PRProviderGitHub,
		"acme/widget",
		"https://api.github.example",
		"https://github.example/acme/widget.git",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := resolver.ResolvePRMutator(ctx, validBridgeBranchAction(publisher.PRProviderGitHub, "acme/widget"))
	if err != context.Canceled {
		t.Fatalf("cancelled resolver error = %v, want context.Canceled", err)
	}
}

const bridgeSecret = "do-not-log-bridge-token"

func newBridgeResolver(
	t *testing.T,
	provider publisher.PRProvider,
	repository, apiBaseURL, repositoryURL string,
) *agentPRMutatorResolver {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(directory, "write-token")
	if err := os.WriteFile(credentialPath, []byte(bridgeSecret), 0600); err != nil {
		t.Fatal(err)
	}
	policy := publisher.Policy{SchemaVersion: 1, Rules: []publisher.PolicyRule{{
		Team: "engineering", Publisher: publisher.GitPublisher, Mode: publisher.ModePullRequest,
		ApprovalPolicyVersion: "engineering/v3", TargetBranch: "refs/heads/main",
		Destination: "forge.example/acme/widget", Adapter: adapterForBridge(provider), Provider: provider,
		Repository: repository, APIBaseURL: apiBaseURL, RepositoryURL: repositoryURL,
		ReadCredentialReference: "read-token", WriteCredentialReference: "write-token",
	}}}
	credentials, err := publisher.NewFileCredentialProvider(policy, directory, map[string]string{"write-token": credentialPath})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := newAgentPRMutatorResolver(
		credentials,
		db.NewAgentSnapshotsFactory(openTestDB(t)),
		&compositionContentStore{},
		snapshot.Canonicalizer{MaxContentBytes: 1 << 20, MaxEntries: 100, TempDir: t.TempDir()},
		bridgeRunner{},
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func adapterForBridge(provider publisher.PRProvider) publisher.AdapterKind {
	if provider == publisher.PRProviderAzureDevOps {
		return publisher.AdapterAzureDevOps
	}
	return publisher.AdapterGitHub
}

func validBridgeBranchAction(provider publisher.PRProvider, repository string) publisher.PRAction {
	request := publisher.BranchPublicationRequest{
		Authority:   publisher.Authority{TeamID: 17, TeamName: "engineering", BuildID: 42, WorkflowRunID: 91, Actor: "alice"},
		Observation: snapshot.SnapshotRef{ID: 21, Type: "pull-request/v1", Digest: bridgeDigest('1')},
		Candidate:   snapshot.SnapshotRef{ID: 22, Type: "repository-change/v1", Digest: bridgeDigest('2')},
		Validation:  snapshot.SnapshotRef{ID: 23, Type: "validation/v1", Digest: bridgeDigest('3')},
		Impact:      snapshot.SnapshotRef{ID: 24, Type: "publish-impact/v1", Digest: bridgeDigest('4')},
		Evidence:    bridgeEvidence(), Destination: "forge.example/acme/widget", ApprovalPolicyVersion: "engineering/v3",
		Locator:   publisher.PRLocator{Provider: provider, Repository: repository},
		SourceRef: "refs/heads/agent/upgrade", TargetRef: "refs/heads/main",
		ExpectedSource:    contracts.PullRequestHeadExpectation{Exists: true, SHA: objectIDForBridge('a')},
		ExpectedTargetSHA: objectIDForBridge('b'), NewSourceSHA: objectIDForBridge('c'),
	}
	return publisher.PRAction{Kind: publisher.OperationPublishPRBranch, Branch: &request}
}

func validBridgeCreateAction() publisher.PRAction {
	branch := validBridgeBranchAction(publisher.PRProviderGitHub, "acme/widget").Branch
	request := publisher.PullRequestPublicationRequest{
		Authority: branch.Authority, Observation: branch.Observation, Candidate: branch.Candidate,
		Validation: branch.Validation, Impact: branch.Impact, Evidence: branch.Evidence,
		Destination: branch.Destination, ApprovalPolicyVersion: branch.ApprovalPolicyVersion,
		Locator: branch.Locator, SourceRef: branch.SourceRef, SourceSHA: branch.NewSourceSHA,
		TargetRef: branch.TargetRef, TargetSHA: branch.ExpectedTargetSHA, Title: "Upgrade widget", Body: "Ready.",
	}
	return publisher.PRAction{Kind: publisher.OperationCreatePR, PullRequest: &request}
}

func validBridgeStatusAction() publisher.PRAction {
	create := validBridgeCreateAction().PullRequest
	request := publisher.StatusPublicationRequest{
		Authority: create.Authority, Observation: create.Observation, Validation: create.Validation, Evidence: create.Evidence,
		Destination: create.Destination, ApprovalPolicyVersion: create.ApprovalPolicyVersion,
		Locator:   publisher.PRLocator{Provider: publisher.PRProviderGitHub, Repository: "acme/widget", ExternalID: "42"},
		TargetRef: create.TargetRef, SourceSHA: create.SourceSHA, State: "success", Description: "Validated", TargetURL: "https://ci.example/runs/42",
	}
	return publisher.PRAction{Kind: publisher.OperationPublishPRStatus, Status: &request}
}

func validBridgeResponseAction() publisher.PRAction {
	status := validBridgeStatusAction().Status
	request := publisher.ResponsePublicationRequest{
		Authority: status.Authority, Observation: status.Observation,
		ResponseSnapshot: snapshot.SnapshotRef{ID: 25, Type: "pull-request-response/v1", Digest: bridgeDigest('5')},
		Evidence:         status.Evidence, Destination: status.Destination, ApprovalPolicyVersion: status.ApprovalPolicyVersion,
		Locator: status.Locator, TargetRef: status.TargetRef,
		Batch:    publisher.PRReviewBatch{ID: "review-17", ReviewID: "17", CommitSHA: status.SourceSHA, Reviewer: "reviewer", Ready: true, ThreadIDs: []string{"thread-101"}},
		Response: contracts.PullRequestResponseBody{BatchID: "review-17", Summary: "Addressed.", Replies: []contracts.PullRequestThreadResponse{{ThreadID: "thread-101", Body: "Updated."}}},
	}
	return publisher.PRAction{Kind: publisher.OperationRespondToReview, Response: &request}
}

func bridgeEvidence() publisher.PublicationEvidence {
	return publisher.PublicationEvidence{Kind: publisher.EvidenceAcceptedReview, AcceptedReview: &publisher.AcceptedReviewEvidence{
		Review:              snapshot.SnapshotRef{ID: 11, Type: "review/v1", Digest: bridgeDigest('a')},
		Candidate:           snapshot.SnapshotRef{ID: 22, Type: "repository/v1", Digest: bridgeDigest('2')},
		Validation:          snapshot.SnapshotRef{ID: 12, Type: "validation/v1", Digest: bridgeDigest('b')},
		ReviewWorkflowRunID: 81, OutcomeRevision: 3, AcceptedBy: "alice", AcceptedAt: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	}}
}

func objectIDForBridge(character byte) string { return strings.Repeat(string(character), 40) }
func bridgeDigest(character byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(character), 64))
}

func branchMutationForAction(t *testing.T, action publisher.PRAction) publisher.BranchMutation {
	t.Helper()
	key, err := action.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	request := action.Branch
	return publisher.BranchMutation{Locator: request.Locator, Ref: request.SourceRef, TargetRef: request.TargetRef, ExpectedSource: request.ExpectedSource, ExpectedTargetSHA: request.ExpectedTargetSHA, NewSourceSHA: request.NewSourceSHA, OperationKey: key}
}

func createMutationForAction(t *testing.T, action publisher.PRAction) publisher.CreatePRMutation {
	t.Helper()
	key, err := action.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	request := action.PullRequest
	return publisher.CreatePRMutation{Locator: request.Locator, SourceRef: request.SourceRef, SourceSHA: request.SourceSHA, TargetRef: request.TargetRef, TargetSHA: request.TargetSHA, Title: request.Title, Body: request.Body, OperationKey: key}
}

func statusMutationForAction(t *testing.T, action publisher.PRAction) publisher.StatusMutation {
	t.Helper()
	key, err := action.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	request := action.Status
	return publisher.StatusMutation{Locator: request.Locator, TargetRef: request.TargetRef, SourceSHA: request.SourceSHA, State: request.State, Description: request.Description, TargetURL: request.TargetURL, OperationKey: key}
}

func responseMutationForAction(t *testing.T, action publisher.PRAction) publisher.ResponseMutation {
	t.Helper()
	key, err := action.OperationKey()
	if err != nil {
		t.Fatal(err)
	}
	request := action.Response
	return publisher.ResponseMutation{Locator: request.Locator, TargetRef: request.TargetRef, Batch: request.Batch, Response: request.Response, OperationKey: key}
}

type bridgeRunner struct{}

func (bridgeRunner) Run(context.Context, directgit.Command) (directgit.CommandResult, error) {
	return directgit.CommandResult{}, nil
}

type capturingPullRequestMutator struct {
	branch   pullrequest.BranchMutation
	create   pullrequest.CreateRequest
	status   pullrequest.StatusRequest
	response pullrequest.ResponseRequest
}

func (mutator *capturingPullRequestMutator) CompareAndSwapBranch(_ context.Context, mutation pullrequest.BranchMutation) (pullrequest.BranchResult, error) {
	mutator.branch = mutation
	return pullrequest.BranchResult{HeadSHA: mutation.NewSourceSHA, Applied: true}, nil
}
func (mutator *capturingPullRequestMutator) FindOrCreatePullRequest(_ context.Context, mutation pullrequest.CreateRequest) (pullrequest.ExternalPullRequest, error) {
	mutator.create = mutation
	return pullrequest.ExternalPullRequest{Locator: mutation.Locator, URL: "https://forge.example/pr/42", State: contracts.PullRequestActive, SourceRef: mutation.SourceRef, SourceSHA: mutation.SourceSHA, TargetRef: mutation.TargetRef, TargetSHA: mutation.TargetSHA}, nil
}
func (mutator *capturingPullRequestMutator) PublishValidationStatus(_ context.Context, mutation pullrequest.StatusRequest) (pullrequest.ExternalResult, error) {
	mutator.status = mutation
	return pullrequest.ExternalResult{OperationKey: mutation.OperationKey, ExternalID: "status-42", URL: "https://forge.example/pr/42"}, nil
}
func (mutator *capturingPullRequestMutator) PublishReviewResponse(_ context.Context, mutation pullrequest.ResponseRequest) (pullrequest.ExternalResult, error) {
	mutator.response = mutation
	return pullrequest.ExternalResult{OperationKey: mutation.OperationKey, ExternalID: "review-17", URL: "https://forge.example/pr/42"}, nil
}
