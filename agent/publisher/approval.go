package publisher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflowwait"
)

type WaitLister interface {
	List(context.Context, int, snapshot.WorkflowRunID) ([]workflowwait.Wait, error)
}

// MergeApprovalContext is encoded as the question/v1 context string for a
// merge approval. Keeping this exact machine-readable envelope inside the
// human-visible question binds the decision to one change, base, destination,
// workflow run, and policy version.
type MergeApprovalContext struct {
	SchemaVersion         string                 `json:"schema_version"`
	WorkflowRunID         snapshot.WorkflowRunID `json:"workflow_run_id"`
	InputSnapshotID       snapshot.SnapshotID    `json:"input_snapshot_id"`
	InputDigest           snapshot.Digest        `json:"input_digest"`
	Publisher             snapshot.TypeRef       `json:"publisher"`
	Mode                  Mode                   `json:"mode"`
	Destination           string                 `json:"destination"`
	TargetBranch          string                 `json:"target_branch"`
	ExpectedBaseSHA       string                 `json:"expected_base_sha"`
	ApprovalPolicyVersion string                 `json:"approval_policy_version"`
	IntentDigest          string                 `json:"intent_digest"`
}

type MergeApprovalRequest struct {
	TeamID        int
	WorkflowRunID snapshot.WorkflowRunID
	BuildID       int64
	Input         snapshot.SnapshotRef
	Approval      snapshot.SnapshotRef
	Publisher     snapshot.TypeRef
	Mode          Mode
	Destination   string
	Parameters    map[string]string
	// ExpectedBaseSHA is server-derived, never authored: it is read from the
	// Input snapshot's sealed intrinsic metadata at execution time. See
	// MergeBaseParameter in mergebase.go for the full decision record. A
	// durable merge request must always carry it, and it must equal
	// Parameters[MergeBaseParameter] so the intent digest covers it.
	ExpectedBaseSHA       string
	ApprovalPolicyVersion string
}

type MergeApprovalVerifier interface {
	Verify(context.Context, MergeApprovalRequest) (ApprovalEvidence, error)
}

// DurableApprovalRequest is the generic, server-owned half of a durable human
// approval check. ExpectedContext must be the exact canonical machine-readable
// context the operation being attempted derives. Typed intent builders --
// today only the merge wrapper here -- remain responsible for deriving and
// validating that context.
type DurableApprovalRequest struct {
	TeamID          int                    `json:"team_id"`
	WorkflowRunID   snapshot.WorkflowRunID `json:"workflow_run_id"`
	BuildID         int64                  `json:"build_id"`
	Approval        snapshot.SnapshotRef   `json:"approval"`
	ExpectedContext json.RawMessage        `json:"expected_context"`
}

func (request DurableApprovalRequest) clone() DurableApprovalRequest {
	request.ExpectedContext = append(json.RawMessage(nil), request.ExpectedContext...)
	return request
}

func (request DurableApprovalRequest) validate() error {
	if request.TeamID <= 0 || request.BuildID <= 0 ||
		request.WorkflowRunID.Validate() != nil ||
		request.Approval.Validate() != nil ||
		request.Approval.Type != snapshot.TypeRef("human-answer/v1") ||
		len(request.ExpectedContext) == 0 || len(request.ExpectedContext) > 64<<10 ||
		!json.Valid(request.ExpectedContext) ||
		!bytes.Equal(bytes.TrimSpace(request.ExpectedContext), request.ExpectedContext) ||
		request.ExpectedContext[0] != '{' {
		return fmt.Errorf("%w: durable approval request is invalid", ErrInvalidRequest)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, request.ExpectedContext); err != nil ||
		!bytes.Equal(compact.Bytes(), request.ExpectedContext) {
		return fmt.Errorf("%w: durable approval context must be canonical JSON", ErrInvalidRequest)
	}
	return nil
}

type ExactApprovalVerifier interface {
	VerifyExact(context.Context, DurableApprovalRequest) (ApprovalEvidence, error)
}

