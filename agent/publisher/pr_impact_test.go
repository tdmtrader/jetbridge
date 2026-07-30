package publisher

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestPRImpactVerificationRequestAllowsOriginalReviewBeforeCurrentBaseline(
	t *testing.T,
) {
	request := validMismatchedPRImpactTestRequest()

	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestPRImpactVerificationRequestStillRequiresValidOriginalReview(
	t *testing.T,
) {
	tests := map[string]func(*PRImpactVerificationRequest){
		"missing": func(request *PRImpactVerificationRequest) {
			request.AcceptedReview = PublicationEvidence{}
		},
		"invalid": func(request *PRImpactVerificationRequest) {
			request.AcceptedReview.AcceptedReview.Candidate.Type =
				"repository-change/v1"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := validMismatchedPRImpactTestRequest()
			mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func validMismatchedPRImpactTestRequest() PRImpactVerificationRequest {
	original := prImpactTestRef(11, "repository/v1", 'a')
	baseline := prImpactTestRef(21, "repository/v1", 'b')
	candidate := prImpactTestRef(31, "repository-change/v1", 'c')
	return PRImpactVerificationRequest{
		TeamID: 7, BindingID: 8,
		ActionDigest:  prImpactTestDigest('d'),
		PolicyVersion: "engineering/v3",
		Observation: prImpactTestRef(
			41, "pull-request/v1", 'e',
		),
		Baseline: baseline,
		BaselineValidation: prImpactTestRef(
			22, "validation/v1", '5',
		),
		Candidate: candidate,
		Validation: prImpactTestRef(
			42, "validation/v1", 'f',
		),
		Impact: prImpactTestRef(
			43, "publish-impact/v1", '1',
		),
		Response: prImpactTestRef(
			44, "pull-request-response/v1", '2',
		),
		AcceptedReview: PublicationEvidence{
			Kind: EvidenceAcceptedReview,
			AcceptedReview: &AcceptedReviewEvidence{
				Review:              prImpactTestRef(12, "review/v1", '3'),
				Candidate:           original,
				Validation:          prImpactTestRef(13, "validation/v1", '4'),
				ReviewWorkflowRunID: 14,
				OutcomeRevision:     2,
				AcceptedBy:          "reviewer",
				AcceptedAt: time.Date(
					2026, time.July, 30, 7, 0, 0, 0, time.UTC,
				),
			},
		},
		Body: contracts.PublishImpactBody{
			BaselineDigest:  baseline.Digest.String(),
			CandidateDigest: candidate.Digest.String(),
			RuleResults: []contracts.PublishImpactRule{{
				ID: "default", Passed: true,
				Reason: "No escalation rule matched.",
			}},
		},
	}
}

func prImpactTestRef(
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	fill byte,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: id, Type: typ, Digest: prImpactTestDigest(fill),
	}
}

func prImpactTestDigest(fill byte) snapshot.Digest {
	return snapshot.Digest(
		"sha256:" + strings.Repeat(string(fill), 64),
	)
}
