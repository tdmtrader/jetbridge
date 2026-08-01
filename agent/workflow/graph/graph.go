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
type Node struct {
	ID          string       `json:"id"`
	Kind        NodeKind     `json:"kind"`
	DisplayName string       `json:"display_name"`
	TypeRef     string       `json:"type_ref,omitempty"`
	Optional    bool         `json:"optional,omitempty"`
	Decorations []Decoration `json:"decorations,omitempty"`

	// Set only when the node is a reusable-node binding.
	ReusableNodeName    string `json:"reusable_node_name,omitempty"`
	ReusableNodeVersion int    `json:"reusable_node_version,omitempty"`
}

// Edge runs from a producing node to a consuming node, labelled with the
// snapshot binding that connects them.
type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	PortName string `json:"port_name"`
	TypeRef  string `json:"type_ref,omitempty"`
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
