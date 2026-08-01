package workflowruns_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun/occurrence"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const graphSeedWorkflow = "small-fix-v3"

// seedPromptText is authored inline in the seed's `review` step. Asserting its
// absence proves the response carries the structurally redacted graph.Graph
// rather than anything derived from the compiled function itself.
const seedPromptText = "Review and correct draft; write the final repository change and report only."

// graphSeedDefinition drives the real authoring pipeline so the shape under
// test is a workflow the rest of the system actually accepts. A synthetic
// FunctionConfig could not prove the graph-to-occurrence join holds.
func graphSeedDefinition(t *testing.T, version int) *workflow.Definition {
	t.Helper()
	manifest, err := workflow.ManifestFromDir(
		filepath.Join("..", "..", "workflow", "seeds", graphSeedWorkflow),
	)
	if err != nil {
		t.Fatalf("ManifestFromDir(%q): %v", graphSeedWorkflow, err)
	}
	compiled, err := workflow.CompileDefinition(manifest)
	if err != nil {
		t.Fatalf("CompileDefinition(%q): %v", graphSeedWorkflow, err)
	}
	return &workflow.Definition{
		ID: version, Name: graphSeedWorkflow, Version: version,
		SchemaVersion: 3, SignatureVersion: 1,
		ContentHash: fmt.Sprintf("content-hash-v%d", version),
		Compiled:    *compiled,
	}
}

type fakeDefinitionStore struct {
	byVersion map[int]*workflow.Definition
	err       error
	asked     []int
}

func (fake *fakeDefinitionStore) Get(_ string, version int) (*workflow.Definition, bool, error) {
	fake.asked = append(fake.asked, version)
	if fake.err != nil {
		return nil, false, fake.err
	}
	definition, found := fake.byVersion[version]
	return definition, found, nil
}

type fakeOccurrenceReader struct {
	byRun map[int64][]occurrence.NodeOccurrence
	err   error
}

func (fake *fakeOccurrenceReader) OccurrencesForRun(
	_ context.Context,
	run db.AgentWorkflowRun,
) ([]occurrence.NodeOccurrence, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	return fake.byRun[int64(run.ID)], nil
}

type graphDeps struct {
	handlerDeps
	definitions *fakeDefinitionStore
	occurrences *fakeOccurrenceReader
}

// graphDefaultDeps pins the run to revision 2 while revision 5 is the promoted
// one, so a handler that reached for the promoted revision would be visible
// rather than accidentally correct.
func graphDefaultDeps(t *testing.T) graphDeps {
	t.Helper()
	deps := graphDeps{handlerDeps: defaultDeps()}
	deps.definitions = &fakeDefinitionStore{byVersion: map[int]*workflow.Definition{
		2: graphSeedDefinition(t, 2),
		5: graphSeedDefinition(t, 5),
	}}
	deps.occurrences = &fakeOccurrenceReader{byRun: map[int64][]occurrence.NodeOccurrence{
		int64(exactLargeRunID): {
			{NodeID: "implement", NodeKind: "agent", Attempt: 1, RetryAttempt: 1,
				PlanID: "1/2", Status: occurrence.StatusSucceeded, DurationSeconds: 42, CostUSD: 1.25},
			{NodeID: "review", NodeKind: "agent", Attempt: 1, RetryAttempt: 1,
				PlanID: "1/3", Status: occurrence.StatusFailed},
		},
	}}
	runs := deps.runs
	runs.get = func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		if teamID != 1 {
			return db.AgentWorkflowRun{}, false, nil
		}
		run := runFixture(id, "small-fix", db.AgentWorkflowRunStatusFailed)
		run.WorkflowVersion = 2
		return run, true, nil
	}
	return deps
}

