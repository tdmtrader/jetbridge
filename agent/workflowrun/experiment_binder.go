package workflowrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/experiment"
)

type experimentNativeBinder interface {
	BindExperimentWithReadySourceAdmission(
		context.Context,
		AdmissionContext,
		BindRequest,
		*int64,
	) (BindResult, error)
}

// ExperimentBinderAdapter is the composition seam between the persistence-
// neutral experiment scheduler and the ordinary durable workflow binder.
type ExperimentBinderAdapter struct {
	binder experimentNativeBinder
}

func NewExperimentBinderAdapter(binder experimentNativeBinder) (*ExperimentBinderAdapter, error) {
	if nilInterface(binder) {
		return nil, fmt.Errorf("workflow run: experiment binder is required")
	}
	return &ExperimentBinderAdapter{binder: binder}, nil
}

func (adapter *ExperimentBinderAdapter) BindAndCreate(
	ctx context.Context,
	admission experiment.AdmissionContext,
	request experiment.BindRequest,
) (experiment.BindResult, error) {
	if err := request.AdmissionGate.Validate(); err != nil {
		return experiment.BindResult{}, fmt.Errorf("%w: %v", experiment.ErrBindInvalidRequest, err)
	}
	expectedDevValidationProvenanceHash := request.ExpectedDevValidationProvenanceHash
	// Experiment candidate and evaluator launches remain unattached: no ticket
	// travels through experiment.AdmissionContext, so an experiment can only
	// ever acquire one by being launched in explicit ticket context, which no
	// caller does today. Keeping experiments out of ticket journals is the
	// same rule that keeps them out of default operational evaluation.
	result, err := adapter.binder.BindExperimentWithReadySourceAdmission(ctx, AdmissionContext{
		TeamID: admission.TeamID, TeamName: admission.TeamName, CreatedBy: admission.CreatedBy,
		Origin: Origin{Kind: admission.Origin.Kind, Reference: admission.Origin.Reference},
	}, BindRequest{
		WorkflowName: request.WorkflowName, Version: request.Version, FunctionID: request.FunctionID,
		Inputs: request.Inputs, IdempotencyKey: request.IdempotencyKey,
		ExpectedWorkflowDefinitionID:        request.DefinitionID,
		ExpectedTargetConfigHash:            request.ExpectedTargetConfigHash,
		ExpectedDevValidationProvenanceHash: &expectedDevValidationProvenanceHash,
		ExperimentAdmission: &ExperimentAdmissionGate{
			ExperimentID: int64(request.AdmissionGate.ExperimentID),
			CellID:       int64(request.AdmissionGate.CellID),
			Phase:        string(request.AdmissionGate.Phase),
		},
	}, request.ResourceSourceAdmissionID)
	if err != nil {
		switch {
		case errors.Is(err, ErrExperimentAdmissionClosed):
			return experiment.BindResult{}, fmt.Errorf("%w: %v", experiment.ErrBindAdmissionClosed, err)
		case errors.Is(err, ErrBudgetDenied):
			return experiment.BindResult{}, fmt.Errorf("%w: %v", experiment.ErrBindBudgetDenied, err)
		case errors.Is(err, ErrInvalidRequest),
			errors.Is(err, ErrDefinitionOrTargetNotFound),
			errors.Is(err, ErrSnapshotUnavailable),
			errors.Is(err, ErrSnapshotTypeMismatch),
			errors.Is(err, ErrIdempotencyConflict):
			return experiment.BindResult{}, fmt.Errorf("%w: %v", experiment.ErrBindInvalidRequest, err)
		default:
			return experiment.BindResult{}, err
		}
	}
	functionID := ""
	if result.Run.FunctionID != nil {
		functionID = *result.Run.FunctionID
	}
	return experiment.BindResult{
		WorkflowRunID: result.Run.ID, WorkflowDefinitionID: int64(result.Run.WorkflowDefinitionID),
		WorkflowName: result.Run.WorkflowName, WorkflowVersion: result.Run.WorkflowVersion,
		FunctionID: functionID, TargetConfigHash: result.Run.ParameterizedConfigHash,
		DevValidationProvenanceHash: result.Run.DevValidationProvenanceHash,
	}, nil
}

var _ experiment.WorkflowBinder = (*ExperimentBinderAdapter)(nil)
