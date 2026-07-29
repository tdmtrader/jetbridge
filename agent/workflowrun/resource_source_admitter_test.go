package workflowrun_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func TestResourceSourceAdmitterCapturesAReadyManualAdmissionForTheActiveTarget(t *testing.T) {
	target := resourceSourceAdmitterTarget()
	rendered, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := snapshot.SnapshotID(101)
	registry := &resourceSourceAdmitterRegistryStub{active: db.AgentWorkflowResourceSourcePipeline{
		PipelineID: 13, TeamID: 7, WorkflowDefinitionID: target.WorkflowDefinitionID,
		WorkflowName: target.WorkflowName, WorkflowVersion: target.WorkflowVersion,
		PipelineConfigVersion: 4, ConfigHash: rendered.ConfigHash,
		State: db.AgentWorkflowResourceSourcePipelineActive,
	}}
	manual := &resourceSourceAdmitterManualStub{admission: db.AgentWorkflowResourceSourceAdmission{
		ID: 41, TeamID: 7, WorkflowDefinitionID: target.WorkflowDefinitionID,
		SourcePipelineID: 13, SourceConfigHash: rendered.ConfigHash,
		IdempotencyKey: "manual:dispatch-1", Mode: db.AgentWorkflowResourceSourceAdmissionManual,
	}}
	capture := &resourceSourceAdmitterCaptureStub{ready: readyResourceSourceAdmission(41, rendered.ConfigHash, snapshotID)}
	snapshots := &resourceSourceAdmitterSnapshotStub{snapshot: snapshot.Snapshot{
		ID: snapshotID, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	admitter, err := workflowrun.NewResourceSourceAdmitter(7, registry, manual, capture, capture, snapshots)
	if err != nil {
		t.Fatal(err)
	}

	ready, err := admitter.AdmitManual(context.Background(), workflowrun.AdmissionContext{
		TeamID: 7, TeamName: "main", CreatedBy: "alice",
		Origin: workflowrun.Origin{Kind: "manual"},
	}, target, "manual:dispatch-1")
	if err != nil {
		t.Fatal(err)
	}
	if ready.AdmissionID != 41 || ready.TeamID != 7 ||
		ready.WorkflowDefinitionID != target.WorkflowDefinitionID || ready.SourceConfigHash != rendered.ConfigHash {
		t.Fatalf("ready admission = %#v", ready)
	}
	want := snapshot.SnapshotRef{ID: snapshotID, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64))}
	if got := ready.Inputs["repository"]; got != want || len(ready.Inputs) != 1 {
		t.Fatalf("ready inputs = %#v, want repository %v", ready.Inputs, want)
	}
	if len(manual.identities) != 1 || manual.identities[0] != (db.ManualAdmissionIdentity{
		WorkflowDefinitionID: target.WorkflowDefinitionID, SourcePipelineID: 13,
		SourceConfigHash: rendered.ConfigHash, IdempotencyKey: "manual:dispatch-1",
	}) {
		t.Fatalf("manual identities = %#v", manual.identities)
	}
	if capture.captureCalls != 1 || capture.readyCalls != 0 || snapshots.calls != 1 {
		t.Fatalf("admission calls = capture:%d ready:%d snapshots:%d", capture.captureCalls, capture.readyCalls, snapshots.calls)
	}
}

func TestResourceSourceAdmitterLoadsReadyAdmissionWithoutConsultingTheActivePipeline(t *testing.T) {
	target := resourceSourceAdmitterTarget()
	rendered, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := snapshot.SnapshotID(101)
	registry := &resourceSourceAdmitterRegistryStub{findErr: errors.New("draining pipeline must not be reopened")}
	readyStore := &resourceSourceAdmitterCaptureStub{ready: readyResourceSourceAdmission(41, rendered.ConfigHash, snapshotID)}
	snapshots := &resourceSourceAdmitterSnapshotStub{snapshot: snapshot.Snapshot{
		ID: snapshotID, Type: "repository/v1", Digest: snapshot.Digest("sha256:" + strings.Repeat("a", 64)),
	}}
	admitter, err := workflowrun.NewResourceSourceAdmitter(
		7, registry, &resourceSourceAdmitterManualStub{}, readyStore, readyStore, snapshots,
	)
	if err != nil {
		t.Fatal(err)
	}

	ready, err := admitter.LoadReady(context.Background(), 7, 41, target)
	if err != nil {
		t.Fatal(err)
	}
	if ready.AdmissionID != 41 || len(ready.Inputs) != 1 {
		t.Fatalf("ready admission = %#v", ready)
	}
	if registry.calls != 0 || readyStore.captureCalls != 0 || readyStore.readyCalls != 1 {
		t.Fatalf("resume called registry/capture: registry=%d capture=%d ready=%d", registry.calls, readyStore.captureCalls, readyStore.readyCalls)
	}
}

