package publisher_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/snapshot/snapshotfakes"
	"github.com/stretchr/testify/require"
)

func TestPublicationEvidenceRequiresExactlyOneMatchingVariant(t *testing.T) {
	acceptedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	accepted := &publisher.AcceptedReviewEvidence{
		Review:              reviewEvidenceRef(21, "review/v1", 'a'),
		Candidate:           reviewEvidenceRef(22, "repository/v1", 'b'),
		Validation:          reviewEvidenceRef(23, "validation/v1", 'c'),
		ReviewWorkflowRunID: 24,
		OutcomeRevision:     3,
		AcceptedBy:          "alice",
		AcceptedAt:          acceptedAt,
	}
	human := &publisher.ApprovalEvidence{
		WaitID:     25,
		Question:   reviewEvidenceRef(26, "question/v1", 'd'),
		Answer:     reviewEvidenceRef(27, "human-answer/v1", 'e'),
		ResolvedBy: "bob",
		ResolvedAt: acceptedAt,
	}

	require.NoError(t, (publisher.PublicationEvidence{
		Kind: publisher.EvidenceAcceptedReview, AcceptedReview: accepted,
	}).Validate())
	require.NoError(t, (publisher.PublicationEvidence{
		Kind: publisher.EvidenceHumanWait, HumanWait: human,
	}).Validate())

	for name, evidence := range map[string]publisher.PublicationEvidence{
		"neither": {},
		"both": {
			Kind: publisher.EvidenceAcceptedReview, AcceptedReview: accepted, HumanWait: human,
		},
		"accepted value with human kind": {
			Kind: publisher.EvidenceHumanWait, AcceptedReview: accepted,
		},
		"human value with accepted kind": {
			Kind: publisher.EvidenceAcceptedReview, HumanWait: human,
		},
		"unknown kind": {
			Kind: "projection_claim", AcceptedReview: accepted,
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, evidence.Validate(), publisher.ErrInvalidRequest)
		})
	}
}

func TestDurableApprovalVerifierBindsAnyTrustedIntentToExactSealedContext(t *testing.T) {
	// Deliberately not the merge envelope: the verifier must bind whatever
	// exact context a typed intent builder derives, without knowing its shape.
	const intended = `{"schema_version":"1.0.0","kind":"unrelated_effect","destination":"effect.example/acme/widget","external_id":"81","action_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","policy_version":"engineering/v1","workflow_run_id":"17"}`
	fixture := newApprovalFixture(t, func(question *contracts.QuestionDocument) {
		question.Context = intended
	}, nil)

	request := publisher.DurableApprovalRequest{
		TeamID:          fixture.request.TeamID,
		WorkflowRunID:   fixture.request.WorkflowRunID,
		BuildID:         fixture.request.BuildID,
		Approval:        fixture.request.Approval,
		ExpectedContext: json.RawMessage(intended),
	}
	evidence, err := fixture.verifier.VerifyExact(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, fixture.wait.ID, evidence.WaitID)
	require.Equal(t, fixture.wait.ResolvedBy, evidence.ResolvedBy)

	request.ExpectedContext = json.RawMessage(strings.Replace(
		intended,
		`"external_id":"81"`,
		`"external_id":"82"`,
		1,
	))
	_, err = fixture.verifier.VerifyExact(context.Background(), request)
	require.ErrorIs(t, err, publisher.ErrInvalidRequest)
}

