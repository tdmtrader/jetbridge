package workflowrun_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestSourceCaptureCoordinatorCapturesDurableSelectionsInSourceNameOrder(t *testing.T) {
	admissions := &sourceCaptureAdmissionStoreStub{capturing: sourceCaptureRecord(nil)}
	executor := &sourceCaptureExecutorStub{}
	coordinator, err := workflowrun.NewSourceCaptureCoordinator(
		7, "main", admissions, &sourceCaptureDefinitionStoreStub{definition: sourceCaptureDefinition()}, executor,
	)
	if err != nil {
		t.Fatal(err)
	}

	actual, err := coordinator.CaptureReady(context.Background(), 7, 41)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Admission.ID != 41 || actual.Admission.Status != db.AgentWorkflowResourceSourceAdmissionReady {
		t.Fatalf("ready admission = %#v", actual)
	}
	if got := executor.sourceNames; !reflect.DeepEqual(got, []string{"a-repository", "z-image"}) {
		t.Fatalf("capture order = %#v, want source-name order", got)
	}
	if got := executor.operationKeys; !reflect.DeepEqual(got, []string{
		sourceCaptureOperationKey(41, "a-repository", sourceCaptureVersionDigest(atc.Version{"ref": "a1"})),
		sourceCaptureOperationKey(41, "z-image", sourceCaptureVersionDigest(atc.Version{"digest": "b1"})),
	}) {
		t.Fatalf("capture operation keys = %#v", got)
	}
	if got := admissions.boundNames; !reflect.DeepEqual(got, []string{"a-repository", "z-image"}) {
		t.Fatalf("bound source order = %#v", got)
	}
}

func TestSourceCaptureCoordinatorMakesNoCaptureCallForReadyAdmission(t *testing.T) {
	ready := db.ReadySourceAdmission{Admission: db.AgentWorkflowResourceSourceAdmission{
		ID: 41, TeamID: 7, Status: db.AgentWorkflowResourceSourceAdmissionReady,
	}}
	admissions := &sourceCaptureAdmissionStoreStub{ready: ready}
	executor := &sourceCaptureExecutorStub{}
	coordinator, err := workflowrun.NewSourceCaptureCoordinator(
		7, "main", admissions, &sourceCaptureDefinitionStoreStub{definition: sourceCaptureDefinition()}, executor,
	)
	if err != nil {
		t.Fatal(err)
	}

	actual, err := coordinator.CaptureReady(context.Background(), 7, 41)
	if err != nil || actual.Admission.ID != 41 {
		t.Fatalf("CaptureReady() = %#v, %v", actual, err)
	}
	if admissions.capturingCalls != 0 || len(executor.sourceNames) != 0 || len(admissions.boundNames) != 0 {
		t.Fatalf("ready admission reached capture: capturing=%d sources=%#v bindings=%#v", admissions.capturingCalls, executor.sourceNames, admissions.boundNames)
	}
}

func TestSourceCaptureCoordinatorSkipsAnAlreadyBoundSourceAfterPartialPass(t *testing.T) {
	bound := snapshot.SnapshotID(100)
	admissions := &sourceCaptureAdmissionStoreStub{capturing: sourceCaptureRecord(map[string]*snapshot.SnapshotID{
		"a-repository": &bound,
	})}
	executor := &sourceCaptureExecutorStub{}
	coordinator, err := workflowrun.NewSourceCaptureCoordinator(
		7, "main", admissions, &sourceCaptureDefinitionStoreStub{definition: sourceCaptureDefinition()}, executor,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = coordinator.CaptureReady(context.Background(), 7, 41)
	if err != nil {
		t.Fatal(err)
	}
	if got := executor.sourceNames; !reflect.DeepEqual(got, []string{"z-image"}) {
		t.Fatalf("partial retry recaptured a bound source: %#v", got)
	}
	if got := admissions.boundNames; !reflect.DeepEqual(got, []string{"z-image"}) {
		t.Fatalf("partial retry bindings = %#v", got)
	}
}

func TestSourceCaptureCoordinatorRetryReusesExactPersistedSelection(t *testing.T) {
	admissions := &sourceCaptureAdmissionStoreStub{capturing: sourceCaptureSingleRecord()}
	executor := &sourceCaptureExecutorStub{pending: true}
	coordinator, err := workflowrun.NewSourceCaptureCoordinator(
		7, "main", admissions, &sourceCaptureDefinitionStoreStub{definition: sourceCaptureSingleDefinition()}, executor,
	)
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		_, err = coordinator.CaptureReady(context.Background(), 7, 41)
		if !errors.Is(err, workflowrun.ErrSourceCapturePending) {
			t.Fatalf("attempt %d error = %v, want pending capture", attempt, err)
		}
	}
	if got := executor.operationKeys; !reflect.DeepEqual(got, []string{
		sourceCaptureOperationKey(41, "a-repository", sourceCaptureVersionDigest(atc.Version{"ref": "a1"})),
		sourceCaptureOperationKey(41, "a-repository", sourceCaptureVersionDigest(atc.Version{"ref": "a1"})),
	}) {
		t.Fatalf("retry operation keys = %#v", got)
	}
	if got := executor.versions; !reflect.DeepEqual(got, []atc.Version{{"ref": "a1"}, {"ref": "a1"}}) {
		t.Fatalf("retry versions = %#v, want exact persisted version twice", got)
	}
	if len(admissions.boundNames) != 0 {
		t.Fatalf("pending capture bound a snapshot: %#v", admissions.boundNames)
	}
}

