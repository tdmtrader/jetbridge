package resourcecapture

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/workflowrun"
)

// WorkflowSourceCapturer is the composition-only adapter from the durable
// workflow-source coordinator to Capturer's exact-selection boundary.
// Conversion is explicit so the public resource-capture API cannot obtain a
// source-admission operation identity or bypass its persisted evidence.
type WorkflowSourceCapturer struct {
	capturer *Capturer
}

func NewWorkflowSourceCapturer(capturer *Capturer) (*WorkflowSourceCapturer, error) {
	if isNil(capturer) {
		return nil, ErrUnavailable
	}
	return &WorkflowSourceCapturer{capturer: capturer}, nil
}

func (adapter *WorkflowSourceCapturer) CapturePersistedSource(
	ctx context.Context,
	source workflowrun.PersistedSourceCapture,
) (workflowrun.SourceCaptureResult, error) {
	resourceConfigVersionID, err := durableIDAsInt(source.ResourceConfigVersionID)
	if err != nil {
		return workflowrun.SourceCaptureResult{}, err
	}
	resourceVersionID, err := durableIDAsInt(source.ResourceVersionID)
	if err != nil {
		return workflowrun.SourceCaptureResult{}, err
	}
	result, err := adapter.capturer.CapturePersistedSelection(ctx, PersistedSelection{
		AdmissionID: source.AdmissionID, WorkflowDefinitionID: source.WorkflowDefinitionID, SourceName: source.SourceName,
		TeamID: source.TeamID, TeamName: source.TeamName,
		SourcePipelineID: source.SourcePipelineID, PipelineID: source.PipelineID,
		Pipeline: source.Pipeline, PipelineConfigVersion: source.PipelineConfigVersion,
		ResourceID: source.ResourceID, Resource: source.Resource, ResourceTypes: source.ResourceTypes,
		ResourceConfigVersionID: resourceConfigVersionID,
		ResourceVersionID:       resourceVersionID, VersionDigest: source.VersionDigest,
		Version: source.Version, SnapshotType: source.SnapshotType,
		CaptureOperationKey: source.CaptureOperationKey,
	})
	if err != nil {
		return workflowrun.SourceCaptureResult{}, err
	}
	return workflowrun.SourceCaptureResult{Snapshot: result.Snapshot}, nil
}

func durableIDAsInt(id int64) (int, error) {
	maxInt := int64(^uint(0) >> 1)
	if id <= 0 || id > maxInt {
		return 0, fmt.Errorf("%w: durable resource identity is out of range", ErrPersistedSelectionDrift)
	}
	return int(id), nil
}
