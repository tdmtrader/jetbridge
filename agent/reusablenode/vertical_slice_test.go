package reusablenode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/workflowtest"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const verticalRuntimeImage = "registry.example/agent-runner@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// TestReusableNodeVerticalSlice catches a break anywhere in the exact lifecycle
// boundary: immutable import, pre-release direct execution, released
// composition, an exact binding keyed by immutable definition identity,
// selected compatible upgrade, or historical pinning. Persistence is
// in-memory, while compilation, rendering, binding, compatibility, promotion
// validation, and upgrade rewriting are production code.
func TestReusableNodeVerticalSlice(t *testing.T) {
	ctx := context.Background()
	platform := newVerticalPlatform()

	nodeV1Manifest, err := workflow.ManifestFromDir("../workflow/seeds/code-review-node-v1")
	if err != nil {
		t.Fatalf("load code-review node seed: %v", err)
	}
	nodeV1, err := platform.ImportManifest("code-review", nodeV1Manifest, "node-author")
	if err != nil {
		t.Fatalf("import code-review@1: %v", err)
	}
	if nodeV1.Version != 1 || nodeV1.Release.ReleasedAt != 0 {
		t.Fatalf("imported node = %+v, want unreleased code-review@1", nodeV1)
	}

	before := verticalSnapshot(101, "repository/v1", "1")
	after := verticalSnapshot(102, "repository/v1", "2")
	runStore := newVerticalRunStore()
	snapshots := verticalSnapshotAuthorizer{
		teamID: 7,
		values: map[snapshot.SnapshotID]snapshot.Snapshot{
			before.ID: before,
			after.ID:  after,
		},
	}
	executions := &verticalExecutionCreator{runs: runStore}
	renderer := workflowrun.WorkflowTargetRenderer{RuntimeImage: verticalRuntimeImage}
	binder, err := workflowrun.NewBinder(
		verticalDefinitionResolver{store: platform.workflows},
		renderer,
		snapshots,
		runStore,
		workflowrun.AllowAllBudgetAdmitter{},
		verticalTemplateSaver{},
		executions,
		verticalCredentialAdmitter{},
		workflowrun.WithNodeStore(platform),
	)
	if err != nil {
		t.Fatalf("construct direct-run binder: %v", err)
	}

	nodeV1Version := nodeV1.Version
	runResult, err := binder.BindAndCreate(ctx, workflowrun.AdmissionContext{
		TeamID:    7,
		TeamName:  "main",
		CreatedBy: "node-tester",
		Origin:    workflowrun.Origin{Kind: "manual"},
	}, workflowrun.BindRequest{
		DefinitionKind: workflow.DefinitionKindNode,
		WorkflowName:   "code-review",
		Version:        &nodeV1Version,
		NodeParameters: map[string]string{"MINIMUM_SEVERITY": "high"},
		Inputs: map[string]snapshot.SnapshotID{
			"before": before.ID,
			"after":  after.ID,
		},
		IdempotencyKey: "code-review-v1-fixture",
	})
	if err != nil {
		t.Fatalf("run unreleased code-review@1: %v", err)
	}
	if !runResult.Created || runResult.Run.Status != db.AgentWorkflowRunStatusRunning {
		t.Fatalf("direct-run result = %+v, want newly running durable run", runResult)
	}
	if runResult.Run.DefinitionKind != workflow.DefinitionKindNode ||
		runResult.Run.WorkflowDefinitionID != nodeV1.ID ||
		runResult.Run.WorkflowVersion != nodeV1.Version ||
		runResult.Run.DefinitionContentHash != nodeV1.ContentHash {
		t.Fatalf("direct run lost exact node identity: %+v", runResult.Run)
	}
	if got := runStore.inputIDs(runResult.Run.ID); !reflect.DeepEqual(got, map[string]snapshot.SnapshotID{
		"after": after.ID, "before": before.ID,
	}) {
		t.Fatalf("direct-run snapshot bindings = %#v", got)
	}
	if got := nodeParameterFromRun(t, runResult.Run, "MINIMUM_SEVERITY"); got != "high" {
		t.Fatalf("direct-run MINIMUM_SEVERITY = %q, want high", got)
	}
	assertFrozenReviewAgent(t, agentFromRun(t, runResult.Run), false)
	if !reflect.DeepEqual(executions.params, map[string]any{
		"snapshot_after":  after.ID.String(),
		"snapshot_before": before.ID.String(),
		"workflow_run_id": runResult.Run.ID.String(),
	}) {
		t.Fatalf("direct execution params = %#v", executions.params)
	}
	historicalConfig := append(json.RawMessage(nil), runResult.Run.ParameterizedConfig...)
	historicalConfigHash := runResult.Run.ParameterizedConfigHash
	historicalInputs := runStore.inputIDs(runResult.Run.ID)

	releaseV1, err := platform.Release("code-review", nodeV1.Version, workflow.ReleaseCompatible, "node-releaser")
	if err != nil {
		t.Fatalf("release code-review@1: %v", err)
	}
	if releaseV1.ReleasedAt == 0 || releaseV1.PredecessorVersion != 0 {
		t.Fatalf("code-review@1 release = %+v", releaseV1)
	}

	workflowV1, err := platform.workflows.ImportManifest(
		"review-change",
		verticalConsumerManifest(1),
		"workflow-author",
	)
	if err != nil {
		t.Fatalf("import workflow using code-review@1: %v", err)
	}
	if _, err := platform.workflows.Promote("review-change", workflowV1.Version, "workflow-promoter"); err != nil {
		t.Fatalf("promote workflow using code-review@1: %v", err)
	}
	liveV1, found, err := platform.workflows.Live("review-change")
	if err != nil || !found {
		t.Fatalf("load promoted workflow: found=%v err=%v", found, err)
	}
	if liveV1.Version != workflowV1.Version || !liveV1.Live {
		t.Fatalf("promoted workflow = %+v", liveV1)
	}
	if len(liveV1.Compiled.Function.Plan) != 1 {
		t.Fatalf("expanded workflow plan has %d leaves, want one", len(liveV1.Compiled.Function.Plan))
	}
	agent, ok := liveV1.Compiled.Function.Plan[0].Config.(*atc.AgentStep)
	if !ok || agent.Name != "review" || agent.FunctionID != "review-change" {
		t.Fatalf("expanded workflow leaf = %#v, want visible review-change agent", liveV1.Compiled.Function.Plan[0].Config)
	}
	assertFrozenReviewAgent(t, agent, true)
	bindings, err := platform.Bindings(liveV1.ID)
	if err != nil {
		t.Fatalf("load workflow node bindings: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("workflow bindings = %#v, want one", bindings)
	}
	bindingV1 := bindings[0]
	if bindingV1.InstanceName != "review-change" ||
		bindingV1.NodeDefinitionID != nodeV1.ID ||
		bindingV1.NodeVersion != nodeV1.Version ||
		bindingV1.NodeContentHash != nodeV1.ContentHash ||
		!reflect.DeepEqual(bindingV1.InputMapping, map[string]string{"after": "candidate", "before": "base"}) ||
		!reflect.DeepEqual(bindingV1.OutputMapping, map[string]string{"review": "review"}) ||
		!reflect.DeepEqual(bindingV1.Parameters, map[string]string{"MINIMUM_SEVERITY": "high"}) {
		t.Fatalf("workflow binding = %+v", bindingV1)
	}

	nodeV2Manifest := cloneManifest(nodeV1Manifest)
	nodeV2Manifest["prompts/review.md"] += "\nReport the tested node version in the review summary.\n"
	nodeV2, err := platform.ImportManifest("code-review", nodeV2Manifest, "node-author")
	if err != nil {
		t.Fatalf("import code-review@2: %v", err)
	}
	if nodeV2.Version != 2 || nodeV2.Release.ReleasedAt != 0 {
		t.Fatalf("imported successor = %+v, want unreleased code-review@2", nodeV2)
	}
	if nodeV2.ContentHash == nodeV1.ContentHash {
		t.Fatal("code-review@2 did not capture the changed prompt")
	}

	nodeV2Version := nodeV2.Version
	testV2, err := binder.BindAndCreate(ctx, workflowrun.AdmissionContext{
		TeamID:    7,
		TeamName:  "main",
		CreatedBy: "node-tester",
		Origin:    workflowrun.Origin{Kind: "manual"},
	}, workflowrun.BindRequest{
		DefinitionKind: workflow.DefinitionKindNode,
		WorkflowName:   "code-review",
		Version:        &nodeV2Version,
		NodeParameters: map[string]string{"MINIMUM_SEVERITY": "medium"},
		Inputs: map[string]snapshot.SnapshotID{
			"before": before.ID,
			"after":  after.ID,
		},
		IdempotencyKey: "code-review-v2-fixture",
	})
	if err != nil {
		t.Fatalf("directly test unreleased code-review@2: %v", err)
	}
	if testV2.Run.WorkflowVersion != 2 || testV2.Run.DefinitionContentHash != nodeV2.ContentHash {
		t.Fatalf("code-review@2 test run = %+v", testV2.Run)
	}
	if got := agentFromRun(t, testV2.Run).Prompt; !strings.Contains(got, "Report the tested node version in the review summary.") {
		t.Fatalf("code-review@2 direct run did not use the changed prompt: %q", got)
	}
	releaseV2, err := platform.Release("code-review", nodeV2.Version, workflow.ReleaseCompatible, "node-releaser")
	if err != nil {
		t.Fatalf("release compatible code-review@2: %v", err)
	}
	if releaseV2.ReleasedAt == 0 ||
		releaseV2.PredecessorVersion != nodeV1.Version ||
		releaseV2.Compatibility != workflow.ReleaseCompatible {
		t.Fatalf("code-review@2 release = %+v", releaseV2)
	}

	upgrade, err := workflow.NewNodeUpgradeService(platform, platform.workflows).Upgrade(ctx, workflow.NodeUpgradeRequest{
		NodeName:  "code-review",
		Version:   nodeV2.Version,
		Workflows: []string{"review-change"},
		CreatedBy: "workflow-upgrader",
	})
	if err != nil {
		t.Fatalf("upgrade selected workflow: %v", err)
	}
	if !reflect.DeepEqual(upgrade.Workflows, []workflow.NodeUpgradeWorkflowResult{{
		Workflow:   "review-change",
		OldVersion: workflowV1.Version,
		NewVersion: workflowV1.Version + 1,
		Status:     workflow.NodeUpgradeCreated,
	}}) {
		t.Fatalf("upgrade result = %#v", upgrade.Workflows)
	}
	upgraded, found, err := platform.workflows.Get("review-change", workflowV1.Version+1)
	if err != nil || !found {
		t.Fatalf("load upgraded revision: found=%v err=%v", found, err)
	}
	if upgraded.Live {
		t.Fatalf("upgraded revision was promoted automatically: %+v", upgraded)
	}
	upgradedSource := upgraded.SourceManifest[workflow.WorkflowFileName]
	if !strings.Contains(upgradedSource, "uses: code-review@2") ||
		strings.Contains(upgradedSource, "uses: code-review@1") {
		t.Fatalf("upgraded source did not rewrite the exact node reference: %q", upgradedSource)
	}
	upgradedBindings, err := platform.Bindings(upgraded.ID)
	if err != nil || len(upgradedBindings) != 1 {
		t.Fatalf("upgraded bindings = %#v err=%v", upgradedBindings, err)
	}
	if upgradedBindings[0].NodeDefinitionID != nodeV2.ID ||
		upgradedBindings[0].NodeVersion != nodeV2.Version ||
		upgradedBindings[0].NodeContentHash != nodeV2.ContentHash {
		t.Fatalf("upgraded binding = %+v, want exact code-review@2", upgradedBindings[0])
	}

	stillLive, found, err := platform.workflows.Live("review-change")
	if err != nil || !found {
		t.Fatalf("reload live workflow: found=%v err=%v", found, err)
	}
	if stillLive.ID != workflowV1.ID || stillLive.Version != workflowV1.Version {
		t.Fatalf("selected upgrade mutated promotion: %+v", stillLive)
	}
	liveSource := stillLive.SourceManifest[workflow.WorkflowFileName]
	if !strings.Contains(liveSource, "uses: code-review@1") ||
		strings.Contains(liveSource, "uses: code-review@2") {
		t.Fatalf("selected upgrade mutated promoted source: %q", liveSource)
	}
	stillLiveBindings, err := platform.Bindings(stillLive.ID)
	if err != nil || len(stillLiveBindings) != 1 || stillLiveBindings[0].NodeVersion != 1 {
		t.Fatalf("promoted workflow lost code-review@1 pin: %#v err=%v", stillLiveBindings, err)
	}
	historical, found, err := runStore.GetKind(ctx, 7, workflow.DefinitionKindNode, runResult.Run.ID)
	if err != nil || !found {
		t.Fatalf("reload historical code-review@1 run: found=%v err=%v", found, err)
	}
	if historical.WorkflowDefinitionID != nodeV1.ID ||
		historical.WorkflowVersion != nodeV1.Version ||
		historical.DefinitionContentHash != nodeV1.ContentHash {
		t.Fatalf("historical run was rebound by release or upgrade: %+v", historical)
	}
	if historical.ParameterizedConfigHash != historicalConfigHash ||
		!bytes.Equal(historical.ParameterizedConfig, historicalConfig) {
		t.Fatalf("historical run config changed: hash=%q config=%s", historical.ParameterizedConfigHash, historical.ParameterizedConfig)
	}
	if got := runStore.inputIDs(historical.ID); !reflect.DeepEqual(got, historicalInputs) {
		t.Fatalf("historical run snapshot bindings changed: %#v", got)
	}
}

type verticalPlatform struct {
	*workflowtest.MemoryNodeStore

	workflows *verticalWorkflowStore

	mu       sync.Mutex
	bindings map[int][]workflow.ResolvedNodeBinding
}

func newVerticalPlatform() *verticalPlatform {
	platform := &verticalPlatform{
		MemoryNodeStore: workflowtest.NewMemoryNodeStore(),
		bindings:        map[int][]workflow.ResolvedNodeBinding{},
	}
	renderer := workflowrun.WorkflowTargetRenderer{RuntimeImage: verticalRuntimeImage}
	base := workflowtest.NewMemoryStoreWithNodeResolver(platform, renderer)
	platform.workflows = &verticalWorkflowStore{Store: base, platform: platform}
	return platform
}

func (platform *verticalPlatform) Bindings(workflowDefinitionID int) ([]workflow.ResolvedNodeBinding, error) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	return cloneBindings(platform.bindings[workflowDefinitionID]), nil
}

