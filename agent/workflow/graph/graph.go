// Package graph derives the redacted semantic DAG of a compiled v3 workflow
// function. It is the single graph model shared by the workflow overview, the
// individual run page, and any later consumer.
//
// Redaction is structural: prompts, task configs, and broker profiles have no
// fields in these types, so a call site cannot forget to redact them.
package graph

import "fmt"

type NodeKind string

const (
	KindInput          NodeKind = "input"
	KindResourceSource NodeKind = "resource_source"
	KindLoad           NodeKind = "load"
	KindAgent          NodeKind = "agent"
	KindTask           NodeKind = "task"
	KindAwait          NodeKind = "await"
	KindPublish        NodeKind = "publish"
	KindOutput         NodeKind = "output"
)

func (kind NodeKind) Validate() error {
	switch kind {
	case KindInput, KindResourceSource, KindLoad, KindAgent, KindTask, KindAwait, KindPublish, KindOutput:
		return nil
	default:
		return fmt.Errorf("graph: unknown node kind %q", kind)
	}
}

// Decoration is a control-machinery wrapper that affects a node without
// becoming a node of its own.
type Decoration string

const (
	DecorationRetry     Decoration = "retry"
	DecorationTimeout   Decoration = "timeout"
	DecorationTry       Decoration = "try"
	DecorationEnsure    Decoration = "ensure"
	DecorationOnFailure Decoration = "on_failure"
	DecorationOnError   Decoration = "on_error"
	DecorationOnAbort   Decoration = "on_abort"
	DecorationOnSuccess Decoration = "on_success"
)

// Node is one semantic workflow element. ID is the stable workflow-local
// identity: the authored function_id for agent and task nodes, and the
// contract-bearing binding or port name for every other kind.
//
// There is deliberately no reusable-node provenance here. A reusable node is
// expanded away by agent/workflow/node_reference.go before compilation: the
// resolved ResolvedNodeBinding is returned alongside the CompiledDefinition
// and stored separately, and the compiled FunctionConfig that Build consumes
// retains no trace of it. Fields Build cannot populate must not sit in a JSON
// contract about to be frozen and decoded by a browser; a later phase that
// threads the bindings in can add them deliberately, with a producer.
type Node struct {
	ID          string   `json:"id"`
	Kind        NodeKind `json:"kind"`
	DisplayName string   `json:"display_name"`
	TypeRef     string   `json:"type_ref,omitempty"`
	// Optional means this node's production is not guaranteed: an optional
	// port, an optional load_snapshot, an optional function output, or
	// anything produced inside a try. It is the graph's rendering of
	// typecheck.go's presence == snapshotConditional.
	Optional    bool         `json:"optional,omitempty"`
	Decorations []Decoration `json:"decorations,omitempty"`
}

// Edge runs from a producing node to a consuming node, labelled with the
// snapshot binding that connects them.
//
// An edge that touches an ENDPOINT node is labelled with that endpoint's
// public port name — which is exactly its DisplayName, and exactly the
// port_name the durable run-level binding is keyed by. That is what lets a run
// page join `run.inputs`/`run.outputs` onto the node that consumed or produced
// them. It falls out for free on the input side (a public input seeds the
// environment under its own port name) and is stated explicitly on the output
// side, where the consumed binding is `outputs[].from` and may be named
// anything (see Build).
//
// An edge between two execution nodes is labelled with the internal binding
// name, which is the only name that intermediate snapshot has.
type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	PortName string `json:"port_name"`
	TypeRef  string `json:"type_ref,omitempty"`
	// Optional means this specific dataflow may not occur in a given run:
	// either the binding itself is conditional (optional port, optional load,
	// optional function output, anything produced inside a try), or the binding
	// has several possible producers and only one of them will have written
	// it. A run page must be able to render an edge that carried nothing
	// without implying the run was wrong, so this ships with the contract
	// rather than being retrofitted after it freezes.
	Optional bool `json:"optional,omitempty"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

func (g Graph) Node(id string) (Node, bool) {
	for _, node := range g.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return Node{}, false
}
