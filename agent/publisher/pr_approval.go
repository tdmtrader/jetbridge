package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
)

// PRApprovalContext is the canonical machine-readable context embedded in a
// server-synthesized reapproval question. The durable verifier later compares
// these exact bytes; no mutable PR projection or authored question can stand
// in for this envelope.
type PRApprovalContext struct {
	SchemaVersion         string                 `json:"schema_version"`
	TeamID                int                    `json:"team_id"`
	WorkflowRunID         snapshot.WorkflowRunID `json:"workflow_run_id"`
	BuildID               int64                  `json:"build_id"`
	BindingID             int64                  `json:"binding_id"`
	ActionDigest          string                 `json:"action_digest"`
	ObservationSnapshotID snapshot.SnapshotID    `json:"observation_snapshot_id"`
	ObservationDigest     snapshot.Digest        `json:"observation_digest"`
	CandidateSnapshotID   snapshot.SnapshotID    `json:"candidate_snapshot_id"`
	CandidateDigest       snapshot.Digest        `json:"candidate_digest"`
	SourceHead            string                 `json:"source_head"`
	TargetHead            string                 `json:"target_head"`
	Destination           string                 `json:"destination"`
	ResponseSnapshotID    snapshot.SnapshotID    `json:"response_snapshot_id"`
	ResponseDigest        snapshot.Digest        `json:"response_digest"`
	ValidationSnapshotID  snapshot.SnapshotID    `json:"validation_snapshot_id"`
	ValidationDigest      snapshot.Digest        `json:"validation_digest"`
	ImpactSnapshotID      snapshot.SnapshotID    `json:"impact_snapshot_id"`
	ImpactDigest          snapshot.Digest        `json:"impact_digest"`
	ApprovalPolicyVersion string                 `json:"approval_policy_version"`
	IntentDigest          string                 `json:"intent_digest"`
}

type PRApprovalRequest struct {
	TeamID                int
	WorkflowRunID         snapshot.WorkflowRunID
	BuildID               int64
	Approval              snapshot.SnapshotRef
	BindingID             int64
	ActionDigest          string
	Observation           snapshot.SnapshotRef
	Candidate             snapshot.SnapshotRef
	SourceHead            string
	TargetHead            string
	Destination           string
	Response              snapshot.SnapshotRef
	Validation            snapshot.SnapshotRef
	Impact                snapshot.SnapshotRef
	ApprovalPolicyVersion string
}

type PRApprovalVerifier interface {
	Verify(context.Context, PRApprovalRequest) (ApprovalEvidence, error)
}

// PRRevisionPublicationRequest is the typed handoff from workflow execution to
// provider-native PR orchestration. It carries the complete exact revision
// intent and exactly one server-verified authority: either the accepted review
// that remains sufficient under impact policy, or the exact human wait that
// approved an escalated revision. The later
// binding-aware adapter must reopen these snapshots, match BindingID and
// ActionDigest to server state, resolve Destination through server policy, and
// durably complete every required provider-native sub-operation before
// ExecutePRRevision returns nil.
//
// Candidate remains repository-change/v1 here: it is the final rebased value
// the approval and validation bind. It must not be silently replaced with the
// pre-revision repository/v1 input when adapting to provider operations.
type PRRevisionPublicationRequest struct {
	Authority             Authority                      `json:"authority"`
	BindingID             int64                          `json:"binding_id"`
	ActionDigest          string                         `json:"action_digest"`
	Observation           snapshot.SnapshotRef           `json:"observation"`
	Candidate             snapshot.SnapshotRef           `json:"candidate"`
	Validation            snapshot.SnapshotRef           `json:"validation"`
	Impact                snapshot.SnapshotRef           `json:"impact"`
	Response              snapshot.SnapshotRef           `json:"response"`
	SourceHead            string                         `json:"source_head"`
	TargetHead            string                         `json:"target_head"`
	Destination           string                         `json:"destination"`
	ApprovalPolicyVersion string                         `json:"approval_policy_version"`
	ApprovalContext       *PRApprovalContext             `json:"approval_context,omitempty"`
	AcceptedReview        *AcceptedReviewEvidenceRequest `json:"accepted_review,omitempty"`
	Evidence              PublicationEvidence            `json:"evidence"`
}

