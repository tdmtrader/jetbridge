package workflowruns

import (
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc/db"
)

// GraphResponse is the exact DAG of one immutable run occurrence together with
// that run's node state.
//
// It is built from the run's OWN revision, never from the currently promoted
// one, so opening an old run shows the shape that actually executed rather than
// today's shape with yesterday's state painted onto it.
//
// Graph is graph.Graph, which is structurally redacted: prompts, task configs,
// and broker profiles have no fields on those types, so this response cannot
// leak them by omission.
type GraphResponse struct {
	WorkflowRunID   snapshot.WorkflowRunID `json:"workflow_run_id"`
	WorkflowName    string                 `json:"workflow_name"`
	WorkflowVersion int                    `json:"workflow_version"`
	// Graph is always a decodable object. When derivation fails it is an empty
	// graph and GraphUnavailable says so, rather than null, which the Elm list
	// decoders reject outright.
	Graph graph.Graph `json:"graph"`
	// GraphUnavailable reports that this run's revision could not be drawn —
	// an unrecognised step kind, or a revision that has since been removed.
	// The run and its node state still render; only the canvas is missing.
	GraphUnavailable bool             `json:"graph_unavailable"`
	Occurrences      []NodeOccurrence `json:"occurrences"`
}

// NodeOccurrence is the redacted per-node projection: one attempt of one
// semantic node within this run.
//
// Exactly one status is reported per occurrence. That is what lets the run page
// supply the shared renderer's node state from a single-run lookup, with no
// second graph model and no run-specific renderer.
type NodeOccurrence struct {
	NodeID   string `json:"node_id"`
	NodeKind string `json:"node_kind"`
	// RetryAttempt is which copy of an authored retry closure this is;
	// Attempt is which recovery attempt of that copy. They come from different
	// records and answer different questions, so they stay separate here for
	// the same reason they are separate in the projection.
	RetryAttempt        int        `json:"retry_attempt"`
	Attempt             int        `json:"attempt"`
	Status              string     `json:"status"`
	PlanID              string     `json:"plan_id"`
	ReusableNodeName    string     `json:"reusable_node_name,omitempty"`
	ReusableNodeVersion int        `json:"reusable_node_version,omitempty"`
	WaitID              *int64     `json:"wait_id"`
	PublicationID       *int64     `json:"publication_id"`
	StartedAt           *time.Time `json:"started_at"`
	CompletedAt         *time.Time `json:"completed_at"`
	DurationSeconds     int        `json:"duration_seconds"`
	CostUSD             float64    `json:"cost_usd"`
}

// Graph serves one run's exact DAG and node state.
func (handler *Handler) Graph(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireNoBody(w, r) {
		return
	}
	workflowName, runID, _, ok := parseRoute(w, r, true, nil)
	if !ok {
		return
	}
	run, ok := handler.loadScopedRun(w, r, workflowName, runID)
	if !ok {
		return
	}

	response := GraphResponse{
		WorkflowRunID:   run.ID,
		WorkflowName:    run.WorkflowName,
		WorkflowVersion: run.WorkflowVersion,
		Graph:           graph.Graph{Nodes: []graph.Node{}, Edges: []graph.Edge{}},
		Occurrences:     []NodeOccurrence{},
	}

	built, available, err := handler.runGraph(run)
	if err != nil {
		handler.writeInternalError(w)
		return
	}
	if available {
		response.Graph = built
	} else {
		response.GraphUnavailable = true
	}

	// The occurrence reader answers frozen history for a terminal run and a
	// live derivation for everything still executing, so an active run and a
	// completed one describe their nodes identically. Failing here is not a
	// degradation: without node state the page would show a shape with no
	// indication of what happened in it.
	occurrences, err := handler.occurrences.OccurrencesForRun(r.Context(), run)
	if err != nil {
		handler.logger.Error("read-run-occurrences-failed", err, lager.Data{
			"workflow": run.WorkflowName, "workflow_run_id": run.ID.String(),
		})
		handler.writeInternalError(w)
		return
	}
	response.Occurrences = presentNodeOccurrences(occurrences)

	writeJSON(w, http.StatusOK, response)
}

// runGraph derives the DAG of the run's own revision. The boolean is false for
// a page that must degrade rather than fail; the error is reserved for a store
// failure, which is not something the reader can work around.
//
// A revision that has since been removed degrades for the same reason an
// unrecognised step kind does: the run, its timings, and its node state are all
// still true and still useful, and blanking the page would discard them to
// report a drawing problem.
func (handler *Handler) runGraph(run db.AgentWorkflowRun) (graph.Graph, bool, error) {
	definition, found, err := handler.definitions.Get(run.WorkflowName, run.WorkflowVersion)
	if err != nil {
		handler.logger.Error("get-run-revision-failed", err, lager.Data{
			"workflow": run.WorkflowName, "version": run.WorkflowVersion,
		})
		return graph.Graph{}, false, err
	}
	if !found || definition == nil {
		handler.logger.Info("run-revision-not-stored", lager.Data{
			"workflow": run.WorkflowName, "version": run.WorkflowVersion,
		})
		return graph.Graph{}, false, nil
	}
	built, err := graph.Build(definition.Compiled.Function)
	if err != nil {
		handler.logger.Error("derive-run-graph-failed", err, lager.Data{
			"workflow": run.WorkflowName, "version": run.WorkflowVersion,
		})
		return graph.Graph{}, false, nil
	}
	return built, true, nil
}

func presentNodeOccurrences(occurrences []occurrence.NodeOccurrence) []NodeOccurrence {
	presented := make([]NodeOccurrence, 0, len(occurrences))
	for _, entry := range occurrences {
		presented = append(presented, NodeOccurrence{
			NodeID: entry.NodeID, NodeKind: entry.NodeKind,
			RetryAttempt: entry.RetryAttempt, Attempt: entry.Attempt,
			Status: string(entry.Status), PlanID: entry.PlanID,
			ReusableNodeName: entry.ReusableNodeName, ReusableNodeVersion: entry.ReusableNodeVersion,
			WaitID: cloneInt64(entry.WaitID), PublicationID: cloneInt64(entry.PublicationID),
			StartedAt: cloneTime(entry.StartedAt), CompletedAt: cloneTime(entry.CompletedAt),
			DurationSeconds: entry.DurationSeconds, CostUSD: entry.CostUSD,
		})
	}
	return presented
}