func resourceSourceAdmitterTarget() workflow.ResourceSourcePipelineTarget {
	return workflow.ResourceSourcePipelineTarget{
		TeamID: 7, WorkflowDefinitionID: 91, WorkflowName: "resource-source", WorkflowVersion: 2,
		Sources: []workflow.ResourceSource{{Name: "repository", Resource: "repository", Type: "repository/v1"}},
		Resources: atc.ResourceConfigs{{
			Name: "repository", Type: "git", Source: atc.Source{"uri": "git@example.invalid:acme/repository.git"},
		}},
	}
}

func readyResourceSourceAdmission(admissionID int64, configHash string, snapshotID snapshot.SnapshotID) db.ReadySourceAdmission {
	return db.ReadySourceAdmission{
		Admission: db.AgentWorkflowResourceSourceAdmission{
			ID: admissionID, TeamID: 7, WorkflowDefinitionID: 91, SourcePipelineID: 13,
			SourceConfigHash: configHash, Status: db.AgentWorkflowResourceSourceAdmissionReady,
		},
		Bindings: []db.AgentWorkflowResourceSourceBinding{{
			AdmissionID: admissionID, SourceName: "repository", SnapshotType: "repository/v1", SnapshotID: &snapshotID,
		}},
	}
}

type resourceSourceAdmitterRegistryStub struct {
	active  db.AgentWorkflowResourceSourcePipeline
	findErr error
	calls   int
}

func (stub *resourceSourceAdmitterRegistryStub) FindActive(_ context.Context, teamID int, workflowName string) (db.AgentWorkflowResourceSourcePipeline, bool, error) {
	stub.calls++
	if stub.findErr != nil {
		return db.AgentWorkflowResourceSourcePipeline{}, false, stub.findErr
	}
	if teamID != stub.active.TeamID || workflowName != stub.active.WorkflowName {
		return db.AgentWorkflowResourceSourcePipeline{}, false, errors.New("unexpected active-pipeline lookup")
	}
	return stub.active, true, nil
}

type resourceSourceAdmitterManualStub struct {
	admission  db.AgentWorkflowResourceSourceAdmission
	identities []db.ManualAdmissionIdentity
}

func (stub *resourceSourceAdmitterManualStub) Admit(_ context.Context, identity db.ManualAdmissionIdentity) (db.AgentWorkflowResourceSourceAdmission, error) {
	stub.identities = append(stub.identities, identity)
	if stub.admission.ID == 0 {
		return db.AgentWorkflowResourceSourceAdmission{}, errors.New("unexpected manual admission")
	}
	return stub.admission, nil
}

type resourceSourceAdmitterCaptureStub struct {
	ready        db.ReadySourceAdmission
	captureCalls int
	readyCalls   int
}

func (stub *resourceSourceAdmitterCaptureStub) CaptureReady(_ context.Context, teamID int, admissionID int64) (db.ReadySourceAdmission, error) {
	stub.captureCalls++
	if teamID != 7 || admissionID != stub.ready.Admission.ID {
		return db.ReadySourceAdmission{}, errors.New("unexpected source capture")
	}
	return stub.ready, nil
}

func (stub *resourceSourceAdmitterCaptureStub) Ready(_ context.Context, teamID int, admissionID int64) (db.ReadySourceAdmission, bool, error) {
	stub.readyCalls++
	if teamID != 7 || admissionID != stub.ready.Admission.ID {
		return db.ReadySourceAdmission{}, false, errors.New("unexpected ready admission lookup")
	}
	return stub.ready, true, nil
}

type resourceSourceAdmitterSnapshotStub struct {
	snapshot snapshot.Snapshot
	calls    int
}

func (stub *resourceSourceAdmitterSnapshotStub) GetAuthorized(_ context.Context, teamID int, snapshotID snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	stub.calls++
	if teamID != 7 || snapshotID != stub.snapshot.ID {
		return snapshot.Snapshot{}, false, errors.New("unexpected source snapshot lookup")
	}
	return stub.snapshot, true, nil
}