// PRRevisionExecutor is deliberately separate from the legacy Executor. A
// typed PR revision can therefore never fall through to direct Git, whose
// Request shape cannot carry provider-native publication evidence.
type PRRevisionExecutor interface {
	ExecutePRRevision(context.Context, PRRevisionPublicationRequest) error
}

type exactPRApprovalVerifier struct {
	exact ExactApprovalVerifier
}

func NewPRApprovalVerifier(exact ExactApprovalVerifier) (PRApprovalVerifier, error) {
	if nilInterface(exact) {
		return nil, fmt.Errorf("publisher: exact durable approval verifier is required")
	}
	return &exactPRApprovalVerifier{exact: exact}, nil
}

// BuildPRApprovalContext derives one strict PR revision intent. Approval itself
// is deliberately optional here because await_snapshot builds the question
// before a human-answer/v1 value exists.
func BuildPRApprovalContext(request PRApprovalRequest) (PRApprovalContext, error) {
	if err := validatePRApprovalRequest(request, false); err != nil {
		return PRApprovalContext{}, err
	}
	intentDigest, err := prApprovalIntentDigest(request)
	if err != nil {
		return PRApprovalContext{}, err
	}
	return PRApprovalContext{
		SchemaVersion:         "1.0.0",
		TeamID:                request.TeamID,
		WorkflowRunID:         request.WorkflowRunID,
		BuildID:               request.BuildID,
		BindingID:             request.BindingID,
		ActionDigest:          request.ActionDigest,
		ObservationSnapshotID: request.Observation.ID,
		ObservationDigest:     request.Observation.Digest,
		CandidateSnapshotID:   request.Candidate.ID,
		CandidateDigest:       request.Candidate.Digest,
		SourceHead:            request.SourceHead,
		TargetHead:            request.TargetHead,
		Destination:           request.Destination,
		ResponseSnapshotID:    request.Response.ID,
		ResponseDigest:        request.Response.Digest,
		ValidationSnapshotID:  request.Validation.ID,
		ValidationDigest:      request.Validation.Digest,
		ImpactSnapshotID:      request.Impact.ID,
		ImpactDigest:          request.Impact.Digest,
		ApprovalPolicyVersion: request.ApprovalPolicyVersion,
		IntentDigest:          intentDigest,
	}, nil
}

func BuildPRRevisionPublicationRequest(
	authority Authority,
	observation snapshot.SnapshotRef,
	response snapshot.SnapshotRef,
	approval PRApprovalRequest,
	evidence ApprovalEvidence,
) (PRRevisionPublicationRequest, error) {
	if authority.TeamID != approval.TeamID ||
		authority.BuildID != approval.BuildID ||
		authority.WorkflowRunID != approval.WorkflowRunID ||
		observation != approval.Observation ||
		response != approval.Response ||
		evidence.Answer != approval.Approval {
		return PRRevisionPublicationRequest{}, fmt.Errorf("%w: PR revision publication authority is invalid", ErrInvalidRequest)
	}
	contextEnvelope, err := BuildPRApprovalContext(approval)
	if err != nil {
		return PRRevisionPublicationRequest{}, err
	}
	request := PRRevisionPublicationRequest{
		Authority:             authority,
		BindingID:             approval.BindingID,
		ActionDigest:          approval.ActionDigest,
		Observation:           observation,
		Candidate:             approval.Candidate,
		Validation:            approval.Validation,
		Impact:                approval.Impact,
		Response:              response,
		SourceHead:            approval.SourceHead,
		TargetHead:            approval.TargetHead,
		Destination:           approval.Destination,
		ApprovalPolicyVersion: approval.ApprovalPolicyVersion,
		ApprovalContext:       &contextEnvelope,
		Evidence: PublicationEvidence{
			Kind: EvidenceHumanWait, HumanWait: &evidence,
		},
	}
	if err := request.Validate(); err != nil {
		return PRRevisionPublicationRequest{}, err
	}
	return request, nil
}