func (platform *verticalPlatform) recordBindings(
	workflowDefinitionID int,
	bindings []workflow.ResolvedNodeBinding,
) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	platform.bindings[workflowDefinitionID] = cloneBindings(bindings)
}

type verticalWorkflowStore struct {
	workflow.Store
	platform *verticalPlatform
}

func (store *verticalWorkflowStore) ImportManifest(
	name string,
	manifest workflow.Manifest,
	createdBy string,
) (*workflow.Definition, error) {
	outcome, err := store.ImportManifestWithOutcome(name, manifest, createdBy)
	if err != nil {
		return nil, err
	}
	return outcome.Definition, nil
}

func (store *verticalWorkflowStore) ImportManifestWithOutcome(
	name string,
	manifest workflow.Manifest,
	createdBy string,
) (workflow.ImportOutcome, error) {
	_, bindings, err := workflow.CompileDefinitionWithNodes(manifest, store.platform)
	if err != nil {
		return workflow.ImportOutcome{}, err
	}
	outcome, err := store.Store.ImportManifestWithOutcome(name, manifest, createdBy)
	if err != nil {
		return workflow.ImportOutcome{}, err
	}
	if outcome.Definition == nil {
		return workflow.ImportOutcome{}, fmt.Errorf("vertical store: import returned no definition")
	}
	store.platform.recordBindings(outcome.Definition.ID, bindings)
	return outcome, nil
}

