package workflowoverview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflow/graph"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

// overviewRunLimit bounds how many runs one aggregation reads. The window plus
// this bound is the honest scope of the counts: a workflow that executes more
// than this inside its selected window aggregates its most recent runs. The
// run list paginates independently and is unaffected.
const overviewRunLimit = 1000

type Config struct {
	Logger      lager.Logger
	Team        TrustedTeam
	Definitions Definitions
	Runs        Runs
	Occurrences Occurrences
	// Now supplies the window's upper bound. Optional; time.Now by default.
	Now func() time.Time
}

type Handler struct {
	logger      lager.Logger
	team        TrustedTeam
	definitions Definitions
	runs        Runs
	occurrences Occurrences
	now         func() time.Time
}

func NewHandler(config Config) (*Handler, error) {
	if config.Team.ID <= 0 || config.Team.Name != atc.DefaultTeamName {
		return nil, fmt.Errorf("workflow overview API: a positive main-team identity is required")
	}
	for name, dependency := range map[string]any{
		"definition store": config.Definitions,
		"run store":        config.Runs,
		"occurrence store": config.Occurrences,
	} {
		if interfaceIsNil(dependency) {
			return nil, fmt.Errorf("workflow overview API: %s is required", name)
		}
	}
	logger := config.Logger
	if logger == nil {
		logger = lager.NewLogger("workflow-overview")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Handler{
		logger: logger, team: config.Team, definitions: config.Definitions,
		runs: config.Runs, occurrences: config.Occurrences, now: now,
	}, nil
}

func (handler *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
		return
	}
	name, query, ok := parseRoute(w, r)
	if !ok {
		return
	}

	// The window vocabulary lives in workflowruns so the run list and this
	// page cannot drift apart: they are one shared scope by design.
	kind := workflowruns.DefaultWindow
	if raw, present := query["window"]; present {
		kind = raw[0]
	}
	duration, supported := workflowruns.Windows[kind]
	if !supported {
		writeError(w, http.StatusBadRequest, "invalid_window", "workflow overview window is invalid")
		return
	}

	now := handler.now()
	window := Window{
		Kind:                       kind,
		From:                       now.Add(-duration),
		To:                         now,
		IncludesActiveBeforeWindow: true,
	}

	response, found, err := handler.build(r.Context(), name, window)
	if err != nil {
		handler.logger.Error("build-overview-failed", err, lager.Data{"workflow": name})
		writeError(w, http.StatusInternalServerError, "internal_error", "workflow overview service failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "workflow was not found")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// build assembles the overview. The boolean is false for a workflow with no
// imported version at all, which is absence rather than failure.
func (handler *Handler) build(
	ctx context.Context,
	name string,
	window Window,
) (Response, bool, error) {
	definition, hasPromoted, found, err := handler.graphDefinition(name)
	if err != nil {
		return Response{}, false, err
	}
	if !found {
		return Response{}, false, nil
	}

	response := Response{
		Workflow: Workflow{
			Name:               name,
			HasPromotedVersion: hasPromoted,
			GraphVersion:       definition.Version,
			ContentHash:        definition.ContentHash,
		},
		Window: window,
		// An empty-but-decodable graph is the degraded shape; GraphUnavailable
		// below distinguishes it from a workflow that genuinely has no nodes.
		Graph:     graph.Graph{Nodes: []graph.Node{}, Edges: []graph.Edge{}},
		NodeState: []NodeState{},
		Revisions: []RevisionBoundary{},
	}

	// A step kind graph.Build does not recognise must not blank an otherwise
	// usable page. Nothing connects atc's step types to the builder at compile
	// time, so this degrades rather than 500s, and logs the cause.
	built, err := graph.Build(definition.Compiled.Function)
	if err != nil {
		handler.logger.Error("derive-workflow-graph-failed", err, lager.Data{
			"workflow": name, "version": definition.Version,
		})
		response.GraphUnavailable = true
	} else {
		response.Graph = built
	}

	runs, err := handler.runs.List(ctx, db.AgentWorkflowRunListFilter{
		TeamID:            handler.team.ID,
		WorkflowName:      name,
		Scope:             db.AgentWorkflowRunScopeOperational,
		CompletedSince:    &window.From,
		IncludeActiveRuns: true,
		Limit:             overviewRunLimit,
	})
	if err != nil {
		return Response{}, false, fmt.Errorf("listing workflow runs: %w", err)
	}

	aggregate, err := handler.aggregate(ctx, runs)
	if err != nil {
		return Response{}, false, err
	}

	executionNodes := map[string]string{}
	if !response.GraphUnavailable {
		executionNodes = occurrence.ExecutionNodesOf(response.Graph)
	}
	response.NodeState = aggregate.nodeState(response.GraphUnavailable, response.Graph, executionNodes)
	if !response.GraphUnavailable {
		for nodeID := range aggregate.nodes {
			if _, present := executionNodes[nodeID]; !present {
				response.HasHistoricalOnlyNodes = true
				break
			}
		}
	}
	response.Revisions = handler.revisionBoundaries(name, runs)
	return response, true, nil
}

// graphDefinition resolves the revision that supplies the canvas: the promoted
// one when there is one, otherwise the latest imported one. Falling back to
// `live` when nothing is promoted would be a silent lie about what the page
// shows, so the fallback is reported rather than hidden.
func (handler *Handler) graphDefinition(name string) (*workflow.Definition, bool, bool, error) {
	definition, found, err := handler.definitions.Live(name)
	if err != nil {
		return nil, false, false, fmt.Errorf("resolving promoted workflow %q: %w", name, err)
	}
	if found && definition != nil {
		return definition, true, true, nil
	}
	definition, found, err = handler.definitions.Latest(name)
	if err != nil {
		return nil, false, false, fmt.Errorf("resolving latest workflow %q: %w", name, err)
	}
	if !found || definition == nil {
		return nil, false, false, nil
	}
	return definition, false, true, nil
}

// nodeAggregate holds one node's separately-reported facts. There is
// deliberately no field that fuses them.
type nodeAggregate struct {
	active         ActiveCounts
	history        HistoryCounts
	needsAttention bool
}

type aggregation struct {
	nodes map[string]*nodeAggregate
}

func (handler *Handler) aggregate(
	ctx context.Context,
	runs []db.AgentWorkflowRun,
) (aggregation, error) {
	result := aggregation{nodes: map[string]*nodeAggregate{}}

	byRun := make(map[int64][]occurrence.NodeOccurrence, len(runs))
	for _, run := range runs {
		occurrences, err := handler.occurrences.OccurrencesForRun(ctx, run)
		if err != nil {
			return aggregation{}, fmt.Errorf("reading occurrences for run %d: %w", run.ID, err)
		}
		byRun[int64(run.ID)] = occurrences

		active := !runIsTerminal(run.Status)
		for _, entry := range occurrences {
			node := result.node(entry.NodeID)
			switch {
			case entry.Status.Terminal():
				node.history.add(entry.Status)
			case active:
				// Only a run that is still executing has occurrences "right
				// now". A finished run's unreached nodes stay pending forever;
				// counting those would report work that will never happen.
				node.active.add(entry.Status)
			}
		}
	}

	for _, closure := range retryClosures(runs) {
		var entries []occurrence.ChainEntry
		for _, run := range closure {
			for _, entry := range byRun[int64(run.ID)] {
				entries = append(entries, occurrence.ChainEntry{
					RunID:        int64(run.ID),
					RunCreatedAt: run.CreatedAt,
					Occurrence:   entry,
				})
			}
		}
		for _, effective := range occurrence.ResolveEffective(entries) {
			if effective.NeedsAttention {
				result.node(effective.NodeID).needsAttention = true
			}
		}
	}

	return result, nil
}

func (result aggregation) node(nodeID string) *nodeAggregate {
	existing, found := result.nodes[nodeID]
	if !found {
		existing = &nodeAggregate{}
		result.nodes[nodeID] = existing
	}
	return existing
}

// nodeState emits one entry per canvas node so a node with no data is
// reportable as such rather than absent. When the graph could not be derived
// there is no canvas to key on, so the observed nodes are reported instead:
// node state keeps working even when the drawing does not.
func (result aggregation) nodeState(
	graphUnavailable bool,
	built graph.Graph,
	executionNodes map[string]string,
) []NodeState {
	var order []string
	if graphUnavailable {
		for nodeID := range result.nodes {
			order = append(order, nodeID)
		}
		sort.Strings(order)
	} else {
		for _, node := range built.Nodes {
			if _, execution := executionNodes[node.ID]; execution {
				order = append(order, node.ID)
			}
		}
	}

	states := make([]NodeState, 0, len(order))
	for _, nodeID := range order {
		entry := result.nodes[nodeID]
		if entry == nil {
			entry = &nodeAggregate{}
		}
		states = append(states, NodeState{
			NodeID:            nodeID,
			Active:            entry.active,
			History:           entry.history,
			NeedsAttention:    entry.needsAttention,
			HasWindowActivity: entry.active.total()+entry.history.total() > 0,
		})
	}
	return states
}

func (counts *ActiveCounts) add(status occurrence.Status) {
	switch status {
	case occurrence.StatusRunning:
		counts.Running++
	case occurrence.StatusWaiting:
		counts.Waiting++
	case occurrence.StatusPending:
		counts.Pending++
	}
}

func (counts *HistoryCounts) add(status occurrence.Status) {
	switch status {
	case occurrence.StatusSucceeded:
		counts.Succeeded++
	case occurrence.StatusFailed:
		counts.Failed++
	case occurrence.StatusErrored:
		counts.Errored++
	case occurrence.StatusAborted:
		counts.Aborted++
	case occurrence.StatusSkipped:
		counts.Skipped++
	}
}

// retryClosures partitions runs into retry-connected groups using
// RetryOfWorkflowRunID alone. A retry target outside the window still joins its
// retries into one closure, because the closure is an identity relation over
// run IDs rather than a subset of the fetched page.
func retryClosures(runs []db.AgentWorkflowRun) [][]db.AgentWorkflowRun {
	parent := map[int64]int64{}
	var find func(int64) int64
	find = func(id int64) int64 {
		root, known := parent[id]
		if !known {
			parent[id] = id
			return id
		}
		if root == id {
			return id
		}
		resolved := find(root)
		parent[id] = resolved
		return resolved
	}
	union := func(left, right int64) {
		leftRoot, rightRoot := find(left), find(right)
		if leftRoot == rightRoot {
			return
		}
		// Merge onto the smaller ID so the root is deterministic regardless of
		// the order runs arrive in.
		if leftRoot < rightRoot {
			parent[rightRoot] = leftRoot
		} else {
			parent[leftRoot] = rightRoot
		}
	}

	for _, run := range runs {
		find(int64(run.ID))
		if run.RetryOfWorkflowRunID != nil {
			union(int64(run.ID), int64(*run.RetryOfWorkflowRunID))
		}
	}

	var order []int64
	grouped := map[int64][]db.AgentWorkflowRun{}
	for _, run := range runs {
		root := find(int64(run.ID))
		if _, seen := grouped[root]; !seen {
			order = append(order, root)
		}
		grouped[root] = append(grouped[root], run)
	}
	closures := make([][]db.AgentWorkflowRun, 0, len(order))
	for _, root := range order {
		closures = append(closures, grouped[root])
	}
	return closures
}

// revisionBoundaries reports, per revision present in the window, the earliest
// run that executed it. The UI marks only the rows where the revision changes.
func (handler *Handler) revisionBoundaries(
	name string,
	runs []db.AgentWorkflowRun,
) []RevisionBoundary {
	earliest := map[int]db.AgentWorkflowRun{}
	for _, run := range runs {
		current, found := earliest[run.WorkflowVersion]
		if !found || run.CreatedAt.Before(current.CreatedAt) ||
			run.CreatedAt.Equal(current.CreatedAt) && run.ID < current.ID {
			earliest[run.WorkflowVersion] = run
		}
	}

	versions := make([]int, 0, len(earliest))
	for version := range earliest {
		versions = append(versions, version)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(versions)))

	boundaries := make([]RevisionBoundary, 0, len(versions))
	for _, version := range versions {
		run := earliest[version]
		boundary := RevisionBoundary{
			Version:    version,
			FirstRunID: run.ID.String(),
			FirstRunAt: run.CreatedAt,
		}
		// Promotion metadata is best-effort: a revision that has since been
		// removed still has runs, and losing its promotion timestamp must not
		// cost the boundary marker.
		if definition, found, err := handler.definitions.Get(name, version); err != nil {
			handler.logger.Error("load-revision-failed", err, lager.Data{
				"workflow": name, "version": version,
			})
		} else if found && definition != nil && definition.PromotedAt > 0 {
			promoted := time.Unix(definition.PromotedAt, 0).UTC()
			boundary.PromotedAt = &promoted
		}
		boundaries = append(boundaries, boundary)
	}
	return boundaries
}

func runIsTerminal(status db.AgentWorkflowRunStatus) bool {
	switch status {
	case db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

// parseRoute mirrors the run API: path identity is read through rata rather
// than FormValue, where a form or query value could shadow routed identity, and
// unsupported query fields are rejected instead of silently ignored.
func parseRoute(w http.ResponseWriter, r *http.Request) (string, url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
		return "", nil, false
	}
	for key, values := range query {
		if key != ":workflow_name" && key != "window" {
			writeError(w, http.StatusBadRequest, "invalid_query", "request query contains unsupported fields")
			return "", nil, false
		}
		if len(values) != 1 || values[0] == "" {
			writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
			return "", nil, false
		}
	}
	name := rata.Param(r, "workflow_name")
	if warning, err := atc.ValidateIdentifier(name); err != nil ||
		warning != nil && warning.Type == "invalid_identifier" {
		writeError(w, http.StatusBadRequest, "invalid_workflow", "workflow name is invalid")
		return "", nil, false
	}
	return name, query, true
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":"internal_error","message":"workflow overview service failed"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func interfaceIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