type DurableApprovalVerifier struct {
	waits         WaitLister
	metadata      snapshot.MetadataStore
	content       snapshot.ContentStore
	canonicalizer snapshot.Canonicalizer
}

type ApprovalVerifierOption func(*DurableApprovalVerifier) error

func WithApprovalCanonicalizer(canonicalizer snapshot.Canonicalizer) ApprovalVerifierOption {
	return func(verifier *DurableApprovalVerifier) error {
		if err := snapshot.ValidateTempDir(canonicalizer.TempDir); err != nil {
			return err
		}
		verifier.canonicalizer = canonicalizer
		return nil
	}
}

func NewDurableApprovalVerifier(
	waits WaitLister,
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	options ...ApprovalVerifierOption,
) (*DurableApprovalVerifier, error) {
	if nilInterface(waits) || nilInterface(metadata) || nilInterface(content) {
		return nil, fmt.Errorf("publisher: approval waits, metadata, and content stores are required")
	}
	verifier := &DurableApprovalVerifier{waits: waits, metadata: metadata, content: content}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("publisher: approval verifier option is required")
		}
		if err := option(verifier); err != nil {
			return nil, fmt.Errorf("publisher: approval verifier option: %w", err)
		}
	}
	return verifier, nil
}

// BuildMergeApprovalContext returns the canonical, human-visible envelope
// for one exact merge intent. IntentDigest covers the complete parameter map,
// not just the fields Jetbridge currently interprets, so a provider adapter
// cannot attach new semantics to an already approved operation.
func BuildMergeApprovalContext(request MergeApprovalRequest) (MergeApprovalContext, error) {
	if err := validateMergeApprovalRequest(request, false); err != nil {
		return MergeApprovalContext{}, err
	}
	digest, err := mergeApprovalIntentDigest(request)
	if err != nil {
		return MergeApprovalContext{}, err
	}
	return MergeApprovalContext{
		SchemaVersion: "1.0.0", WorkflowRunID: request.WorkflowRunID,
		InputSnapshotID: request.Input.ID, InputDigest: request.Input.Digest,
		Publisher: request.Publisher, Mode: request.Mode,
		Destination: request.Destination, TargetBranch: request.Parameters["target_branch"],
		ExpectedBaseSHA:       request.ExpectedBaseSHA,
		ApprovalPolicyVersion: request.ApprovalPolicyVersion,
		IntentDigest:          digest,
	}, nil
}

