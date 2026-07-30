package agentchildexecutions_test

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
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
		WorkflowDefinitionID: 31, WorkflowRunID: 3, NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker", LeaseDuration: time.Minute,
		Inputs: map[string]snapshot.SnapshotRef{"workspace": workspace, "validation": validation},
	}, "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", broker.ExecutionIdentity{IdempotencyKey: "review", Tool: broker.ToolRequestReview, Attachments: []string{"workspace", "validation"}}, agentchildexecutions.CandidateResult{
		Body: json.RawMessage(`{"conclusion":"accept","summary":"looks good","findings":[]}`),
	})
	if err != nil {
		t.Fatalf("Seal(): %v", err)
	}
	if creator.request.BuildID != 2 || creator.request.TeamID != 1 || len(creator.request.OutputDeclarations) != 1 || creator.request.OutputDeclarations[0].Type != "review/v1" {
		t.Fatalf("snapshot request = %#v", creator.request)
	}
	if creator.request.WorkflowDefinitionID == nil || *creator.request.WorkflowDefinitionID != 31 || creator.request.WorkflowRunID == nil || *creator.request.WorkflowRunID != snapshot.WorkflowRunID(3) {
		t.Fatalf("snapshot occurrence authority = definition=%v run=%v", creator.request.WorkflowDefinitionID, creator.request.WorkflowRunID)
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
		WorkflowDefinitionID: 31, WorkflowRunID: 3, NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker", LeaseDuration: time.Minute,
		Inputs: map[string]snapshot.SnapshotRef{"workspace": {ID: 1, Type: "repository-change/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}},
	}, "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", broker.ExecutionIdentity{Tool: broker.ToolConsultAgent, Attachments: []string{"design"}}, agentchildexecutions.CandidateResult{
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
		TeamID: 1, TeamName: "main", BuildID: 2, SnapshotCreatedBy: "atc", WorkflowDefinitionID: 31, WorkflowRunID: 3,
		NodePlanID: "node", ParentAttempt: 1, BrokerInstance: "broker", LeaseDuration: time.Minute,
		Inputs: map[string]snapshot.SnapshotRef{"api-contract": apiContract},
	}, "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", broker.ExecutionIdentity{IdempotencyKey: "consult", Tool: broker.ToolConsultAgent, Attachments: []string{"api-contract"}}, agentchildexecutions.CandidateResult{
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

func TestOrdinaryResultSealerBuildsAuthoritativeRepositoryChangeFromBoundBase(t *testing.T) {
	creator := &recordingSnapshotCreator{}
	base := snapshot.SnapshotRef{ID: 41, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64))}
	metadata := json.RawMessage(`{"repository_id":"sha256:` + strings.Repeat("a", 64) + `","object_format":"sha1","head_sha":"` + strings.Repeat("1", 40) + `","tree_sha":"` + strings.Repeat("2", 40) + `","root_commits":["` + strings.Repeat("1", 40) + `"]}`)
	sealer, err := agentchildexecutions.NewOrdinaryResultSealer(creator, fakeWorkspaceMetadata{
		snapshot: snapshot.Snapshot{ID: base.ID, Type: base.Type, Digest: base.Digest, IntrinsicMetadata: metadata},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := completeScope()
	delete(scope.Inputs, "workspace")
	scope.WorkspaceBase = &base
	capture := broker.WorkspaceCapture{
		BaseCommit: strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree: strings.Repeat("3", 40), Patch: []byte("patch"),
		PatchDigest: "sha256:a4895eb44afc336fecbba6e520cd67e178dace0276655d102fceffa8e5f70570",
		EntryCount:  7, PolicyRevision: "git-workspace-capture/v2",
	}
	sealed, err := sealer.SealWorkspace(context.Background(), scope, "7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98", broker.ExecutionIdentity{
		IdempotencyKey: "review", Tool: broker.ToolRequestReview,
	}, capture)
	if err != nil {
		t.Fatalf("SealWorkspace(): %v", err)
	}
	if sealed.ID != 99 || creator.request.InputOrder[0] != "base" ||
		creator.request.Inputs["base"] != base ||
		creator.request.OutputDeclarations[0].Type != "repository-change/v1" {
		t.Fatalf("seal request = %#v sealed=%#v", creator.request, sealed)
	}
	files := readTarFiles(t, creator.request.Outputs[0])
	if string(files["content/workspace.patch"]) != "patch" {
		t.Fatalf("workspace patch = %q", files["content/workspace.patch"])
	}
	var record contracts.Record[contracts.RepositoryChangeBody]
	if err := json.Unmarshal(files["record.json"], &record); err != nil {
		t.Fatal(err)
	}
	if record.Body.RepositoryID != "sha256:"+strings.Repeat("a", 64) ||
		record.Body.BaseSHA != capture.BaseCommit || record.Body.ResultTree != capture.ResultTree ||
		record.Body.Representation != "patch" || len(record.Subjects) != 1 ||
		record.Subjects[0] != contracts.SubjectFromInput("base", contracts.SubjectRoleBase, "base", base) {
		t.Fatalf("repository change record = %#v", record)
	}
}

func TestOrdinaryResultSealerUsesDistinctStableProductionIdentityPerChild(t *testing.T) {
	creator := &collisionSnapshotCreator{seen: map[string]struct{}{}}
	base := snapshot.SnapshotRef{ID: 41, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("4", 64))}
	metadata := json.RawMessage(`{"repository_id":"sha256:` + strings.Repeat("a", 64) + `","object_format":"sha1","head_sha":"` + strings.Repeat("1", 40) + `","tree_sha":"` + strings.Repeat("2", 40) + `","root_commits":["` + strings.Repeat("1", 40) + `"]}`)
	sealer, err := agentchildexecutions.NewOrdinaryResultSealer(creator, fakeWorkspaceMetadata{
		snapshot: snapshot.Snapshot{ID: base.ID, Type: base.Type, Digest: base.Digest, IntrinsicMetadata: metadata},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseScope := completeScope()
	baseScope.Inputs = map[string]snapshot.SnapshotRef{}
	baseScope.WorkspaceBase = &base
	capture := broker.WorkspaceCapture{
		BaseCommit: strings.Repeat("1", 40), BaseTree: strings.Repeat("2", 40),
		ResultTree:  strings.Repeat("2", 40),
		PatchDigest: "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		EntryCount:  1, PolicyRevision: "git-workspace-capture/v2",
	}
	ids := []string{
		"7c5b1d7f-4ab1-451a-a6c8-d6b0a4d4dd98",
		"c34a6e95-2e3a-45b0-b3f0-30c4e09acb7d",
	}
	var group sync.WaitGroup
	errs := make(chan error, len(ids))
	for _, executionID := range ids {
		executionID := executionID
		group.Add(1)
		go func() {
			defer group.Done()
			identity := broker.ExecutionIdentity{
				IdempotencyKey: executionID, Tool: broker.ToolRequestReview,
				Attachments: []string{"workspace"},
			}
			workspaceRef, err := sealer.SealWorkspace(context.Background(), baseScope, executionID, identity, capture)
			if err != nil {
				errs <- err
				return
			}
			resultScope := baseScope
			resultScope.Inputs = map[string]snapshot.SnapshotRef{"workspace": workspaceRef}
			_, err = sealer.Seal(context.Background(), resultScope, executionID, identity, agentchildexecutions.CandidateResult{
				Body: json.RawMessage(`{"conclusion":"accept","summary":"ok","findings":[]}`),
			})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(creator.planIDs) != 2 {
		t.Fatalf("child plan IDs = %#v", creator.planIDs)
	}
	for _, planID := range creator.planIDs {
		if !strings.HasPrefix(planID, "agent-child/sha256:") || len(planID) != len("agent-child/sha256:")+64 {
			t.Fatalf("child plan ID = %q", planID)
		}
	}
}

type fakeWorkspaceMetadata struct {
	snapshot snapshot.Snapshot
}

func (store fakeWorkspaceMetadata) GetAuthorized(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	return store.snapshot, id == store.snapshot.ID, nil
}

type recordingSnapshotCreator struct{ request snapshot.SealRequest }

func (creator *recordingSnapshotCreator) Seal(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	creator.request = request
	return map[string]snapshot.SealedOutput{request.Outputs[0].ClientKey: {Port: request.OutputDeclarations[0], Snapshot: snapshot.SnapshotRef{ID: 99, Type: request.OutputDeclarations[0].Type, Digest: snapshot.Digest("sha256:" + strings.Repeat("c", 64))}}}, nil
}

type collisionSnapshotCreator struct {
	mu      sync.Mutex
	seen    map[string]struct{}
	planIDs []string
	nextID  atomic.Int64
}

func (creator *collisionSnapshotCreator) Seal(_ context.Context, request snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	creator.mu.Lock()
	defer creator.mu.Unlock()
	key := fmt.Sprintf("%d/%s/%s/%s", request.BuildID, request.PlanID, request.Attempt, request.OutputDeclarations[0].Name)
	if _, found := creator.seen[key]; found {
		return nil, fmt.Errorf("production collision: %s", key)
	}
	creator.seen[key] = struct{}{}
	foundPlan := false
	for _, planID := range creator.planIDs {
		if planID == request.PlanID {
			foundPlan = true
		}
	}
	if !foundPlan {
		creator.planIDs = append(creator.planIDs, request.PlanID)
	}
	id := snapshot.SnapshotID(creator.nextID.Add(1))
	output := request.Outputs[0]
	return map[string]snapshot.SealedOutput{output.ClientKey: {
		Port: output.Port, Snapshot: snapshot.SnapshotRef{
			ID: id, Type: output.Port.Type,
			Digest: snapshot.Digest("sha256:" + fmt.Sprintf("%064x", id)),
		},
	}}, nil
}

func (*collisionSnapshotCreator) Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error) {
	return snapshot.Snapshot{}, nil
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

func readTarFiles(t *testing.T, output snapshot.OutputSource) map[string][]byte {
	t.Helper()
	stream, err := output.OpenTar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	reader := tar.NewReader(stream)
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = content
	}
}
