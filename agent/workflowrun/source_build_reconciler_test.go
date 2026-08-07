package workflowrun

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const sourceBuildTestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSourceBuildReconcilerClaimsSuccessfulActiveBuildWithItsExactPersistedMapping(t *testing.T) {
	pipeline := sourceBuildPipeline(db.AgentWorkflowResourceSourcePipelineActive)
	builds := &sourceBuildStoreStub{
		successful: []db.SourceBuild{{
			ID:         301,
			PipelineID: pipeline.PipelineID,
			JobID:      501,
			TeamID:     7,
		}},
		mapping: []db.SelectedSource{{
			SourceName:              "repo",
			ResourceName:            "source-repo",
			SelectingBuildID:        301,
			ResourceID:              71,
			ResourceConfigVersionID: 81,
			ResourceVersionID:       81,
			VersionDigest:           "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Version:                 atc.Version{"ref": "one"},
		}},
	}
	admissions := &reconcilerAdmissionStoreStub{admission: db.AgentWorkflowResourceSourceAdmission{
		ID:                   41,
		TeamID:               7,
		WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
		SourcePipelineID:     pipeline.PipelineID,
		SourceConfigHash:     pipeline.ConfigHash,
		IdempotencyKey:       "source-build:13:301",
		Mode:                 db.AgentWorkflowResourceSourceAdmissionAutomatic,
		Status:               db.AgentWorkflowResourceSourceAdmissionSelecting,
	}}
	reconciler, err := NewSourceBuildReconciler(
		7,
		&sourceBuildPipelineStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipeline{pipeline}},
		builds,
		admissions,
		&sourceBuildDefinitionStoreStub{definition: sourceBuildDefinition(pipeline)},
	)
	if err != nil {
		t.Fatalf("construct source build reconciler: %v", err)
	}

	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(admissions.claims) != 1 {
		t.Fatalf("automatic claims = %#v, want one exact successful build claim", admissions.claims)
	}
	claim := admissions.claims[0]
	if claim.teamID != 7 || claim.pipelineID != 13 || claim.buildID != 301 || claim.claim.IdempotencyKey != "source-build:13:301" {
		t.Fatalf("automatic claim = %#v, want trusted exact build identity", claim)
	}
	if builds.mappingCalls != 1 {
		t.Fatalf("exact mapping reads = %d, want one persisted mapping read", builds.mappingCalls)
	}
	if len(admissions.bindings) != 1 {
		t.Fatalf("selection bindings = %#v, want one", admissions.bindings)
	}
	binding := admissions.bindings[0]
	if binding.buildID != 301 || binding.admissionID != 41 || len(binding.sources) != 1 {
		t.Fatalf("selection binding = %#v, want exact build mapping", binding)
	}
	if binding.sources[0].SnapshotType != snapshot.TypeRef("repository/v1") {
		t.Fatalf("snapshot type = %q, want declaration type", binding.sources[0].SnapshotType)
	}
	if binding.sources[0].CaptureOperationKey == "" {
		t.Fatal("capture operation key was not derived from the durable selection")
	}
	wantOperationKey, err := db.WorkflowResourceSourceCaptureOperationKey(
		pipeline.TeamID,
		pipeline.WorkflowDefinitionID,
		pipeline.PipelineID,
		pipeline.PipelineConfigVersion,
		"repo",
		"source-repo",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		snapshot.TypeRef("repository/v1"),
	)
	if err != nil {
		t.Fatalf("derive canonical operation key: %v", err)
	}
	if binding.sources[0].CaptureOperationKey != wantOperationKey {
		t.Fatalf("capture operation key = %q, want Task 12 canonical key %q", binding.sources[0].CaptureOperationKey, wantOperationKey)
	}
}

