// Package publisher defines explicit, idempotent external side-effect
// operations over validated snapshots.
package publisher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

var ErrInvalidRequest = errors.New("publisher: invalid request")

const (
	GitPublisher      snapshot.TypeRef = "git-publisher/v1"
	WorkItemPublisher snapshot.TypeRef = "work-item-publisher/v1"
)

type Mode string

const (
	ModeBranch  Mode = "branch"
	ModeMerge   Mode = "merge"
	ModeComment Mode = "comment"
	ModeState   Mode = "state"
)

var parameterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Authority is trusted invocation identity supplied by the server. A caller
// may select publication parameters, but it must never select the team,
// build, workflow run, or audit actor used for authorization and credentials.
// WorkflowRunID is populated by the durable store after it verifies the
// build-to-run association; it may be zero only on an unacquired request.
// PlanID locates the publishing step inside the build's plan. Like BuildID it
// is server-supplied identity rather than authored configuration, and it is
// what lets the durable node-occurrence projection join a publish node to the
// occurrence it produced — agent_publication_occurrences is otherwise keyed
// (publication_id, workflow_run_id, build_id) and carries no plan identity at
// all. It is deliberately outside operation identity: Request.OperationKey
// does not see it, so publishing the same effect from a different plan
// position stays the same idempotent operation.
type Authority struct {
	TeamID        int                    `json:"team_id"`
	TeamName      string                 `json:"team_name"`
	BuildID       int64                  `json:"build_id"`
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	PlanID        string                 `json:"plan_id,omitempty"`
	Actor         string                 `json:"actor"`
}

// ApprovalEvidence identifies the exact durable human wait that authorized a
// merge. The exec step derives it from server-owned workflow state; authored
// workflow configuration can only name the approval artifact to consume.
type ApprovalEvidence struct {
	WaitID     workflowwait.ID      `json:"wait_id"`
	Question   snapshot.SnapshotRef `json:"question"`
	Answer     snapshot.SnapshotRef `json:"answer"`
	ResolvedBy string               `json:"resolved_by"`
	ResolvedAt time.Time            `json:"resolved_at"`
}

func (evidence ApprovalEvidence) validate() error {
	if evidence.WaitID.Validate() != nil || evidence.Question.Validate() != nil || evidence.Answer.Validate() != nil ||
		evidence.Question.Type != snapshot.TypeRef("question/v1") ||
		evidence.Answer.Type != snapshot.TypeRef("human-answer/v1") ||
		!boundedText(evidence.ResolvedBy, 256, false) || evidence.ResolvedAt.IsZero() {
		return fmt.Errorf("%w: durable merge approval evidence is invalid", ErrInvalidRequest)
	}
	return nil
}

func (evidence ApprovalEvidence) Validate() error {
	return evidence.validate()
}

type EvidenceKind string

const (
	EvidenceAcceptedReview EvidenceKind = "accepted_review"
	EvidenceHumanWait      EvidenceKind = "human_wait"
)

// AcceptedReviewEvidence is the immutable authority chain for publishing a
// candidate that an exact code-review workflow output accepted. Candidate is
// resolved from the run's primary input binding; it is never inferred from a
// record digest or copied from a review projection.
type AcceptedReviewEvidence struct {
	Review              snapshot.SnapshotRef   `json:"review"`
	Candidate           snapshot.SnapshotRef   `json:"candidate"`
	Validation          snapshot.SnapshotRef   `json:"validation"`
	ReviewWorkflowRunID snapshot.WorkflowRunID `json:"review_workflow_run_id"`
	OutcomeRevision     int64                  `json:"outcome_revision"`
	AcceptedBy          string                 `json:"accepted_by"`
	AcceptedAt          time.Time              `json:"accepted_at"`
}

func (evidence AcceptedReviewEvidence) Validate() error {
	if evidence.Review.Validate() != nil || evidence.Review.Type != snapshot.TypeRef("review/v1") ||
		evidence.Candidate.Validate() != nil || evidence.Candidate.Type != snapshot.TypeRef("repository/v1") ||
		evidence.Validation.Validate() != nil || evidence.Validation.Type != snapshot.TypeRef("validation/v1") ||
		evidence.ReviewWorkflowRunID.Validate() != nil || evidence.OutcomeRevision <= 0 ||
		!boundedText(evidence.AcceptedBy, 256, false) || !boundedEvidenceTime(evidence.AcceptedAt) {
		return fmt.Errorf("%w: accepted review evidence is invalid", ErrInvalidRequest)
	}
	return nil
}

