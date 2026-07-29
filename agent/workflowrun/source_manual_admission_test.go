package workflowrun

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

func TestSourceManualAdmissionPersistsOneNormalBuildBeforeRetryableCheckRequests(t *testing.T) {
	admissions := &manualAdmissionStoreStub{admission: db.AgentWorkflowResourceSourceAdmission{
		ID: 41, TeamID: 7, WorkflowDefinitionID: 91, SourcePipelineID: 13,
		SourceConfigHash: sourceBuildTestHash, IdempotencyKey: "manual:dispatch-1",
		Mode:   db.AgentWorkflowResourceSourceAdmissionManual,
		Status: db.AgentWorkflowResourceSourceAdmissionSelecting,
	}}
	builds := &sourceBuildStoreStub{ensureBuildID: 301, ensureCreated: []bool{true, false}}
	service, err := NewSourceManualAdmission(7, admissions, builds)
	if err != nil {
		t.Fatalf("construct manual admission service: %v", err)
	}
	identity := db.ManualAdmissionIdentity{
		WorkflowDefinitionID: 91, SourcePipelineID: 13,
		SourceConfigHash: sourceBuildTestHash, IdempotencyKey: "manual:dispatch-1",
	}
	first, err := service.Admit(context.Background(), identity)
	if err != nil {
		t.Fatalf("first manual admission: %v", err)
	}
	second, err := service.Admit(context.Background(), identity)
	if err != nil {
		t.Fatalf("retry manual admission: %v", err)
	}
	if first.ID != 41 || second.ID != first.ID {
		t.Fatalf("admission IDs = %d and %d, want one durable admission 41", first.ID, second.ID)
	}
	if builds.ensureCalls != 2 || len(builds.checkRequests) != 2 {
		t.Fatalf("manual retry must ensure one build and re-request checks: ensures=%d checks=%#v", builds.ensureCalls, builds.checkRequests)
	}
	for _, request := range builds.checkRequests {
		if request.teamID != 7 || request.pipelineID != 13 || request.buildID != 301 {
			t.Fatalf("check request = %#v, want trusted normal admit build", request)
		}
	}
}

func TestSourceManualAdmissionDoesNotRequestChecksBeforeBuildAttachment(t *testing.T) {
	want := errors.New("database unavailable")
	admissions := &manualAdmissionStoreStub{admission: db.AgentWorkflowResourceSourceAdmission{
		ID: 41, TeamID: 7, WorkflowDefinitionID: 91, SourcePipelineID: 13,
		SourceConfigHash: sourceBuildTestHash, IdempotencyKey: "manual:dispatch-1",
		Mode:   db.AgentWorkflowResourceSourceAdmissionManual,
		Status: db.AgentWorkflowResourceSourceAdmissionSelecting,
	}}
	builds := &sourceBuildStoreStub{ensureErr: want}
	service, err := NewSourceManualAdmission(7, admissions, builds)
	if err != nil {
		t.Fatalf("construct manual admission service: %v", err)
	}
	_, err = service.Admit(context.Background(), db.ManualAdmissionIdentity{
		WorkflowDefinitionID: 91, SourcePipelineID: 13,
		SourceConfigHash: sourceBuildTestHash, IdempotencyKey: "manual:dispatch-1",
	})
	if !errors.Is(err, want) || len(builds.checkRequests) != 0 {
		t.Fatalf("admit = %v, checks=%#v; want build error and no checks", err, builds.checkRequests)
	}
}

type manualAdmissionStoreStub struct {
	admission db.AgentWorkflowResourceSourceAdmission
	calls     int
}

func (store *manualAdmissionStoreStub) CreateManual(_ context.Context, teamID int, identity db.ManualAdmissionIdentity) (db.AgentWorkflowResourceSourceAdmission, bool, error) {
	store.calls++
	if teamID != store.admission.TeamID || identity.WorkflowDefinitionID != store.admission.WorkflowDefinitionID || identity.SourcePipelineID != store.admission.SourcePipelineID || identity.SourceConfigHash != store.admission.SourceConfigHash || identity.IdempotencyKey != store.admission.IdempotencyKey {
		return db.AgentWorkflowResourceSourceAdmission{}, false, errors.New("unexpected manual admission identity")
	}
	return store.admission, store.calls == 1, nil
}

func (*manualAdmissionStoreStub) ClaimBuild(context.Context, int, int, int64, db.BuildClaim) (db.AgentWorkflowResourceSourceAdmission, bool, error) {
	return db.AgentWorkflowResourceSourceAdmission{}, false, errors.New("unexpected automatic claim")
}
func (*manualAdmissionStoreStub) BindSelection(context.Context, int, int64, int64, []db.SelectedSource) (bool, error) {
	return false, errors.New("unexpected selection binding")
}
func (*manualAdmissionStoreStub) BindCapture(context.Context, int, int64, string, snapshot.SnapshotID) (bool, error) {
	return false, errors.New("unexpected capture binding")
}
func (*manualAdmissionStoreStub) Ready(context.Context, int, int64) (db.ReadySourceAdmission, bool, error) {
	return db.ReadySourceAdmission{}, false, errors.New("unexpected ready lookup")
}
func (*manualAdmissionStoreStub) Capturing(context.Context, int, int64) (db.CapturingSourceAdmission, bool, error) {
	return db.CapturingSourceAdmission{}, false, errors.New("unexpected capturing lookup")
}
