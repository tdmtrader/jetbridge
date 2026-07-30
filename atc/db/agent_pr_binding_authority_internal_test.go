package db

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestValidateAgentPRBindingInitialPublications(t *testing.T) {
	request, accepted, branchPublication, creationPublication :=
		agentPRBindingInitialAuthorityFixture(t)

	if err := validateAgentPRBindingInitialPublications(
		request, accepted, branchPublication, creationPublication,
	); err != nil {
		t.Fatalf("validate exact initial publications: %v", err)
	}

	tests := map[string]func(
		*pullrequest.CreateBinding,
		*publisher.Publication,
		*publisher.Publication,
	){
		"legacy origin occurrence": func(
			_ *pullrequest.CreateBinding,
			branch *publisher.Publication,
			_ *publisher.Publication,
		) {
			branch.OperationKind = ""
			branch.PRAction = nil
		},
		"create occurrence substituted for branch": func(
			_ *pullrequest.CreateBinding,
			branch *publisher.Publication,
			create *publisher.Publication,
		) {
			*branch = create.Clone()
			branch.ID = snapshot.DatabaseID(request.
				OriginatingPublicationOccurrence)
		},
		"status occurrence substituted for branch": func(
			_ *pullrequest.CreateBinding,
			branch *publisher.Publication,
			_ *publisher.Publication,
		) {
			branch.OperationKind = publisher.OperationPublishPRStatus
			branch.PRAction.Kind = publisher.OperationPublishPRStatus
		},
		"response occurrence substituted for branch": func(
			_ *pullrequest.CreateBinding,
			branch *publisher.Publication,
			_ *publisher.Publication,
		) {
			branch.OperationKind = publisher.OperationRespondToReview
			branch.PRAction.Kind = publisher.OperationRespondToReview
		},
		"branch result does not establish binding head": func(
			_ *pullrequest.CreateBinding,
			branch *publisher.Publication,
			_ *publisher.Publication,
		) {
			branch.Result.HeadSHA = strings.Repeat("f", 40)
		},
		"create candidate differs from branch": func(
			_ *pullrequest.CreateBinding,
			_ *publisher.Publication,
			create *publisher.Publication,
		) {
			create.PRAction.PullRequest.Candidate =
				agentPRBindingAuthorityRef(
					88, "repository-change/v1", '8',
				)
		},
		"create validation differs from branch": func(
			_ *pullrequest.CreateBinding,
			_ *publisher.Publication,
			create *publisher.Publication,
		) {
			create.PRAction.PullRequest.Validation =
				agentPRBindingAuthorityRef(89, "validation/v1", '9')
		},
		"create impact differs from branch": func(
			_ *pullrequest.CreateBinding,
			_ *publisher.Publication,
			create *publisher.Publication,
		) {
			create.PRAction.PullRequest.Impact =
				agentPRBindingAuthorityRef(
					90, "publish-impact/v1", 'a',
				)
		},
		"create observation differs from branch": func(
			_ *pullrequest.CreateBinding,
			_ *publisher.Publication,
			create *publisher.Publication,
		) {
			create.PRAction.PullRequest.Observation =
				agentPRBindingAuthorityRef(
					91, "pull-request/v1", 'b',
				)
		},
		"create evidence differs from branch": func(
			_ *pullrequest.CreateBinding,
			_ *publisher.Publication,
			create *publisher.Publication,
		) {
			create.PRAction.PullRequest.Evidence.AcceptedReview.
				OutcomeRevision++
		},
		"origin evidence differs from accepted authority": func(
			_ *pullrequest.CreateBinding,
			branch *publisher.Publication,
			_ *publisher.Publication,
		) {
			branch.PRAction.Branch.Evidence.AcceptedReview.Candidate =
				agentPRBindingAuthorityRef(92, "repository/v1", 'c')
		},
		"branch destination differs from binding": func(
			request *pullrequest.CreateBinding,
			_ *publisher.Publication,
			_ *publisher.Publication,
		) {
			request.Destination = "github.example/acme/other"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changedRequest := request
			changedBranch := branchPublication.Clone()
			changedCreate := creationPublication.Clone()
			mutate(&changedRequest, &changedBranch, &changedCreate)

			err := validateAgentPRBindingInitialPublications(
				changedRequest, accepted, changedBranch, changedCreate,
			)
			if !errors.Is(err, pullrequest.ErrBindingConflict) {
				t.Fatalf("validation error = %v, want binding conflict", err)
			}
		})
	}
	for name, mutate := range map[string]func(
		*pullrequest.AcceptedReviewAuthority,
	){
		"accepted authority belongs to another team": func(
			accepted *pullrequest.AcceptedReviewAuthority,
		) {
			accepted.TeamID++
		},
		"accepted authority names another occurrence": func(
			accepted *pullrequest.AcceptedReviewAuthority,
		) {
			accepted.PublicationOccurrenceID++
		},
	} {
		t.Run(name, func(t *testing.T) {
			changedAccepted := accepted
			mutate(&changedAccepted)
			err := validateAgentPRBindingInitialPublications(
				request, changedAccepted, branchPublication,
				creationPublication,
			)
			if !errors.Is(err, pullrequest.ErrBindingConflict) {
				t.Fatalf("validation error = %v, want binding conflict", err)
			}
		})
	}
}

