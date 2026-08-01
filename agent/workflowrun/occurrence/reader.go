package occurrence

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

// FrozenSource reads the durable projection written at run finalization.
// db.AgentWorkflowRunNodeOccurrencesFactory is the production one.
type FrozenSource interface {
	ForRun(context.Context, int64) ([]db.AgentWorkflowRunNodeOccurrence, error)
}

// Reader answers "what are this run's node occurrences" for a read surface.
//
// It is the live half of the two call sites Derive exists to share. A terminal
// run is served from the frozen projection, which is the only record that
// survives Concourse build and template GC. Every other run is derived from
// current authoritative rows, so the overview's primary job — showing what
// needs attention NOW — is never answered from stale history.
//
// The order matters in one direction only: frozen first for terminal runs.
// Deriving a terminal run live would silently lose deterministic task steps
// once their build events are reclaimed, and would disagree with the immutable
// history every other surface reads.
type Reader struct {
	frozen      FrozenSource
	evidence    EvidenceSource
	definitions DefinitionSource
}

func NewReader(
	frozen FrozenSource,
	evidence EvidenceSource,
	definitions DefinitionSource,
) (*Reader, error) {
	if frozen == nil {
		return nil, fmt.Errorf("occurrence: reader requires a frozen projection source")
	}
	if evidence == nil {
		return nil, fmt.Errorf("occurrence: reader requires an evidence source")
	}
	if definitions == nil {
		return nil, fmt.Errorf("occurrence: reader requires a definition source")
	}
	return &Reader{frozen: frozen, evidence: evidence, definitions: definitions}, nil
}

// OccurrencesForRun returns one run's occurrences.
//
// A run with no recorded plan yet returns nothing rather than an error. That
// is not an edge case: every run sits in 'admitting' between creation and
// RecordPlan, and treating a normally-admitting run as a failure would turn
// the whole overview into a 500 the moment anyone started a workflow.
func (reader *Reader) OccurrencesForRun(
	ctx context.Context,
	run db.AgentWorkflowRun,
) ([]NodeOccurrence, error) {
	if runIsTerminal(run.Status) {
		frozen, err := reader.frozen.ForRun(ctx, int64(run.ID))
		if err != nil {
			return nil, fmt.Errorf("occurrence: reading frozen projection for run %d: %w", run.ID, err)
		}
		if len(frozen) > 0 {
			return fromProjection(frozen), nil
		}
		// A terminal run predating the projection, or one whose freeze failed,
		// falls through to live derivation. It may be incomplete — that is the
		// honest consequence of GC, not something to hide behind an error.
	}

	if len(run.ActualPlan) == 0 {
		return nil, nil
	}

	// The definition is loaded by the run's OWN workflow version, never the
	// promoted one, for the same reason the freezer does: a run's occurrences
	// describe the revision that actually executed.
	executionNodes, err := executionNodesFor(reader.definitions, run)
	if err != nil {
		return nil, err
	}

	evidence, err := reader.evidence.EvidenceForRun(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("occurrence: gathering evidence for run %d: %w", run.ID, err)
	}
	return Derive(sourcesFrom(run, evidence, executionNodes))
}

// runIsTerminal mirrors db.AgentWorkflowRunStatus's terminal set. 'canceling'
// is deliberately active: the run is still executing and its frozen projection
// does not exist yet.
func runIsTerminal(status db.AgentWorkflowRunStatus) bool {
	switch status {
	case db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

func fromProjection(rows []db.AgentWorkflowRunNodeOccurrence) []NodeOccurrence {
	result := make([]NodeOccurrence, 0, len(rows))
	for _, row := range rows {
		result = append(result, NodeOccurrence{
			WorkflowRunID:        snapshot.WorkflowRunID(row.WorkflowRunID),
			TeamID:               row.TeamID,
			WorkflowName:         row.WorkflowName,
			WorkflowDefinitionID: row.WorkflowDefinitionID,
			WorkflowVersion:      row.WorkflowVersion,
			NodeID:               row.NodeID,
			NodeKind:             row.NodeKind,
			RetryAttempt:         row.RetryAttempt,
			Attempt:              row.Attempt,
			PlanID:               row.PlanID,
			Status:               Status(row.Status),
			ReusableNodeName:     row.ReusableNodeName,
			ReusableNodeVersion:  intOrZero(row.ReusableNodeVersion),
			WaitID:               row.WaitID,
			PublicationID:        row.PublicationID,
			StartedAt:            copyTime(row.StartedAt),
			CompletedAt:          copyTime(row.CompletedAt),
			DurationSeconds:      row.DurationSeconds,
			CostUSD:              row.CostUSD,
		})
	}
	return result
}

func intOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
