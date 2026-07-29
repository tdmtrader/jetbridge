package workflowrun

import (
	"context"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db"
)

type resourceSourceActivePipelineStore interface {
	FindActive(context.Context, int, string) (db.AgentWorkflowResourceSourcePipeline, bool, error)
}

type resourceSourceManualAdmitter interface {
	Admit(context.Context, db.ManualAdmissionIdentity) (db.AgentWorkflowResourceSourceAdmission, error)
}

type resourceSourceCaptureCoordinator interface {
	CaptureReady(context.Context, int, int64) (db.ReadySourceAdmission, error)
}

type resourceSourceReadyStore interface {
	Ready(context.Context, int, int64) (db.ReadySourceAdmission, bool, error)
}

// SourceResourceAdmitter coordinates the live source-pipeline admission only
// for a first manual dispatch. Every reuse path reads the immutable ready
// admission and its already captured snapshots without reopening the source
// pipeline.
type SourceResourceAdmitter struct {
	teamID    int
	pipelines resourceSourceActivePipelineStore
	manual    resourceSourceManualAdmitter
	captures  resourceSourceCaptureCoordinator
	ready     resourceSourceReadyStore
	snapshots SnapshotAuthorizer
}

func NewResourceSourceAdmitter(
	trustedTeamID int,
	pipelines resourceSourceActivePipelineStore,
	manual resourceSourceManualAdmitter,
	captures resourceSourceCaptureCoordinator,
	ready resourceSourceReadyStore,
	snapshots SnapshotAuthorizer,
) (ResourceSourceAdmitter, error) {
	if trustedTeamID <= 0 {
		return nil, fmt.Errorf("workflow run: resource source admitter team ID must be positive")
	}
	for name, dependency := range map[string]any{
		"source pipeline store": pipelines,
		"manual admission":      manual,
		"source capture":        captures,
		"ready admission store": ready,
		"snapshot authorizer":   snapshots,
	} {
		if nilInterface(dependency) {
			return nil, fmt.Errorf("workflow run: resource source admitter %s is required", name)
		}
	}
	return &SourceResourceAdmitter{
		teamID: trustedTeamID, pipelines: pipelines, manual: manual,
		captures: captures, ready: ready, snapshots: snapshots,
	}, nil
}

// ReadyResourceSourceAdmitter is the experiment-only reuse surface. It can
// validate a server-provided ready admission but deliberately cannot schedule
// a manual source build; an experiment using a different source definition
// must have its own explicitly prepared admission.
type ReadyResourceSourceAdmitter struct {
	teamID    int
	ready     resourceSourceReadyStore
	snapshots SnapshotAuthorizer
}

func NewReadyResourceSourceAdmitter(
	trustedTeamID int,
	ready resourceSourceReadyStore,
	snapshots SnapshotAuthorizer,
) (ResourceSourceAdmitter, error) {
	if trustedTeamID <= 0 || nilInterface(ready) || nilInterface(snapshots) {
		return nil, fmt.Errorf("workflow run: ready resource source admitter dependencies are required")
	}
	return &ReadyResourceSourceAdmitter{teamID: trustedTeamID, ready: ready, snapshots: snapshots}, nil
}

func (admitter *ReadyResourceSourceAdmitter) AdmitManual(
	context.Context,
	AdmissionContext,
	workflow.ResourceSourcePipelineTarget,
	string,
) (ReadySourceAdmission, error) {
	return ReadySourceAdmission{}, fmt.Errorf("%w: experiment resource sources require a prepared ready admission", ErrInvalidRequest)
}

func (admitter *ReadyResourceSourceAdmitter) LoadReady(
	ctx context.Context,
	teamID int,
	admissionID int64,
	target workflow.ResourceSourcePipelineTarget,
) (ReadySourceAdmission, error) {
	// The shared validation performs no source-pipeline lookup or capture.
	delegate := SourceResourceAdmitter{teamID: admitter.teamID, ready: admitter.ready, snapshots: admitter.snapshots}
	return delegate.LoadReady(ctx, teamID, admissionID, target)
}

func (admitter *SourceResourceAdmitter) AdmitManual(
	ctx context.Context,
	admission AdmissionContext,
	target workflow.ResourceSourcePipelineTarget,
	idempotencyKey string,
) (ReadySourceAdmission, error) {
	if ctx == nil || admission.TeamID != admitter.teamID || strings.TrimSpace(idempotencyKey) == "" {
		return ReadySourceAdmission{}, fmt.Errorf("%w: resource source manual admission is invalid", ErrInvalidRequest)
	}
	rendered, err := validateResourceSourceTarget(admitter.teamID, target)
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	registered, found, err := admitter.pipelines.FindActive(ctx, admitter.teamID, target.WorkflowName)
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	if !found || registered.TeamID != admitter.teamID ||
		registered.WorkflowDefinitionID != target.WorkflowDefinitionID ||
		registered.WorkflowName != target.WorkflowName ||
		registered.WorkflowVersion != target.WorkflowVersion ||
		registered.ConfigHash != rendered.ConfigHash ||
		registered.State != db.AgentWorkflowResourceSourcePipelineActive {
		return ReadySourceAdmission{}, fmt.Errorf("%w: active resource source pipeline changed", ErrInvalidRequest)
	}
	created, err := admitter.manual.Admit(ctx, db.ManualAdmissionIdentity{
		WorkflowDefinitionID: target.WorkflowDefinitionID,
		SourcePipelineID:     registered.PipelineID,
		SourceConfigHash:     rendered.ConfigHash,
		IdempotencyKey:       idempotencyKey,
	})
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	if created.ID <= 0 || created.TeamID != admitter.teamID ||
		created.WorkflowDefinitionID != target.WorkflowDefinitionID ||
		created.SourcePipelineID != registered.PipelineID ||
		created.SourceConfigHash != rendered.ConfigHash {
		return ReadySourceAdmission{}, fmt.Errorf("%w: manual source admission identity drifted", ErrInvalidRequest)
	}
	ready, err := admitter.captures.CaptureReady(ctx, admitter.teamID, created.ID)
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	return admitter.readySourceAdmission(ctx, target, rendered.ConfigHash, ready)
}