func TestSourceBuildReconcilerCapturesAndLaunchesAnAutomaticAdmissionWithServerIdentity(t *testing.T) {
	pipeline := sourceBuildPipeline(db.AgentWorkflowResourceSourcePipelineActive)
	builds := &sourceBuildStoreStub{
		successful: []db.SourceBuild{{
			ID:         301,
			PipelineID: pipeline.PipelineID,
			JobID:      501,
			TeamID:     7,
		}},
		mapping: []db.SelectedSource{{
			SourceName:              "repo",
			ResourceName:            "source-repo",
			SelectingBuildID:        301,
			ResourceID:              71,
			ResourceConfigVersionID: 81,
			ResourceVersionID:       81,
			VersionDigest:           "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Version:                 atc.Version{"ref": "one"},
		}},
	}
	admissions := &reconcilerAdmissionStoreStub{admission: db.AgentWorkflowResourceSourceAdmission{
		ID:                   41,
		TeamID:               7,
		WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
		SourcePipelineID:     pipeline.PipelineID,
		SourceConfigHash:     pipeline.ConfigHash,
		IdempotencyKey:       "source-build:13:301",
		Mode:                 db.AgentWorkflowResourceSourceAdmissionAutomatic,
		Status:               db.AgentWorkflowResourceSourceAdmissionSelecting,
	}}
	ready := ReadySourceAdmission{
		AdmissionID:          41,
		TeamID:               7,
		WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
		WorkflowName:         pipeline.WorkflowName,
		WorkflowVersion:      pipeline.WorkflowVersion,
		SourceConfigHash:     pipeline.ConfigHash,
		Inputs: map[string]snapshot.SnapshotRef{"repo": {
			ID: 91, Type: snapshot.TypeRef("repository/v1"),
			Digest: snapshot.Digest("sha256:" + sourceBuildTestHash),
		}},
	}
	captures := &sourceBuildCaptureStub{}
	sources := &sourceBuildReadyAdmitterStub{ready: ready}
	launcher := &sourceBuildReadyLauncherStub{}
	reconciler, err := NewSourceBuildReconciler(
		7,
		&sourceBuildPipelineStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipeline{pipeline}},
		builds,
		admissions,
		&sourceBuildDefinitionStoreStub{definition: sourceBuildDefinition(pipeline)},
		WithAutomaticSourceLaunch("research", captures, sources, launcher),
	)
	if err != nil {
		t.Fatalf("construct source build reconciler: %v", err)
	}

	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if captures.calls != 1 || captures.teamID != 7 || captures.admissionID != 41 {
		t.Fatalf("capture = calls %d team %d admission %d, want one trusted capture", captures.calls, captures.teamID, captures.admissionID)
	}
	if sources.calls != 1 || sources.admissionID != 41 {
		t.Fatalf("ready load = calls %d admission %d, want captured admission", sources.calls, sources.admissionID)
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("automatic launches = %#v, want one", launcher.calls)
	}
	call := launcher.calls[0]
	if call.idempotencyKey != "source-build:13:301" ||
		call.admission.TeamID != 7 ||
		call.admission.TeamName != "research" ||
		call.admission.CreatedBy != "workflow-resource-source-reconciler" ||
		call.admission.Origin.Kind != "resource-source-build" ||
		call.admission.Origin.Reference != "pipeline:13:build:301" ||
		call.ready.AdmissionID != 41 {
		t.Fatalf("automatic launch = %#v, want server-derived build authority", call)
	}
}

func TestSourceBuildReconcilerRetriesCaptureAndReadyLaunchWithoutRebindingSelection(t *testing.T) {
	pipeline := sourceBuildPipeline(db.AgentWorkflowResourceSourcePipelineDraining)
	admissionID := int64(41)
	builds := &sourceBuildStoreStub{successful: []db.SourceBuild{{
		ID: 301, PipelineID: pipeline.PipelineID, JobID: 501, TeamID: 7,
		AdmissionID: &admissionID, AdmissionMode: db.AgentWorkflowResourceSourceAdmissionAutomatic,
		AdmissionStatus: db.AgentWorkflowResourceSourceAdmissionCapturing,
	}}}
	admissions := &reconcilerAdmissionStoreStub{}
	captures := &sourceBuildCaptureStub{}
	ready := ReadySourceAdmission{
		AdmissionID: 41, TeamID: 7, WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
		WorkflowName: pipeline.WorkflowName, WorkflowVersion: pipeline.WorkflowVersion,
		SourceConfigHash: pipeline.ConfigHash,
		Inputs: map[string]snapshot.SnapshotRef{"repo": {
			ID: 91, Type: snapshot.TypeRef("repository/v1"),
			Digest: snapshot.Digest("sha256:" + sourceBuildTestHash),
		}},
	}
	sources := &sourceBuildReadyAdmitterStub{ready: ready}
	launcher := &sourceBuildReadyLauncherStub{}
	reconciler, err := NewSourceBuildReconciler(
		7,
		&sourceBuildPipelineStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipeline{pipeline}},
		builds,
		admissions,
		&sourceBuildDefinitionStoreStub{definition: sourceBuildDefinition(pipeline)},
		WithAutomaticSourceLaunch("research", captures, sources, launcher),
	)
	if err != nil {
		t.Fatalf("construct source build reconciler: %v", err)
	}

	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("retry reconcile: %v", err)
	}

	if builds.mappingCalls != 0 || len(admissions.bindings) != 0 {
		t.Fatalf("durable capture retry reopened selection: mappings=%d bindings=%#v", builds.mappingCalls, admissions.bindings)
	}
	if captures.calls != 2 || sources.calls != 2 || len(launcher.calls) != 2 {
		t.Fatalf("retry calls = capture %d ready %d launch %d, want two idempotent passes", captures.calls, sources.calls, len(launcher.calls))
	}
	for _, call := range launcher.calls {
		if call.idempotencyKey != "source-build:13:301" || call.ready.AdmissionID != admissionID {
			t.Fatalf("retry launch identity drifted: %#v", call)
		}
	}
}

