package occurrence

import (
	"fmt"
	"sort"
	"time"

	"github.com/concourse/concourse/agent/workflow"
)

// Derive maps authoritative execution records onto one occurrence per
// semantic node attempt. It never originates a fact: every status traces to a
// durable record, and a node with no evidence is pending.
//
// It is called from two places with identical semantics — live, so the
// overview's current-attention view is never stale, and at run finalization,
// to freeze durable history before Concourse build and template GC reclaim the
// sources. Sharing one function between them is what prevents a second
// execution truth.
func Derive(sources Sources) ([]NodeOccurrence, error) {
	if len(sources.Run.ActualPlan) == 0 {
		return nil, fmt.Errorf("occurrence: run %d has no frozen actual plan", sources.Run.ID)
	}
	// An empty node set would silently project nothing, which at the freeze
	// call site means losing a run's entire history. A caller that could not
	// derive the graph must fail loudly instead.
	if len(sources.ExecutionNodes) == 0 {
		return nil, fmt.Errorf("occurrence: run %d has no graph execution nodes", sources.Run.ID)
	}

	planned, err := PlanNodes(sources.Run.ActualPlan)
	if err != nil {
		return nil, err
	}
	nodes := retainGraphNodes(planned, sources.ExecutionNodes)

	metricsByPlan := map[string][]AttemptMetric{}
	for _, metric := range sources.AttemptMetrics {
		metricsByPlan[metric.PlanID] = append(metricsByPlan[metric.PlanID], metric)
	}
	for planID := range metricsByPlan {
		attempts := metricsByPlan[planID]
		sort.SliceStable(attempts, func(i, j int) bool {
			return attempts[i].ExecutionAttempt < attempts[j].ExecutionAttempt
		})
	}

	var result []NodeOccurrence
	for _, group := range groupByNode(nodes) {
		result = append(result, deriveNode(sources, group, metricsByPlan)...)
	}
	return result, nil
}

// retainGraphNodes keeps only the plan nodes the run's own graph recognises as
// execution nodes of the same kind. Everything else — most importantly the
// synthetic per-input-port and per-resource-source loads RenderFunction
// prepends — is not a graph node at all, so projecting it would put an
// identity into the projection that no graph node can ever join to.
func retainGraphNodes(nodes []PlanNode, executionNodes map[string]string) []PlanNode {
	retained := make([]PlanNode, 0, len(nodes))
	for _, node := range nodes {
		if executionNodes[node.NodeID] == node.Kind {
			retained = append(retained, node)
		}
	}
	return retained
}

// groupByNode collects the plan nodes that share one identity, in first-seen
// order. A retry closure is the only way one identity occupies several plan
// IDs, because the type checker enforces identity uniqueness elsewhere.
func groupByNode(nodes []PlanNode) [][]PlanNode {
	var order []string
	byNode := map[string][]PlanNode{}
	for _, node := range nodes {
		if _, found := byNode[node.NodeID]; !found {
			order = append(order, node.NodeID)
		}
		byNode[node.NodeID] = append(byNode[node.NodeID], node)
	}
	groups := make([][]PlanNode, 0, len(order))
	for _, nodeID := range order {
		groups = append(groups, byNode[nodeID])
	}
	return groups
}

// deriveNode projects every plan copy of one node identity that has evidence.
// A retry copy that never ran is not a fact of its own: emitting it would
// leave a finished workflow showing attention-worthy pending nodes for
// attempts that never happened. When no copy ran, the node is still in the
// plan, so it is projected exactly once as pending.
//
// RetryAttempt is assigned here, as the copy's 1-based ordinal within its node
// group in plan order, rather than taken from PlanNode.RetryAttempt. The two
// agree exactly for a single-level retry closure, which is every retry the
// planner has ever materialized for a shipped workflow. They diverge for
// NESTED retry: atc/builds/planner.go materializes outer*inner copies of the
// leaf while walkPlan numbers each copy by its innermost index alone, so
// several distinct plan copies would share one number — and the projection's
// PRIMARY KEY (workflow_run_id, node_id, retry_attempt, attempt) would collide,
// silently discarding real history. The ordinal is injective over the group by
// construction, so every materialized copy keeps its own durable row.
func deriveNode(sources Sources, group []PlanNode, metricsByPlan map[string][]AttemptMetric) []NodeOccurrence {
	var result []NodeOccurrence
	for ordinal, node := range group {
		node.RetryAttempt = ordinal + 1
		result = append(result, deriveCopy(sources, node, metricsByPlan[node.PlanID])...)
	}
	if len(result) > 0 {
		return result
	}
	unreached := group[0]
	unreached.RetryAttempt = 1
	return []NodeOccurrence{baseOccurrence(sources, unreached)}
}

