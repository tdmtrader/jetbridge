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

// NodeDefinitionSource resolves an exact stored reusable-node version, the
// same way DefinitionSource resolves a workflow one.
//
// A run of a reusable node carries definition_kind 'node' and names a NODE
// definition in workflow_name/workflow_version. The workflow store's reads are
// scoped to definition_kind = 'workflow', so resolving a node run through it
// can only ever miss — which turned every node run's freeze into an error on a
// completely healthy path. The two kinds live in one table under
// UNIQUE (definition_kind, name, version), so the kind must select the reader
// rather than being left to a lookup that might match the wrong one.
type NodeDefinitionSource interface {
	Get(name string, version int) (*workflow.NodeDefinition, bool, error)
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
	nodes       NodeDefinitionSource
	store       ProjectionStore
}

func NewFreezer(
	evidence EvidenceSource,
	definitions DefinitionSource,
	nodes NodeDefinitionSource,
	store ProjectionStore,
) (*Freezer, error) {
	if evidence == nil {
		return nil, fmt.Errorf("occurrence: freezer requires an evidence source")
	}
	if definitions == nil {
		return nil, fmt.Errorf("occurrence: freezer requires a definition source")
	}
	if nodes == nil {
		return nil, fmt.Errorf("occurrence: freezer requires a node definition source")
	}
	if store == nil {
		return nil, fmt.Errorf("occurrence: freezer requires a projection store")
	}
	return &Freezer{evidence: evidence, definitions: definitions, nodes: nodes, store: store}, nil
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
	return executionNodesFor(freezer.definitions, freezer.nodes, run)
}

// executionNodesFor is shared by the freeze and the live read so both project
// against the same node set. Two copies of this resolution would be two chances
// for the frozen history and the live view of the same run to disagree.
//
// The run's own definition_kind selects which store answers: a reusable-node
// run names a node definition, and the workflow store cannot see one.
func executionNodesFor(
	definitions DefinitionSource,
	nodes NodeDefinitionSource,
	run db.AgentWorkflowRun,
) (map[string]string, error) {
	function, err := runFunction(definitions, nodes, run)
	if err != nil {
		return nil, err
	}
	built, err := graph.Build(function)
	if err != nil {
		return nil, fmt.Errorf("occurrence: deriving graph for %s %q version %d: %w",
			runDefinitionKind(run), run.WorkflowName, run.WorkflowVersion, err)
	}
	return ExecutionNodesOf(built), nil
}

func runFunction(
	definitions DefinitionSource,
	nodes NodeDefinitionSource,
	run db.AgentWorkflowRun,
) (*workflow.FunctionConfig, error) {
	if run.DefinitionKind == workflow.DefinitionKindNode {
		definition, found, err := nodes.Get(run.WorkflowName, run.WorkflowVersion)
		if err != nil {
			return nil, fmt.Errorf("occurrence: loading node %q version %d: %w",
				run.WorkflowName, run.WorkflowVersion, err)
		}
		if !found || definition == nil {
			return nil, fmt.Errorf("occurrence: node %q has no version %d",
				run.WorkflowName, run.WorkflowVersion)
		}
		function := definition.Compiled.Function
		return &function, nil
	}
	definition, found, err := definitions.Get(run.WorkflowName, run.WorkflowVersion)
	if err != nil {
		return nil, fmt.Errorf("occurrence: loading workflow %q version %d: %w",
			run.WorkflowName, run.WorkflowVersion, err)
	}
	if !found || definition == nil {
		return nil, fmt.Errorf("occurrence: workflow %q has no version %d",
			run.WorkflowName, run.WorkflowVersion)
	}
	return definition.Compiled.Function, nil
}

func runDefinitionKind(run db.AgentWorkflowRun) string {
	if run.DefinitionKind == workflow.DefinitionKindNode {
		return "node"
	}
	return "workflow"
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
// stores.
//
// ReusableNodeName and ReusableNodeVersion are set only for a run OF a
// reusable node, where the run itself names the node version that executed.
// Inside an ordinary workflow run they stay empty, because a reusable node is
// expanded away before compilation and graph.Build cannot see it — this call
// site must not invent it. The table's CHECK requires the name and the version
// to be present or absent together, which both cases satisfy.
func projectionRow(occurrence NodeOccurrence) db.AgentWorkflowRunNodeOccurrence {
	var reusableVersion *int
	if occurrence.ReusableNodeName != "" && occurrence.ReusableNodeVersion > 0 {
		version := occurrence.ReusableNodeVersion
		reusableVersion = &version
	}
	return db.AgentWorkflowRunNodeOccurrence{
		ReusableNodeName:     occurrence.ReusableNodeName,
		ReusableNodeVersion:  reusableVersion,
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