type verticalDefinitionResolver struct {
	store workflow.Store
}

func (resolver verticalDefinitionResolver) Live(
	_ context.Context,
	name string,
) (workflow.Definition, bool, error) {
	definition, found, err := resolver.store.Live(name)
	if definition == nil {
		return workflow.Definition{}, found, err
	}
	return *definition, found, err
}

func (resolver verticalDefinitionResolver) Get(
	_ context.Context,
	name string,
	version int,
) (workflow.Definition, bool, error) {
	definition, found, err := resolver.store.Get(name, version)
	if definition == nil {
		return workflow.Definition{}, found, err
	}
	return *definition, found, err
}

type verticalSnapshotAuthorizer struct {
	teamID int
	values map[snapshot.SnapshotID]snapshot.Snapshot
}

func (authorizer verticalSnapshotAuthorizer) GetAuthorized(
	_ context.Context,
	teamID int,
	id snapshot.SnapshotID,
) (snapshot.Snapshot, bool, error) {
	if teamID != authorizer.teamID {
		return snapshot.Snapshot{}, false, nil
	}
	value, found := authorizer.values[id]
	return value.Clone(), found, nil
}

type verticalRunStore struct {
	mu       sync.Mutex
	nextID   snapshot.WorkflowRunID
	runs     map[snapshot.WorkflowRunID]db.AgentWorkflowRun
	byKey    map[string]snapshot.WorkflowRunID
	bindings map[snapshot.WorkflowRunID][]db.AgentWorkflowRunSnapshotBinding
}