// deriveCopy projects the evidence attached to one plan ID, returning nothing
// when that copy has none.
func deriveCopy(sources Sources, node PlanNode, metrics []AttemptMetric) []NodeOccurrence {
	base := baseOccurrence(sources, node)

	// Await and publish nodes are evidenced by their own tables rather than by
	// metrics, and are matched on the same plan ID.
	switch node.Kind {
	case KindAwait:
		wait, found := findWait(sources.Waits, node.PlanID)
		if !found {
			// The step can fail — or finish early — long before it creates its
			// wait row: atc/exec/await_snapshot_step.go returns on validation,
			// authorization, snapshot availability and the
			// reapproval-not-required fast path, all ahead of waits.CreateOrGet.
			// The engine still recorded the step's outcome, so falling through
			// to it is what keeps the node that killed the run from freezing as
			// 'pending' — which is the projection's word for "never reached".
			return fallbackFromBuildStep(sources, base, node)
		}
		base.Status = waitStatus(wait)
		base.WaitID = int64Ref(wait.ID)
		base.StartedAt = timePtr(wait.CreatedAt)
		if base.Status.Terminal() && wait.ResolvedAt != nil {
			base.CompletedAt = wait.ResolvedAt
			base.DurationSeconds = durationSeconds(wait.CreatedAt, *wait.ResolvedAt)
		}
		base.Status = settleAgainstRun(sources, base.Status)
		return []NodeOccurrence{base}

	case KindPublish:
		publication, found := findPublication(sources.Publications, node.PlanID)
		if !found {
			// Same reasoning as the await arm: atc/exec/publish_snapshot_step.go
			// has many error returns — a rejected merge approval among them —
			// ahead of the agent_publication_occurrences insert. This is also
			// the build-step fallback migration 1773106157 promises for
			// publications whose plan_id is NULL.
			return fallbackFromBuildStep(sources, base, node)
		}
		base.Status = publicationStatus(publication.Status)
		base.PublicationID = int64Ref(publication.ID)
		base.StartedAt = timePtr(publication.CreatedAt)
		if base.Status.Terminal() {
			base.CompletedAt = timePtr(publication.UpdatedAt)
			base.DurationSeconds = durationSeconds(publication.CreatedAt, publication.UpdatedAt)
		}
		base.Status = settleAgainstRun(sources, base.Status)
		return []NodeOccurrence{base}
	}

	if len(metrics) == 0 {
		// Deterministic task steps have no durable metrics row of their own,
		// so the freeze reads their terminal state from build step state while
		// the build still exists.
		return fallbackFromBuildStep(sources, base, node)
	}

	result := make([]NodeOccurrence, 0, len(metrics))
	for _, metric := range metrics {
		occurrence := base
		occurrence.Attempt = metric.ExecutionAttempt
		occurrence.Status = agentMetricStatus(metric.Status)
		occurrence.CostUSD = metric.CostUSD
		occurrence.StartedAt = timePtr(metric.CreatedAt)
		if occurrence.Status.Terminal() {
			occurrence.CompletedAt = timePtr(metric.UpdatedAt)
			occurrence.DurationSeconds = durationSeconds(metric.CreatedAt, metric.UpdatedAt)
		}
		occurrence.Status = settleAgainstRun(sources, occurrence.Status)
		result = append(result, occurrence)
	}
	return result
}

// fallbackFromBuildStep is the one place a node's status comes from the build's
// own step events. Every node kind reaches it: it is a deterministic task's
// only durable evidence, and for await and publish it is what the engine
// recorded when the step died before writing its own row. A copy with no build
// step evidence either returns nothing, so the node stays pending exactly when
// nothing at all is known about it.
func fallbackFromBuildStep(sources Sources, base NodeOccurrence, node PlanNode) []NodeOccurrence {
	status, found := sources.BuildStepStatus[node.PlanID]
	if !found {
		return nil
	}
	base.Status = status
	return []NodeOccurrence{base}
}