func TestSourceBuildReconcilerDoesNotClaimFreshAutomaticBuildFromInactiveRevision(t *testing.T) {
	pipeline := sourceBuildPipeline(db.AgentWorkflowResourceSourcePipelineDraining)
	builds := &sourceBuildStoreStub{successful: []db.SourceBuild{{
		ID: 301, PipelineID: pipeline.PipelineID, JobID: 501, TeamID: 7,
	}}}
	admissions := &reconcilerAdmissionStoreStub{}
	reconciler, err := NewSourceBuildReconciler(
		7,
		&sourceBuildPipelineStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipeline{pipeline}},
		builds,
		admissions,
		&sourceBuildDefinitionStoreStub{definition: sourceBuildDefinition(pipeline)},
	)
	if err != nil {
		t.Fatalf("construct source build reconciler: %v", err)
	}

	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(admissions.claims) != 0 || builds.mappingCalls != 0 {
		t.Fatalf("draining revision created a fresh automatic claim: claims=%#v mappings=%d", admissions.claims, builds.mappingCalls)
	}
}

func TestSourceBuildReconcilerRefusesAnUnclaimedManuallyTriggeredBuild(t *testing.T) {
	pipeline := sourceBuildPipeline(db.AgentWorkflowResourceSourcePipelineActive)
	builds := &sourceBuildStoreStub{successful: []db.SourceBuild{{
		ID: 301, PipelineID: pipeline.PipelineID, JobID: 501, TeamID: 7,
		ManuallyTriggered: true,
	}}}
	admissions := &reconcilerAdmissionStoreStub{}
	reconciler, err := NewSourceBuildReconciler(
		7,
		&sourceBuildPipelineStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipeline{pipeline}},
		builds,
		admissions,
		&sourceBuildDefinitionStoreStub{definition: sourceBuildDefinition(pipeline)},
	)
	if err != nil {
		t.Fatalf("construct source build reconciler: %v", err)
	}

	err = reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected an unowned manually-triggered build to fail closed")
	}
	if len(admissions.claims) != 0 {
		t.Fatalf("automatic claims = %#v, want none for a manual build", admissions.claims)
	}
	if builds.mappingCalls != 0 {
		t.Fatalf("exact mapping reads = %d, want none before a valid manual owner", builds.mappingCalls)
	}
}

func TestSourceBuildReconcilerRejectsMappingWhoseSourceNamesDoNotMatchDeclaration(t *testing.T) {
	pipeline := sourceBuildPipeline(db.AgentWorkflowResourceSourcePipelineActive)
	builds := &sourceBuildStoreStub{
		successful: []db.SourceBuild{{ID: 301, PipelineID: pipeline.PipelineID, JobID: 501, TeamID: 7}},
		mapping: []db.SelectedSource{{
			SourceName: "unrecognized", ResourceName: "source-repo", SelectingBuildID: 301,
			ResourceID: 71, ResourceConfigVersionID: 81, ResourceVersionID: 81,
			VersionDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Version:       atc.Version{"ref": "one"},
		}},
	}
	admissions := &reconcilerAdmissionStoreStub{admission: db.AgentWorkflowResourceSourceAdmission{
		ID: 41, TeamID: 7, WorkflowDefinitionID: pipeline.WorkflowDefinitionID,
		SourcePipelineID: pipeline.PipelineID, SourceConfigHash: pipeline.ConfigHash,
		IdempotencyKey: "source-build:13:301", Mode: db.AgentWorkflowResourceSourceAdmissionAutomatic,
		Status: db.AgentWorkflowResourceSourceAdmissionSelecting,
	}}
	reconciler, err := NewSourceBuildReconciler(7,
		&sourceBuildPipelineStoreStub{pipelines: []db.AgentWorkflowResourceSourcePipeline{pipeline}},
		builds, admissions, &sourceBuildDefinitionStoreStub{definition: sourceBuildDefinition(pipeline)},
	)
	if err != nil {
		t.Fatalf("construct source build reconciler: %v", err)
	}

	err = reconciler.Reconcile(context.Background())
	if err == nil {
		t.Fatal("expected mismatched source mapping to fail closed")
	}
	if len(admissions.bindings) != 0 {
		t.Fatalf("selection bindings = %#v, want none after mapping validation failure", admissions.bindings)
	}
}