func graphHandler(t *testing.T, deps graphDeps) *workflowruns.Handler {
	t.Helper()
	handler, err := workflowruns.NewHandler(workflowruns.Config{
		Team:     workflowruns.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: deps.identity, Binder: deps.binder, Runs: deps.runs,
		Canceler: deps.canceler, Manifests: deps.manifests,
		Definitions: deps.definitions, Occurrences: deps.occurrences,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func graphRequest() *http.Request {
	return request(
		http.MethodGet,
		"/api/v1/agent/workflows/small-fix/runs/"+exactLargeRunID.String()+"/graph",
		"small-fix", exactLargeRunID.String(), nil, "",
	)
}

type graphResponseBody struct {
	WorkflowRunID    string `json:"workflow_run_id"`
	WorkflowName     string `json:"workflow_name"`
	WorkflowVersion  int    `json:"workflow_version"`
	GraphUnavailable bool   `json:"graph_unavailable"`
	Graph            struct {
		Nodes []map[string]any `json:"nodes"`
		Edges []map[string]any `json:"edges"`
	} `json:"graph"`
	Occurrences []map[string]any `json:"occurrences"`
}

func decodeGraphResponse(t *testing.T, recorder *httptest.ResponseRecorder) graphResponseBody {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response graphResponseBody
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return response
}

func TestRunGraphUsesTheRunsOwnRevision(t *testing.T) {
	deps := graphDefaultDeps(t)
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	response := decodeGraphResponse(t, recorder)
	if response.WorkflowVersion != 2 {
		t.Fatalf("workflow_version = %d, want the run's own revision 2", response.WorkflowVersion)
	}
	for _, asked := range deps.definitions.asked {
		if asked != 2 {
			t.Fatalf("asked for revision %d; the run page must never read the promoted one", asked)
		}
	}
	if len(response.Graph.Nodes) == 0 {
		t.Fatal("expected a graph")
	}
	if response.GraphUnavailable {
		t.Fatal("graph_unavailable = true for a workflow that builds")
	}
	if len(response.Occurrences) == 0 {
		t.Fatal("expected node occurrences for the run")
	}
	if response.WorkflowRunID != exactLargeRunID.String() {
		t.Fatalf("workflow_run_id = %q, want the exact durable ID as a string", response.WorkflowRunID)
	}
}

// A run ID exceeds the range JavaScript numbers represent exactly, so it must
// cross the wire quoted or the browser silently rounds it.
func TestRunGraphQuotesTheDurableRunID(t *testing.T) {
	deps := graphDefaultDeps(t)
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := `"workflow_run_id":"` + exactLargeRunID.String() + `"`
	if !strings.Contains(recorder.Body.String(), want) {
		t.Fatalf("body = %s, want %s", recorder.Body.String(), want)
	}
}

func TestRunGraphNeverLeaksPrompts(t *testing.T) {
	deps := graphDefaultDeps(t)
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, private := range []string{
		seedPromptText,
		"secret-a", "secret-b", "secret-c", "secret-d", "secret-e",
		"private_plan", "private_concrete", "private_parameterized",
	} {
		if strings.Contains(body, private) {
			t.Fatalf("the run graph response carried private detail %q: %s", private, body)
		}
	}
}

func TestRunGraphIsTeamScoped(t *testing.T) {
	deps := graphDefaultDeps(t)
	runs := deps.runs
	runs.get = func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		run := runFixture(id, "small-fix", db.AgentWorkflowRunStatusFailed)
		run.TeamID = 2
		run.TeamName = "other"
		run.WorkflowVersion = 2
		return run, true, nil
	}
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for another team's run", recorder.Code)
	}
}

func TestRunGraphIsWorkflowScoped(t *testing.T) {
	deps := graphDefaultDeps(t)
	runs := deps.runs
	runs.get = func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		run := runFixture(id, "some-other-workflow", db.AgentWorkflowRunStatusFailed)
		run.WorkflowVersion = 2
		return run, true, nil
	}
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a run belonging to another workflow", recorder.Code)
	}
}

func TestRunGraphIsMissingRunNotFound(t *testing.T) {
	deps := graphDefaultDeps(t)
	runs := deps.runs
	runs.get = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{}, false, nil
	}
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

// A step kind graph.Build does not recognise, or a revision that has since been
// removed, must not blank an otherwise usable page. The run and its node state
// still render; only the canvas is missing. This matches what the overview
// endpoint already does.
func TestRunGraphDegradesWhenTheGraphCannotBeDerived(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		prepare func(*fakeDefinitionStore)
	}{
		{"unrecognised step kind", func(store *fakeDefinitionStore) {
			broken := store.byVersion[2]
			broken.Compiled.Function.Plan = []atc.Step{
				{Config: &atc.SetPipelineStep{Name: "unrecognised"}},
			}
		}},
		{"revision no longer stored", func(store *fakeDefinitionStore) {
			delete(store.byVersion, 2)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			deps := graphDefaultDeps(t)
			testCase.prepare(deps.definitions)
			recorder := httptest.NewRecorder()
			graphHandler(t, deps).Graph(recorder, graphRequest())

			response := decodeGraphResponse(t, recorder)
			if !response.GraphUnavailable {
				t.Fatal("graph_unavailable = false, want a degraded page")
			}
			if response.Graph.Nodes == nil || response.Graph.Edges == nil {
				t.Fatal("the degraded graph must still decode as an empty graph, not null")
			}
			if len(response.Occurrences) == 0 {
				t.Fatal("the run's node state must survive a missing canvas")
			}
			if response.WorkflowVersion != 2 {
				t.Fatalf("workflow_version = %d, want 2", response.WorkflowVersion)
			}
		})
	}
}

