package occurrence

import (
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/atc/db"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusErrored   Status = "errored"
	StatusAborted   Status = "aborted"
	StatusSkipped   Status = "skipped"
)

func (status Status) Validate() error {
	switch status {
	case StatusPending, StatusRunning, StatusWaiting, StatusSucceeded,
		StatusFailed, StatusErrored, StatusAborted, StatusSkipped:
		return nil
	default:
		return fmt.Errorf("occurrence: invalid status %q", status)
	}
}

// Terminal reports whether the status can no longer change for this attempt.
func (status Status) Terminal() bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusErrored, StatusAborted, StatusSkipped:
		return true
	default:
		return false
	}
}

// NodeOccurrence is one attempt of one semantic node within one workflow run.
//
// There are two independent attempt axes and they are kept separate rather
// than fused, because they answer different questions and come from different
// records. RetryAttempt is which copy of an authored retry closure this is,
// read from the plan's structure. Attempt is which recovery attempt of that
// copy this is. Pinned to 1 since agent_run_metrics carries no attempt
// dimension; the per-attempt table it once read is written only under
// checkpoints, which no deployment enables. A
// durable projection keyed on only one of them would collide.
type NodeOccurrence struct {
	WorkflowRunID        snapshot.WorkflowRunID
	TeamID               int
	WorkflowName         string
	WorkflowDefinitionID int
	WorkflowVersion      int
	NodeID               string
	NodeKind             string
	RetryAttempt         int
	Attempt              int
	PlanID               string
	Status               Status
	ReusableNodeName     string
	ReusableNodeVersion  int
	WaitID               *int64
	PublicationID        *int64
	StartedAt            *time.Time
	CompletedAt          *time.Time
	DurationSeconds      int
	CostUSD              float64
}

// AttemptMetric is the narrow projection of agent_run_metrics the
// derivation needs. It is a local type so the derivation does not depend on
// the full DB row shape.
type AttemptMetric struct {
	PlanID           string
	ExecutionAttempt int
	Status           string
	CostUSD          float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Wait is the narrow projection of agent_workflow_waits.
//
// TimeoutPolicy is carried because status alone cannot distinguish a wait that
// expired without an answer from one that expired onto its declared default.
// atc/db/agent_workflow_waits_factory.go writes status 'timed_out' for both.
type Wait struct {
	ID            int64
	PlanID        string
	OutputName    string
	Status        string
	TimeoutPolicy string
	CreatedAt     time.Time
	ResolvedAt    *time.Time
}

// Publication is the narrow projection of agent_publication_occurrences.
type Publication struct {
	ID        int64
	PlanID    string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Sources is every authoritative record the derivation reads. Callers supply
// whatever they have; absent evidence yields a pending occurrence rather than
// an error, because a run may legitimately not have reached a node.
type Sources struct {
	Run            db.AgentWorkflowRun
	AttemptMetrics []AttemptMetric
	Waits          []Wait
	Publications   []Publication
	// BuildStepStatus maps a plan ID to a terminal build step status for nodes
	// that have no other durable evidence, notably deterministic tasks. It is
	// read from build events while they still exist.
	BuildStepStatus map[string]Status
	// ExecutionNodes is the execution-node set graph.Build derives for this
	// run's own workflow version, as node ID to node kind. Derive projects only
	// plan nodes in this set. See ExecutionNodesOf.
	ExecutionNodes map[string]string
}

// ExecutionNodesOf projects a derived graph onto the node set a run's
// projection may contain: the occurrence-bearing kinds, keyed by node ID.
//
// This is what makes the graph-to-occurrence join exact by construction rather
// than a guess across prefixes. RenderFunction prepends a synthetic
// load_snapshot per input port and per bound resource source, named with the
// BARE port name, so the runtime plan contains a node called "before" while
// graph.Build calls the same concept "input:before". Endpoint nodes never
// carry occurrences, so a synthetic load is absent from this set and Derive
// drops it, while an authored load_snapshot is a graph execution node with a
// bare ID and is kept — with no special case for either.
//
// The kind travels with the ID because the node ID alone is not unique across
// kinds: an input port and an agent function may share a name, and matching on
// the pair keeps the synthetic load from colliding with the agent on the
// projection's primary key.
func ExecutionNodesOf(built graph.Graph) map[string]string {
	nodes := make(map[string]string, len(built.Nodes))
	for _, node := range built.Nodes {
		switch node.Kind {
		case graph.KindAgent, graph.KindTask, graph.KindAwait, graph.KindPublish, graph.KindLoad:
			nodes[node.ID] = string(node.Kind)
		}
	}
	return nodes
}
