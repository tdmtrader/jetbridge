package workflowoverview_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoverview"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const seedWorkflow = "small-fix-v3"

var referenceNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// compileSeed drives the real authoring pipeline so every fixture here is a
// workflow the rest of the system actually accepts. A synthetic FunctionConfig
// could not prove the graph-to-occurrence join holds.
func compileSeed(t *testing.T, name string) *workflow.CompiledDefinition {
	t.Helper()
	manifest, err := workflow.ManifestFromDir(
		filepath.Join("..", "..", "workflow", "seeds", name),
	)
	if err != nil {
		t.Fatalf("ManifestFromDir(%q): %v", name, err)
	}
	compiled, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition(%q): %v", name, err)
	}
	return compiled
}

func seedDefinition(t *testing.T, version int, live bool) *workflow.Definition {
	t.Helper()
	compiled := compileSeed(t, seedWorkflow)
	return &workflow.Definition{
		ID: version, Name: seedWorkflow, Version: version,
		SchemaVersion: 3, SignatureVersion: 1,
		ContentHash: fmt.Sprintf("content-hash-v%d", version),
		Live:        live, Compiled: *compiled,
	}
}

type fakeDefinitions struct {
	live      *workflow.Definition
	latest    *workflow.Definition
	byVersion map[int]*workflow.Definition
	liveErr   error
}

func (fake *fakeDefinitions) Live(string) (*workflow.Definition, bool, error) {
	if fake.liveErr != nil {
		return nil, false, fake.liveErr
	}
	return fake.live, fake.live != nil, nil
}

func (fake *fakeDefinitions) Latest(string) (*workflow.Definition, bool, error) {
	return fake.latest, fake.latest != nil, nil
}

func (fake *fakeDefinitions) Get(_ string, version int) (*workflow.Definition, bool, error) {
	found, present := fake.byVersion[version]
	return found, present, nil
}

type fakeRuns struct {
	runs       []db.AgentWorkflowRun
	lastFilter db.AgentWorkflowRunListFilter
	err        error
}

func (fake *fakeRuns) List(_ context.Context, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
	fake.lastFilter = filter
	return fake.runs, fake.err
}

type fakeOccurrences struct {
	byRun map[int64][]occurrence.NodeOccurrence
	err   error
}

func (fake *fakeOccurrences) OccurrencesForRun(_ context.Context, run db.AgentWorkflowRun) ([]occurrence.NodeOccurrence, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.byRun[int64(run.ID)], nil
}

type deps struct {
	definitions *fakeDefinitions
	runs        *fakeRuns
	occurrences *fakeOccurrences
}

func defaultDeps(t *testing.T) deps {
	t.Helper()
	promoted := seedDefinition(t, 4, true)
	return deps{
		definitions: &fakeDefinitions{
			live: promoted, latest: promoted,
			byVersion: map[int]*workflow.Definition{4: promoted},
		},
		runs:        &fakeRuns{},
		occurrences: &fakeOccurrences{byRun: map[int64][]occurrence.NodeOccurrence{}},
	}
}