// PublicationEvidence is a closed, mutually exclusive union. AcceptedReview
// authorizes initial PR publication without manufacturing another approval;
// HumanWait records a later exact durable approval when policy escalates.
type PublicationEvidence struct {
	Kind           EvidenceKind            `json:"kind"`
	AcceptedReview *AcceptedReviewEvidence `json:"accepted_review,omitempty"`
	HumanWait      *ApprovalEvidence       `json:"human_wait,omitempty"`
}

func (evidence PublicationEvidence) Validate() error {
	switch evidence.Kind {
	case EvidenceAcceptedReview:
		if evidence.AcceptedReview == nil || evidence.HumanWait != nil {
			return fmt.Errorf("%w: accepted review evidence must be exclusive", ErrInvalidRequest)
		}
		return evidence.AcceptedReview.Validate()
	case EvidenceHumanWait:
		if evidence.HumanWait == nil || evidence.AcceptedReview != nil {
			return fmt.Errorf("%w: human wait evidence must be exclusive", ErrInvalidRequest)
		}
		return evidence.HumanWait.Validate()
	default:
		return fmt.Errorf("%w: publication evidence kind is invalid", ErrInvalidRequest)
	}
}

func (evidence PublicationEvidence) Clone() PublicationEvidence {
	if evidence.AcceptedReview != nil {
		value := *evidence.AcceptedReview
		evidence.AcceptedReview = &value
	}
	if evidence.HumanWait != nil {
		value := *evidence.HumanWait
		evidence.HumanWait = &value
	}
	return evidence
}

type AcceptedReviewEvidenceRequest struct {
	Review              snapshot.SnapshotRef   `json:"review"`
	Candidate           snapshot.SnapshotRef   `json:"candidate"`
	Validation          snapshot.SnapshotRef   `json:"validation"`
	ReviewWorkflowRunID snapshot.WorkflowRunID `json:"review_workflow_run_id"`
	OutcomeRevision     int64                  `json:"outcome_revision"`
}

func (request AcceptedReviewEvidenceRequest) validate() error {
	if request.Review.Validate() != nil || request.Review.Type != snapshot.TypeRef("review/v1") ||
		request.Candidate.Validate() != nil || request.Candidate.Type != snapshot.TypeRef("repository/v1") ||
		request.Validation.Validate() != nil || request.Validation.Type != snapshot.TypeRef("validation/v1") ||
		request.ReviewWorkflowRunID.Validate() != nil || request.OutcomeRevision <= 0 {
		return fmt.Errorf("%w: accepted review evidence request is invalid", ErrInvalidRequest)
	}
	return nil
}

func (request AcceptedReviewEvidenceRequest) Validate() error {
	return request.validate()
}

func (request AcceptedReviewEvidenceRequest) Matches(evidence PublicationEvidence) bool {
	if request.validate() != nil ||
		evidence.Kind != EvidenceAcceptedReview ||
		evidence.AcceptedReview == nil ||
		evidence.HumanWait != nil ||
		evidence.Validate() != nil {
		return false
	}
	accepted := evidence.AcceptedReview
	return accepted.Review == request.Review &&
		accepted.Candidate == request.Candidate &&
		accepted.Validation == request.Validation &&
		accepted.ReviewWorkflowRunID == request.ReviewWorkflowRunID &&
		accepted.OutcomeRevision == request.OutcomeRevision
}

type EvidenceRequest struct {
	TeamID         int                            `json:"team_id"`
	AcceptedReview *AcceptedReviewEvidenceRequest `json:"accepted_review,omitempty"`
	HumanWait      *DurableApprovalRequest        `json:"human_wait,omitempty"`
}

func (request EvidenceRequest) validate() error {
	if request.TeamID <= 0 || (request.AcceptedReview == nil) == (request.HumanWait == nil) {
		return fmt.Errorf("%w: publication evidence request must select exactly one variant", ErrInvalidRequest)
	}
	if request.AcceptedReview != nil {
		return request.AcceptedReview.validate()
	}
	if request.HumanWait.TeamID != request.TeamID {
		return fmt.Errorf("%w: human wait team does not match publication evidence", ErrInvalidRequest)
	}
	return request.HumanWait.validate()
}

