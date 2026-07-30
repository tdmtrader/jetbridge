package agentchildexecutions

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
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
type OrdinaryResultSealer struct{ creator snapshot.SnapshotCreator }

func NewOrdinaryResultSealer(creator snapshot.SnapshotCreator) (*OrdinaryResultSealer, error) {
	if creator == nil {
		return nil, fmt.Errorf("agent child ordinary result sealer: snapshot creator is required")
	}
	return &OrdinaryResultSealer{creator: creator}, nil
}

func (sealer *OrdinaryResultSealer) Seal(ctx context.Context, scope Scope, execution broker.ExecutionIdentity, candidate CandidateResult) (snapshot.SnapshotRef, error) {
	if sealer == nil || sealer.creator == nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child ordinary result sealer: snapshot creator is required")
	}
	if err := scope.Validate(); err != nil {
		return snapshot.SnapshotRef{}, err
	}
	resultType, err := resultTypeForTool(execution.Tool)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	subjects, inputs, inputOrder, err := resultAuthority(scope, execution.Tool, execution.Attachments)
	if err != nil {
		return snapshot.SnapshotRef{}, err
	}
	body, _, err := contracts.NormalizeRawRecordBody(resultType, subjects, candidate.Body)
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child ordinary result sealer: invalid candidate body: %w", err)
	}
	record, err := contracts.NewRecord(resultType, subjects, body)
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child ordinary result sealer: construct authoritative record: %w", err)
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child ordinary result sealer: encode authoritative record: %w", err)
	}
	outputs, err := sealer.creator.Seal(ctx, snapshot.SealRequest{
		BuildID: scope.BuildID, TeamID: scope.TeamID, TeamName: scope.TeamName, CreatedBy: scope.SnapshotCreatedBy,
		PlanID: scope.NodePlanID, Attempt: strconv.Itoa(scope.ParentAttempt), StepKind: "agent-child-execution", StepName: execution.IdempotencyKey,
		InputOrder: inputOrder, Inputs: inputs,
		OutputDeclarations: []snapshot.Port{{Name: "result", Type: resultType}},
		Outputs: []snapshot.OutputSource{{
			ClientKey: "result", Port: snapshot.Port{Name: "result", Type: resultType},
			OpenTar:        recordTar(recordJSON),
			SourceMetadata: resultProvenance(execution.Tool),
		}},
	})
	if err != nil {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child ordinary result sealer: seal result: %w", err)
	}
	output, found := outputs["result"]
	if !found || output.Port.Type != resultType || output.Snapshot.Type != resultType || output.Snapshot.ID <= 0 {
		return snapshot.SnapshotRef{}, fmt.Errorf("agent child ordinary result sealer: snapshot creator returned no valid result")
	}
	return output.Snapshot, nil
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
	inputs := make(map[string]snapshot.SnapshotRef, len(attachments))
	subjects := make([]contracts.Subject, 0, len(attachments))
	seen := make(map[string]struct{}, len(attachments))
	for index, name := range attachments {
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
	if tool == broker.ToolRequestReview && attachments[0] != "workspace" {
		return nil, nil, nil, fmt.Errorf("agent child ordinary result sealer: review requires authoritative workspace input")
	}
	inputOrder := append([]string(nil), attachments...)
	sort.Slice(subjects, func(i, j int) bool { return subjects[i].ID < subjects[j].ID })
	return subjects, inputs, inputOrder, nil
}

func resultProvenance(tool broker.Tool) json.RawMessage {
	// Static-review and tests-not-run are fixed server facts, never candidate
	// attributes. Consultation has no review provenance.
	if tool != broker.ToolRequestReview {
		return nil
	}
	return json.RawMessage(`{"static_review":true,"tests_run":false,"provenance":"atc"}`)
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