// settleAgainstRun refuses to project an in-flight status onto a run that has
// already finished.
//
// A wait left 'waiting' or a publication left 'pending' by a build that was
// aborted, errored or drained is not work still in flight — the process that
// would have advanced it is gone. Freezing the live value would pin the run in
// the attention lens permanently, because the frozen row is immutable and no
// retry supersedes a live occurrence. The run's own terminal status is a
// durable fact, so this reads a second fact against a first rather than
// originating one. Pending is untouched: it means "never reached", which stays
// true on a finished run.
func settleAgainstRun(sources Sources, status Status) Status {
	if status != StatusRunning && status != StatusWaiting {
		return status
	}
	if !runIsTerminal(sources.Run.Status) {
		return status
	}
	return StatusAborted
}

func baseOccurrence(sources Sources, node PlanNode) NodeOccurrence {
	occurrence := NodeOccurrence{
		WorkflowRunID:        sources.Run.ID,
		TeamID:               sources.Run.TeamID,
		WorkflowName:         sources.Run.WorkflowName,
		WorkflowDefinitionID: sources.Run.WorkflowDefinitionID,
		WorkflowVersion:      sources.Run.WorkflowVersion,
		NodeID:               node.NodeID,
		NodeKind:             node.Kind,
		PlanID:               node.PlanID,
		RetryAttempt:         node.RetryAttempt,
		Attempt:              1,
		Status:               StatusPending,
	}
	// A run OF a reusable node is the one case where the run itself names the
	// node version that executed, which is what the projection's
	// reusable_node_name/reusable_node_version columns exist to record. Inside
	// an ordinary workflow run the node has been expanded away and there is
	// nothing here to read.
	if sources.Run.DefinitionKind == workflow.DefinitionKindNode {
		occurrence.ReusableNodeName = sources.Run.WorkflowName
		occurrence.ReusableNodeVersion = sources.Run.WorkflowVersion
	}
	return occurrence
}

// agentMetricStatus maps agent_run_metrics.status onto occurrence status.
// 'incomplete' means a missing flight recording on an otherwise successful
// step (migration 1773106126), so it must not render as a failure.
func agentMetricStatus(status string) Status {
	switch status {
	case "ok", "incomplete":
		return StatusSucceeded
	case "failed":
		return StatusFailed
	case "error":
		return StatusErrored
	case "parked":
		return StatusRunning
	default:
		return StatusPending
	}
}

// waitStatus maps agent_workflow_waits onto occurrence status.
//
// A cancelled wait is an abort. A timed-out wait depends on its timeout
// policy, and the status column alone cannot tell the two apart:
// atc/db/agent_workflow_waits_factory.go writes 'timed_out' both when the wait
// expired with no answer and when it expired onto its declared default
// snapshot. Under policy 'fail' the workflow could not obtain the human answer
// it required, which is a failure. Under policy 'default' the wait resolved
// with the answer the author declared for exactly this case and the workflow
// proceeded, so painting it red would be a false alarm.
func waitStatus(wait Wait) Status {
	switch wait.Status {
	case "waiting":
		return StatusWaiting
	case "resolved":
		return StatusSucceeded
	case "timed_out":
		if wait.TimeoutPolicy == "default" {
			return StatusSucceeded
		}
		return StatusFailed
	case "cancelled":
		return StatusAborted
	default:
		return StatusPending
	}
}

// publicationStatus maps agent_publication_occurrences.status. Both
// 'stale_base' and 'rebase_required' are unresolved outbound effects that need
// human attention, so they surface as failures rather than as pending.
func publicationStatus(status string) Status {
	switch status {
	case "pending":
		return StatusRunning
	case "succeeded":
		return StatusSucceeded
	case "failed", "stale_base", "rebase_required":
		return StatusFailed
	default:
		return StatusPending
	}
}

func findWait(waits []Wait, planID string) (Wait, bool) {
	for _, wait := range waits {
		if wait.PlanID == planID {
			return wait, true
		}
	}
	return Wait{}, false
}

func findPublication(publications []Publication, planID string) (Publication, bool) {
	for _, publication := range publications {
		if publication.PlanID == planID {
			return publication, true
		}
	}
	return Publication{}, false
}

func int64Ref(value int64) *int64 { return &value }

func durationSeconds(from, to time.Time) int {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return 0
	}
	return int(to.Sub(from).Seconds())
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