type sourceCaptureAdmissionStoreStub struct {
	ready          db.ReadySourceAdmission
	capturing      db.CapturingSourceAdmission
	capturingCalls int
	boundNames     []string
}

func (store *sourceCaptureAdmissionStoreStub) Ready(_ context.Context, teamID int, admissionID int64) (db.ReadySourceAdmission, bool, error) {
	if teamID != 7 || admissionID != 41 {
		return db.ReadySourceAdmission{}, false, errors.New("unexpected ready authority")
	}
	if store.ready.Admission.ID != 0 {
		return store.ready, true, nil
	}
	bound := 0
	for _, binding := range store.capturing.Bindings {
		if binding.SnapshotID != nil {
			bound++
		}
	}
	if store.capturing.Admission.ID != 0 && len(store.boundNames)+bound == len(store.capturing.Bindings) {
		return db.ReadySourceAdmission{Admission: db.AgentWorkflowResourceSourceAdmission{
			ID: 41, TeamID: 7, Status: db.AgentWorkflowResourceSourceAdmissionReady,
		}}, true, nil
	}
	return db.ReadySourceAdmission{}, false, nil
}

func (store *sourceCaptureAdmissionStoreStub) Capturing(_ context.Context, teamID int, admissionID int64) (db.CapturingSourceAdmission, bool, error) {
	store.capturingCalls++
	if teamID != 7 || admissionID != 41 {
		return db.CapturingSourceAdmission{}, false, errors.New("unexpected capturing authority")
	}
	return store.capturing, store.capturing.Admission.ID != 0, nil
}

func (store *sourceCaptureAdmissionStoreStub) BindCapture(_ context.Context, teamID int, admissionID int64, sourceName string, snapshotID snapshot.SnapshotID) (bool, error) {
	if teamID != 7 || admissionID != 41 || sourceName == "" || snapshotID <= 0 {
		return false, errors.New("unexpected capture binding")
	}
	store.boundNames = append(store.boundNames, sourceName)
	return true, nil
}

type sourceCaptureDefinitionStoreStub struct {
	definition workflow.Definition
}

func (store *sourceCaptureDefinitionStoreStub) Get(name string, version int) (*workflow.Definition, bool, error) {
	if name != store.definition.Name || version != store.definition.Version {
		return nil, false, nil
	}
	copy := store.definition
	return &copy, true, nil
}

type sourceCaptureExecutorStub struct {
	pending       bool
	sourceNames   []string
	operationKeys []string
	versions      []atc.Version
}

func (executor *sourceCaptureExecutorStub) CapturePersistedSource(_ context.Context, source workflowrun.PersistedSourceCapture) (workflowrun.SourceCaptureResult, error) {
	executor.sourceNames = append(executor.sourceNames, source.SourceName)
	executor.operationKeys = append(executor.operationKeys, source.CaptureOperationKey)
	executor.versions = append(executor.versions, source.Version)
	if executor.pending {
		return workflowrun.SourceCaptureResult{}, nil
	}
	return workflowrun.SourceCaptureResult{Snapshot: &snapshot.Snapshot{
		ID: snapshot.SnapshotID(100 + len(executor.sourceNames)), Type: source.SnapshotType,
	}}, nil
}