func newVerticalRunStore() *verticalRunStore {
	return &verticalRunStore{
		nextID:   700,
		runs:     map[snapshot.WorkflowRunID]db.AgentWorkflowRun{},
		byKey:    map[string]snapshot.WorkflowRunID{},
		bindings: map[snapshot.WorkflowRunID][]db.AgentWorkflowRunSnapshotBinding{},
	}
}

func (store *verticalRunStore) FindByIdempotencyKey(
	ctx context.Context,
	teamID int,
	key string,
) (db.AgentWorkflowRun, bool, error) {
	return store.FindByIdempotencyKeyKind(ctx, teamID, workflow.DefinitionKindWorkflow, key)
}

func (store *verticalRunStore) FindByIdempotencyKeyKind(
	_ context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	key string,
) (db.AgentWorkflowRun, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	id, found := store.byKey[verticalRunKey(teamID, kind, key)]
	if !found {
		return db.AgentWorkflowRun{}, false, nil
	}
	return cloneRun(store.runs[id]), true, nil
}

func (store *verticalRunStore) Get(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool, error) {
	return store.GetKind(ctx, teamID, workflow.DefinitionKindWorkflow, id)
}

func (store *verticalRunStore) GetKind(
	_ context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	id snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	run, found := store.runs[id]
	if !found || run.TeamID != teamID || run.DefinitionKind != kind {
		return db.AgentWorkflowRun{}, false, nil
	}
	return cloneRun(run), true, nil
}