// BuildAcceptedPRRevisionPublicationRequest builds the no-wait branch selected
// by the server-derived impact record. The exact accepted-review verifier runs
// before this builder; the request retains both the lookup identity and its
// resolved immutable evidence so a later adapter cannot substitute a
// projection or silently route the revision through legacy direct Git.
func BuildAcceptedPRRevisionPublicationRequest(
	authority Authority,
	observation snapshot.SnapshotRef,
	response snapshot.SnapshotRef,
	intent PRApprovalRequest,
	accepted AcceptedReviewEvidenceRequest,
	evidence PublicationEvidence,
) (PRRevisionPublicationRequest, error) {
	if authority.TeamID != intent.TeamID ||
		authority.BuildID != intent.BuildID ||
		authority.WorkflowRunID != intent.WorkflowRunID ||
		observation != intent.Observation ||
		response != intent.Response ||
		intent.Approval != (snapshot.SnapshotRef{}) ||
		accepted.validate() != nil ||
		!acceptedReviewEvidenceMatchesRequest(evidence, accepted) {
		return PRRevisionPublicationRequest{}, fmt.Errorf("%w: accepted-review PR revision authority is invalid", ErrInvalidRequest)
	}
	if err := validatePRApprovalRequest(intent, false); err != nil {
		return PRRevisionPublicationRequest{}, err
	}
	request := PRRevisionPublicationRequest{
		Authority:             authority,
		BindingID:             intent.BindingID,
		ActionDigest:          intent.ActionDigest,
		Observation:           observation,
		Candidate:             intent.Candidate,
		Validation:            intent.Validation,
		Impact:                intent.Impact,
		Response:              response,
		SourceHead:            intent.SourceHead,
		TargetHead:            intent.TargetHead,
		Destination:           intent.Destination,
		ApprovalPolicyVersion: intent.ApprovalPolicyVersion,
		AcceptedReview:        &accepted,
		Evidence:              evidence.Clone(),
	}
	if err := request.Validate(); err != nil {
		return PRRevisionPublicationRequest{}, err
	}
	return request, nil
}

func (request PRRevisionPublicationRequest) Validate() error {
	if err := request.Authority.validate(true); err != nil {
		return err
	}
	if request.Observation.Validate() != nil ||
		request.Observation.Type != snapshot.TypeRef("pull-request/v1") ||
		request.Response.Validate() != nil ||
		request.Response.Type != snapshot.TypeRef("pull-request-response/v1") ||
		request.Evidence.Validate() != nil {
		return fmt.Errorf("%w: PR revision publication evidence is invalid", ErrInvalidRequest)
	}
	intent := PRApprovalRequest{
		TeamID:                request.Authority.TeamID,
		WorkflowRunID:         request.Authority.WorkflowRunID,
		BuildID:               request.Authority.BuildID,
		BindingID:             request.BindingID,
		ActionDigest:          request.ActionDigest,
		Observation:           request.Observation,
		Candidate:             request.Candidate,
		SourceHead:            request.SourceHead,
		TargetHead:            request.TargetHead,
		Destination:           request.Destination,
		Response:              request.Response,
		Validation:            request.Validation,
		Impact:                request.Impact,
		ApprovalPolicyVersion: request.ApprovalPolicyVersion,
	}
	switch request.Evidence.Kind {
	case EvidenceHumanWait:
		if request.AcceptedReview != nil || request.ApprovalContext == nil ||
			request.Evidence.HumanWait == nil {
			return fmt.Errorf("%w: PR revision human-wait authority is invalid", ErrInvalidRequest)
		}
		intent.Approval = request.Evidence.HumanWait.Answer
		contextEnvelope, err := BuildPRApprovalContext(intent)
		if err != nil || contextEnvelope != *request.ApprovalContext {
			return fmt.Errorf("%w: PR revision publication intent is invalid", ErrInvalidRequest)
		}
	case EvidenceAcceptedReview:
		if request.ApprovalContext != nil || request.AcceptedReview == nil ||
			request.AcceptedReview.validate() != nil ||
			!acceptedReviewEvidenceMatchesRequest(request.Evidence, *request.AcceptedReview) ||
			validatePRApprovalRequest(intent, false) != nil {
			return fmt.Errorf("%w: PR revision accepted-review authority is invalid", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: PR revision publication evidence is invalid", ErrInvalidRequest)
	}
	return nil
}

func acceptedReviewEvidenceMatchesRequest(
	evidence PublicationEvidence,
	request AcceptedReviewEvidenceRequest,
) bool {
	return request.Matches(evidence)
}

func (verifier *exactPRApprovalVerifier) Verify(
	ctx context.Context,
	request PRApprovalRequest,
) (ApprovalEvidence, error) {
	if verifier == nil || nilInterface(verifier.exact) || ctx == nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: PR approval verifier and context are required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return ApprovalEvidence{}, err
	}
	if err := validatePRApprovalRequest(request, true); err != nil {
		return ApprovalEvidence{}, err
	}
	contextEnvelope, err := BuildPRApprovalContext(request)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	expectedContext, err := json.Marshal(contextEnvelope)
	if err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: encode PR approval context", ErrInvalidRequest)
	}
	return verifier.exact.VerifyExact(ctx, DurableApprovalRequest{
		TeamID:          request.TeamID,
		WorkflowRunID:   request.WorkflowRunID,
		BuildID:         request.BuildID,
		Approval:        request.Approval,
		ExpectedContext: expectedContext,
	})
}