func sourceCaptureRecord(bound map[string]*snapshot.SnapshotID) db.CapturingSourceAdmission {
	repositoryVersion := atc.Version{"ref": "a1"}
	imageVersion := atc.Version{"digest": "b1"}
	bindings := []db.AgentWorkflowResourceSourceBinding{
		{
			AdmissionID: 41, SourceName: "a-repository", ResourceName: "repository", SelectingBuildID: 301,
			SourcePipelineID: 13, PipelineConfigVersion: 4, ResourceID: 71, ResourceConfigVersionID: 81, ResourceVersionID: 81,
			VersionDigest: sourceCaptureVersionDigest(repositoryVersion), Version: repositoryVersion, SnapshotType: "repository/v1",
			CaptureOperationKey: sourceCaptureOperationKey(41, "a-repository", sourceCaptureVersionDigest(repositoryVersion)), SnapshotID: bound["a-repository"],
		},
		{
			AdmissionID: 41, SourceName: "z-image", ResourceName: "image", SelectingBuildID: 301,
			SourcePipelineID: 13, PipelineConfigVersion: 4, ResourceID: 72, ResourceConfigVersionID: 82, ResourceVersionID: 82,
			VersionDigest: sourceCaptureVersionDigest(imageVersion), Version: imageVersion, SnapshotType: "registry-image/v1",
			CaptureOperationKey: sourceCaptureOperationKey(41, "z-image", sourceCaptureVersionDigest(imageVersion)), SnapshotID: bound["z-image"],
		},
	}
	return db.CapturingSourceAdmission{
		Admission: db.AgentWorkflowResourceSourceAdmission{
			ID: 41, TeamID: 7, WorkflowDefinitionID: 91, SourcePipelineID: 13,
			SourceConfigHash: strings.Repeat("c", 64), Status: db.AgentWorkflowResourceSourceAdmissionCapturing,
		},
		PipelineRegistration: db.AgentWorkflowResourceSourcePipeline{
			PipelineID: 13, TeamID: 7, WorkflowDefinitionID: 91, WorkflowName: "source-capture", WorkflowVersion: 2,
			PipelineConfigVersion: 4, ConfigHash: strings.Repeat("c", 64), State: db.AgentWorkflowResourceSourcePipelineActive,
		},
		TeamName: "main", Pipeline: atc.PipelineRef{Name: "source-pipeline"}, Bindings: bindings,
	}
}

func sourceCaptureSingleRecord() db.CapturingSourceAdmission {
	record := sourceCaptureRecord(nil)
	record.Bindings = record.Bindings[:1]
	return record
}

func sourceCaptureDefinition() workflow.Definition {
	return workflow.Definition{ID: 91, Name: "source-capture", Version: 2, Compiled: workflow.CompiledDefinition{
		Function: &workflow.FunctionConfig{
			Resources: atc.ResourceConfigs{
				{Name: "repository", Type: "git", Source: atc.Source{"uri": "git@example.invalid:acme/repository.git"}},
				{Name: "image", Type: "registry-image", Source: atc.Source{"repository": "registry.example.invalid/acme/image"}},
			},
			ResourceSources: []workflow.ResourceSource{
				{Name: "a-repository", Resource: "repository", Type: "repository/v1"},
				{Name: "z-image", Resource: "image", Type: "registry-image/v1"},
			},
		},
	}}
}

func sourceCaptureSingleDefinition() workflow.Definition {
	definition := sourceCaptureDefinition()
	definition.Compiled.Function.Resources = definition.Compiled.Function.Resources[:1]
	definition.Compiled.Function.ResourceSources = definition.Compiled.Function.ResourceSources[:1]
	return definition
}

func sourceCaptureOperationKey(_ int64, sourceName, digest string) string {
	resourceName := "repository"
	typeRef := snapshot.TypeRef("repository/v1")
	if sourceName == "z-image" {
		resourceName = "image"
		typeRef = snapshot.TypeRef("registry-image/v1")
	}
	key, err := db.WorkflowResourceSourceCaptureOperationKey(
		7, 91, 13, 4, sourceName, resourceName, digest, typeRef,
	)
	if err != nil {
		panic(err)
	}
	return key
}

func sourceCaptureVersionDigest(version atc.Version) string {
	encoded, err := json.Marshal(version)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", sum)
}
