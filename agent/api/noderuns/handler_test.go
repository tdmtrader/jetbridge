package noderuns_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/api/noderuns"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const exactNodeRunID snapshot.WorkflowRunID = 9007199254740993

type fakeBinder struct {
	bind func(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error)
}

func (fake *fakeBinder) BindAndCreate(ctx context.Context, admission workflowrun.AdmissionContext, request workflowrun.BindRequest) (workflowrun.BindResult, error) {
	return fake.bind(ctx, admission, request)
}

type fakeRunStore struct {
	get       func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	list      func(context.Context, workflow.DefinitionKind, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error)
	snapshots func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
}

func (fake *fakeRunStore) GetKind(ctx context.Context, teamID int, kind workflow.DefinitionKind, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return fake.get(ctx, teamID, kind, id)
}

func (fake *fakeRunStore) ListKind(ctx context.Context, kind workflow.DefinitionKind, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
	return fake.list(ctx, kind, filter)
}

func (fake *fakeRunStore) Snapshots(ctx context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	return fake.snapshots(ctx, id)
}

type fakeManifestStore struct{}

func (fakeManifestStore) GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	return snapshot.Snapshot{}, false, nil
}

func TestCreateBindsExactNodeVersionAndOnlyCallerInputsParametersAndIdempotencyKey(t *testing.T) {
	var received workflowrun.BindRequest
	deps := defaultDependencies()
	deps.binder.bind = func(_ context.Context, admission workflowrun.AdmissionContext, request workflowrun.BindRequest) (workflowrun.BindResult, error) {
		if admission.TeamID != 1 || admission.TeamName != atc.DefaultTeamName || admission.CreatedBy != "alice" || admission.Origin != (workflowrun.Origin{Kind: "manual"}) {
			t.Fatalf("admission = %#v", admission)
		}
		received = request
		run := nodeRunFixture(exactNodeRunID, "code-review", db.AgentWorkflowRunStatusRunning)
		run.IdempotencyKey = request.IdempotencyKey
		run.CreatedBy = admission.CreatedBy
		return workflowrun.BindResult{Run: run, Created: true}, nil
	}
	handler := mustHandler(t, deps)

	recorder := httptest.NewRecorder()
	handler.Create(recorder, nodeRequest(http.MethodPost, "/api/v1/agent/nodes/code-review/versions/5/runs", "code-review", "5", "", `{"inputs":{"repository":"9007199254740995"},"params":{"MINIMUM_SEVERITY":"high"},"idempotency_key":"node-test-1"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if received.DefinitionKind != workflow.DefinitionKindNode || received.WorkflowName != "code-review" || received.Version == nil || *received.Version != 5 ||
		received.Inputs["repository"] != snapshot.SnapshotID(9007199254740995) || received.NodeParameters["MINIMUM_SEVERITY"] != "high" || received.IdempotencyKey != "node-test-1" ||
		received.FunctionID != "" || received.ExpectedWorkflowDefinitionID != 0 || received.ExpectedTargetConfigHash != "" || received.RetryOf != nil {
		t.Fatalf("bind request = %#v", received)
	}
	if !strings.Contains(recorder.Body.String(), `"workflow_run_id":"9007199254740993"`) {
		t.Fatalf("response did not retain the exact quoted run identity: %s", recorder.Body.String())
	}
}

func TestCreateRejectsImplementationOverridesAndUnknownFields(t *testing.T) {
	deps := defaultDependencies()
	binderCalls := 0
	deps.binder.bind = func(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error) {
		binderCalls++
		return workflowrun.BindResult{}, nil
	}
	handler := mustHandler(t, deps)
	for _, body := range []string{
		`{"function_id":"review","idempotency_key":"key"}`,
		`{"implementation":"caller-selected","idempotency_key":"key"}`,
		`{"idempotency_key":"key","version":5}`,
	} {
		recorder := httptest.NewRecorder()
		handler.Create(recorder, nodeRequest(http.MethodPost, "/runs", "code-review", "5", "", body))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, recorder.Code)
		}
	}
	if binderCalls != 0 {
		t.Fatalf("binder calls = %d, want 0", binderCalls)
	}
}

func TestListAndGetUseNodeKindScopedStore(t *testing.T) {
	deps := defaultDependencies()
	getKinds, listKinds := []workflow.DefinitionKind{}, []workflow.DefinitionKind{}
	deps.runs.get = func(_ context.Context, teamID int, kind workflow.DefinitionKind, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		getKinds = append(getKinds, kind)
		if teamID != 1 || kind != workflow.DefinitionKindNode || id != exactNodeRunID {
			return db.AgentWorkflowRun{}, false, nil
		}
		return nodeRunFixture(id, "code-review", db.AgentWorkflowRunStatusRunning), true, nil
	}
	deps.runs.list = func(_ context.Context, kind workflow.DefinitionKind, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
		listKinds = append(listKinds, kind)
		if kind != workflow.DefinitionKindNode || filter.TeamID != 1 || filter.WorkflowName != "code-review" {
			t.Fatalf("list filter = %#v, kind = %q", filter, kind)
		}
		return []db.AgentWorkflowRun{nodeRunFixture(exactNodeRunID, "code-review", db.AgentWorkflowRunStatusRunning)}, nil
	}
	handler := mustHandler(t, deps)

	listRecorder := httptest.NewRecorder()
	handler.List(listRecorder, nodeRequest(http.MethodGet, "/api/v1/agent/nodes/code-review/versions/5/runs", "code-review", "5", "", ""))
	if listRecorder.Code != http.StatusOK || len(listKinds) != 1 || listKinds[0] != workflow.DefinitionKindNode {
		t.Fatalf("list status/kinds = %d/%#v, body = %s", listRecorder.Code, listKinds, listRecorder.Body.String())
	}

	getRecorder := httptest.NewRecorder()
	handler.Get(getRecorder, nodeRequest(http.MethodGet, "/api/v1/agent/nodes/code-review/runs/9007199254740993", "code-review", "", "9007199254740993", ""))
	if getRecorder.Code != http.StatusOK || len(getKinds) != 1 || getKinds[0] != workflow.DefinitionKindNode {
		t.Fatalf("get status/kinds = %d/%#v, body = %s", getRecorder.Code, getKinds, getRecorder.Body.String())
	}
}

func TestCreateAndGetRejectCollectionFilters(t *testing.T) {
	handler := mustHandler(t, defaultDependencies())
	for _, test := range []struct {
		name    string
		method  string
		path    string
		version string
		runID   string
		body    string
	}{
		{"create", http.MethodPost, "/runs?status=running", "5", "", `{"idempotency_key":"key"}`},
		{"get", http.MethodGet, "/runs?limit=2", "", "9007199254740993", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handlerRequest := nodeRequest(test.method, test.path, "code-review", test.version, test.runID, test.body)
			handler.Create(recorder, handlerRequest)
			if test.name == "get" {
				recorder = httptest.NewRecorder()
				handler.Get(recorder, handlerRequest)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

type dependencies struct {
	binder *fakeBinder
	runs   *fakeRunStore
}

func defaultDependencies() dependencies {
	return dependencies{
		binder: &fakeBinder{bind: func(_ context.Context, _ workflowrun.AdmissionContext, request workflowrun.BindRequest) (workflowrun.BindResult, error) {
			return workflowrun.BindResult{Run: nodeRunFixture(exactNodeRunID, request.WorkflowName, db.AgentWorkflowRunStatusRunning), Created: true}, nil
		}},
		runs: &fakeRunStore{
			get: func(_ context.Context, _ int, _ workflow.DefinitionKind, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
				return nodeRunFixture(id, "code-review", db.AgentWorkflowRunStatusRunning), true, nil
			},
			list: func(_ context.Context, _ workflow.DefinitionKind, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
				return []db.AgentWorkflowRun{nodeRunFixture(exactNodeRunID, filter.WorkflowName, db.AgentWorkflowRunStatusRunning)}, nil
			},
			snapshots: func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
				return nil, nil
			},
		},
	}
}

func mustHandler(t *testing.T, deps dependencies) *noderuns.Handler {
	t.Helper()
	handler, err := noderuns.NewHandler(noderuns.Config{
		Team:     workflowrunsapi.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: func(*http.Request) (string, error) { return "alice", nil },
		Binder:   deps.binder, Runs: deps.runs, Manifests: fakeManifestStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func nodeRunFixture(id snapshot.WorkflowRunID, name string, status db.AgentWorkflowRunStatus) db.AgentWorkflowRun {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return db.AgentWorkflowRun{
		ID: id, DefinitionKind: workflow.DefinitionKindNode, TeamID: 1, TeamName: atc.DefaultTeamName,
		WorkflowDefinitionID: 7, WorkflowName: name, WorkflowVersion: 5, SchemaVersion: 1, SignatureVersion: 1,
		DefinitionContentHash: "definition-sha256", ParameterizedConfigHash: "parameters-sha256",
		IdempotencyKey: "node-test-1", OriginKind: "manual", CreatedBy: "alice", Status: status,
		CreatedAt: now, UpdatedAt: now,
	}
}

func nodeRequest(method, path, name, version, runID, body string) *http.Request {
	query := url.Values{":node_name": []string{name}}
	if version != "" {
		query.Set(":version", version)
	}
	if runID != "" {
		query.Set(":workflow_run_id", runID)
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	req := httptest.NewRequest(method, path+separator+query.Encode(), strings.NewReader(body))
	if body == "" {
		req.Body = http.NoBody
		req.ContentLength = 0
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

var _ = json.RawMessage{}
