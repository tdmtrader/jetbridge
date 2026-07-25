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
	ModeBranch      Mode = "branch"
	ModePullRequest Mode = "pull-request"
	ModeMerge       Mode = "merge"
	ModeComment     Mode = "comment"
	ModeState       Mode = "state"
)

var parameterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// Authority is trusted invocation identity supplied by the server. A caller
// may select publication parameters, but it must never select the team,
// build, workflow run, or audit actor used for authorization and credentials.
// WorkflowRunID is populated by the durable store after it verifies the
// build-to-run association; it may be zero only on an unacquired request.
type Authority struct {
	TeamID        int                    `json:"team_id"`
	TeamName      string                 `json:"team_name"`
	BuildID       int64                  `json:"build_id"`
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
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
	return nil
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
		case ModeBranch, ModePullRequest:
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