func (store *verticalRunStore) CreateWithInputs(
	_ context.Context,
	request db.AgentWorkflowRunCreateRequest,
) (db.AgentWorkflowRun, bool, error) {
	if err := request.Validate(); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := verticalRunKey(request.TeamID, request.DefinitionKind, request.IdempotencyKey)
	if id, found := store.byKey[key]; found {
		return cloneRun(store.runs[id]), false, nil
	}
	store.nextID++
	run := db.AgentWorkflowRun{
		ID:                          store.nextID,
		DefinitionKind:              request.DefinitionKind,
		TeamID:                      request.TeamID,
		TeamName:                    request.TeamName,
		WorkflowDefinitionID:        request.WorkflowDefinitionID,
		WorkflowName:                request.WorkflowName,
		WorkflowVersion:             request.WorkflowVersion,
		SchemaVersion:               request.SchemaVersion,
		SignatureVersion:            request.SignatureVersion,
		DefinitionContentHash:       request.DefinitionContentHash,
		FunctionID:                  cloneString(request.FunctionID),
		IdempotencyKey:              request.IdempotencyKey,
		ParameterizedConfig:         append(json.RawMessage(nil), request.ParameterizedConfig...),
		ParameterizedConfigHash:     request.ParameterizedConfigHash,
		DevValidationProvenanceHash: request.DevValidationProvenanceHash,
		OriginKind:                  request.OriginKind,
		OriginReference:             request.OriginReference,
		CreatedBy:                   request.CreatedBy,
		Status:                      request.Status,
		CreatedAt:                   time.Unix(1_800_000_000, 0).UTC(),
		UpdatedAt:                   time.Unix(1_800_000_000, 0).UTC(),
	}
	store.runs[run.ID] = run
	store.byKey[key] = run.ID
	ports := make([]string, 0, len(request.Inputs))
	for port := range request.Inputs {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	for _, port := range ports {
		store.bindings[run.ID] = append(store.bindings[run.ID], db.AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: run.ID,
			Direction:     db.AgentWorkflowRunSnapshotInput,
			PortName:      port,
			Snapshot:      request.Inputs[port],
		})
	}
	return cloneRun(run), true, nil
}