func newHandler(t *testing.T, dependencies deps) *workflowoverview.Handler {
	t.Helper()
	handler, err := workflowoverview.NewHandler(workflowoverview.Config{
		Team:        workflowoverview.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Definitions: dependencies.definitions,
		Runs:        dependencies.runs,
		Occurrences: dependencies.occurrences,
		Now:         func() time.Time { return referenceNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

// overviewRequest builds a request the way rata routes one: the path parameter
// travels in the URL query, not the form, because the handler deliberately
// reads routed identity through rata rather than FormValue.
func overviewRequest(workflowName string, query url.Values) *http.Request {
	values := make(url.Values, len(query)+1)
	for key, existing := range query {
		values[key] = append([]string(nil), existing...)
	}
	values.Set(":workflow_name", workflowName)
	return httptest.NewRequest(http.MethodGet, "/?"+values.Encode(), nil)
}

func doOverview(t *testing.T, handler *workflowoverview.Handler, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.Overview(recorder, overviewRequest(seedWorkflow, query))
	return recorder
}

func decodeOK(t *testing.T, recorder *httptest.ResponseRecorder) workflowoverview.Response {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}
	var response workflowoverview.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return response
}

func runFixture(id int64, version int, status db.AgentWorkflowRunStatus, createdAt time.Time) db.AgentWorkflowRun {
	run := db.AgentWorkflowRun{
		ID: snapshot.WorkflowRunID(id), TeamID: 1, TeamName: atc.DefaultTeamName,
		WorkflowName: seedWorkflow, WorkflowVersion: version, WorkflowDefinitionID: version,
		Status: status, OriginKind: "manual", CreatedAt: createdAt,
	}
	switch status {
	case db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		completed := createdAt.Add(time.Minute)
		run.CompletedAt = &completed
	}
	return run
}

func occurrenceFixture(runID int64, nodeID string, status occurrence.Status) occurrence.NodeOccurrence {
	return occurrence.NodeOccurrence{
		WorkflowRunID: snapshot.WorkflowRunID(runID), TeamID: 1,
		WorkflowName: seedWorkflow, WorkflowVersion: 4, WorkflowDefinitionID: 4,
		NodeID: nodeID, NodeKind: "agent", RetryAttempt: 1, Attempt: 1,
		PlanID: fmt.Sprintf("%d/%s", runID, nodeID), Status: status,
	}
}

func stateFor(t *testing.T, response workflowoverview.Response, nodeID string) workflowoverview.NodeState {
	t.Helper()
	for _, state := range response.NodeState {
		if state.NodeID == nodeID {
			return state
		}
	}
	t.Fatalf("no node state for %q; got %+v", nodeID, response.NodeState)
	return workflowoverview.NodeState{}
}

func TestOverviewLabelsItsWindowExplicitly(t *testing.T) {
	handler := newHandler(t, defaultDeps(t))

	response := decodeOK(t, doOverview(t, handler, url.Values{"window": {"7d"}}))

	if response.Window.Kind != "7d" {
		t.Fatalf("expected the window kind to be echoed, got %q", response.Window.Kind)
	}
	if !response.Window.IncludesActiveBeforeWindow {
		t.Fatal("the response must state that active runs bypass the window")
	}
	if response.Window.To.Sub(response.Window.From) != 7*24*time.Hour {
		t.Fatalf("expected a seven-day window, got %s", response.Window.To.Sub(response.Window.From))
	}
	if !response.Window.To.Equal(referenceNow) {
		t.Fatalf("expected the window to end now, got %s", response.Window.To)
	}
}

func TestOverviewDefaultsToSevenDays(t *testing.T) {
	handler := newHandler(t, defaultDeps(t))

	response := decodeOK(t, doOverview(t, handler, nil))

	if response.Window.Kind != "7d" {
		t.Fatalf("expected the default window to be 7d, got %q", response.Window.Kind)
	}
	if response.Window.To.Sub(response.Window.From) != 7*24*time.Hour {
		t.Fatalf("expected a seven-day default span, got %s", response.Window.To.Sub(response.Window.From))
	}
}

func TestOverviewSupportsEveryDeclaredWindow(t *testing.T) {
	for kind, want := range map[string]time.Duration{
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
	} {
		handler := newHandler(t, defaultDeps(t))
		response := decodeOK(t, doOverview(t, handler, url.Values{"window": {kind}}))
		if response.Window.Kind != kind {
			t.Fatalf("window %q: echoed %q", kind, response.Window.Kind)
		}
		if got := response.Window.To.Sub(response.Window.From); got != want {
			t.Fatalf("window %q: span %s, want %s", kind, got, want)
		}
	}
}

func TestOverviewRejectsUnknownWindow(t *testing.T) {
	handler := newHandler(t, defaultDeps(t))

	recorder := doOverview(t, handler, url.Values{"window": {"90d"}})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported window, got %d", recorder.Code)
	}
}

func TestOverviewRejectsUnsupportedQueryFields(t *testing.T) {
	handler := newHandler(t, defaultDeps(t))

	recorder := doOverview(t, handler, url.Values{"scope": {"all"}})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unsupported query field, got %d", recorder.Code)
	}
}

// The window is a bound on the query the store runs, not a label. Echoing it
// while asking for something else would be the most convincing possible lie.
func TestOverviewWindowBoundsTheRunQueryAndUnionsActiveRuns(t *testing.T) {
	dependencies := defaultDeps(t)
	handler := newHandler(t, dependencies)

	decodeOK(t, doOverview(t, handler, url.Values{"window": {"24h"}}))

	filter := dependencies.runs.lastFilter
	if filter.CompletedSince == nil {
		t.Fatal("expected the window to bound completed history")
	}
	if want := referenceNow.Add(-24 * time.Hour); !filter.CompletedSince.Equal(want) {
		t.Fatalf("completed-since = %s, want %s", filter.CompletedSince, want)
	}
	if !filter.IncludeActiveRuns {
		t.Fatal("active runs must be unioned in regardless of age")
	}
	if filter.WorkflowName != seedWorkflow || filter.TeamID != 1 {
		t.Fatalf("unexpected run scope: %+v", filter)
	}
}

func TestOverviewExcludesExperimentsByDefault(t *testing.T) {
	dependencies := defaultDeps(t)
	handler := newHandler(t, dependencies)

	decodeOK(t, doOverview(t, handler, nil))

	if dependencies.runs.lastFilter.Scope != db.AgentWorkflowRunScopeOperational {
		t.Fatalf("expected the operational scope, got %q", dependencies.runs.lastFilter.Scope)
	}
}

// A long-running run created before the window is exactly the run the page
// exists to show. It must reach the aggregate, not be filtered out by age.
func TestOverviewCountsAnActiveRunCreatedBeforeTheWindow(t *testing.T) {
	dependencies := defaultDeps(t)
	old := runFixture(11, 4, db.AgentWorkflowRunStatusRunning, referenceNow.Add(-30*24*time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{old}
	dependencies.occurrences.byRun[11] = []occurrence.NodeOccurrence{
		occurrenceFixture(11, "implement", occurrence.StatusRunning),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, url.Values{"window": {"24h"}}))

	implement := stateFor(t, response, "implement")
	if implement.Active.Running != 1 {
		t.Fatalf("expected the pre-window active run to be counted, got %+v", implement.Active)
	}
	if !implement.NeedsAttention {
		t.Fatal("a running node needs attention")
	}
	if !implement.HasWindowActivity {
		t.Fatal("an active run is activity")
	}
}

func TestOverviewNeverEmitsOneAggregateStatus(t *testing.T) {
	dependencies := defaultDeps(t)
	running := runFixture(21, 4, db.AgentWorkflowRunStatusRunning, referenceNow.Add(-time.Hour))
	failed := runFixture(22, 4, db.AgentWorkflowRunStatusFailed, referenceNow.Add(-2*time.Hour))
	succeeded := runFixture(23, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-3*time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{running, failed, succeeded}
	dependencies.occurrences.byRun[21] = []occurrence.NodeOccurrence{
		occurrenceFixture(21, "implement", occurrence.StatusRunning),
	}
	dependencies.occurrences.byRun[22] = []occurrence.NodeOccurrence{
		occurrenceFixture(22, "implement", occurrence.StatusFailed),
	}
	dependencies.occurrences.byRun[23] = []occurrence.NodeOccurrence{
		occurrenceFixture(23, "implement", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	recorder := doOverview(t, handler, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body)
	}

	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	states, _ := raw["node_state"].([]any)
	if len(states) == 0 {
		t.Fatal("expected node state entries")
	}
	for _, entry := range states {
		object := entry.(map[string]any)
		if _, present := object["status"]; present {
			t.Fatal("node state must not collapse mixed concurrent runs into one status field")
		}
		if _, present := object["active"]; !present {
			t.Fatal("node state must report active counts")
		}
		if _, present := object["history"]; !present {
			t.Fatal("node state must report windowed history counts")
		}
	}

	response := decodeOK(t, recorder)
	implement := stateFor(t, response, "implement")
	if implement.Active.Running != 1 {
		t.Fatalf("active running = %d, want 1", implement.Active.Running)
	}
	if implement.History.Failed != 1 || implement.History.Succeeded != 1 {
		t.Fatalf("history = %+v, want one failure and one success", implement.History)
	}
}

func TestOverviewReportsNoWindowActivityDistinctlyFromSuccess(t *testing.T) {
	dependencies := defaultDeps(t)
	run := runFixture(31, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[31] = []occurrence.NodeOccurrence{
		occurrenceFixture(31, "implement", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if !stateFor(t, response, "implement").HasWindowActivity {
		t.Fatal("a node with a succeeded occurrence has window activity")
	}
	untouched := stateFor(t, response, "review")
	if untouched.HasWindowActivity {
		t.Fatal("a node with no occurrence must report no window activity")
	}
	if untouched.History.Succeeded != 0 || untouched.Active.Running != 0 {
		t.Fatalf("a no-data node must not carry counts, got %+v / %+v", untouched.Active, untouched.History)
	}
	if untouched.NeedsAttention {
		t.Fatal("a no-data node is not attention-worthy")
	}
}

// A pending occurrence on a finished run is a node the run never reached. It
// is frozen pending forever; counting it as active would report permanent
// phantom work on every workflow that ever took a branch.
func TestOverviewDoesNotCountUnreachedNodesOfFinishedRunsAsActive(t *testing.T) {
	dependencies := defaultDeps(t)
	run := runFixture(41, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[41] = []occurrence.NodeOccurrence{
		occurrenceFixture(41, "implement", occurrence.StatusSucceeded),
		occurrenceFixture(41, "review", occurrence.StatusPending),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	review := stateFor(t, response, "review")
	if review.Active.Pending != 0 {
		t.Fatalf("expected no active pending on a finished run, got %+v", review.Active)
	}
	if review.HasWindowActivity {
		t.Fatal("an unreached node of a finished run is no-data, not activity")
	}
}

func TestOverviewCountsPendingOfAStillExecutingRunAsActive(t *testing.T) {
	dependencies := defaultDeps(t)
	run := runFixture(42, 4, db.AgentWorkflowRunStatusRunning, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[42] = []occurrence.NodeOccurrence{
		occurrenceFixture(42, "review", occurrence.StatusPending),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	review := stateFor(t, response, "review")
	if review.Active.Pending != 1 {
		t.Fatalf("expected one active pending, got %+v", review.Active)
	}
	if review.NeedsAttention {
		t.Fatal("pending is not itself a call to action")
	}
}

// Retry resolution is the difference between an honest attention view and one
// that stays red after the problem is fixed.
func TestOverviewRetrySuccessClearsAttentionButKeepsHistory(t *testing.T) {
	dependencies := defaultDeps(t)
	original := runFixture(51, 4, db.AgentWorkflowRunStatusFailed, referenceNow.Add(-3*time.Hour))
	retryOf := original.ID
	retry := runFixture(52, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	retry.RetryOfWorkflowRunID = &retryOf
	retry.OriginKind = "retry"
	dependencies.runs.runs = []db.AgentWorkflowRun{retry, original}
	dependencies.occurrences.byRun[51] = []occurrence.NodeOccurrence{
		occurrenceFixture(51, "implement", occurrence.StatusFailed),
	}
	dependencies.occurrences.byRun[52] = []occurrence.NodeOccurrence{
		occurrenceFixture(52, "implement", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	implement := stateFor(t, response, "implement")
	if implement.NeedsAttention {
		t.Fatal("a successful retry resolves the earlier failure")
	}
	if implement.History.Failed != 1 {
		t.Fatalf("the failure must remain in history, got %+v", implement.History)
	}
	if implement.History.Succeeded != 1 {
		t.Fatalf("the retry's success must be in history, got %+v", implement.History)
	}
}

func TestOverviewUnresolvedFailureNeedsAttention(t *testing.T) {
	dependencies := defaultDeps(t)
	run := runFixture(61, 4, db.AgentWorkflowRunStatusFailed, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[61] = []occurrence.NodeOccurrence{
		occurrenceFixture(61, "implement", occurrence.StatusFailed),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if !stateFor(t, response, "implement").NeedsAttention {
		t.Fatal("a failure with no successful continuation needs attention")
	}
}

// Two unrelated runs must not resolve each other. Without a closure boundary a
// success anywhere in the window would silence every failure of that node.
func TestOverviewUnrelatedSuccessDoesNotResolveAnotherRunsFailure(t *testing.T) {
	dependencies := defaultDeps(t)
	failed := runFixture(71, 4, db.AgentWorkflowRunStatusFailed, referenceNow.Add(-3*time.Hour))
	unrelated := runFixture(72, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{unrelated, failed}
	dependencies.occurrences.byRun[71] = []occurrence.NodeOccurrence{
		occurrenceFixture(71, "implement", occurrence.StatusFailed),
	}
	dependencies.occurrences.byRun[72] = []occurrence.NodeOccurrence{
		occurrenceFixture(72, "implement", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if !stateFor(t, response, "implement").NeedsAttention {
		t.Fatal("an unrelated run's success must not clear a separate failure")
	}
}

// Two retries of the same source belong to one closure even when the source
// itself fell outside the window. The closure is an identity relation over run
// IDs, not a subset of the fetched page.
func TestOverviewJoinsRetriesOfASourceOutsideTheWindow(t *testing.T) {
	dependencies := defaultDeps(t)
	absentSource := snapshot.WorkflowRunID(300)
	failedRetry := runFixture(301, 4, db.AgentWorkflowRunStatusFailed, referenceNow.Add(-3*time.Hour))
	failedRetry.RetryOfWorkflowRunID = &absentSource
	succeededRetry := runFixture(302, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	succeededRetry.RetryOfWorkflowRunID = &absentSource
	dependencies.runs.runs = []db.AgentWorkflowRun{succeededRetry, failedRetry}
	dependencies.occurrences.byRun[301] = []occurrence.NodeOccurrence{
		occurrenceFixture(301, "implement", occurrence.StatusFailed),
	}
	dependencies.occurrences.byRun[302] = []occurrence.NodeOccurrence{
		occurrenceFixture(302, "implement", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	implement := stateFor(t, response, "implement")
	if implement.NeedsAttention {
		t.Fatal("sibling retries of one source resolve each other")
	}
	if implement.History.Failed != 1 || implement.History.Succeeded != 1 {
		t.Fatalf("both outcomes stay in history, got %+v", implement.History)
	}
}

func TestOverviewUnpromotedWorkflowReportsLatestVersion(t *testing.T) {
	dependencies := defaultDeps(t)
	latest := seedDefinition(t, 9, false)
	dependencies.definitions.live = nil
	dependencies.definitions.latest = latest
	dependencies.definitions.byVersion = map[int]*workflow.Definition{9: latest}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if response.Workflow.HasPromotedVersion {
		t.Fatal("expected the workflow to report no promoted version")
	}
	if response.Workflow.GraphVersion != 9 {
		t.Fatalf("expected the latest imported version to supply the graph, got %d", response.Workflow.GraphVersion)
	}
	if len(response.Graph.Nodes) == 0 {
		t.Fatal("an unpromoted workflow must still render a graph")
	}
	if response.GraphUnavailable {
		t.Fatal("the graph is available")
	}
}

func TestOverviewPrefersThePromotedVersionOverTheLatest(t *testing.T) {
	dependencies := defaultDeps(t)
	promoted := seedDefinition(t, 4, true)
	latest := seedDefinition(t, 9, false)
	dependencies.definitions.live = promoted
	dependencies.definitions.latest = latest
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if !response.Workflow.HasPromotedVersion {
		t.Fatal("expected a promoted version")
	}
	if response.Workflow.GraphVersion != 4 {
		t.Fatalf("expected the promoted version to supply the graph, got %d", response.Workflow.GraphVersion)
	}
}

func TestOverviewMissingWorkflowIsNotFound(t *testing.T) {
	dependencies := defaultDeps(t)
	dependencies.definitions.live = nil
	dependencies.definitions.latest = nil
	handler := newHandler(t, dependencies)

	recorder := doOverview(t, handler, nil)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a workflow with no versions, got %d", recorder.Code)
	}
}

// One step kind graph.Build does not recognise must not blank an otherwise
// usable page.
func TestOverviewDegradesWhenTheGraphCannotBeDerived(t *testing.T) {
	dependencies := defaultDeps(t)
	broken := seedDefinition(t, 4, true)
	broken.Compiled.Function.Plan = []atc.Step{{Config: &atc.SetPipelineStep{Name: "unrecognised"}}}
	dependencies.definitions.live = broken
	run := runFixture(81, 4, db.AgentWorkflowRunStatusFailed, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[81] = []occurrence.NodeOccurrence{
		occurrenceFixture(81, "implement", occurrence.StatusFailed),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if !response.GraphUnavailable {
		t.Fatal("expected the response to say the graph is unavailable")
	}
	if response.Graph.Nodes == nil || response.Graph.Edges == nil {
		t.Fatal("the degraded graph must still decode as empty lists, never null")
	}
	if len(response.Graph.Nodes) != 0 {
		t.Fatalf("expected no nodes, got %d", len(response.Graph.Nodes))
	}
	if response.Workflow.GraphVersion != 4 {
		t.Fatal("run data must survive a graph failure")
	}
	implement := stateFor(t, response, "implement")
	if implement.History.Failed != 1 {
		t.Fatalf("node state must keep working without a canvas, got %+v", implement.History)
	}
	if response.HasHistoricalOnlyNodes {
		t.Fatal("an underivable graph cannot classify a node as historical-only")
	}
}

func TestOverviewFlagsNodesThePromotedGraphNoLongerContains(t *testing.T) {
	dependencies := defaultDeps(t)
	run := runFixture(91, 3, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[91] = []occurrence.NodeOccurrence{
		occurrenceFixture(91, "implement", occurrence.StatusSucceeded),
		occurrenceFixture(91, "retired-node", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if !response.HasHistoricalOnlyNodes {
		t.Fatal("expected the retired node to be reported")
	}
	for _, state := range response.NodeState {
		if state.NodeID == "retired-node" {
			t.Fatal("a node outside the promoted graph must not join the canvas")
		}
	}
}

func TestOverviewDoesNotFlagHistoricalOnlyNodesWhenAllAreCurrent(t *testing.T) {
	dependencies := defaultDeps(t)
	run := runFixture(92, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour))
	dependencies.runs.runs = []db.AgentWorkflowRun{run}
	dependencies.occurrences.byRun[92] = []occurrence.NodeOccurrence{
		occurrenceFixture(92, "implement", occurrence.StatusSucceeded),
		occurrenceFixture(92, "approval", occurrence.StatusSucceeded),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if response.HasHistoricalOnlyNodes {
		t.Fatal("every observed node is in the promoted graph")
	}
}

// Endpoint nodes never carry occurrences, so they must never appear as node
// state — the join would be a guess rather than exact.
func TestOverviewNodeStateCoversExactlyTheExecutionNodes(t *testing.T) {
	handler := newHandler(t, defaultDeps(t))

	response := decodeOK(t, doOverview(t, handler, nil))

	got := map[string]bool{}
	for _, state := range response.NodeState {
		got[state.NodeID] = true
	}
	for _, want := range []string{"implement", "review", "dev-validation-repository-gates", "prepare-question", "approval"} {
		if !got[want] {
			t.Fatalf("expected node state for %q, got %v", want, got)
		}
	}
	for nodeID := range got {
		if nodeID == "input:repository" || nodeID == "output:change" {
			t.Fatalf("endpoint node %q must not carry node state", nodeID)
		}
	}
	if len(got) != 5 {
		t.Fatalf("expected exactly the five execution nodes, got %v", got)
	}
}

func TestOverviewReportsRevisionBoundaries(t *testing.T) {
	dependencies := defaultDeps(t)
	promotedAt := referenceNow.Add(-10 * 24 * time.Hour)
	fourth := seedDefinition(t, 4, true)
	fourth.PromotedAt = promotedAt.Unix()
	third := seedDefinition(t, 3, false)
	dependencies.definitions.live = fourth
	dependencies.definitions.byVersion = map[int]*workflow.Definition{3: third, 4: fourth}
	dependencies.runs.runs = []db.AgentWorkflowRun{
		runFixture(103, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour)),
		runFixture(102, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-2*time.Hour)),
		runFixture(101, 3, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-5*time.Hour)),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if len(response.Revisions) != 2 {
		t.Fatalf("expected two revision boundaries, got %+v", response.Revisions)
	}
	if response.Revisions[0].Version != 4 || response.Revisions[1].Version != 3 {
		t.Fatalf("expected newest revision first, got %+v", response.Revisions)
	}
	if response.Revisions[0].FirstRunID != "102" {
		t.Fatalf("expected the earliest run at version 4, got %q", response.Revisions[0].FirstRunID)
	}
	if !response.Revisions[0].FirstRunAt.Equal(referenceNow.Add(-2 * time.Hour)) {
		t.Fatalf("unexpected boundary time %s", response.Revisions[0].FirstRunAt)
	}
	if response.Revisions[0].PromotedAt == nil || !response.Revisions[0].PromotedAt.Equal(promotedAt.Truncate(time.Second)) {
		t.Fatalf("expected the promotion timestamp, got %v", response.Revisions[0].PromotedAt)
	}
	if response.Revisions[1].PromotedAt != nil {
		t.Fatal("an unpromoted revision has no promotion timestamp")
	}
}

// Equal creation timestamps are ordinary: runs admitted in the same batch
// share one clock tick. Without the ID tiebreak the boundary row is whichever
// one the store happened to return first.
func TestOverviewRevisionBoundaryBreaksTiesByRunID(t *testing.T) {
	dependencies := defaultDeps(t)
	sameInstant := referenceNow.Add(-time.Hour)
	dependencies.runs.runs = []db.AgentWorkflowRun{
		runFixture(205, 4, db.AgentWorkflowRunStatusSucceeded, sameInstant),
		runFixture(204, 4, db.AgentWorkflowRunStatusSucceeded, sameInstant),
		runFixture(206, 4, db.AgentWorkflowRunStatusSucceeded, sameInstant),
	}
	handler := newHandler(t, dependencies)

	response := decodeOK(t, doOverview(t, handler, nil))

	if len(response.Revisions) != 1 {
		t.Fatalf("expected one revision boundary, got %+v", response.Revisions)
	}
	if response.Revisions[0].FirstRunID != "204" {
		t.Fatalf("expected the lowest run ID to break the tie, got %q", response.Revisions[0].FirstRunID)
	}
}

// The aggregation is bounded, and the bound must be explicit rather than
// whatever the store defaults to.
func TestOverviewAsksForABoundedRunPopulation(t *testing.T) {
	dependencies := defaultDeps(t)
	handler := newHandler(t, dependencies)

	decodeOK(t, doOverview(t, handler, nil))

	if dependencies.runs.lastFilter.Limit != 1000 {
		t.Fatalf("expected an explicit aggregation bound, got limit %d", dependencies.runs.lastFilter.Limit)
	}
}

func TestOverviewRunIDsAreStringsSoLargeIdentitiesSurvive(t *testing.T) {
	dependencies := defaultDeps(t)
	dependencies.runs.runs = []db.AgentWorkflowRun{
		runFixture(9007199254740993, 4, db.AgentWorkflowRunStatusSucceeded, referenceNow.Add(-time.Hour)),
	}
	handler := newHandler(t, dependencies)

	recorder := doOverview(t, handler, nil)
	var raw map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	boundaries := raw["revision_boundaries"].([]any)
	first := boundaries[0].(map[string]any)
	if first["first_run_id"] != "9007199254740993" {
		t.Fatalf("expected an exact quoted run ID, got %v", first["first_run_id"])
	}
}

func TestOverviewPropagatesStoreFailuresAsInternalErrors(t *testing.T) {
	for name, mutate := range map[string]func(deps){
		"definitions": func(d deps) { d.definitions.liveErr = errors.New("boom") },
		"runs":        func(d deps) { d.runs.err = errors.New("boom") },
		"occurrences": func(d deps) {
			d.runs.runs = []db.AgentWorkflowRun{
				runFixture(1, 4, db.AgentWorkflowRunStatusRunning, referenceNow),
			}
			d.occurrences.err = errors.New("boom")
		},
	} {
		dependencies := defaultDeps(t)
		mutate(dependencies)
		handler := newHandler(t, dependencies)

		recorder := doOverview(t, handler, nil)

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("%s: expected 500, got %d", name, recorder.Code)
		}
	}
}

func TestOverviewRejectsNonGetMethods(t *testing.T) {
	handler := newHandler(t, defaultDeps(t))
	recorder := httptest.NewRecorder()
	request := overviewRequest(seedWorkflow, nil)
	request.Method = http.MethodPost

	handler.Overview(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", recorder.Code)
	}
}

func TestNewHandlerRequiresItsDependencies(t *testing.T) {
	base := defaultDeps(t)
	for name, config := range map[string]workflowoverview.Config{
		"team": {
			Definitions: base.definitions, Runs: base.runs, Occurrences: base.occurrences,
		},
		"definitions": {
			Team: workflowoverview.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
			Runs: base.runs, Occurrences: base.occurrences,
		},
		"runs": {
			Team:        workflowoverview.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
			Definitions: base.definitions, Occurrences: base.occurrences,
		},
		"occurrences": {
			Team:        workflowoverview.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
			Definitions: base.definitions, Runs: base.runs,
		},
	} {
		if _, err := workflowoverview.NewHandler(config); err == nil {
			t.Fatalf("%s: expected a construction error", name)
		}
	}
}