func TestDurableApprovalVerifierExactContextDoesNotLoosenMergeVerification(t *testing.T) {
	fixture := newApprovalFixture(t, nil, nil)
	expected, err := publisher.BuildMergeApprovalContext(fixture.request)
	require.NoError(t, err)
	raw, err := json.Marshal(expected)
	require.NoError(t, err)

	exact, err := fixture.verifier.VerifyExact(context.Background(), publisher.DurableApprovalRequest{
		TeamID:          fixture.request.TeamID,
		WorkflowRunID:   fixture.request.WorkflowRunID,
		BuildID:         fixture.request.BuildID,
		Approval:        fixture.request.Approval,
		ExpectedContext: raw,
	})
	require.NoError(t, err)
	legacy, err := fixture.verifier.Verify(context.Background(), fixture.request)
	require.NoError(t, err)
	require.Equal(t, legacy, exact)

	invalid := publisher.DurableApprovalRequest{
		TeamID:        fixture.request.TeamID,
		WorkflowRunID: fixture.request.WorkflowRunID,
		BuildID:       fixture.request.BuildID,
		Approval: snapshot.SnapshotRef{
			ID: 99, Type: "human-answer/v1",
			Digest: snapshot.Digest("sha256:" + strings.Repeat("f", 64)),
		},
		ExpectedContext: raw,
	}
	_, err = fixture.verifier.VerifyExact(context.Background(), invalid)
	require.ErrorIs(t, err, publisher.ErrInvalidRequest)
}

func TestEvidenceVerifierRejectsBothOrNeitherRequestVariant(t *testing.T) {
	verifier, err := publisher.NewEvidenceVerifier(
		reviewRunEvidenceResolverFunc(func(
			context.Context,
			int,
			snapshot.WorkflowRunID,
		) (publisher.ReviewRunEvidence, bool, error) {
			return publisher.ReviewRunEvidence{}, false, nil
		}),
		reviewOutcomeReaderFunc(func(
			context.Context,
			int,
			snapshot.WorkflowRunID,
			snapshot.SnapshotID,
		) (workflowoutcomes.Outcome, bool, error) {
			return workflowoutcomes.Outcome{}, false, nil
		}),
		&snapshotfakes.FakeMetadataStore{},
		&snapshotfakes.FakeContentStore{},
		exactApprovalVerifierFunc(func(
			context.Context,
			publisher.DurableApprovalRequest,
		) (publisher.ApprovalEvidence, error) {
			return publisher.ApprovalEvidence{}, publisher.ErrInvalidRequest
		}),
		snapshot.Canonicalizer{TempDir: t.TempDir()},
	)
	require.NoError(t, err)

	human := &publisher.DurableApprovalRequest{
		TeamID: 9, WorkflowRunID: 17, BuildID: 12,
		Approval:        reviewEvidenceRef(43, "human-answer/v1", 'e'),
		ExpectedContext: json.RawMessage(`{"schema_version":"1.0.0"}`),
	}
	accepted := &publisher.AcceptedReviewEvidenceRequest{
		Review:              reviewEvidenceRef(41, "review/v1", 'a'),
		Candidate:           reviewEvidenceRef(44, "repository/v1", 'c'),
		Validation:          reviewEvidenceRef(42, "validation/v1", 'b'),
		ReviewWorkflowRunID: 17,
		OutcomeRevision:     1,
	}
	for name, request := range map[string]publisher.EvidenceRequest{
		"neither": {TeamID: 9},
		"both":    {TeamID: 9, AcceptedReview: accepted, HumanWait: human},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := verifier.Verify(context.Background(), request)
			require.ErrorIs(t, err, publisher.ErrInvalidRequest)
		})
	}
}

type exactApprovalVerifierFunc func(context.Context, publisher.DurableApprovalRequest) (publisher.ApprovalEvidence, error)

func (function exactApprovalVerifierFunc) VerifyExact(
	ctx context.Context,
	request publisher.DurableApprovalRequest,
) (publisher.ApprovalEvidence, error) {
	if function == nil {
		return publisher.ApprovalEvidence{}, publisher.ErrInvalidRequest
	}
	return function(ctx, request)
}

func reviewEvidenceRef(id snapshot.SnapshotID, typeRef snapshot.TypeRef, fill byte) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID: id, Type: typeRef,
		Digest: snapshot.Digest("sha256:" + strings.Repeat(string(fill), 64)),
	}
}

var _ publisher.ExactApprovalVerifier = exactApprovalVerifierFunc(nil)