func (store *verticalRunStore) Snapshots(
	_ context.Context,
	runID snapshot.WorkflowRunID,
) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]db.AgentWorkflowRunSnapshotBinding(nil), store.bindings[runID]...), nil
}

func (store *verticalRunStore) Transition(
	_ context.Context,
	runID snapshot.WorkflowRunID,
	from db.AgentWorkflowRunStatus,
	to db.AgentWorkflowRunStatus,
	message string,
) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	run, found := store.runs[runID]
	if !found || run.Status != from {
		return false, nil
	}
	run.Status = to
	run.ErrorMessage = message
	store.runs[runID] = run
	return true, nil
}

func (store *verticalRunStore) attachExecution(
	runID snapshot.WorkflowRunID,
	template workflowrun.WorkflowRunTemplateRef,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	run, found := store.runs[runID]
	if !found {
		return fmt.Errorf("vertical run %s was not allocated", runID.String())
	}
	pipelineRunID, instancePipelineID := 801, 802
	plannedBuildID := int64(803)
	concreteHash := run.ParameterizedConfigHash
	run.PipelineRunID = &pipelineRunID
	run.TemplatePipelineID = &template.PipelineID
	run.InstancePipelineID = &instancePipelineID
	run.ConcreteConfig = append(json.RawMessage(nil), run.ParameterizedConfig...)
	run.ConcreteConfigHash = &concreteHash
	run.PlannedBuildID = &plannedBuildID
	store.runs[runID] = run
	return nil
}

func (store *verticalRunStore) inputIDs(runID snapshot.WorkflowRunID) map[string]snapshot.SnapshotID {
	store.mu.Lock()
	defer store.mu.Unlock()
	values := map[string]snapshot.SnapshotID{}
	for _, binding := range store.bindings[runID] {
		if binding.Direction == db.AgentWorkflowRunSnapshotInput {
			values[binding.PortName] = binding.Snapshot.ID
		}
	}
	return values
}

type verticalTemplateSaver struct{}