func (request EvidenceRequest) clone() EvidenceRequest {
	if request.AcceptedReview != nil {
		value := *request.AcceptedReview
		request.AcceptedReview = &value
	}
	if request.HumanWait != nil {
		value := request.HumanWait.clone()
		request.HumanWait = &value
	}
	return request
}

func boundedEvidenceTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}

func (authority Authority) validate(requireWorkflowRun bool) error {
	if authority.TeamID <= 0 || authority.BuildID <= 0 ||
		!boundedText(authority.TeamName, 256, false) || !boundedText(authority.Actor, 256, false) {
		return fmt.Errorf("%w: trusted publication authority is invalid", ErrInvalidRequest)
	}
	if requireWorkflowRun {
		if err := authority.WorkflowRunID.Validate(); err != nil {
			return fmt.Errorf("%w: trusted workflow run is invalid", ErrInvalidRequest)
		}
	} else if authority.WorkflowRunID != 0 {
		if err := authority.WorkflowRunID.Validate(); err != nil {
			return fmt.Errorf("%w: trusted workflow run is invalid", ErrInvalidRequest)
		}
	}
	// PlanID is optional: publications recorded before the projection existed
	// carry none, and a request that never reaches a workflow run has no plan
	// position worth asserting. When present it must still be sane text,
	// because it is written straight into a durable join key.
	if authority.PlanID != "" && !boundedText(authority.PlanID, 256, false) {
		return fmt.Errorf("%w: trusted publication plan is invalid", ErrInvalidRequest)
	}
	return nil
}

// OperationIdentity is the authority as the publication OPERATION persists it,
// which is the authority without its plan position.
//
// A publication operation is deliberately shared across plan positions: the
// same effect published from a retry copy, or replayed from a later step, is
// one operation with one lease and one result, and agent_publications stores
// no plan column at all. Plan identity is occurrence-scoped — it lives on
// agent_publication_occurrences, where the node-occurrence projection joins to
// it. A store that revalidates a rehydrated request against the live one must
// therefore compare THIS, or it would reject the very replay the shared
// operation exists to allow.
func (authority Authority) OperationIdentity() Authority {
	authority.PlanID = ""
	return authority
}

type Request struct {
	Publisher             snapshot.TypeRef     `json:"publisher"`
	Input                 snapshot.SnapshotRef `json:"input"`
	Destination           string               `json:"destination"`
	Mode                  Mode                 `json:"mode"`
	Parameters            map[string]string    `json:"parameters"`
	ApprovalPolicyVersion string               `json:"approval_policy_version"`
	ApprovedBy            string               `json:"approved_by,omitempty"`
	Approval              *ApprovalEvidence    `json:"approval,omitempty"`
	Authority             Authority            `json:"authority"`
}

