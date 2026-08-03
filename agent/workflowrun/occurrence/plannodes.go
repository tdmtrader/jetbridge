// Package occurrence derives per-node occurrence state for one workflow run
// from authoritative execution records. It is called twice with the same
// semantics: live, so the overview's current-attention view is never stale,
// and at run finalization, to freeze durable history before Concourse
// reclaims the underlying build.
//
// The derivation never originates a fact. Every status traces to a durable
// record, and a node with no evidence is pending. That is what keeps this a
// projection of execution truth rather than a second one.
package occurrence

import (
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc"
)

// Node kinds, matching graph.NodeKind for the occurrence-bearing kinds so the
// overview can join a plan node to its graph node without translation.
const (
	KindAgent   = "agent"
	KindTask    = "task"
	KindAwait   = "await"
	KindPublish = "publish"
	KindLoad    = "load"
)

// PlanNode is one semantic node located in a run's frozen actual plan.
//
// NodeID is the stable workflow-local identity registered by the type checker:
// the authored function_id for agent and task nodes, and the contract-bearing
// binding name for await, publish, and load nodes. It is deliberately bare,
// because that is what agent/workflow/graph emits for execution nodes.
//
// RetryAttempt is the index of the NEAREST ENCLOSING retry closure this node
// belongs to, counted from one. atc/builds/planner.go materializes one full
// copy of the wrapped step per configured attempt, each with its own plan ID,
// and atc/engine/builder.go numbers those copies by their index at execution
// time, so this is the engine's own number for a single-level closure.
//
// It is NOT the projection's key. Nested retry multiplies the copies while
// this number repeats once per outer copy, so deriveNode renumbers each copy
// by its ordinal within its node group before projecting it. Read this field
// as "which turn of the innermost closure", not as "which copy".
type PlanNode struct {
	PlanID       string
	NodeID       string
	Kind         string
	DisplayName  string
	RetryAttempt int
}

// PlanNodes decodes a frozen actual plan and returns its semantic nodes in
// plan order. Control wrappers are traversed but never emitted, matching the
// rule that wrappers decorate nodes rather than becoming them.
func PlanNodes(actualPlan []byte) ([]PlanNode, error) {
	var plan atc.Plan
	if err := json.Unmarshal(actualPlan, &plan); err != nil {
		return nil, fmt.Errorf("occurrence: decoding actual plan: %w", err)
	}
	var nodes []PlanNode
	walkPlan(plan, 1, &nodes)
	return nodes, nil
}

// walkPlan mirrors the traversal in atc/workflowprovenance/provenance.go so
// wrapped and conditional steps stay consistent with the evidence capture that
// runs over the same plan.
//
// retryAttempt is the attempt number inherited from the nearest enclosing
// retry closure. Nested retries take the innermost number, because that is the
// closure whose copies actually differ.
func walkPlan(plan atc.Plan, retryAttempt int, nodes *[]PlanNode) {
	emit := func(nodeID, kind, displayName string) {
		*nodes = append(*nodes, PlanNode{
			PlanID:       string(plan.ID),
			NodeID:       nodeID,
			Kind:         kind,
			DisplayName:  displayName,
			RetryAttempt: retryAttempt,
		})
	}

	switch {
	case plan.Agent != nil:
		emit(plan.Agent.FunctionID, KindAgent, plan.Agent.Name)
	case plan.Task != nil:
		emit(plan.Task.FunctionID, KindTask, plan.Task.Name)
	case plan.AwaitSnapshot != nil:
		emit(plan.AwaitSnapshot.Name, KindAwait, plan.AwaitSnapshot.Name)
	case plan.PublishSnapshot != nil:
		emit(plan.PublishSnapshot.Name, KindPublish, plan.PublishSnapshot.Name)
	case plan.LoadSnapshot != nil:
		emit(plan.LoadSnapshot.Name, KindLoad, plan.LoadSnapshot.Name)

	case plan.Retry != nil:
		// Each copy is a distinct attempt of the same node, numbered exactly
		// as atc/engine/builder.go numbers it.
		for index, child := range *plan.Retry {
			walkPlan(child, index+1, nodes)
		}
	case plan.Do != nil:
		for _, child := range *plan.Do {
			walkPlan(child, retryAttempt, nodes)
		}
	case plan.InParallel != nil:
		for _, child := range plan.InParallel.Steps {
			walkPlan(child, retryAttempt, nodes)
		}
	case plan.Try != nil:
		walkPlan(plan.Try.Step, retryAttempt, nodes)
	case plan.Timeout != nil:
		walkPlan(plan.Timeout.Step, retryAttempt, nodes)
	case plan.OnSuccess != nil:
		walkPlan(plan.OnSuccess.Step, retryAttempt, nodes)
		walkPlan(plan.OnSuccess.Next, retryAttempt, nodes)
	case plan.OnFailure != nil:
		walkPlan(plan.OnFailure.Step, retryAttempt, nodes)
		walkPlan(plan.OnFailure.Next, retryAttempt, nodes)
	case plan.OnError != nil:
		walkPlan(plan.OnError.Step, retryAttempt, nodes)
		walkPlan(plan.OnError.Next, retryAttempt, nodes)
	case plan.OnAbort != nil:
		walkPlan(plan.OnAbort.Step, retryAttempt, nodes)
		walkPlan(plan.OnAbort.Next, retryAttempt, nodes)
	case plan.Ensure != nil:
		walkPlan(plan.Ensure.Step, retryAttempt, nodes)
		walkPlan(plan.Ensure.Next, retryAttempt, nodes)

		// plan.Across is deliberately not traversed. Its substeps live in an
		// uninterpolated template whose plan IDs are rewritten per substep at
		// execution time, so the template's IDs join to no evidence row.
		// Emitting them would manufacture permanently-pending nodes for work
		// that actually ran, which is worse than omitting them, and inventing
		// the interpolated IDs here would originate a fact.
	}
}
