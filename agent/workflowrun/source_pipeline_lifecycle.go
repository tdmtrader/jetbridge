package workflowrun

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/atc/db"
)

// SourcePipelineLifecycleStore is the server-owned mutation boundary for
// standing source-selection pipelines. Ordinary pipeline mutations cannot
// implement it because source registrations are immutable authorities.
type SourcePipelineLifecycleStore interface {
	ResourceSourcePipelineLifecycle(context.Context, int) ([]db.AgentWorkflowResourceSourcePipelineLifecycle, error)
	UnpauseActiveResourceSourcePipeline(context.Context, int, db.AgentWorkflowResourceSourcePipeline) (bool, error)
	PauseDrainedResourceSourcePipeline(context.Context, int, db.AgentWorkflowResourceSourcePipeline) (bool, error)
	ArchiveDrainedResourceSourcePipeline(context.Context, int, db.AgentWorkflowResourceSourcePipeline) (bool, error)
}

type ResourceSourcePipelineLifecycle interface {
	Reconcile(context.Context) error
}

type SourcePipelineLifecycle struct {
	teamID int
	store  SourcePipelineLifecycleStore
}

func NewSourcePipelineLifecycle(trustedTeamID int, store SourcePipelineLifecycleStore) (*SourcePipelineLifecycle, error) {
	if trustedTeamID <= 0 {
		return nil, fmt.Errorf("workflowrun: source pipeline lifecycle team ID must be positive")
	}
	if store == nil {
		return nil, fmt.Errorf("workflowrun: source pipeline lifecycle store is required")
	}
	return &SourcePipelineLifecycle{teamID: trustedTeamID, store: store}, nil
}

// Reconcile takes at most one visible step per pipeline. A new active source
// pipeline becomes schedulable only by unpausing it. Draining pipelines first
// wait for scheduler work, then pause, and only archive after both scheduler
// work and nonterminal admissions have finished.
func (lifecycle *SourcePipelineLifecycle) Reconcile(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("workflowrun: source pipeline lifecycle context is required")
	}
	candidates, err := lifecycle.store.ResourceSourcePipelineLifecycle(ctx, lifecycle.teamID)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		switch candidate.State {
		case db.AgentWorkflowResourceSourcePipelineActive:
			if candidate.Paused {
				if _, err := lifecycle.store.UnpauseActiveResourceSourcePipeline(ctx, lifecycle.teamID, candidate.AgentWorkflowResourceSourcePipeline); err != nil {
					return err
				}
			}
		case db.AgentWorkflowResourceSourcePipelineDraining:
			if candidate.InFlightBuilds > 0 {
				continue
			}
			if !candidate.Paused {
				if _, err := lifecycle.store.PauseDrainedResourceSourcePipeline(ctx, lifecycle.teamID, candidate.AgentWorkflowResourceSourcePipeline); err != nil {
					return err
				}
				continue
			}
			if candidate.NonterminalAdmissions == 0 && !candidate.Archived {
				if _, err := lifecycle.store.ArchiveDrainedResourceSourcePipeline(ctx, lifecycle.teamID, candidate.AgentWorkflowResourceSourcePipeline); err != nil {
					return err
				}
			}
		case db.AgentWorkflowResourceSourcePipelineArchived:
			continue
		default:
			return fmt.Errorf("workflowrun: invalid source pipeline lifecycle state %q", candidate.State)
		}
	}
	return nil
}
