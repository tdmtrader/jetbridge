package occurrence

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/atc/db"
)

// EvidenceSource gathers every authoritative execution record one run's
// projection derives from. db.AgentWorkflowRunEvidenceFactory is the
// production one.
type EvidenceSource interface {
	EvidenceForRun(context.Context, db.AgentWorkflowRun) (db.AgentWorkflowRunEvidence, error)
}

// DefinitionSource resolves an exact stored workflow version. It is the
// narrow read of workflow.Store the freezer needs, and it is deliberately the
// version-addressed method rather than Live or Latest.
type DefinitionSource interface {
	Get(name string, version int) (*workflow.Definition, bool, error)
}

// ProjectionStore writes the frozen rows.
type ProjectionStore interface {
	Freeze(context.Context, []db.AgentWorkflowRunNodeOccurrence) error
}

// Freezer is the production node-occurrence freezer: the implementation of
// workflowrun.NodeOccurrenceFreezer that turns one just-terminalized run into
// durable per-node history.
//
// It reads authoritative state and writes a projection of it. It originates
// nothing: a node with no evidence is frozen pending, and a run whose graph
// cannot be derived is not frozen at all, because a partial projection would
// be written as immutable truth.
type Freezer struct {
	evidence    EvidenceSource
	definitions DefinitionSource
	store       ProjectionStore
}

func NewFreezer(
	evidence EvidenceSource,
	definitions DefinitionSource,
	store ProjectionStore,
) (*Freezer, error) {
	if evidence == nil {
		return nil, fmt.Errorf("occurrence: freezer requires an evidence source")
	}
	if definitions == nil {
		return nil, fmt.Errorf("occurrence: freezer requires a definition source")
	}
	if store == nil {
		return nil, fmt.Errorf("occurrence: freezer requires a projection store")
	}
	return &Freezer{evidence: evidence, definitions: definitions, store: store}, nil
}

// FreezeRun projects one terminal run and writes the result.
//
// Every failure is returned rather than partially applied. The reconciler
// logs and swallows it: the run IS finalized, and losing this run's history is
// bad where a run that can never terminalize is worse.
func (freezer *Freezer) FreezeRun(ctx context.Context, run db.AgentWorkflowRun) error {
	executionNodes, err := freezer.executionNodes(run)
	if err != nil {
		return err
	}

	evidence, err := freezer.evidence.EvidenceForRun(ctx, run)
	if err != nil {
		return fmt.Errorf("occurrence: gathering evidence for run %d: %w", run.ID, err)
	}

	occurrences, err := Derive(sourcesFrom(run, evidence, executionNodes))
	if err != nil {
		return err
	}

	rows := make([]db.AgentWorkflowRunNodeOccurrence, 0, len(occurrences))
	for _, occurrence := range occurrences {
		rows = append(rows, projectionRow(occurrence))
	}
	if err := freezer.store.Freeze(ctx, rows); err != nil {
		return fmt.Errorf("occurrence: freezing run %d: %w", run.ID, err)
	}
	return nil
}

// executionNodes derives the run's own graph.
//
// The definition is loaded by the run's OWN workflow_version, never the
// currently live one. A run's projection describes the revision that actually
// executed; resolving the promoted version instead would silently produce a
// plausible-looking projection of the wrong workflow for exactly the runs a
// human most wants to inspect — old ones, whose workflow has since moved on.
func (freezer *Freezer) executionNodes(run db.AgentWorkflowRun) (map[string]string, error) {
	definition, found, err := freezer.definitions.Get(run.WorkflowName, run.WorkflowVersion)
	if err != nil {
		return nil, fmt.Errorf("occurrence: loading workflow %q version %d: %w",
			run.WorkflowName, run.WorkflowVersion, err)
	}
	if !found || definition == nil {
		return nil, fmt.Errorf("occurrence: workflow %q has no version %d",
			run.WorkflowName, run.WorkflowVersion)
	}
	built, err := graph.Build(definition.Compiled.Function)
	if err != nil {
		return nil, fmt.Errorf("occurrence: deriving graph for workflow %q version %d: %w",
			run.WorkflowName, run.WorkflowVersion, err)
	}
	return ExecutionNodesOf(built), nil
}

