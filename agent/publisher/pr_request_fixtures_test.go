package publisher_test

import (
	"strings"
	"time"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// These fixtures were defined in pr_service_test.go, which went with the PR
// service itself. They are kept only because pr_actions_target_ref_test.go and
// policy_test.go still exercise the request types in pr_actions.go, which the
// publication store and the publisher policy both still reference. They go with
// those.

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
		TargetRef:             create.TargetRef,
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
		TargetRef:             status.TargetRef,
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