type sourceBuildStoreStub struct {
	ensureBuildID int
	ensureCreated []bool
	ensureErr     error
	ensureCalls   int

	checkRequests []sourceBuildCheckRequest

	successful          []db.SourceBuild
	successfulErr       error
	mapping             []db.SelectedSource
	mappingFound        bool
	mappingErr          error
	mappingCalls        int
	bindingMapping      map[int][]db.SelectedSource
	bindingMappingCalls int
}

type sourceBuildCheckRequest struct {
	teamID     int
	pipelineID int
	buildID    int
}

func (store *sourceBuildStoreStub) EnsureManualBuild(_ context.Context, _ int, _ int64, _ int) (int, bool, error) {
	store.ensureCalls++
	if store.ensureErr != nil {
		return 0, false, store.ensureErr
	}
	created := false
	if len(store.ensureCreated) >= store.ensureCalls {
		created = store.ensureCreated[store.ensureCalls-1]
	}
	return store.ensureBuildID, created, nil
}

func (store *sourceBuildStoreStub) RequestPostDispatchChecks(_ context.Context, teamID, pipelineID, buildID int) error {
	store.checkRequests = append(store.checkRequests, sourceBuildCheckRequest{teamID: teamID, pipelineID: pipelineID, buildID: buildID})
	return nil
}

func (store *sourceBuildStoreStub) SuccessfulUnclaimedBuilds(_ context.Context, _ int, _ int) ([]db.SourceBuild, error) {
	return append([]db.SourceBuild(nil), store.successful...), store.successfulErr
}

func (store *sourceBuildStoreStub) ExactInputMapping(_ context.Context, _ int, _ int, _ int) ([]db.SelectedSource, bool, error) {
	store.mappingCalls++
	found := store.mappingFound || len(store.mapping) > 0
	return append([]db.SelectedSource(nil), store.mapping...), found, store.mappingErr
}

type sourceBuildPipelineStoreStub struct {
	pipelines []db.AgentWorkflowResourceSourcePipeline
	err       error
}

func (store *sourceBuildPipelineStoreStub) ResourceSourcePipelineLifecycle(
	_ context.Context,
	_ int,
) ([]db.AgentWorkflowResourceSourcePipelineLifecycle, error) {
	values := make([]db.AgentWorkflowResourceSourcePipelineLifecycle, len(store.pipelines))
	for index, pipeline := range store.pipelines {
		values[index].AgentWorkflowResourceSourcePipeline = pipeline
	}
	return values, store.err
}

type sourceBuildDefinitionStoreStub struct {
	definition workflow.Definition
	err        error
}

func (store *sourceBuildDefinitionStoreStub) Get(name string, version int) (*workflow.Definition, bool, error) {
	if store.err != nil {
		return nil, false, store.err
	}
	if name != store.definition.Name || version != store.definition.Version {
		return nil, false, nil
	}
	copy := store.definition
	return &copy, true, nil
}

type sourceBuildClaim struct {
	teamID     int
	pipelineID int
	buildID    int64
	claim      db.BuildClaim
}

type sourceBuildBinding struct {
	teamID      int
	admissionID int64
	buildID     int64
	sources     []db.SelectedSource
}

type sourceBuildCaptureStub struct {
	calls       int
	teamID      int
	admissionID int64
	err         error
}

func (stub *sourceBuildCaptureStub) CaptureReady(
	_ context.Context,
	teamID int,
	admissionID int64,
) (db.ReadySourceAdmission, error) {
	stub.calls++
	stub.teamID = teamID
	stub.admissionID = admissionID
	return db.ReadySourceAdmission{}, stub.err
}

type sourceBuildReadyAdmitterStub struct {
	calls       int
	admissionID int64
	ready       ReadySourceAdmission
	err         error
}

func (*sourceBuildReadyAdmitterStub) AdmitManual(
	context.Context,
	AdmissionContext,
	workflow.ResourceSourcePipelineTarget,
	string,
) (ReadySourceAdmission, error) {
	return ReadySourceAdmission{}, errors.New("unexpected manual admission")
}

func (stub *sourceBuildReadyAdmitterStub) LoadReady(
	_ context.Context,
	_ int,
	admissionID int64,
	_ workflow.ResourceSourcePipelineTarget,
) (ReadySourceAdmission, error) {
	stub.calls++
	stub.admissionID = admissionID
	return cloneReadySourceAdmission(stub.ready), stub.err
}