func agentPRBindingInitialAuthorityFixture(
	t *testing.T,
) (
	pullrequest.CreateBinding,
	pullrequest.AcceptedReviewAuthority,
	publisher.Publication,
	publisher.Publication,
) {
	t.Helper()
	authority := publisher.Authority{
		TeamID: 17, TeamName: "engineering", BuildID: 42,
		WorkflowRunID: 51, Actor: "concourse",
	}
	observation := agentPRBindingAuthorityRef(
		1, "pull-request/v1", '1',
	)
	candidate := agentPRBindingAuthorityRef(
		2, "repository-change/v1", '2',
	)
	validation := agentPRBindingAuthorityRef(3, "validation/v1", '3')
	impact := agentPRBindingAuthorityRef(4, "publish-impact/v1", '4')
	review := agentPRBindingAuthorityRef(5, "review/v1", '5')
	acceptedCandidate := agentPRBindingAuthorityRef(
		6, "repository/v1", '6',
	)
	acceptedValidation := agentPRBindingAuthorityRef(
		7, "validation/v1", '7',
	)
	accepted, err := pullrequest.NewAcceptedReviewAuthority(
		pullrequest.AcceptedReviewAuthoritySpec{
			TeamID: 17, PublicationOccurrenceID: 101,
			Review: review, Candidate: acceptedCandidate,
			Validation:          acceptedValidation,
			ReviewWorkflowRunID: 61, OutcomeRevision: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence := publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview,
		AcceptedReview: &publisher.AcceptedReviewEvidence{
			Review: review, Candidate: acceptedCandidate,
			Validation:          acceptedValidation,
			ReviewWorkflowRunID: 61,
			OutcomeRevision:     3,
			AcceptedBy:          "reviewer",
			AcceptedAt: time.Date(
				2026, time.July, 30, 8, 0, 0, 0, time.UTC,
			),
		},
	}
	locator := publisher.PRLocator{
		Provider: publisher.PRProviderGitHub, Repository: "acme/widget",
	}
	sourceSHA := strings.Repeat("a", 40)
	targetSHA := strings.Repeat("b", 40)
	branchAction := publisher.PRAction{
		Kind: publisher.OperationPublishPRBranch,
		Branch: &publisher.BranchPublicationRequest{
			Authority: authority, Observation: observation,
			Candidate: candidate, Validation: validation, Impact: impact,
			Evidence:              evidence.Clone(),
			Destination:           "github.example/acme/widget",
			ApprovalPolicyVersion: "engineering/v3", Locator: locator,
			SourceRef: "refs/heads/agent/change",
			TargetRef: "refs/heads/main",
			ExpectedSource: publisher.HeadExpectation{
				Exists: false,
			},
			ExpectedTargetSHA: targetSHA, NewSourceSHA: sourceSHA,
		},
	}
	createAction := publisher.PRAction{
		Kind: publisher.OperationCreatePR,
		PullRequest: &publisher.PullRequestPublicationRequest{
			Authority: authority, Observation: observation,
			Candidate: candidate, Validation: validation, Impact: impact,
			Evidence:              evidence.Clone(),
			Destination:           "github.example/acme/widget",
			ApprovalPolicyVersion: "engineering/v3", Locator: locator,
			SourceRef: "refs/heads/agent/change", SourceSHA: sourceSHA,
			TargetRef: "refs/heads/main", TargetSHA: targetSHA,
			Title: "Validated change", Body: "Ready for provider review.",
		},
	}
	branchPublication := publisher.Publication{
		ID: 101, OperationKind: publisher.OperationPublishPRBranch,
		PRAction: &branchAction, Status: publisher.StatusSucceeded,
		Result: publisher.Result{
			Status:     publisher.StatusSucceeded,
			ExternalID: branchAction.Branch.SourceRef,
			HeadSHA:    sourceSHA, BaseSHA: targetSHA,
		},
	}
	creationPublication := publisher.Publication{
		ID: 102, OperationKind: publisher.OperationCreatePR,
		PRAction: &createAction, Status: publisher.StatusSucceeded,
		Result: publisher.Result{
			Status: publisher.StatusSucceeded, ExternalID: "42",
			URL:     "https://github.example/acme/widget/pull/42",
			HeadSHA: sourceSHA, BaseSHA: targetSHA,
		},
	}
	request := pullrequest.CreateBinding{
		TeamID: 17,
		Locator: pullrequest.Locator{
			Provider:   pullrequest.ProviderGitHub,
			Repository: "acme/widget", ExternalID: "42",
		},
		URL:                              "https://github.example/acme/widget/pull/42",
		SourceRef:                        "refs/heads/agent/change",
		TargetRef:                        "refs/heads/main",
		Destination:                      "github.example/acme/widget",
		ApprovalPolicyVersion:            "engineering/v3",
		OriginatingWorkflowRunID:         51,
		OriginatingPublicationOccurrence: 101,
		CreationPublicationOccurrenceID:  102,
		LastReconciledSourceSHA:          sourceSHA,
		LastReconciledTargetSHA:          targetSHA,
	}
	return request, accepted, branchPublication, creationPublication
}

func agentPRBindingAuthorityRef(
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	fill byte,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: id, Type: typ,
		Digest: snapshot.Digest(
			"sha256:" + strings.Repeat(string(fill), 64),
		),
	}
}