func TestRunGraphFailsClosedOnDependencyErrors(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		prepare func(*graphDeps)
	}{
		{"run store", func(deps *graphDeps) {
			deps.runs.get = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
				return db.AgentWorkflowRun{}, false, errors.New("postgres secret-password")
			}
		}},
		{"definition store", func(deps *graphDeps) {
			deps.definitions.err = errors.New("postgres secret-password")
		}},
		{"occurrence reader", func(deps *graphDeps) {
			deps.occurrences.err = errors.New("postgres secret-password")
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			deps := graphDefaultDeps(t)
			testCase.prepare(&deps)
			recorder := httptest.NewRecorder()
			graphHandler(t, deps).Graph(recorder, graphRequest())

			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", recorder.Code)
			}
			if strings.Contains(recorder.Body.String(), "secret-password") {
				t.Fatalf("dependency error leaked: %s", recorder.Body.String())
			}
		})
	}
}

// Exactly one of running, waiting, failed, or succeeded is non-zero per node is
// an Elm-side property, but it only holds if the server reports one status per
// occurrence and nothing else. This pins the projection's shape.
func TestRunGraphPresentsOneStatusPerOccurrence(t *testing.T) {
	deps := graphDefaultDeps(t)
	recorder := httptest.NewRecorder()
	graphHandler(t, deps).Graph(recorder, graphRequest())

	response := decodeGraphResponse(t, recorder)
	if len(response.Occurrences) != 2 {
		t.Fatalf("occurrences = %d, want 2", len(response.Occurrences))
	}
	byNode := map[string]map[string]any{}
	for _, entry := range response.Occurrences {
		nodeID, ok := entry["node_id"].(string)
		if !ok {
			t.Fatalf("occurrence has no node_id: %v", entry)
		}
		byNode[nodeID] = entry
	}
	implement, found := byNode["implement"]
	if !found {
		t.Fatalf("no occurrence for implement: %v", response.Occurrences)
	}
	if implement["status"] != "succeeded" {
		t.Fatalf("implement status = %v, want succeeded", implement["status"])
	}
	if implement["cost_usd"] != 1.25 {
		t.Fatalf("implement cost_usd = %v, want 1.25", implement["cost_usd"])
	}
	if implement["duration_seconds"] != float64(42) {
		t.Fatalf("implement duration_seconds = %v, want 42", implement["duration_seconds"])
	}
	if byNode["review"]["status"] != "failed" {
		t.Fatalf("review status = %v, want failed", byNode["review"]["status"])
	}
}

func TestRunGraphRejectsAnUnsupportedMethodAndQuery(t *testing.T) {
	deps := graphDefaultDeps(t)
	handler := graphHandler(t, deps)

	recorder := httptest.NewRecorder()
	handler.Graph(recorder, request(
		http.MethodPost, "/api/v1/agent/workflows/small-fix/runs/1/graph",
		"small-fix", exactLargeRunID.String(), nil, "",
	))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.Graph(recorder, request(
		http.MethodGet, "/api/v1/agent/workflows/small-fix/runs/1/graph",
		"small-fix", exactLargeRunID.String(),
		map[string][]string{"window": {"7d"}}, "",
	))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unsupported query field", recorder.Code)
	}
}

func TestNewHandlerRequiresTheGraphDependencies(t *testing.T) {
	base := defaultDeps()
	definitions := &fakeDefinitionStore{}
	occurrences := &fakeOccurrenceReader{}
	for name, config := range map[string]workflowruns.Config{
		"definitions": {
			Team: workflowruns.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
			Identity: base.identity, Binder: base.binder, Runs: base.runs,
			Canceler: base.canceler, Manifests: base.manifests, Occurrences: occurrences,
		},
		"occurrences": {
			Team: workflowruns.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
			Identity: base.identity, Binder: base.binder, Runs: base.runs,
			Canceler: base.canceler, Manifests: base.manifests, Definitions: definitions,
		},
	} {
		if _, err := workflowruns.NewHandler(config); err == nil {
			t.Fatalf("a handler with no %s was accepted", name)
		}
	}
}