func validatePRApprovalRequest(request PRApprovalRequest, requireApproval bool) error {
	if request.TeamID <= 0 ||
		request.WorkflowRunID.Validate() != nil ||
		request.BuildID <= 0 ||
		request.BindingID <= 0 ||
		!operationKeyPattern.MatchString(request.ActionDigest) ||
		request.Observation.Validate() != nil ||
		request.Observation.Type != snapshot.TypeRef("pull-request/v1") ||
		request.Candidate.Validate() != nil ||
		request.Candidate.Type != snapshot.TypeRef("repository-change/v1") ||
		!validGitObjectID(request.SourceHead) ||
		!validGitObjectID(request.TargetHead) ||
		len(request.SourceHead) != len(request.TargetHead) ||
		!boundedText(request.Destination, 2048, false) ||
		request.Response.Validate() != nil ||
		request.Response.Type != snapshot.TypeRef("pull-request-response/v1") ||
		request.Validation.Validate() != nil ||
		request.Validation.Type != snapshot.TypeRef("validation/v1") ||
		request.Impact.Validate() != nil ||
		request.Impact.Type != snapshot.TypeRef("publish-impact/v1") ||
		!boundedText(request.ApprovalPolicyVersion, 128, false) {
		return fmt.Errorf("%w: PR approval request is invalid", ErrInvalidRequest)
	}
	if requireApproval &&
		(request.Approval.Validate() != nil ||
			request.Approval.Type != snapshot.TypeRef("human-answer/v1")) {
		return fmt.Errorf("%w: PR approval request is invalid", ErrInvalidRequest)
	}
	return nil
}

func prApprovalIntentDigest(request PRApprovalRequest) (string, error) {
	payload, err := json.Marshal(struct {
		TeamID                int                    `json:"team_id"`
		WorkflowRunID         snapshot.WorkflowRunID `json:"workflow_run_id"`
		BuildID               int64                  `json:"build_id"`
		BindingID             int64                  `json:"binding_id"`
		ActionDigest          string                 `json:"action_digest"`
		Observation           snapshot.SnapshotRef   `json:"observation"`
		Candidate             snapshot.SnapshotRef   `json:"candidate"`
		SourceHead            string                 `json:"source_head"`
		TargetHead            string                 `json:"target_head"`
		Destination           string                 `json:"destination"`
		Response              snapshot.SnapshotRef   `json:"response"`
		Validation            snapshot.SnapshotRef   `json:"validation"`
		Impact                snapshot.SnapshotRef   `json:"impact"`
		ApprovalPolicyVersion string                 `json:"approval_policy_version"`
	}{
		TeamID:                request.TeamID,
		WorkflowRunID:         request.WorkflowRunID,
		BuildID:               request.BuildID,
		BindingID:             request.BindingID,
		ActionDigest:          request.ActionDigest,
		Observation:           request.Observation,
		Candidate:             request.Candidate,
		SourceHead:            request.SourceHead,
		TargetHead:            request.TargetHead,
		Destination:           request.Destination,
		Response:              request.Response,
		Validation:            request.Validation,
		Impact:                request.Impact,
		ApprovalPolicyVersion: request.ApprovalPolicyVersion,
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode PR approval intent", ErrInvalidRequest)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("jetbridge-pr-approval-intent/v1\x00"))
	_, _ = hash.Write(payload)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

var _ PRApprovalVerifier = (*exactPRApprovalVerifier)(nil)
