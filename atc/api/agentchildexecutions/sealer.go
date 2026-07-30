package agentchildexecutions

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/google/uuid"
)

// CandidateResult is the sole terminal value that crosses the broker/ATC
// boundary. It deliberately contains no claimed type, subjects, profile, or
// provenance; those are execution authority owned by ATC.
type CandidateResult struct {
	Body json.RawMessage `json:"body"`
}

// OrdinaryResultSealer adapts broker results to the ordinary snapshot seal
// path. Its scope is assembled by ATC when it mints the bootstrap capability;
// the sidecar supplies only a typed candidate body.
type workspaceMetadataStore interface {
	GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

type OrdinaryResultSealer struct {
	creator  snapshot.SnapshotCreator
	metadata workspaceMetadataStore
}

func NewOrdinaryResultSealer(creator snapshot.SnapshotCreator, metadata ...workspaceMetadataStore) (*OrdinaryResultSealer, error) {
	if creator == nil {
		return nil, fmt.Errorf("agent child ordinary result sealer: snapshot creator is required")
	}
	if len(metadata) > 1 {
		return nil, fmt.Errorf("agent child ordinary result sealer: at most one metadata store is allowed")
	}
	sealer := &OrdinaryResultSealer{creator: creator}
	if len(metadata) == 1 {
		sealer.metadata = metadata[0]
	}
	return sealer, nil
}

func (sealer *OrdinaryResultSealer) SealWorkspace(
	ctx context.Context,
	scope Scope,
	executionID string,
	execution broker.ExecutionIdentity,
	capture broker.WorkspaceCapture,
) (snapshot.SnapshotRef, error) {
	if sealer == nil || sealer.creator == nil || sealer.metadata == nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: creator and metadata store are required")
	}
	if err := scope.Validate(); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if execution.Tool != broker.ToolRequestReview || scope.WorkspaceBase == nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: request_review base authority is required")
	}
	productionPlanID, err := childProductionPlanID(executionID)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if err := capture.Validate(); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	base := *scope.WorkspaceBase
	manifest, found, err := sealer.metadata.GetAuthorized(ctx, scope.TeamID, base.ID)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	if !found || manifest.ID != base.ID || manifest.Type != base.Type ||
		manifest.Digest != base.Digest || base.Type != "repository/v1" {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: exact base snapshot is unavailable")
	}
	repository, err := contracts.DecodeRepositoryMetadata(manifest.IntrinsicMetadata)
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: decode base repository metadata: %w", err)
	}
	if repository.HeadSHA != capture.BaseCommit || repository.TreeSHA != capture.BaseTree {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: capture base does not match exact repository")
	}
	subjects := []contracts.Subject{
		contracts.SubjectFromInput("base", contracts.SubjectRoleBase, "base", base),
	}
	body := contracts.RepositoryChangeBody{
		RepositoryID: repository.RepositoryID, BaseSHA: repository.HeadSHA,
		Representation: "patch",
		Payload: contracts.ContentRef{
			Path: "content/workspace.patch", Digest: snapshot.Digest(capture.PatchDigest),
			MediaType: "text/x-diff",
		},
		ResultTree: capture.ResultTree,
	}
	record, err := contracts.NewRecord(snapshot.TypeRef("repository-change/v1"), subjects, body)
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: construct authoritative record: %w", err)
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	workflowDefinitionID := scope.WorkflowDefinitionID
	workflowRunID := snapshot.WorkflowRunID(scope.WorkflowRunID)
	outputs, err := sealer.creator.Seal(ctx, snapshot.SealRequest{
		BuildID: scope.BuildID, TeamID: scope.TeamID, TeamName: scope.TeamName,
		CreatedBy: scope.SnapshotCreatedBy, PlanID: productionPlanID,
		Attempt: strconv.Itoa(scope.ParentAttempt), StepKind: "agent-child-workspace",
		StepName: execution.IdempotencyKey, WorkflowDefinitionID: &workflowDefinitionID,
		WorkflowRunID: &workflowRunID, InputOrder: []string{"base"},
		Inputs:             map[string]snapshot.SnapshotRef{"base": base},
		OutputDeclarations: []snapshot.Port{{Name: "workspace", Type: "repository-change/v1"}},
		Outputs: []snapshot.OutputSource{{
			ClientKey: "workspace", Port: snapshot.Port{Name: "workspace", Type: "repository-change/v1"},
			OpenTar:        recordAndPatchTar(recordJSON, capture.Patch),
			SourceMetadata: workspaceProvenance(scope),
		}},
	})
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: seal repository change: %w", err)
	}
	output, found := outputs["workspace"]
	if !found || output.Snapshot.Validate() != nil || output.Snapshot.Type != "repository-change/v1" {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child workspace sealer: snapshot creator returned no valid workspace")
	}
	return output.Snapshot, nil
}

