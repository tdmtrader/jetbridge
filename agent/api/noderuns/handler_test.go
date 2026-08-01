package noderuns_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type fakeCanceler struct {
	cancel func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
}

func (fake *fakeCanceler) Cancel(ctx context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return fake.cancel(ctx, teamID, id)
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

func cancelRequest(runID string) *http.Request {
	return nodeRequest(http.MethodPost, "/api/v1/agent/nodes/code-review/runs/"+runID+"/cancel", "code-review", "", runID, "")
}

func TestCancelUsesTeamScopedServiceAndIsIdempotentForCancelingOrAborted(t *testing.T) {
	deps := defaultDependencies()
	var gotTeam int
	var gotID snapshot.WorkflowRunID
	status := db.AgentWorkflowRunStatusCanceling
	deps.canceler.cancel = func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		gotTeam, gotID = teamID, id
		return nodeRunFixture(id, "code-review", status), true, nil
	}
	handler := mustHandler(t, deps)

	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, cancelRequest("9007199254740993"))
	if recorder.Code != http.StatusAccepted || gotTeam != 1 || gotID != exactNodeRunID {
		t.Fatalf("cancel status/team/id = %d/%d/%s, body = %s", recorder.Code, gotTeam, gotID.String(), recorder.Body.String())
	}

	status = db.AgentWorkflowRunStatusAborted
	recorder = httptest.NewRecorder()
	handler.Cancel(recorder, cancelRequest("9007199254740993"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("already-aborted cancel status = %d, want 200", recorder.Code)
	}
}

// TestCancelRefusesAWorkflowRunAddressedAsANode is the mutation test for the
// kind check: node and workflow runs share the durable run table and the ID
// space, so a workflow-kind run returned for a node-scoped lookup must be
// refused before the shared (kind-blind) Canceler is ever invoked.
func TestCancelRefusesAWorkflowRunAddressedAsANode(t *testing.T) {
	deps := defaultDependencies()
	getCalled := false
	deps.runs.get = func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		getCalled = true
		run := nodeRunFixture(exactNodeRunID, "code-review", db.AgentWorkflowRunStatusRunning)
		run.DefinitionKind = workflow.DefinitionKindWorkflow
		return run, true, nil
	}
	canceled := false
	deps.canceler.cancel = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		canceled = true
		return db.AgentWorkflowRun{}, false, nil
	}
	handler := mustHandler(t, deps)

	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, cancelRequest("9007199254740993"))
	if recorder.Code != http.StatusNotFound || !getCalled || canceled {
		t.Fatalf("status = %d getCalled = %v canceled = %v, want 404 with GetKind consulted and canceler untouched; body = %s",
			recorder.Code, getCalled, canceled, recorder.Body.String())
	}
}

// TestCancelRefusesAnotherTeamsNodeRun is the mutation test for the team
// check: an ID match alone must not be enough to cancel a run owned by a
// different team.
func TestCancelRefusesAnotherTeamsNodeRun(t *testing.T) {
	deps := defaultDependencies()
	deps.runs.get = func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		run := nodeRunFixture(exactNodeRunID, "code-review", db.AgentWorkflowRunStatusRunning)
		run.TeamID = 999
		return run, true, nil
	}
	canceled := false
	deps.canceler.cancel = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		canceled = true
		return db.AgentWorkflowRun{}, false, nil
	}
	handler := mustHandler(t, deps)

	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, cancelRequest("9007199254740993"))
	if recorder.Code != http.StatusNotFound || canceled {
		t.Fatalf("status = %d canceled = %v, want 404 with canceler untouched; body = %s", recorder.Code, canceled, recorder.Body.String())
	}
}

// TestCancelRefusesAnotherNodesRunAddressedByID is the mutation test for the
// name check: an ID match against the right team and kind still must not
// cancel a run that belongs to a different node name than the route names.
func TestCancelRefusesAnotherNodesRunAddressedByID(t *testing.T) {
	deps := defaultDependencies()
	deps.runs.get = func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return nodeRunFixture(exactNodeRunID, "other-node", db.AgentWorkflowRunStatusRunning), true, nil
	}
	canceled := false
	deps.canceler.cancel = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		canceled = true
		return db.AgentWorkflowRun{}, false, nil
	}
	handler := mustHandler(t, deps)

	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, cancelRequest("9007199254740993"))
	if recorder.Code != http.StatusNotFound || canceled {
		t.Fatalf("status = %d canceled = %v, want 404 with canceler untouched; body = %s", recorder.Code, canceled, recorder.Body.String())
	}
}

func TestCancelReportsNotFoundWhenTheRunDoesNotExist(t *testing.T) {
	deps := defaultDependencies()
	deps.runs.get = func(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{}, false, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, cancelRequest("9007199254740993"))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCancelMapsConflictAndInternalErrorsWithoutDisclosure(t *testing.T) {
	tests := []struct {
		name       string
		found      bool
		err        error
		status     db.AgentWorkflowRunStatus
		wantStatus int
		wantCode   string
	}{
		{name: "illegal state", found: true, err: fmt.Errorf("%w: secret state", workflowrunsapi.ErrCancelConflict), wantStatus: http.StatusConflict, wantCode: "conflict"},
		{name: "raced deletion", found: false, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "dependency", found: true, err: errors.New("secret abort backend"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
		{name: "invalid service result", found: true, status: db.AgentWorkflowRunStatusRunning, wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDependencies()
			deps.canceler.cancel = func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
				return nodeRunFixture(id, "code-review", test.status), test.found, test.err
			}
			handler := mustHandler(t, deps)
			recorder := httptest.NewRecorder()
			handler.Cancel(recorder, cancelRequest("9007199254740993"))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != test.wantCode || strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("response = %+v body=%s", response, recorder.Body.String())
			}
		})
	}
}

func TestCancelRejectsWrongMethodAndBody(t *testing.T) {
	handler := mustHandler(t, defaultDependencies())

	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, nodeRequest(http.MethodGet, "/api/v1/agent/nodes/code-review/runs/9007199254740993/cancel", "code-review", "", "9007199254740993", ""))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong-method status = %d, want 405", recorder.Code)
	}

	deps := defaultDependencies()
	cancelCalls := 0
	deps.canceler.cancel = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		cancelCalls++
		return db.AgentWorkflowRun{}, false, nil
	}
	handler = mustHandler(t, deps)
	recorder = httptest.NewRecorder()
	request := nodeRequest(http.MethodPost, "/api/v1/agent/nodes/code-review/runs/9007199254740993/cancel", "code-review", "", "9007199254740993", `{}`)
	handler.Cancel(recorder, request)
	if recorder.Code != http.StatusBadRequest || cancelCalls != 0 {
		t.Fatalf("cancel-with-body status/calls = %d/%d, want 400/0", recorder.Code, cancelCalls)
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
	binder   *fakeBinder
	runs     *fakeRunStore
	canceler *fakeCanceler
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
		canceler: &fakeCanceler{cancel: func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return nodeRunFixture(id, "code-review", db.AgentWorkflowRunStatusCanceling), true, nil
		}},
	}
}

func mustHandler(t *testing.T, deps dependencies) *noderuns.Handler {
	t.Helper()
	handler, err := noderuns.NewHandler(noderuns.Config{
		Team:     workflowrunsapi.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: func(*http.Request) (string, error) { return "alice", nil },
		Binder:   deps.binder, Runs: deps.runs, Canceler: deps.canceler, Manifests: fakeManifestStore{},
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
