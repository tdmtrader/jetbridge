package workflowrun

import (
	"context"
	"errors"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

var (
	// ErrCancelConflict is the stable public classification for cancellation
	// requests that lost to non-aborted terminal history.
	ErrCancelConflict = errors.New("workflow run: cancellation conflicts with durable state")
	// ErrCancelFailure deliberately bounds dependency and corrupt-identity
	// failures. Raw database and build errors must not cross the API seam.
	ErrCancelFailure = errors.New("workflow run: cancellation failed")
)

// CancellationStore is the locked durable-state surface needed by Canceler.
// db.AgentWorkflowRunsFactory satisfies it directly.
type CancellationStore interface {
	Get(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	Transition(context.Context, snapshot.WorkflowRunID, db.AgentWorkflowRunStatus, db.AgentWorkflowRunStatus, string) (bool, error)
	Finalize(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error)
	ValidateCancellationTarget(context.Context, int, snapshot.WorkflowRunID, int64) (bool, error)
}

// BuildLookup is the exact selected-build lookup used for abort delivery.
// db.BuildFactory satisfies it directly.
type BuildLookup interface {
	BuildForAPI(int) (db.BuildForAPI, bool, error)
}

// Canceler coordinates the durable canceling CAS with the exact selected
// Concourse build. It is safe to replay after process restarts.
type Canceler struct {
	store  CancellationStore
	builds BuildLookup
}

func NewCanceler(store CancellationStore, builds BuildLookup) (*Canceler, error) {
	if nilInterface(store) || nilInterface(builds) {
		return nil, ErrCancelFailure
	}
	return &Canceler{store: store, builds: builds}, nil
}

func (c *Canceler) Cancel(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool, error) {
	if err := contextError(ctx); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	if teamID <= 0 || id.Validate() != nil {
		return db.AgentWorkflowRun{}, false, ErrCancelFailure
	}

	run, found, err := c.load(ctx, teamID, id)
	if err != nil || !found {
		return run, found, err
	}

	// A CAS can lose admitting->running while cancellation is arriving, and a
	// locked finalization can lose to another reconciler. Each loss is followed
	// by a team-scoped reload; the bound prevents corrupt stores from spinning.
	for attempts := 0; attempts < 4; attempts++ {
		switch run.Status {
		case db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning:
			if err := contextError(ctx); err != nil {
				return db.AgentWorkflowRun{}, true, err
			}
			_, transitionErr := c.store.Transition(
				ctx, id, run.Status, db.AgentWorkflowRunStatusCanceling, "",
			)
			if transitionErr != nil {
				return db.AgentWorkflowRun{}, true, ErrCancelFailure
			}
			run, found, err = c.load(ctx, teamID, id)
			if err != nil || !found {
				if err == nil {
					err = ErrCancelFailure
				}
				return db.AgentWorkflowRun{}, true, err
			}
			continue

		case db.AgentWorkflowRunStatusCanceling:
			if run.PlannedBuildID != nil {
				return c.abortSelectedBuild(ctx, teamID, id, run)
			}
			_, _, finalizeErr := c.store.Finalize(ctx, db.AgentWorkflowRunFinalization{
				WorkflowRunID:           id,
				ExpectedStatus:          db.AgentWorkflowRunStatusCanceling,
				ExpectedExecutionStatus: cloneExecutionStatus(run.ExecutionStatus),
				ExpectedActualPlanHash:  cloneString(run.ActualPlanHash),
				TerminalStatus:          db.AgentWorkflowRunStatusAborted,
			})
			if finalizeErr != nil {
				return db.AgentWorkflowRun{}, true, ErrCancelFailure
			}
			run, found, err = c.load(ctx, teamID, id)
			if err != nil || !found {
				if err == nil {
					err = ErrCancelFailure
				}
				return db.AgentWorkflowRun{}, true, err
			}
			continue

		case db.AgentWorkflowRunStatusAborted:
			return run, true, nil
		case db.AgentWorkflowRunStatusSucceeded,
			db.AgentWorkflowRunStatusFailed,
			db.AgentWorkflowRunStatusErrored:
			return db.AgentWorkflowRun{}, true, ErrCancelConflict
		default:
			return db.AgentWorkflowRun{}, true, ErrCancelFailure
		}
	}
	return db.AgentWorkflowRun{}, true, ErrCancelFailure
}

func (c *Canceler) abortSelectedBuild(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
	run db.AgentWorkflowRun,
) (db.AgentWorkflowRun, bool, error) {
	buildID64 := *run.PlannedBuildID
	buildID := int(buildID64)
	if buildID64 <= 0 || int64(buildID) != buildID64 {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
	if err := contextError(ctx); err != nil {
		return db.AgentWorkflowRun{}, true, err
	}
	build, found, err := c.builds.BuildForAPI(buildID)
	if err != nil {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
	if !found {
		// The selected build and durable execution are committed together. If
		// the build has since been collected, the reconciler owns terminal
		// classification from the captured execution evidence; cancellation is
		// already durably accepted and remains safe to replay.
		return run, true, nil
	}
	if nilInterface(build) {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
	if build.ID() != buildID || build.TeamID() != run.TeamID || run.TeamID != teamID {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
	linked, err := c.store.ValidateCancellationTarget(ctx, teamID, id, buildID64)
	if err != nil || !linked {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
	if err := contextError(ctx); err != nil {
		return db.AgentWorkflowRun{}, true, err
	}
	if err := build.MarkAsAborted(); err != nil {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}

	reloaded, found, err := c.load(ctx, teamID, id)
	if err != nil || !found {
		if err == nil {
			err = ErrCancelFailure
		}
		return db.AgentWorkflowRun{}, true, err
	}
	switch reloaded.Status {
	case db.AgentWorkflowRunStatusCanceling, db.AgentWorkflowRunStatusAborted:
		return reloaded, true, nil
	case db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored:
		return db.AgentWorkflowRun{}, true, ErrCancelConflict
	default:
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
}

func (c *Canceler) load(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool, error) {
	if err := contextError(ctx); err != nil {
		return db.AgentWorkflowRun{}, false, err
	}
	run, found, err := c.store.Get(ctx, teamID, id)
	if err != nil {
		return db.AgentWorkflowRun{}, false, ErrCancelFailure
	}
	if !found {
		return db.AgentWorkflowRun{}, false, nil
	}
	if run.ID != id || run.TeamID != teamID {
		return db.AgentWorkflowRun{}, true, ErrCancelFailure
	}
	return run, true, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrCancelFailure
	}
	return ctx.Err()
}

func cloneExecutionStatus(value *db.AgentWorkflowRunExecutionStatus) *db.AgentWorkflowRunExecutionStatus {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