func (sealer *OrdinaryResultSealer) Seal(ctx context.Context, scope Scope, executionID string, execution broker.ExecutionIdentity, candidate CandidateResult) (SealedResult, error) {
	if sealer == nil || sealer.creator == nil {
		return SealedResult{}, fmt.Errorf("agent child ordinary result sealer: snapshot creator is required")
	}
	if err := scope.Validate(); err != nil {
		return SealedResult{}, err
	}
	productionPlanID, err := childProductionPlanID(executionID)
	if err != nil {
		return SealedResult{}, err
	}
	resultType, err := resultTypeForTool(execution.Tool)
	if err != nil {
		return SealedResult{}, err
	}
	subjects, inputs, inputOrder, err := resultAuthority(scope, execution.Tool, execution.Attachments)
	if err != nil {
		return SealedResult{}, err
	}
	body, normalized, err := contracts.NormalizeRawRecordBody(resultType, subjects, candidate.Body)
	if err != nil {
		return SealedResult{}, fmt.Errorf("agent child ordinary result sealer: invalid candidate body: %w", err)
	}
	record, err := contracts.NewRecord(resultType, subjects, body)
	if err != nil {
		return SealedResult{}, fmt.Errorf("agent child ordinary result sealer: construct authoritative record: %w", err)
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return SealedResult{}, fmt.Errorf("agent child ordinary result sealer: encode authoritative record: %w", err)
	}
	workflowDefinitionID := scope.WorkflowDefinitionID
	workflowRunID := snapshot.WorkflowRunID(scope.WorkflowRunID)
	outputs, err := sealer.creator.Seal(ctx, snapshot.SealRequest{
		BuildID: scope.BuildID, TeamID: scope.TeamID, TeamName: scope.TeamName, CreatedBy: scope.SnapshotCreatedBy,
		PlanID: productionPlanID, Attempt: strconv.Itoa(scope.ParentAttempt), StepKind: "agent-child-execution", StepName: execution.IdempotencyKey,
		WorkflowDefinitionID: &workflowDefinitionID, WorkflowRunID: &workflowRunID,
		InputOrder: inputOrder, Inputs: inputs,
		OutputDeclarations: []snapshot.Port{{Name: "result", Type: resultType}},
		Outputs: []snapshot.OutputSource{{
			ClientKey: "result", Port: snapshot.Port{Name: "result", Type: resultType},
			OpenTar:        recordTar(recordJSON),
			SourceMetadata: resultProvenance(scope, execution.Tool),
		}},
	})
	if err != nil {
		return SealedResult{}, fmt.Errorf("agent child ordinary result sealer: seal result: %w", err)
	}
	output, found := outputs["result"]
	if !found || output.Port.Type != resultType || output.Snapshot.Type != resultType || output.Snapshot.ID <= 0 {
		return SealedResult{}, fmt.Errorf("agent child ordinary result sealer: snapshot creator returned no valid result")
	}
	return SealedResult{Snapshot: output.Snapshot, Body: normalized}, nil
}

func resultTypeForTool(tool broker.Tool) (snapshot.TypeRef, error) {
	switch tool {
	case broker.ToolRequestReview:
		return snapshot.TypeRef("review/v1"), nil
	case broker.ToolConsultAgent:
		return snapshot.TypeRef("consultation/v1"), nil
	default:
		return "", fmt.Errorf("agent child ordinary result sealer: unsupported tool %q", tool)
	}
}