func (verticalTemplateSaver) SaveOrReuse(
	_ context.Context,
	admission workflowrun.AdmissionContext,
	spec workflowrun.ImmutableTemplateSpec,
) (workflowrun.WorkflowRunTemplateRef, error) {
	if admission.TeamID <= 0 || !strings.HasPrefix(spec.Name, "agent-node-code-review-v") ||
		spec.FullHash == "" || len(spec.CanonicalJSON) == 0 {
		return workflowrun.WorkflowRunTemplateRef{}, fmt.Errorf("vertical template received incomplete immutable identity")
	}
	return workflowrun.WorkflowRunTemplateRef{
		PipelineID:    800,
		TeamID:        admission.TeamID,
		Name:          spec.Name,
		ConfigVersion: 1,
		FullHash:      spec.FullHash,
	}, nil
}

type verticalExecutionCreator struct {
	runs   *verticalRunStore
	params map[string]any
}

func (creator *verticalExecutionCreator) CreateRunForWorkflowRun(
	_ context.Context,
	runID snapshot.WorkflowRunID,
	template workflowrun.WorkflowRunTemplateRef,
	envelope workflow.ExecutionEnvelope,
	createdBy string,
	beforeCommit workflowrun.BeforeWorkflowRunCommit,
) (workflowrun.WorkflowRunExecution, bool, error) {
	if createdBy == "" || beforeCommit != nil {
		return workflowrun.WorkflowRunExecution{}, false, fmt.Errorf("vertical execution received invalid audit or hook")
	}
	params, err := envelope.Params()
	if err != nil {
		return workflowrun.WorkflowRunExecution{}, false, err
	}
	creator.params = params
	if err := creator.runs.attachExecution(runID, template); err != nil {
		return workflowrun.WorkflowRunExecution{}, false, err
	}
	return workflowrun.WorkflowRunExecution{}, true, nil
}

type verticalCredentialAdmitter struct{}

func (verticalCredentialAdmitter) AdmitModelCredential(context.Context) error { return nil }

func verticalConsumerManifest(nodeVersion int) workflow.Manifest {
	return workflow.Manifest{workflow.WorkflowFileName: fmt.Sprintf(`schema_version: 3
name: review-change
signature_version: 1
inputs:
  - {name: base, type: repository/v1}
  - {name: candidate, type: repository/v1}
outputs:
  - {name: review, type: review/v1, from: review}
plan:
  - node: review-change
    uses: code-review@%d
    input_mapping: {before: base, after: candidate}
    output_mapping: {review: review}
    params: {MINIMUM_SEVERITY: high}
`, nodeVersion)}
}

func verticalSnapshot(id snapshot.SnapshotID, typeRef snapshot.TypeRef, digestDigit string) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:             id,
		Type:           typeRef,
		Digest:         snapshot.Digest("sha256:" + strings.Repeat(digestDigit, 64)),
		ByteSize:       1,
		FileCount:      1,
		Representation: "application/vnd.test",
		ContentState:   snapshot.ContentStateAvailable,
		CreatedAt:      time.Unix(1_800_000_000, 0).UTC(),
	}
}

func nodeParameterFromRun(t *testing.T, run db.AgentWorkflowRun, name string) string {
	t.Helper()
	return agentFromRun(t, run).Env[name]
}

func agentFromRun(t *testing.T, run db.AgentWorkflowRun) *atc.AgentStep {
	t.Helper()
	var config atc.Config
	if err := json.Unmarshal(run.ParameterizedConfig, &config); err != nil {
		t.Fatalf("decode durable node config: %v", err)
	}
	var found *atc.AgentStep
	for _, job := range config.Jobs {
		for _, step := range job.PlanSequence {
			if err := step.Config.Visit(atc.StepRecursor{
				OnAgent: func(agent *atc.AgentStep) error {
					if found != nil {
						return fmt.Errorf("rendered node contains multiple agent leaves")
					}
					found = agent
					return nil
				},
			}); err != nil {
				t.Fatalf("inspect durable node config: %v", err)
			}
		}
	}
	if found == nil {
		t.Fatal("rendered node contains no agent leaf")
	}
	return found
}