func sourcesFrom(
	run db.AgentWorkflowRun,
	evidence db.AgentWorkflowRunEvidence,
	executionNodes map[string]string,
) Sources {
	sources := Sources{
		Run:             run,
		ExecutionNodes:  executionNodes,
		BuildStepStatus: map[string]Status{},
	}
	for _, metric := range evidence.AttemptMetrics {
		sources.AttemptMetrics = append(sources.AttemptMetrics, AttemptMetric{
			PlanID:           metric.PlanID,
			ExecutionAttempt: metric.ExecutionAttempt,
			Status:           metric.Status,
			CostUSD:          metric.CostUSD,
			CreatedAt:        metric.CreatedAt,
			UpdatedAt:        metric.UpdatedAt,
		})
	}
	for _, wait := range evidence.Waits {
		sources.Waits = append(sources.Waits, Wait{
			ID:            wait.ID,
			PlanID:        wait.PlanID,
			OutputName:    wait.OutputName,
			Status:        wait.Status,
			TimeoutPolicy: wait.TimeoutPolicy,
			CreatedAt:     wait.CreatedAt,
			ResolvedAt:    wait.ResolvedAt,
		})
	}
	for _, publication := range evidence.Publications {
		sources.Publications = append(sources.Publications, Publication{
			ID:        publication.ID,
			PlanID:    publication.PlanID,
			Status:    publication.Status,
			CreatedAt: publication.CreatedAt,
			UpdatedAt: publication.UpdatedAt,
		})
	}
	for planID, status := range evidence.BuildStepStatus {
		mapped, ok := buildStepStatus(status)
		if !ok {
			continue
		}
		sources.BuildStepStatus[planID] = mapped
	}
	return sources
}

// buildStepStatus maps a terminal build step status onto an occurrence status.
// An unrecognised value is dropped rather than coerced: a status this code
// does not understand must leave the node pending, which is honest, instead of
// being frozen forever as a guess.
func buildStepStatus(status string) (Status, bool) {
	switch status {
	case db.AgentNodeBuildStepSucceeded:
		return StatusSucceeded, true
	case db.AgentNodeBuildStepFailed:
		return StatusFailed, true
	case db.AgentNodeBuildStepErrored:
		return StatusErrored, true
	default:
		return "", false
	}
}

// projectionRow converts a derived occurrence into the row shape the table
// stores. ReusableNodeName and ReusableNodeVersion stay empty: a reusable node
// is expanded away before compilation, so graph.Build cannot see it and this
// call site must not invent it. The table's CHECK requires the name and the
// version to be absent together, which is exactly what an unset pair is.
func projectionRow(occurrence NodeOccurrence) db.AgentWorkflowRunNodeOccurrence {
	return db.AgentWorkflowRunNodeOccurrence{
		WorkflowRunID:        int64(occurrence.WorkflowRunID),
		NodeID:               occurrence.NodeID,
		RetryAttempt:         occurrence.RetryAttempt,
		Attempt:              occurrence.Attempt,
		TeamID:               occurrence.TeamID,
		WorkflowName:         occurrence.WorkflowName,
		WorkflowDefinitionID: occurrence.WorkflowDefinitionID,
		WorkflowVersion:      occurrence.WorkflowVersion,
		NodeKind:             occurrence.NodeKind,
		PlanID:               occurrence.PlanID,
		Status:               string(occurrence.Status),
		WaitID:               occurrence.WaitID,
		PublicationID:        occurrence.PublicationID,
		StartedAt:            occurrence.StartedAt,
		CompletedAt:          occurrence.CompletedAt,
		DurationSeconds:      occurrence.DurationSeconds,
		CostUSD:              occurrence.CostUSD,
	}
}
