package resourcecapture

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc/db"
)

type PipelineRunStore interface {
	ListRuns(templatePipelineID int, limit int) ([]db.PipelineRun, error)
	CreateRunForServerTemplate(context.Context, db.WorkflowRunTemplateRef, map[string]any, string) (db.PipelineRun, error)
}

type OperationLocker interface {
	WithLock(context.Context, string, func() error) error
}

type DurableExecutionStore struct {
	runs   PipelineRunStore
	locker OperationLocker
}

func NewExecutionStore(runs PipelineRunStore, locker OperationLocker) (*DurableExecutionStore, error) {
	if isNil(runs) || isNil(locker) {
		return nil, fmt.Errorf("resource capture: pipeline run store and operation locker are required")
	}
	return &DurableExecutionStore{runs: runs, locker: locker}, nil
}

func (store *DurableExecutionStore) StartOrGet(ctx context.Context, request ExecutionRequest) (Execution, bool, error) {
	if err := contextError(ctx); err != nil {
		return Execution{}, false, err
	}
	if request.TeamID <= 0 || request.TeamName == "" || request.OperationKey == "" || request.CreatedBy == "" ||
		request.Template.ID <= 0 || request.Template.TeamID != request.TeamID || request.Template.Name == "" ||
		request.Template.ConfigVersion <= 0 || request.Template.FullHash == "" || request.RetryPipelineRunID < 0 {
		return Execution{}, false, fmt.Errorf("resource capture: invalid execution request")
	}
	var execution Execution
	var created bool
	err := store.locker.WithLock(ctx, "resource-capture/"+request.OperationKey, func() error {
		if err := contextError(ctx); err != nil {
			return err
		}
		runs, err := store.runs.ListRuns(request.Template.ID, 2)
		if err != nil {
			return fmt.Errorf("list capture runs: %w", err)
		}
		running := 0
		var newest Execution
		for _, candidate := range runs {
			converted, conversionErr := executionFromRun(candidate, request.Template.ID)
			if conversionErr != nil {
				return conversionErr
			}
			if converted.Status == db.PipelineRunRunning {
				running++
			}
			if newest.PipelineRunID == 0 {
				newest = converted
			}
		}
		if running > 1 {
			return fmt.Errorf("resource capture: operation has concurrent running generations")
		}
		if running == 1 && newest.Status != db.PipelineRunRunning {
			return fmt.Errorf("resource capture: an older generation is still running")
		}
		var run db.PipelineRun
		create := len(runs) == 0
		if create && request.RetryPipelineRunID > 0 {
			return fmt.Errorf("resource capture: retry generation is absent from durable history")
		}
		if !create {
			if request.RetryPipelineRunID > newest.PipelineRunID {
				return fmt.Errorf("resource capture: retry generation is ahead of durable history")
			}
			create = request.RetryPipelineRunID == newest.PipelineRunID && newest.Status != db.PipelineRunRunning
			if !create {
				run = runs[0]
			}
		}
		if create {
			run, err = store.runs.CreateRunForServerTemplate(ctx, db.WorkflowRunTemplateRef{
				PipelineID: request.Template.ID, TeamID: request.Template.TeamID, Name: request.Template.Name,
				ConfigVersion: request.Template.ConfigVersion, FullHash: request.Template.FullHash,
			}, map[string]any{}, request.CreatedBy)
			if err != nil {
				return fmt.Errorf("create capture run: %w", err)
			}
			created = true
		}
		var conversionErr error
		execution, conversionErr = executionFromRun(run, request.Template.ID)
		return conversionErr
	})
	if err != nil {
		return Execution{}, false, err
	}
	return execution, created, nil
}

func executionFromRun(run db.PipelineRun, templateID int) (Execution, error) {
	if isNil(run) || run.ID() <= 0 || run.TemplatePipelineID() != templateID {
		return Execution{}, fmt.Errorf("resource capture: pipeline run association mismatch")
	}
	instanceID, found := run.InstancePipelineID()
	if !found || instanceID <= 0 {
		return Execution{}, fmt.Errorf("resource capture: pipeline run has no instance")
	}
	execution := Execution{
		PipelineRunID: int64(run.ID()), TemplatePipelineID: run.TemplatePipelineID(),
		InstancePipelineID: instanceID, Status: run.Status(),
	}
	if err := execution.validate(); err != nil {
		return Execution{}, err
	}
	return execution, nil
}