func assertFrozenReviewAgent(t *testing.T, agent *atc.AgentStep, mapped bool) {
	t.Helper()
	if agent.Model != "sonnet" ||
		agent.BudgetSliceUSD != 5 ||
		!strings.Contains(agent.Prompt, "Compare the immutable repositories mounted at `before` and `after`.") ||
		!reflect.DeepEqual(agent.Skills, []string{"review"}) ||
		agent.SkillFiles["skills/review/SKILL.md"] == "" {
		t.Fatalf(
			"frozen review agent = model %q budget %v prompt %q skills %q skill_files %#v",
			agent.Model,
			agent.BudgetSliceUSD,
			agent.Prompt,
			agent.Skills,
			agent.SkillFiles,
		)
	}
	if len(agent.Sidecars) != 1 || agent.Sidecars[0].Config == nil {
		t.Fatalf("frozen review sidecars = %#v", agent.Sidecars)
	}
	sidecar := agent.Sidecars[0].Config
	if sidecar.Image != "registry.example/dev-mcp@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		!reflect.DeepEqual(sidecar.Command, []string{"/usr/local/bin/dev-mcp"}) ||
		len(sidecar.Ports) != 1 || sidecar.Ports[0].ContainerPort != 8080 {
		t.Fatalf("frozen review sidecar = %+v", sidecar)
	}
	if agent.Capabilities != nil {
		t.Fatalf("source capability aliases survived compilation: %#v", agent.Capabilities)
	}
	if !reflect.DeepEqual(agent.Inputs, []string{"before", "after"}) ||
		!reflect.DeepEqual(agent.Outputs, []string{"review"}) {
		t.Fatalf("logical review ports = inputs %q outputs %q", agent.Inputs, agent.Outputs)
	}
	if mapped {
		if !reflect.DeepEqual(agent.InputMapping, map[string]string{"after": "candidate", "before": "base"}) ||
			!reflect.DeepEqual(agent.OutputMapping, map[string]string{"review": "review"}) {
			t.Fatalf("expanded review mappings = inputs %#v outputs %#v", agent.InputMapping, agent.OutputMapping)
		}
		return
	}
	if len(agent.InputMapping) != 0 || len(agent.OutputMapping) != 0 {
		t.Fatalf("direct review acquired workflow mappings: inputs %#v outputs %#v", agent.InputMapping, agent.OutputMapping)
	}
}

func verticalRunKey(teamID int, kind workflow.DefinitionKind, key string) string {
	return fmt.Sprintf("%d\x00%s\x00%s", teamID, kind, key)
}

func cloneRun(run db.AgentWorkflowRun) db.AgentWorkflowRun {
	run.ParameterizedConfig = append(json.RawMessage(nil), run.ParameterizedConfig...)
	run.ConcreteConfig = append(json.RawMessage(nil), run.ConcreteConfig...)
	run.FunctionID = cloneString(run.FunctionID)
	run.ConcreteConfigHash = cloneString(run.ConcreteConfigHash)
	return run
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneManifest(source workflow.Manifest) workflow.Manifest {
	cloned := make(workflow.Manifest, len(source))
	for path, content := range source {
		cloned[path] = content
	}
	return cloned
}

func cloneBindings(source []workflow.ResolvedNodeBinding) []workflow.ResolvedNodeBinding {
	cloned := make([]workflow.ResolvedNodeBinding, len(source))
	for index, binding := range source {
		cloned[index] = binding
		cloned[index].InputMapping = cloneStringMap(binding.InputMapping)
		cloned[index].OutputMapping = cloneStringMap(binding.OutputMapping)
		cloned[index].Parameters = cloneStringMap(binding.Parameters)
	}
	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