func resultAuthority(scope Scope, tool broker.Tool, attachments []string) ([]contracts.Subject, map[string]snapshot.SnapshotRef, []string, error) {
	if len(attachments) == 0 {
		return nil, nil, nil, fmt.Errorf("agent child ordinary result sealer: result inputs are required")
	}
	requested := make(map[string]struct{}, len(attachments))
	for _, name := range attachments {
		requested[name] = struct{}{}
	}
	ordered := make([]string, 0, len(attachments))
	switch tool {
	case broker.ToolRequestReview:
		ordered = append(ordered, "workspace")
		if _, found := requested["validation"]; found {
			ordered = append(ordered, "validation")
		}
	case broker.ToolConsultAgent:
		if _, found := requested["design"]; found {
			ordered = append(ordered, "design")
		}
		if _, found := requested["api-contract"]; found {
			ordered = append(ordered, "api-contract")
		}
	default:
		return nil, nil, nil, fmt.Errorf("agent child ordinary result sealer: unsupported tool %q", tool)
	}
	inputs := make(map[string]snapshot.SnapshotRef, len(ordered))
	subjects := make([]contracts.Subject, 0, len(attachments))
	seen := make(map[string]struct{}, len(attachments))
	for index, name := range ordered {
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, nil, fmt.Errorf("agent child ordinary result sealer: duplicate authority input %q", name)
		}
		seen[name] = struct{}{}
		ref, found := scope.Inputs[name]
		if !found || ref.Validate() != nil {
			return nil, nil, nil, fmt.Errorf("agent child ordinary result sealer: immutable input authority %q is unavailable", name)
		}
		role := contracts.SubjectRoleContext
		if index == 0 || name == "workspace" || name == "design" {
			role = contracts.SubjectRolePrimary
		}
		if index > 0 && (name == "validation" || name == "api-contract") {
			role = contracts.SubjectRoleEvidence
		}
		inputs[name] = ref
		subjects = append(subjects, contracts.SubjectFromInput(name, role, name, ref))
	}
	inputOrder := append([]string(nil), ordered...)
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].ID < subjects[j].ID })
	return subjects, inputs, inputOrder, nil
}

func resultProvenance(scope Scope, tool broker.Tool) json.RawMessage {
	// Static-review and tests-not-run are fixed server facts, never candidate
	// attributes. Consultation has no review provenance.
	if tool != broker.ToolRequestReview {
		encoded, _ := json.Marshal(struct {
			ParentPlanID string `json:"parent_plan_id"`
			Provenance   string `json:"provenance"`
		}{ParentPlanID: scope.NodePlanID, Provenance: "atc"})
		return encoded
	}
	encoded, _ := json.Marshal(struct {
		ParentPlanID string `json:"parent_plan_id"`
		StaticReview bool   `json:"static_review"`
		TestsRun     bool   `json:"tests_run"`
		Provenance   string `json:"provenance"`
	}{ParentPlanID: scope.NodePlanID, StaticReview: true, TestsRun: false, Provenance: "atc"})
	return encoded
}

func workspaceProvenance(scope Scope) json.RawMessage {
	encoded, _ := json.Marshal(struct {
		Capture      string `json:"capture"`
		ParentPlanID string `json:"parent_plan_id"`
		Provenance   string `json:"provenance"`
	}{
		Capture: "git-workspace-capture/v2", ParentPlanID: scope.NodePlanID,
		Provenance: "atc",
	})
	return encoded
}

func childProductionPlanID(executionID string) (string, error) {
	parsed, err := uuid.Parse(executionID)
	if err != nil {
		return "", fmt.Errorf("agent child sealer: execution ID must be a UUID")
	}
	sum := sha256.Sum256([]byte(parsed.String()))
	return "agent-child/sha256:" + hex.EncodeToString(sum[:]), nil
}

func recordTar(record []byte) func(context.Context) (io.ReadCloser, error) {
	return func(context.Context) (io.ReadCloser, error) {
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if err := writer.WriteHeader(&tar.Header{Name: "record.json", Mode: 0o600, Size: int64(len(record))}); err != nil {
			return nil, err
		}
		if _, err := writer.Write(record); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(archive.Bytes())), nil
	}
}

func recordAndPatchTar(record, patch []byte) func(context.Context) (io.ReadCloser, error) {
	return func(context.Context) (io.ReadCloser, error) {
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		for _, file := range []struct {
			name string
			body []byte
		}{
			{name: "record.json", body: record},
			{name: "content/workspace.patch", body: patch},
		} {
			if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.body))}); err != nil {
				return nil, err
			}
			if _, err := writer.Write(file.body); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(archive.Bytes())), nil
	}
}

// Keep this helper deterministic should ATC ever choose to add unreferenced
// inputs to Scope. Only execution-declared inputs above are seal lineage.
func sortedInputNames(inputs map[string]snapshot.SnapshotRef) []string {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
