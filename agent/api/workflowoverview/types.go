// Package workflowoverview serves the aggregate state a workflow page needs:
// the promoted revision's semantic graph, per-node state across all relevant
// runs, and revision boundaries. It deliberately never emits one ambiguous
// aggregate node status, because a node representing several concurrent runs
// has no single canonical state.
package workflowoverview

import (
	"context"
	"time"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc/db"
)

// TrustedTeam is resolved server-side, exactly as it is for the run routes.
type TrustedTeam struct {
	ID   int
	Name string
}

// Definitions is the narrow read of workflow.Store the overview needs. Live
// resolves the PROMOTED revision, Latest the highest imported one, and Get an
// exact revision for revision-boundary metadata.
type Definitions interface {
	Live(name string) (*workflow.Definition, bool, error)
	Latest(name string) (*workflow.Definition, bool, error)
	Get(name string, version int) (*workflow.Definition, bool, error)
}

// Runs is the team-filtered run list. db.AgentWorkflowRunsFactory satisfies it.
type Runs interface {
	List(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error)
}

// Occurrences answers one run's node occurrences. occurrence.Reader is the
// production implementation: frozen history for terminal runs, live derivation
// for everything still executing.
type Occurrences interface {
	OccurrencesForRun(context.Context, db.AgentWorkflowRun) ([]occurrence.NodeOccurrence, error)
}

type Response struct {
	Workflow Workflow `json:"workflow"`
	Window   Window   `json:"window"`
	// Graph is the promoted revision's redacted semantic DAG. It is always a
	// decodable object: when derivation fails it is an empty graph and
	// GraphUnavailable says so, rather than null, which the Elm list decoders
	// reject outright.
	Graph graph.Graph `json:"graph"`
	// GraphUnavailable reports that graph.Build could not derive this
	// revision's DAG — one unrecognised step kind, most likely. Everything
	// else on the page still works; only the canvas is missing.
	GraphUnavailable bool               `json:"graph_unavailable"`
	NodeState        []NodeState        `json:"node_state"`
	Revisions        []RevisionBoundary `json:"revision_boundaries"`
	// HasHistoricalOnlyNodes reports that runs in the window touched nodes the
	// promoted graph no longer contains. The UI surfaces a discovery
	// affordance; it never renders a union graph.
	HasHistoricalOnlyNodes bool `json:"has_historical_only_nodes"`
}

type Workflow struct {
	Name               string `json:"name"`
	HasPromotedVersion bool   `json:"has_promoted_version"`
	// GraphVersion is the revision that supplied the rendered graph: the
	// promoted version when one exists, otherwise the latest imported one.
	GraphVersion int    `json:"graph_version"`
	ContentHash  string `json:"content_hash"`
}

type Window struct {
	Kind string    `json:"kind"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// IncludesActiveBeforeWindow is always true and is stated rather than
	// implied: active runs are represented regardless of how long ago they
	// began, so changing the window never changes the meaning of active state.
	IncludesActiveBeforeWindow bool `json:"includes_active_before_window"`
}

// ActiveCounts are the nonterminal occurrences of one node right now, counted
// only across runs that are themselves still executing. A finished run has no
// occurrence "right now": its unreached nodes are frozen pending forever, and
// counting those would report permanent phantom work.
type ActiveCounts struct {
	Running int `json:"running"`
	Waiting int `json:"waiting"`
	Pending int `json:"pending"`
}

func (counts ActiveCounts) total() int {
	return counts.Running + counts.Waiting + counts.Pending
}

// HistoryCounts are the terminal occurrences of one node inside the window.
type HistoryCounts struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Errored   int `json:"errored"`
	Aborted   int `json:"aborted"`
	Skipped   int `json:"skipped"`
}

func (counts HistoryCounts) total() int {
	return counts.Succeeded + counts.Failed + counts.Errored + counts.Aborted + counts.Skipped
}

// NodeState reports active and windowed-history counts separately, plus the
// resolved attention state. There is deliberately no single `status` field:
// several concurrent runs of one node have no canonical status, and inventing
// one — worst-wins, latest-wins, anything — would be a fact the platform does
// not have.
type NodeState struct {
	NodeID  string        `json:"node_id"`
	Active  ActiveCounts  `json:"active"`
	History HistoryCounts `json:"history"`
	// NeedsAttention is the retry-chain-resolved answer to "does this node
	// need action now". A successful retry clears an earlier failure here
	// while leaving that failure in History.
	NeedsAttention bool `json:"needs_attention"`
	// HasWindowActivity distinguishes a node with no data from a healthy one,
	// so the UI can render no-data distinctly rather than as green.
	HasWindowActivity bool `json:"has_window_activity"`
}

// RevisionBoundary marks where the run list crosses from one workflow revision
// to another. FirstRunID is a string because a durable run ID exceeds the
// range JavaScript numbers represent exactly.
type RevisionBoundary struct {
	Version    int        `json:"version"`
	PromotedAt *time.Time `json:"promoted_at"`
	FirstRunID string     `json:"first_run_id"`
	FirstRunAt time.Time  `json:"first_run_at"`
}

// ErrorResponse mirrors the run API's bounded public error envelope.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