func (request Request) Validate() error {
	if err := request.Authority.validate(false); err != nil {
		return err
	}
	if err := request.Publisher.Validate(); err != nil {
		return fmt.Errorf("%w: publisher: %v", ErrInvalidRequest, err)
	}
	if request.Publisher != GitPublisher && request.Publisher != WorkItemPublisher {
		return fmt.Errorf("%w: unsupported publisher %q", ErrInvalidRequest, request.Publisher)
	}
	if err := request.Input.Validate(); err != nil {
		return fmt.Errorf("%w: input: %v", ErrInvalidRequest, err)
	}
	if !boundedText(request.Destination, 2048, false) {
		return fmt.Errorf("%w: destination is invalid", ErrInvalidRequest)
	}
	if !boundedText(request.ApprovalPolicyVersion, 128, false) {
		return fmt.Errorf("%w: approval_policy_version is required", ErrInvalidRequest)
	}
	for name, value := range request.Parameters {
		if !parameterNamePattern.MatchString(name) {
			return fmt.Errorf("%w: parameter name %q is invalid", ErrInvalidRequest, name)
		}
		// Approval attribution is injected from authenticated build metadata.
		// Do not let an authored backend parameter masquerade as that actor.
		if name == "approved_by" {
			return fmt.Errorf("%w: parameter %q is reserved", ErrInvalidRequest, name)
		}
		if len(value) > 64<<10 || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("%w: parameter %q is invalid", ErrInvalidRequest, name)
		}
	}
	switch request.Publisher {
	case GitPublisher:
		if request.Input.Type != "repository-change/v1" {
			return fmt.Errorf("%w: git publisher requires repository-change/v1", ErrInvalidRequest)
		}
		switch request.Mode {
		case ModeBranch:
			if !requiredParameter(request.Parameters, "source_branch") || !requiredParameter(request.Parameters, "target_branch") {
				return fmt.Errorf("%w: branch publication requires source_branch and target_branch", ErrInvalidRequest)
			}
		case ModeMerge:
			// A durable merge request always carries the base assertion. It is
			// not authored: the execution boundary stamps it from the bound
			// repository-change/v1 snapshot (see MergeBaseParameter).
			if !requiredParameter(request.Parameters, "target_branch") || !requiredParameter(request.Parameters, MergeBaseParameter) {
				return fmt.Errorf("%w: merge requires target_branch and %s", ErrInvalidRequest, MergeBaseParameter)
			}
			if !boundedText(request.ApprovedBy, 256, false) {
				return fmt.Errorf("%w: merge requires verified approval", ErrInvalidRequest)
			}
			if request.Approval == nil || request.Approval.validate() != nil || request.Approval.ResolvedBy != request.ApprovedBy {
				return fmt.Errorf("%w: merge requires exact durable approval evidence", ErrInvalidRequest)
			}
		default:
			return fmt.Errorf("%w: unsupported git publisher mode %q", ErrInvalidRequest, request.Mode)
		}
	case WorkItemPublisher:
		if request.ApprovedBy != "" || request.Approval != nil {
			return fmt.Errorf("%w: approval actor is only valid for merge", ErrInvalidRequest)
		}
		switch request.Mode {
		case ModeComment:
			if !requiredParameter(request.Parameters, "body") {
				return fmt.Errorf("%w: comment publication requires body", ErrInvalidRequest)
			}
		case ModeState:
			if !requiredParameter(request.Parameters, "state") {
				return fmt.Errorf("%w: state publication requires state", ErrInvalidRequest)
			}
		default:
			return fmt.Errorf("%w: unsupported work-item publisher mode %q", ErrInvalidRequest, request.Mode)
		}
	}
	if request.Publisher == GitPublisher && request.Mode != ModeMerge && (request.ApprovedBy != "" || request.Approval != nil) {
		return fmt.Errorf("%w: approval actor is only valid for merge", ErrInvalidRequest)
	}
	return nil
}

// ValidatePersisted additionally requires the workflow-run identity derived
// and verified by Store.Acquire. External side-effect adapters must only act
// on requests that pass this stronger validation.
func (request Request) ValidatePersisted() error {
	if err := request.Validate(); err != nil {
		return err
	}
	return request.Authority.validate(true)
}

func (request Request) Clone() Request {
	request.Parameters = cloneParameters(request.Parameters)
	if request.Approval != nil {
		approval := *request.Approval
		request.Approval = &approval
	}
	return request
}

// OperationKey is the canonical semantic idempotency identity. Approval actor
// is audit attribution, not part of the external operation identity.
func (request Request) OperationKey() (string, error) {
	request = request.Clone()
	if request.Parameters == nil {
		request.Parameters = map[string]string{}
	}
	if err := request.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		TeamID                int               `json:"team_id"`
		Publisher             snapshot.TypeRef  `json:"publisher"`
		InputSnapshotID       string            `json:"input_snapshot_id"`
		Destination           string            `json:"destination"`
		Mode                  Mode              `json:"mode"`
		Parameters            map[string]string `json:"parameters"`
		ApprovalPolicyVersion string            `json:"approval_policy_version"`
	}{
		TeamID: request.Authority.TeamID, Publisher: request.Publisher, InputSnapshotID: request.Input.ID.String(),
		Destination: request.Destination, Mode: request.Mode, Parameters: request.Parameters,
		ApprovalPolicyVersion: request.ApprovalPolicyVersion,
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode operation identity: %v", ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func requiredParameter(parameters map[string]string, name string) bool {
	value, found := parameters[name]
	return found && strings.TrimSpace(value) != ""
}

func boundedText(value string, maximum int, allowNewline bool) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && !(allowNewline && (character == '\n' || character == '\r' || character == '\t')) {
			return false
		}
	}
	return true
}

func cloneParameters(parameters map[string]string) map[string]string {
	if parameters == nil {
		return nil
	}
	copy := make(map[string]string, len(parameters))
	for name, value := range parameters {
		copy[name] = value
	}
	return copy
}
