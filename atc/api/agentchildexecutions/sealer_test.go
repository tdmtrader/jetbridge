package agentchildexecutions_test

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/atc/api/agentchildexecutions"
)

func TestOrdinaryResultSealerDerivesReviewAuthorityAndUsesSnapshotCreator(t *testing.T) {
	creator := &recordingSnapshotCreator{}
	sealer, err := agentchildexecutions.NewOrdinaryResultSealer(creator)
	if err != nil {
		t.Fatal(err)
	}
	workspace := snapshot.SnapshotRef{ID: 11, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}
	validation := snapshot.SnapshotRef{ID: 12, Type: "validation/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("b", 64))}
	_, err = sealer.Seal(context.Background(), agentchildexecutions.Scope{
		TeamID: 1, TeamName: "main", BuildID: 2, SnapshotCreatedBy: "atc",
		WorkflowRunID: 3, NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker", LeaseDuration: time.Minute,
		Inputs: map[string]snapshot.SnapshotRef{"workspace": workspace, "validation": validation},
	}, broker.ExecutionIdentity{IdempotencyKey: "review", Tool: broker.ToolRequestReview, Attachments: []string{"workspace", "validation"}}, agentchildexecutions.CandidateResult{
		Body: json.RawMessage(`{"conclusion":"accept","summary":"looks good","findings":[]}`),
	})
	if err != nil {
		t.Fatalf("Seal(): %v", err)
	}
	if creator.request.BuildID != 2 || creator.request.TeamID != 1 || len(creator.request.OutputDeclarations) != 1 || creator.request.OutputDeclarations[0].Type != "review/v1" {
		t.Fatalf("snapshot request = %#v", creator.request)
	}
	record := readRecordFromOutput(t, creator.request.Outputs[0])
	if record.Type != "review/v1" || len(record.Subjects) != 2 || record.Subjects[0] != contracts.SubjectFromInput("validation", contracts.SubjectRoleEvidence, "validation", validation) || record.Subjects[1] != contracts.SubjectFromInput("workspace", contracts.SubjectRolePrimary, "workspace", workspace) {
		t.Fatalf("server-derived record = %#v", record)
	}
}

func TestOrdinaryResultSealerRejectsSidecarResultTypeAndMissingAuthorityInput(t *testing.T) {
	sealer, err := agentchildexecutions.NewOrdinaryResultSealer(&recordingSnapshotCreator{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sealer.Seal(context.Background(), agentchildexecutions.Scope{
		TeamID: 1, TeamName: "main", BuildID: 2, SnapshotCreatedBy: "atc",
		WorkflowRunID: 3, NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker", LeaseDuration: time.Minute,
		Inputs: map[string]snapshot.SnapshotRef{"workspace": {ID: 1, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}},
	}, broker.ExecutionIdentity{Tool: broker.ToolConsultAgent, Attachments: []string{"design"}}, agentchildexecutions.CandidateResult{
		Body: json.RawMessage(`{"answer":"answer","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`),
	})
	if err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("Seal() error = %v, want missing ATC input authority", err)
	}
}

func TestOrdinaryResultSealerMakesSoleConsultationAttachmentPrimary(t *testing.T) {
	creator := &recordingSnapshotCreator{}
	sealer, err := agentchildexecutions.NewOrdinaryResultSealer(creator)
	if err != nil {
		t.Fatal(err)
	}
	apiContract := snapshot.SnapshotRef{ID: 13, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("d", 64))}
	_, err = sealer.Seal(context.Background(), agentchildexecutions.Scope{
		TeamID: 1, TeamName: "main", BuildID: 2, SnapshotCreatedBy: "atc", WorkflowRunID: 3,
		NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker", LeaseDuration: time.Minute,
		Inputs: map[string]snapshot.SnapshotRef{"api-contract": apiContract},
	}, broker.ExecutionIdentity{IdempotencyKey: "consult", Tool: broker.ToolConsultAgent, Attachments: []string{"api-contract"}}, agentchildexecutions.CandidateResult{
		Body: json.RawMessage(`{"answer":"answer","claims":[],"assumptions":[],"uncertainties":[],"recommendations":[]}`),
	})
	if err != nil {
		t.Fatalf("Seal(): %v", err)
	}
	record := readRecordFromOutput(t, creator.request.Outputs[0])
	if len(record.Subjects) != 1 || record.Subjects[0].Role != contracts.SubjectRolePrimary {
		t.Fatalf("consultation subjects = %#v", record.Subjects)
	}
}

type recordingSnapshotCreator struct{ request snapshot.SealRequest }

func (creator *recordingSnapshotCreator) Seal(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	creator.request = request
	return map[string]snapshot.SealedOutput{"result": {Port: request.OutputDeclarations[0], Snapshot: snapshot.SnapshotRef{ID: 99, Type: request.OutputDeclarations[0].Type, Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64))}}}, nil
}
func (*recordingSnapshotCreator) Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error) {
	return snapshot.Snapshot{}, nil
}

func readRecordFromOutput(t *testing.T, output snapshot.OutputSource) contracts.Record[json.RawMessage] {
	t.Helper()
	stream, err := output.OpenTar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reader := tar.NewReader(stream)
	header, err := reader.Next()
	if err != nil || header.Name != "record.json" {
		t.Fatalf("tar header = %#v err=%v", header, err)
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var record contracts.Record[json.RawMessage]
	if err := json.Unmarshal(contents, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