func validateMergeApprovalRequest(request MergeApprovalRequest, requireApproval bool) error {
	if request.TeamID <= 0 || request.BuildID <= 0 || request.WorkflowRunID.Validate() != nil ||
		request.Input.Validate() != nil || request.Input.Type != snapshot.TypeRef("repository-change/v1") ||
		request.Publisher != GitPublisher || request.Mode != ModeMerge ||
		!boundedText(request.Destination, 2048, false) ||
		// The base assertion is stamped by the server from the bound
		// repository-change/v1 snapshot, so a durable request always has one
		// and it is always a complete Git object ID.
		!validGitObjectID(request.ExpectedBaseSHA) ||
		!boundedText(request.ApprovalPolicyVersion, 128, false) {
		return fmt.Errorf("%w: merge approval request is invalid", ErrInvalidRequest)
	}
	if requireApproval && (request.Approval.Validate() != nil || request.Approval.Type != snapshot.TypeRef("human-answer/v1")) {
		return fmt.Errorf("%w: merge approval request is invalid", ErrInvalidRequest)
	}
	parameters := cloneParameters(request.Parameters)
	if parameters[MergeBaseParameter] != request.ExpectedBaseSHA || !requiredParameter(parameters, "target_branch") {
		return fmt.Errorf("%w: merge approval request parameters are invalid", ErrInvalidRequest)
	}
	// Reuse the publisher request's parameter validation without pretending
	// the pre-wait intent already carries human approval evidence.
	probe := Request{
		Publisher: request.Publisher, Input: request.Input, Destination: request.Destination,
		Mode: request.Mode, Parameters: parameters, ApprovalPolicyVersion: request.ApprovalPolicyVersion,
		ApprovedBy: "approval-probe",
		Approval: &ApprovalEvidence{
			WaitID:     workflowwait.ID(1),
			Question:   snapshot.SnapshotRef{ID: 1, Type: "question/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("0", 64))},
			Answer:     snapshot.SnapshotRef{ID: 2, Type: "human-answer/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("1", 64))},
			ResolvedBy: "approval-probe", ResolvedAt: requestTimeProbe,
		},
		Authority: Authority{
			TeamID: request.TeamID, TeamName: "approval-probe", BuildID: request.BuildID,
			WorkflowRunID: request.WorkflowRunID, Actor: "approval-probe",
		},
	}
	if err := probe.ValidatePersisted(); err != nil {
		return fmt.Errorf("%w: merge approval request parameters are invalid", ErrInvalidRequest)
	}
	return nil
}

var requestTimeProbe = time.Unix(1, 0).UTC()

func mergeApprovalIntentDigest(request MergeApprovalRequest) (string, error) {
	payload, err := json.Marshal(struct {
		Publisher             snapshot.TypeRef     `json:"publisher"`
		Input                 snapshot.SnapshotRef `json:"input"`
		Destination           string               `json:"destination"`
		Mode                  Mode                 `json:"mode"`
		Parameters            map[string]string    `json:"parameters"`
		ApprovalPolicyVersion string               `json:"approval_policy_version"`
	}{
		Publisher: request.Publisher, Input: request.Input, Destination: request.Destination,
		Mode: request.Mode, Parameters: cloneParameters(request.Parameters),
		ApprovalPolicyVersion: request.ApprovalPolicyVersion,
	})
	if err != nil {
		return "", fmt.Errorf("%w: encode merge approval intent", ErrInvalidRequest)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("jetbridge-merge-approval-intent/v1\x00"))
	_, _ = hash.Write(payload)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (verifier *DurableApprovalVerifier) Verify(ctx context.Context, request MergeApprovalRequest) (ApprovalEvidence, error) {
	if ctx == nil || validateMergeApprovalRequest(request, true) != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge approval request is invalid", ErrInvalidRequest)
	}
	approvalContext, err := BuildMergeApprovalContext(request)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	expectedContext, err := json.Marshal(approvalContext)
	if err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: encode merge approval context", ErrInvalidRequest)
	}
	return verifier.VerifyExact(ctx, DurableApprovalRequest{
		TeamID: request.TeamID, WorkflowRunID: request.WorkflowRunID,
		BuildID: request.BuildID, Approval: request.Approval,
		ExpectedContext: expectedContext,
	})
}

func (verifier *DurableApprovalVerifier) VerifyExact(
	ctx context.Context,
	request DurableApprovalRequest,
) (ApprovalEvidence, error) {
	if verifier == nil || ctx == nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: durable approval verifier and context are required", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return ApprovalEvidence{}, err
	}
	request = request.clone()
	if err := request.validate(); err != nil {
		return ApprovalEvidence{}, err
	}
	waits, err := verifier.waits.List(ctx, request.TeamID, request.WorkflowRunID)
	if err != nil {
		return ApprovalEvidence{}, fmt.Errorf("publisher: list durable approval waits: %w", err)
	}
	var matched *workflowwait.Wait
	for index := range waits {
		wait := &waits[index]
		if wait.Key.TeamID != request.TeamID ||
			wait.Key.WorkflowRunID != request.WorkflowRunID ||
			wait.Key.BuildID != request.BuildID ||
			wait.Status != workflowwait.StatusResolved ||
			wait.ResolutionSource != "human" || wait.Answer == nil || *wait.Answer != request.Approval {
			continue
		}
		if matched != nil {
			return ApprovalEvidence{}, fmt.Errorf("%w: merge approval evidence is ambiguous", ErrInvalidRequest)
		}
		matched = wait
	}
	if matched == nil || matched.ResolvedAt == nil ||
		!boundedText(matched.ResolvedBy, 256, false) ||
		!boundedEvidenceTime(*matched.ResolvedAt) {
		return ApprovalEvidence{}, fmt.Errorf("%w: exact resolved durable approval was not found", ErrInvalidRequest)
	}

	questionManifest, err := verifier.authorizedManifest(ctx, request.TeamID, matched.Question)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	answerManifest, err := verifier.authorizedManifest(ctx, request.TeamID, request.Approval)
	if err != nil {
		return ApprovalEvidence{}, err
	}
	var question contracts.QuestionDocument
	if err := verifier.readDocument(ctx, questionManifest, "question.json", &question); err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge approval question is unavailable", ErrInvalidRequest)
	}
	if err := question.Validate(); err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge approval question is invalid", ErrInvalidRequest)
	}
	var answer contracts.HumanAnswerDocument
	if err := verifier.readDocument(ctx, answerManifest, "human-answer.json", &answer); err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge approval answer is unavailable", ErrInvalidRequest)
	}
	if err := answer.Validate(); err != nil {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge approval answer is invalid", ErrInvalidRequest)
	}
	if answer.TimedOut || answer.Answer != "approve" || answer.AnsweredBy != matched.ResolvedBy {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge was not affirmatively approved by the resolving actor", ErrInvalidRequest)
	}
	if !containsExact(question.Options, "approve") || !containsExact(question.Options, "reject") || question.Default == "approve" {
		return ApprovalEvidence{}, fmt.Errorf("%w: merge approval question is not fail-closed", ErrInvalidRequest)
	}
	if question.Context != string(request.ExpectedContext) {
		return ApprovalEvidence{}, fmt.Errorf("%w: durable approval does not bind the exact publication intent", ErrInvalidRequest)
	}
	evidence := ApprovalEvidence{
		WaitID: matched.ID, Question: matched.Question, Answer: request.Approval,
		ResolvedBy: matched.ResolvedBy, ResolvedAt: matched.ResolvedAt.UTC(),
	}
	if err := evidence.Validate(); err != nil {
		return ApprovalEvidence{}, err
	}
	return evidence, nil
}