type sourceBuildReadyLaunch struct {
	admission      AdmissionContext
	ready          ReadySourceAdmission
	idempotencyKey string
}

type sourceBuildReadyLauncherStub struct {
	calls []sourceBuildReadyLaunch
	err   error
}

func (stub *sourceBuildReadyLauncherStub) BindReadySourceAdmission(
	_ context.Context,
	admission AdmissionContext,
	ready ReadySourceAdmission,
	idempotencyKey string,
) (BindResult, error) {
	stub.calls = append(stub.calls, sourceBuildReadyLaunch{
		admission: cloneAdmission(admission), ready: cloneReadySourceAdmission(ready),
		idempotencyKey: idempotencyKey,
	})
	return BindResult{}, stub.err
}

type reconcilerAdmissionStoreStub struct {
	admission db.AgentWorkflowResourceSourceAdmission
	claims    []sourceBuildClaim
	bindings  []sourceBuildBinding
}

func (*reconcilerAdmissionStoreStub) CreateManual(context.Context, int, db.ManualAdmissionIdentity) (db.AgentWorkflowResourceSourceAdmission, bool, error) {
	return db.AgentWorkflowResourceSourceAdmission{}, false, errors.New("unexpected manual admission")
}

func (store *reconcilerAdmissionStoreStub) ClaimBuild(
	_ context.Context,
	teamID, pipelineID int,
	buildID int64,
	claim db.BuildClaim,
) (db.AgentWorkflowResourceSourceAdmission, bool, error) {
	store.claims = append(store.claims, sourceBuildClaim{teamID: teamID, pipelineID: pipelineID, buildID: buildID, claim: claim})
	return store.admission, true, nil
}

func (store *reconcilerAdmissionStoreStub) BindSelection(
	_ context.Context,
	teamID int,
	admissionID, buildID int64,
	sources []db.SelectedSource,
) (bool, error) {
	store.bindings = append(store.bindings, sourceBuildBinding{teamID: teamID, admissionID: admissionID, buildID: buildID, sources: append([]db.SelectedSource(nil), sources...)})
	return true, nil
}

func (*reconcilerAdmissionStoreStub) BindCapture(context.Context, int, int64, string, snapshot.SnapshotID) (bool, error) {
	return false, errors.New("unexpected capture binding")
}

func (*reconcilerAdmissionStoreStub) Ready(context.Context, int, int64) (db.ReadySourceAdmission, bool, error) {
	return db.ReadySourceAdmission{}, false, errors.New("unexpected ready lookup")
}

func (*reconcilerAdmissionStoreStub) Capturing(context.Context, int, int64) (db.CapturingSourceAdmission, bool, error) {
	return db.CapturingSourceAdmission{}, false, errors.New("unexpected capturing lookup")
}

func sourceBuildPipeline(state db.AgentWorkflowResourceSourcePipelineState) db.AgentWorkflowResourceSourcePipeline {
	return db.AgentWorkflowResourceSourcePipeline{
		PipelineID: 13, TeamID: 7, WorkflowDefinitionID: 91, WorkflowName: "source-workflow",
		WorkflowVersion: 2, PipelineConfigVersion: 4, ConfigHash: sourceBuildTestHash, State: state,
	}
}

func sourceBuildDefinition(pipeline db.AgentWorkflowResourceSourcePipeline) workflow.Definition {
	sourceType := snapshot.TypeRef("repository/v1")
	return workflow.Definition{
		ID: pipeline.WorkflowDefinitionID, Name: pipeline.WorkflowName, Version: pipeline.WorkflowVersion,
		SchemaVersion: 3, SignatureVersion: 1,
		Compiled: workflow.CompiledDefinition{
			SchemaVersion: 3, Name: pipeline.WorkflowName,
			Function: &workflow.FunctionConfig{
				SignatureVersion: 1,
				Resources: atc.ResourceConfigs{{
					Name: "source-repo", Type: "git",
					Source: atc.Source{"uri": "https://example.invalid/source.git"},
				}},
				ResourceSources: []workflow.ResourceSource{{
					Name: "repo", Resource: "source-repo", Type: sourceType,
				}},
				Plan: []atc.Step{{Config: &atc.AgentStep{
					Name: "consume-source", FunctionID: "consume-source",
					Prompt: "consume source", Inputs: []string{"repo"},
					SnapshotInputs: map[string]atc.SnapshotInputConfig{
						"repo": {Type: sourceType},
					},
				}}},
			},
		},
	}
}