func (admitter *SourceResourceAdmitter) LoadReady(
	ctx context.Context,
	teamID int,
	admissionID int64,
	target workflow.ResourceSourcePipelineTarget,
) (ReadySourceAdmission, error) {
	if ctx == nil || teamID != admitter.teamID || admissionID <= 0 {
		return ReadySourceAdmission{}, fmt.Errorf("%w: resource source ready admission is invalid", ErrInvalidRequest)
	}
	rendered, err := validateResourceSourceTarget(admitter.teamID, target)
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	ready, found, err := admitter.ready.Ready(ctx, teamID, admissionID)
	if err != nil {
		return ReadySourceAdmission{}, err
	}
	if !found {
		return ReadySourceAdmission{}, ErrSourceCapturePending
	}
	return admitter.readySourceAdmission(ctx, target, rendered.ConfigHash, ready)
}

func validateResourceSourceTarget(
	trustedTeamID int,
	target workflow.ResourceSourcePipelineTarget,
) (workflow.RenderedResourceSourcePipeline, error) {
	if target.TeamID != trustedTeamID {
		return workflow.RenderedResourceSourcePipeline{}, fmt.Errorf("%w: resource source target belongs to another team", ErrInvalidRequest)
	}
	rendered, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		return workflow.RenderedResourceSourcePipeline{}, fmt.Errorf("%w: resource source target is invalid: %v", ErrInvalidRequest, err)
	}
	return rendered, nil
}

func (admitter *SourceResourceAdmitter) readySourceAdmission(
	ctx context.Context,
	target workflow.ResourceSourcePipelineTarget,
	configHash string,
	ready db.ReadySourceAdmission,
) (ReadySourceAdmission, error) {
	if ready.Admission.ID <= 0 || ready.Admission.TeamID != admitter.teamID ||
		ready.Admission.WorkflowDefinitionID != target.WorkflowDefinitionID ||
		ready.Admission.SourceConfigHash != configHash ||
		ready.Admission.Status != db.AgentWorkflowResourceSourceAdmissionReady {
		return ReadySourceAdmission{}, fmt.Errorf("%w: ready source admission identity drifted", ErrInvalidRequest)
	}
	expected := make(map[string]snapshot.TypeRef, len(target.Sources))
	for _, source := range target.Sources {
		if _, duplicate := expected[source.Name]; duplicate {
			return ReadySourceAdmission{}, fmt.Errorf("%w: resource source target is duplicated", ErrInvalidRequest)
		}
		expected[source.Name] = source.Type
	}
	if len(ready.Bindings) != len(expected) {
		return ReadySourceAdmission{}, fmt.Errorf("%w: ready source admission bindings do not cover the target", ErrInvalidRequest)
	}
	inputs := make(map[string]snapshot.SnapshotRef, len(ready.Bindings))
	for _, binding := range ready.Bindings {
		expectedType, found := expected[binding.SourceName]
		if !found || expectedType != binding.SnapshotType || binding.AdmissionID != ready.Admission.ID ||
			binding.SnapshotID == nil || binding.SnapshotID.Validate() != nil {
			return ReadySourceAdmission{}, fmt.Errorf("%w: ready source binding drifted", ErrInvalidRequest)
		}
		if _, duplicate := inputs[binding.SourceName]; duplicate {
			return ReadySourceAdmission{}, fmt.Errorf("%w: ready source binding is duplicated", ErrInvalidRequest)
		}
		value, found, err := admitter.snapshots.GetAuthorized(ctx, admitter.teamID, *binding.SnapshotID)
		if err != nil {
			return ReadySourceAdmission{}, err
		}
		if !found || value.ID != *binding.SnapshotID || value.Type != binding.SnapshotType {
			return ReadySourceAdmission{}, fmt.Errorf("%w: ready source snapshot is unavailable or has the wrong type", ErrSnapshotUnavailable)
		}
		ref := snapshot.SnapshotRef{ID: value.ID, Type: value.Type, Digest: value.Digest}
		if err := ref.Validate(); err != nil {
			return ReadySourceAdmission{}, fmt.Errorf("%w: ready source snapshot identity is invalid", ErrSnapshotUnavailable)
		}
		inputs[binding.SourceName] = ref
	}
	return ReadySourceAdmission{
		AdmissionID: ready.Admission.ID, TeamID: admitter.teamID,
		WorkflowDefinitionID: ready.Admission.WorkflowDefinitionID,
		WorkflowName:         target.WorkflowName,
		WorkflowVersion:      target.WorkflowVersion,
		SourceConfigHash:     ready.Admission.SourceConfigHash,
		Inputs:               inputs,
	}, nil
}

func cloneReadySourceAdmission(ready ReadySourceAdmission) ReadySourceAdmission {
	ready.Inputs = cloneRefs(ready.Inputs)
	return ready
}

var _ ResourceSourceAdmitter = (*SourceResourceAdmitter)(nil)
var _ ResourceSourceAdmitter = (*ReadyResourceSourceAdmitter)(nil)