func (verifier *DurableApprovalVerifier) authorizedManifest(ctx context.Context, teamID int, ref snapshot.SnapshotRef) (snapshot.Snapshot, error) {
	manifest, found, err := verifier.metadata.GetAuthorized(ctx, teamID, ref.ID)
	if err != nil || !found || manifest.Validate() != nil || manifest.ContentState != snapshot.ContentStateAvailable ||
		manifest.ID != ref.ID || manifest.Type != ref.Type || manifest.Digest != ref.Digest {
		return snapshot.Snapshot{}, fmt.Errorf("%w: approval snapshot is unavailable", ErrInvalidRequest)
	}
	return manifest, nil
}

func (verifier *DurableApprovalVerifier) readDocument(ctx context.Context, manifest snapshot.Snapshot, name string, destination any) error {
	reader, err := verifier.content.Open(ctx, manifest)
	if err != nil || reader == nil {
		return fmt.Errorf("open snapshot content")
	}
	tree, captureErr := verifier.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if err := errors.Join(captureErr, closeErr); err != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return err
	}
	defer tree.Close()
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize || tree.FileCount != manifest.FileCount {
		return fmt.Errorf("snapshot content does not match manifest")
	}
	root, err := os.OpenRoot(tree.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	var payload bytes.Buffer
	if _, err := io.Copy(&payload, io.LimitReader(file, (1<<20)+1)); err != nil || payload.Len() > 1<<20 {
		return fmt.Errorf("approval document exceeds limit")
	}
	decoder := json.NewDecoder(&payload)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("approval document has trailing data")
	}
	return nil
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var _ MergeApprovalVerifier = (*DurableApprovalVerifier)(nil)
var _ ExactApprovalVerifier = (*DurableApprovalVerifier)(nil)
