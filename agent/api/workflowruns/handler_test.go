package workflowruns_test

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

	"github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const exactLargeRunID snapshot.WorkflowRunID = 9007199254740993
const exactLargeSnapshotID snapshot.SnapshotID = 9007199254740995

type fakeBinder struct {
	bind func(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error)
}

func (fake *fakeBinder) BindAndCreate(
	ctx context.Context,
	admission workflowrun.AdmissionContext,
	request workflowrun.BindRequest,
) (workflowrun.BindResult, error) {
	return fake.bind(ctx, admission, request)
}

type fakeRunStore struct {
	get          func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	list         func(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error)
	statusCounts func(context.Context, db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error)
	snapshots    func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error)
}

func (fake *fakeRunStore) Get(ctx context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return fake.get(ctx, teamID, id)
}

func (fake *fakeRunStore) List(ctx context.Context, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
	return fake.list(ctx, filter)
}

func (fake *fakeRunStore) CountByStatus(ctx context.Context, filter db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error) {
	return fake.statusCounts(ctx, filter)
}

func (fake *fakeRunStore) Snapshots(ctx context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
	return fake.snapshots(ctx, id)
}

type fakeCanceler struct {
	cancel func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
}

func (fake *fakeCanceler) Cancel(ctx context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
	return fake.cancel(ctx, teamID, id)
}

type fakeManifestStore struct {
	get func(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

func (fake *fakeManifestStore) GetAuthorized(ctx context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
	return fake.get(ctx, teamID, id)
}

type handlerDeps struct {
	binder    *fakeBinder
	runs      *fakeRunStore
	canceler  *fakeCanceler
	manifests *fakeManifestStore
	identity  workflowruns.IdentityFunc
}

func defaultDeps() handlerDeps {
	deps := handlerDeps{}
	deps.binder = &fakeBinder{bind: func(_ context.Context, admission workflowrun.AdmissionContext, request workflowrun.BindRequest) (workflowrun.BindResult, error) {
		run := runFixture(exactLargeRunID, request.WorkflowName, db.AgentWorkflowRunStatusRunning)
		run.IdempotencyKey = request.IdempotencyKey
		run.CreatedBy = admission.CreatedBy
		run.OriginKind = admission.Origin.Kind
		run.OriginReference = admission.Origin.Reference
		run.FunctionID = nil
		run.RetryOfWorkflowRunID = nil
		return workflowrun.BindResult{Run: run, Created: true}, nil
	}}
	deps.runs = &fakeRunStore{
		get: func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			if teamID != 1 {
				return db.AgentWorkflowRun{}, false, nil
			}
			return runFixture(id, "deploy", db.AgentWorkflowRunStatusFailed), true, nil
		},
		list: func(_ context.Context, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
			return []db.AgentWorkflowRun{runFixture(exactLargeRunID, filter.WorkflowName, db.AgentWorkflowRunStatusFailed)}, nil
		},
		statusCounts: func(context.Context, db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error) {
			return map[db.AgentWorkflowRunStatus]int64{}, nil
		},
		snapshots: func(_ context.Context, id snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
			return bindingsFor(id), nil
		},
	}
	deps.canceler = &fakeCanceler{cancel: func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return runFixture(id, "deploy", db.AgentWorkflowRunStatusCanceling), true, nil
	}}
	deps.manifests = &fakeManifestStore{get: func(_ context.Context, _ int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		return manifestFor(id), true, nil
	}}
	deps.identity = func(*http.Request) (string, error) { return "alice", nil }
	return deps
}

func TestOperationalStatusCountsAreExactTeamScopedAndExcludeExperiments(t *testing.T) {
	deps := defaultDeps()
	var got db.AgentWorkflowRunCountFilter
	deps.runs.statusCounts = func(_ context.Context, filter db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error) {
		got = filter
		return map[db.AgentWorkflowRunStatus]int64{
			db.AgentWorkflowRunStatusAdmitting: 1007,
			db.AgentWorkflowRunStatusRunning:   4,
			db.AgentWorkflowRunStatusFailed:    2,
		}, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.OperationalStatusCounts(recorder, request(
		http.MethodGet,
		"/api/v1/agent/workflows/deploy/runs/operational-status-counts",
		"deploy",
		"",
		nil,
		"",
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	wantFilter := db.AgentWorkflowRunCountFilter{
		TeamID: 1, WorkflowName: "deploy", ExcludeOriginKind: "experiment",
	}
	if got != wantFilter {
		t.Fatalf("filter = %+v, want %+v", got, wantFilter)
	}
	want := `{"workflow_name":"deploy","counts":{"aborted":0,"admitting":1007,"canceling":0,"errored":0,"failed":2,"running":4,"succeeded":0}}`
	if strings.TrimSpace(recorder.Body.String()) != want {
		t.Fatalf("response = %s, want %s", recorder.Body.String(), want)
	}
}

func TestOperationalStatusCountsFailClosedOnStoreError(t *testing.T) {
	deps := defaultDeps()
	deps.runs.statusCounts = func(context.Context, db.AgentWorkflowRunCountFilter) (map[db.AgentWorkflowRunStatus]int64, error) {
		return nil, errors.New("postgres secret-password")
	}
	recorder := httptest.NewRecorder()
	mustHandler(t, deps).OperationalStatusCounts(recorder, request(
		http.MethodGet,
		"/api/v1/agent/workflows/deploy/runs/operational-status-counts",
		"deploy",
		"",
		nil,
		"",
	))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "postgres") || strings.Contains(recorder.Body.String(), "secret-password") {
		t.Fatalf("dependency error leaked: %s", recorder.Body.String())
	}
}

func mustHandler(t *testing.T, deps handlerDeps) *workflowruns.Handler {
	t.Helper()
	handler, err := workflowruns.NewHandler(workflowruns.Config{
		Team:     workflowruns.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: deps.identity, Binder: deps.binder, Runs: deps.runs,
		Canceler: deps.canceler, Manifests: deps.manifests,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func runFixture(id snapshot.WorkflowRunID, workflowName string, status db.AgentWorkflowRunStatus) db.AgentWorkflowRun {
	now := time.Date(2026, 7, 22, 12, 30, 0, 0, time.UTC)
	functionID := "publish"
	concreteHash := "instance-config-sha256"
	actualPlanHash := "actual-plan-sha256"
	pipelineRunID := 44
	templatePipelineID := 45
	instancePipelineID := 46
	plannedBuildID := int64(47)
	retryOf := snapshot.WorkflowRunID(9007199254740991)
	executionStatus := db.AgentWorkflowRunExecutionStatusFailed
	completedAt := now.Add(4 * time.Minute)
	run := db.AgentWorkflowRun{
		ID: id, TeamID: 1, TeamName: atc.DefaultTeamName,
		WorkflowDefinitionID: 7, WorkflowName: workflowName, WorkflowVersion: 3,
		SchemaVersion: 3, SignatureVersion: 2, DefinitionContentHash: "definition-sha256",
		FunctionID: &functionID, IdempotencyKey: "original-key",
		ParameterizedConfig:     []byte(`{"private_parameterized":"secret-a"}`),
		ParameterizedConfigHash: "parameterized-sha256",
		ConcreteConfig:          []byte(`{"private_concrete":"secret-b"}`), ConcreteConfigHash: &concreteHash,
		ActualPlan: []byte(`{"private_plan":"secret-c"}`), ActualPlanHash: &actualPlanHash,
		ResolvedDependencies: []byte(`{"private_dependency":"secret-d"}`),
		OriginKind:           "ticket", OriginReference: "T-7", CreatedBy: "creator@example.test",
		Status: status, ErrorMessage: "storage credential secret-e",
		RetryOfWorkflowRunID: &retryOf, PipelineRunID: &pipelineRunID,
		TemplatePipelineID: &templatePipelineID, InstancePipelineID: &instancePipelineID,
		PlannedBuildID: &plannedBuildID, ReconcileAfter: now,
		CreatedAt: now, UpdatedAt: now.Add(time.Minute), StartedAt: timePointer(now.Add(2 * time.Minute)),
	}
	if terminalStatus(status) {
		run.ExecutionStatus = &executionStatus
		run.CompletedAt = &completedAt
	}
	return run
}

func terminalStatus(status db.AgentWorkflowRunStatus) bool {
	switch status {
	case db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func bindingsFor(runID snapshot.WorkflowRunID) []db.AgentWorkflowRunSnapshotBinding {
	return []db.AgentWorkflowRunSnapshotBinding{
		binding(runID, db.AgentWorkflowRunSnapshotOutput, "report", 103, 'd'),
		binding(runID, db.AgentWorkflowRunSnapshotInput, "ticket", 102, 'c'),
		binding(runID, db.AgentWorkflowRunSnapshotOutput, "artifact", 101, 'b'),
		binding(runID, db.AgentWorkflowRunSnapshotInput, "repo", 100, 'a'),
	}
}

func binding(
	runID snapshot.WorkflowRunID,
	direction db.AgentWorkflowRunSnapshotDirection,
	port string,
	id snapshot.SnapshotID,
	digestByte byte,
) db.AgentWorkflowRunSnapshotBinding {
	return db.AgentWorkflowRunSnapshotBinding{
		WorkflowRunID: runID, Direction: direction, PortName: port,
		Snapshot: snapshot.SnapshotRef{ID: id, Type: snapshot.TypeRef("example.test/v1"), Digest: digest(digestByte)},
	}
}

func manifestFor(id snapshot.SnapshotID) snapshot.Snapshot {
	state := snapshot.ContentStateAvailable
	digestByte := byte('b')
	if id == 103 {
		state = snapshot.ContentStateExpired
		digestByte = 'd'
	}
	return snapshot.Snapshot{
		ID: id, Type: snapshot.TypeRef("example.test/v1"), Digest: digest(digestByte),
		ByteSize: 123, FileCount: 4, Representation: "tar",
		IntrinsicMetadata: []byte(`{"public":"metadata"}`), ContentState: state,
		CreatedAt: time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC),
	}
}

func digest(character byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(character), 64))
}

func request(
	method string,
	path string,
	workflowName string,
	runID string,
	query url.Values,
	body string,
) *http.Request {
	values := make(url.Values, len(query)+2)
	for key, existing := range query {
		values[key] = append([]string(nil), existing...)
	}
	values.Set(":workflow_name", workflowName)
	if runID != "" {
		values.Set(":workflow_run_id", runID)
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body == "" {
		req.Body = http.NoBody
		req.ContentLength = 0
	}
	return req
}

func jsonRequest(method, path, workflowName, runID string, query url.Values, body string) *http.Request {
	req := request(method, path, workflowName, runID, query, body)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) workflowruns.ErrorResponse {
	t.Helper()
	var response workflowruns.ErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func TestNewHandlerRejectsMissingInjectedDependencies(t *testing.T) {
	deps := defaultDeps()
	_, err := workflowruns.NewHandler(workflowruns.Config{
		Team:     workflowruns.TrustedTeam{ID: 1, Name: atc.DefaultTeamName},
		Identity: deps.identity, Runs: deps.runs, Canceler: deps.canceler, Manifests: deps.manifests,
	})
	if err == nil {
		t.Fatal("NewHandler accepted a missing binder")
	}
}

func TestCreateUsesStrictQuotedIDsAndServerDerivedAdmission(t *testing.T) {
	deps := defaultDeps()
	var gotAdmission workflowrun.AdmissionContext
	var gotRequest workflowrun.BindRequest
	created := true
	deps.binder.bind = func(_ context.Context, admission workflowrun.AdmissionContext, bindRequest workflowrun.BindRequest) (workflowrun.BindResult, error) {
		gotAdmission = admission
		gotRequest = bindRequest
		run := runFixture(exactLargeRunID, "deploy", db.AgentWorkflowRunStatusRunning)
		run.IdempotencyKey = bindRequest.IdempotencyKey
		run.CreatedBy = admission.CreatedBy
		run.OriginKind = admission.Origin.Kind
		run.OriginReference = admission.Origin.Reference
		run.FunctionID = nil
		run.RetryOfWorkflowRunID = nil
		return workflowrun.BindResult{Run: run, Created: created}, nil
	}
	handler := mustHandler(t, deps)
	body := `{"version":3,"inputs":{"repo":"9007199254740995"},"idempotency_key":"deploy-001"}`

	recorder := httptest.NewRecorder()
	handler.Create(recorder, jsonRequest(http.MethodPost, "/api/v1/agent/workflows/deploy/runs", "deploy", "", nil, body))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotAdmission.TeamID != 1 || gotAdmission.TeamName != atc.DefaultTeamName || gotAdmission.CreatedBy != "alice" {
		t.Fatalf("admission identity = %+v", gotAdmission)
	}
	if gotAdmission.Origin != (workflowrun.Origin{Kind: "manual"}) {
		t.Fatalf("origin = %+v, want server-controlled manual origin", gotAdmission.Origin)
	}
	if gotRequest.WorkflowName != "deploy" || gotRequest.Version == nil || *gotRequest.Version != 3 ||
		gotRequest.Inputs["repo"] != exactLargeSnapshotID || gotRequest.IdempotencyKey != "deploy-001" ||
		gotRequest.FunctionID != "" || gotRequest.RetryOf != nil {
		t.Fatalf("bind request = %+v", gotRequest)
	}
	var detail workflowruns.RunDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.WorkflowRunID != exactLargeRunID {
		t.Fatalf("workflow run ID = %s", detail.WorkflowRunID.String())
	}
	if !strings.Contains(recorder.Body.String(), `"workflow_run_id":"9007199254740993"`) {
		t.Fatalf("workflow run ID was not encoded as an exact quoted decimal: %s", recorder.Body.String())
	}

	created = false
	recorder = httptest.NewRecorder()
	handler.Create(recorder, jsonRequest(http.MethodPost, "/api/v1/agent/workflows/deploy/runs", "deploy", "", nil, body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("idempotent replay status = %d, want 200", recorder.Code)
	}
}

func TestCreateRejectsAnInconsistentBinderResultAsInternalState(t *testing.T) {
	deps := defaultDeps()
	deps.binder.bind = func(_ context.Context, _ workflowrun.AdmissionContext, request workflowrun.BindRequest) (workflowrun.BindResult, error) {
		run := runFixture(exactLargeRunID, request.WorkflowName, db.AgentWorkflowRunStatusRunning)
		// A binder implementation must not substitute a different immutable
		// request identity in the response.
		run.IdempotencyKey = "other-key"
		return workflowrun.BindResult{Run: run, Created: true}, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Create(recorder, jsonRequest(http.MethodPost, "/runs", "deploy", "", nil, `{"version":3,"idempotency_key":"key"}`))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "other-key") {
		t.Fatalf("inconsistent binder state leaked: %s", recorder.Body.String())
	}
}

func TestCreateStrictlyRejectsMalformedOrUnboundedBodies(t *testing.T) {
	deps := defaultDeps()
	bindCalls := 0
	deps.binder.bind = func(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error) {
		bindCalls++
		return workflowrun.BindResult{}, nil
	}
	handler := mustHandler(t, deps)
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{name: "numeric snapshot ID", body: `{"inputs":{"repo":9007199254740995},"idempotency_key":"key"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "leading-zero snapshot ID", body: `{"inputs":{"repo":"01"},"idempotency_key":"key"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "duplicate input port", body: `{"inputs":{"repo":"1","repo":"2"},"idempotency_key":"key"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "duplicate top-level field", body: `{"idempotency_key":"a","idempotency_key":"b"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"idempotency_key":"key","created_by":"mallory"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "caller-controlled function", body: `{"idempotency_key":"key","function_id":"private"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "zero version", body: `{"version":0,"idempotency_key":"key"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "null inputs", body: `{"inputs":null,"idempotency_key":"key"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "missing key", body: `{"inputs":{}}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "trailing value", body: `{"idempotency_key":"key"} {}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "wrong media type", body: `{"idempotency_key":"key"}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "oversized", body: `{"idempotency_key":"` + strings.Repeat("x", 70<<10) + `"}`, contentType: "application/json", wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := request(http.MethodPost, "/api/v1/agent/workflows/deploy/runs", "deploy", "", nil, test.body)
			req.Header.Set("Content-Type", test.contentType)
			handler.Create(recorder, req)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
	if bindCalls != 0 {
		t.Fatalf("binder called %d times for rejected requests", bindCalls)
	}
}

func TestCreateMapsDomainErrorsWithoutDisclosingTheirDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "invalid", err: workflowrun.ErrInvalidRequest, wantStatus: 400, wantCode: "invalid_request"},
		{name: "not found", err: workflowrun.ErrDefinitionOrTargetNotFound, wantStatus: 404, wantCode: "not_found"},
		{name: "legacy", err: workflowrun.ErrLegacyDefinition, wantStatus: 422, wantCode: "validation_failed"},
		{name: "unavailable", err: workflowrun.ErrSnapshotUnavailable, wantStatus: 422, wantCode: "inputs_unavailable"},
		{name: "type mismatch", err: workflowrun.ErrSnapshotTypeMismatch, wantStatus: 422, wantCode: "inputs_unavailable"},
		{name: "budget", err: workflowrun.ErrBudgetDenied, wantStatus: 422, wantCode: "admission_denied"},
		{name: "idempotency", err: workflowrun.ErrIdempotencyConflict, wantStatus: 409, wantCode: "conflict"},
		{name: "platform", err: workflowrun.ErrPlatformFailure, wantStatus: 500, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDeps()
			deps.binder.bind = func(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error) {
				return workflowrun.BindResult{}, fmt.Errorf("%w: private storage path /secret/token", test.err)
			}
			handler := mustHandler(t, deps)
			recorder := httptest.NewRecorder()
			handler.Create(recorder, jsonRequest(http.MethodPost, "/api/v1/agent/workflows/deploy/runs", "deploy", "", nil, `{"idempotency_key":"key"}`))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			response := decodeError(t, recorder)
			if response.Error != test.wantCode {
				t.Fatalf("error code = %q, want %q", response.Error, test.wantCode)
			}
			if strings.Contains(recorder.Body.String(), "private") || strings.Contains(recorder.Body.String(), "/secret") {
				t.Fatalf("private dependency error disclosed: %s", recorder.Body.String())
			}
		})
	}
}

func TestListUsesStrictCombinedFiltersAndReturnsAnEmptyArray(t *testing.T) {
	deps := defaultDeps()
	var gotFilter db.AgentWorkflowRunListFilter
	deps.runs.list = func(_ context.Context, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
		gotFilter = filter
		return nil, nil
	}
	handler := mustHandler(t, deps)
	query := url.Values{
		"status": {"failed"}, "origin_kind": {"ticket"},
		"origin_reference": {"T-7"}, "limit": {"25"},
	}
	recorder := httptest.NewRecorder()
	handler.List(recorder, request(http.MethodGet, "/api/v1/agent/workflows/deploy/runs", "deploy", "", query, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	want := db.AgentWorkflowRunListFilter{
		TeamID: 1, WorkflowName: "deploy", Status: db.AgentWorkflowRunStatusFailed,
		OriginKind: "ticket", OriginReference: "T-7", Limit: 26,
	}
	if gotFilter != want {
		t.Fatalf("filter = %+v, want %+v", gotFilter, want)
	}
	if strings.TrimSpace(recorder.Body.String()) != "[]" {
		t.Fatalf("empty list = %q, want []", recorder.Body.String())
	}
}

func TestListUsesAnOpaqueExclusiveCursorAndPreservesFiltersInTheNextLink(t *testing.T) {
	deps := defaultDeps()
	createdAt := time.Date(2026, time.July, 22, 12, 0, 0, 123456000, time.UTC)
	runs := []db.AgentWorkflowRun{
		runFixture(30, "deploy", db.AgentWorkflowRunStatusFailed),
		runFixture(29, "deploy", db.AgentWorkflowRunStatusFailed),
		runFixture(28, "deploy", db.AgentWorkflowRunStatusFailed),
	}
	for index := range runs {
		runs[index].CreatedAt = createdAt
	}
	var calls []db.AgentWorkflowRunListFilter
	deps.runs.list = func(_ context.Context, filter db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
		calls = append(calls, filter)
		if filter.Before == nil {
			return runs, nil
		}
		return runs[2:], nil
	}
	handler := mustHandler(t, deps)
	query := url.Values{
		"status": {"failed"}, "origin_kind": {"ticket"},
		"origin_reference": {"T-7 & more"}, "limit": {"2"},
	}
	recorder := httptest.NewRecorder()
	handler.List(recorder, request(http.MethodGet, "/api/v1/agent/workflows/deploy/runs", "deploy", "", query, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first page status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	var first []workflowruns.RunSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].WorkflowRunID != 30 || first[1].WorkflowRunID != 29 {
		t.Fatalf("first page IDs = %+v, want [30 29]", first)
	}
	next := recorder.Header().Get("X-Next-Cursor")
	decoded, err := pagination.Decode(next)
	if err != nil {
		t.Fatalf("decode next cursor %q: %v", next, err)
	}
	if !decoded.CreatedAt.Equal(createdAt) || decoded.ID != 29 {
		t.Fatalf("next cursor = %#v, want (%s,29)", decoded, createdAt)
	}
	link := recorder.Header().Get("Link")
	if !strings.Contains(link, `rel="next"`) {
		t.Fatalf("next Link = %q", link)
	}
	start := strings.IndexByte(link, '<')
	end := strings.IndexByte(link, '>')
	if start != 0 || end < 0 {
		t.Fatalf("malformed next Link = %q", link)
	}
	nextURL, err := url.Parse(link[start+1 : end])
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := query
	wantQuery.Set("cursor", next)
	if nextURL.EscapedPath() != "/api/v1/agent/workflows/deploy/runs" ||
		nextURL.Query().Encode() != wantQuery.Encode() {
		t.Fatalf("next URL = %q, want path and filters %q", nextURL.String(), wantQuery.Encode())
	}

	secondQuery := nextURL.Query()
	recorder = httptest.NewRecorder()
	handler.List(recorder, request(http.MethodGet, "/api/v1/agent/workflows/deploy/runs", "deploy", "", secondQuery, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("second page status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	var second []workflowruns.RunSummary
	if err := json.Unmarshal(recorder.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].WorkflowRunID != 28 {
		t.Fatalf("second page IDs = %+v, want [28]", second)
	}
	if recorder.Header().Get("X-Next-Cursor") != "" || recorder.Header().Get("Link") != "" {
		t.Fatalf("terminal page exposed next headers: %#v", recorder.Header())
	}
	if len(calls) != 2 || calls[0].Limit != 3 || calls[0].Before != nil ||
		calls[1].Limit != 3 || calls[1].Before == nil ||
		!calls[1].Before.CreatedAt.Equal(createdAt) || calls[1].Before.ID != 29 {
		t.Fatalf("list calls = %#v", calls)
	}
}

func TestListRejectsUnsupportedOrMalformedFilters(t *testing.T) {
	deps := defaultDeps()
	listCalls := 0
	deps.runs.list = func(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error) {
		listCalls++
		return nil, nil
	}
	handler := mustHandler(t, deps)
	tests := []url.Values{
		{"status": {"unknown"}},
		{"limit": {"0"}},
		{"limit": {"1001"}},
		{"limit": {"01"}},
		{"origin_kind": {"Ticket"}},
		{"status": {"failed", "succeeded"}},
		{"cursor": {"secret"}},
		{"workflow_name": {"shadow"}},
	}
	for _, query := range tests {
		recorder := httptest.NewRecorder()
		handler.List(recorder, request(http.MethodGet, "/api/v1/agent/workflows/deploy/runs", "deploy", "", query, ""))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("query %v status = %d, want 400", query, recorder.Code)
		}
	}
	if listCalls != 0 {
		t.Fatalf("store called %d times for invalid filters", listCalls)
	}
}

func TestGetPresentsSafeDistinctIDsSortedBindingsAndNoPrivateState(t *testing.T) {
	deps := defaultDeps()
	deps.runs.get = func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return runFixture(id, "deploy", db.AgentWorkflowRunStatusSucceeded), true, nil
	}
	manifestTeamIDs := []int{}
	deps.manifests.get = func(_ context.Context, teamID int, id snapshot.SnapshotID) (snapshot.Snapshot, bool, error) {
		manifestTeamIDs = append(manifestTeamIDs, teamID)
		return manifestFor(id), true, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Get(recorder, request(http.MethodGet, "/api/v1/agent/workflows/deploy/runs/9007199254740993", "deploy", exactLargeRunID.String(), nil, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var detail workflowruns.RunDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.WorkflowRunID != exactLargeRunID || detail.PipelineRunID == nil || *detail.PipelineRunID != 44 {
		t.Fatalf("durable/pipeline IDs were conflated: %+v", detail.RunSummary)
	}
	if len(detail.Inputs) != 2 || detail.Inputs[0].Port != "repo" || detail.Inputs[1].Port != "ticket" {
		t.Fatalf("inputs not sorted: %+v", detail.Inputs)
	}
	if len(detail.Outputs) != 2 || detail.Outputs[0].Port != "artifact" || detail.Outputs[1].Port != "report" {
		t.Fatalf("outputs not sorted: %+v", detail.Outputs)
	}
	if detail.Outputs[1].Snapshot.ContentState != snapshot.ContentStateExpired {
		t.Fatalf("expired manifest state was lost: %+v", detail.Outputs[1])
	}
	if len(manifestTeamIDs) != 2 || manifestTeamIDs[0] != 1 || manifestTeamIDs[1] != 1 {
		t.Fatalf("manifest reads were not team-scoped: %v", manifestTeamIDs)
	}
	raw := recorder.Body.String()
	for _, forbidden := range []string{
		"parameterized_config\"", "concrete_config\"", "actual_plan\"", "resolved_dependencies",
		"error_message", "template_pipeline_id", "instance_pipeline_id", "secret-a", "secret-b", "secret-c", "secret-d", "secret-e",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("response disclosed %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{
		`"workflow_run_id":"9007199254740993"`, `"pipeline_run_id":44`,
		`"parameterized_config_hash":"parameterized-sha256"`, `"instance_config_hash":"instance-config-sha256"`,
		`"actual_plan_hash":"actual-plan-sha256"`,
	} {
		if !strings.Contains(raw, required) {
			t.Errorf("response missing %s: %s", required, raw)
		}
	}
}

func TestGetReturnsEmptyArraysAndHidesMissingOrCrossScopedRuns(t *testing.T) {
	deps := defaultDeps()
	deps.runs.snapshots = func(context.Context, snapshot.WorkflowRunID) ([]db.AgentWorkflowRunSnapshotBinding, error) {
		return nil, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Get(recorder, request(http.MethodGet, "/runs/1", "deploy", "1", nil, ""))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"inputs":[]`) || !strings.Contains(recorder.Body.String(), `"outputs":[]`) {
		t.Fatalf("empty binding arrays were not canonical: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	tests := []struct {
		name string
		get  func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	}{
		{name: "missing", get: func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return db.AgentWorkflowRun{}, false, nil
		}},
		{name: "other workflow", get: func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			return runFixture(id, "other", db.AgentWorkflowRunStatusFailed), true, nil
		}},
		{name: "other team", get: func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
			run := runFixture(id, "deploy", db.AgentWorkflowRunStatusFailed)
			run.TeamID = 2
			run.TeamName = "other"
			return run, true, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caseDeps := defaultDeps()
			caseDeps.runs.get = test.get
			caseHandler := mustHandler(t, caseDeps)
			caseRecorder := httptest.NewRecorder()
			caseHandler.Get(caseRecorder, request(http.MethodGet, "/runs/1", "deploy", "1", nil, ""))
			if caseRecorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", caseRecorder.Code)
			}
		})
	}

	badIDRecorder := httptest.NewRecorder()
	handler.Get(badIDRecorder, request(http.MethodGet, "/runs/01", "deploy", "01", nil, ""))
	if badIDRecorder.Code != http.StatusBadRequest {
		t.Fatalf("noncanonical ID status = %d, want 400", badIDRecorder.Code)
	}
}

func TestOutputsHidesUnpromotedPartialManifestsForFailedRuns(t *testing.T) {
	deps := defaultDeps()
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Outputs(recorder, request(http.MethodGet, "/runs/9007199254740993/outputs", "deploy", exactLargeRunID.String(), nil, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response workflowruns.OutputsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.WorkflowRunID != exactLargeRunID || len(response.Outputs) != 0 {
		t.Fatalf("output manifests = %+v", response)
	}
}

func TestRetryPinsOriginalTargetAndInputsWithANewImmediateSourceLink(t *testing.T) {
	deps := defaultDeps()
	var gotAdmission workflowrun.AdmissionContext
	var gotRequest workflowrun.BindRequest
	newID := snapshot.WorkflowRunID(9007199254740997)
	deps.binder.bind = func(_ context.Context, admission workflowrun.AdmissionContext, bindRequest workflowrun.BindRequest) (workflowrun.BindResult, error) {
		gotAdmission = admission
		gotRequest = bindRequest
		run := runFixture(newID, "deploy", db.AgentWorkflowRunStatusRunning)
		retryOf := exactLargeRunID
		run.RetryOfWorkflowRunID = &retryOf
		run.IdempotencyKey = bindRequest.IdempotencyKey
		run.CreatedBy = admission.CreatedBy
		run.OriginKind = admission.Origin.Kind
		run.OriginReference = admission.Origin.Reference
		return workflowrun.BindResult{Run: run, Created: true}, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Retry(recorder, jsonRequest(http.MethodPost, "/runs/9007199254740993/retry", "deploy", exactLargeRunID.String(), nil, `{"idempotency_key":"retry-001"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if gotAdmission.TeamID != 1 || gotAdmission.CreatedBy != "alice" || gotAdmission.Origin.Kind != "retry" || gotAdmission.Origin.Reference != exactLargeRunID.String() {
		t.Fatalf("retry admission = %+v", gotAdmission)
	}
	if gotRequest.WorkflowName != "deploy" || gotRequest.Version == nil || *gotRequest.Version != 3 || gotRequest.FunctionID != "publish" ||
		gotRequest.IdempotencyKey != "retry-001" || gotRequest.RetryOf == nil || *gotRequest.RetryOf != exactLargeRunID {
		t.Fatalf("retry bind request = %+v", gotRequest)
	}
	if len(gotRequest.Inputs) != 2 || gotRequest.Inputs["repo"] != 100 || gotRequest.Inputs["ticket"] != 102 {
		t.Fatalf("retry inputs = %+v; outputs must not be copied", gotRequest.Inputs)
	}
	if !strings.Contains(recorder.Body.String(), `"retry_of_workflow_run_id":"9007199254740993"`) {
		t.Fatalf("immediate retry link missing: %s", recorder.Body.String())
	}
}

func TestRetryRejectsAnInconsistentOrReopenedBinderResult(t *testing.T) {
	deps := defaultDeps()
	deps.binder.bind = func(_ context.Context, admission workflowrun.AdmissionContext, request workflowrun.BindRequest) (workflowrun.BindResult, error) {
		// Reusing the source durable ID would reopen/overwrite history instead
		// of creating a new retry, even if every other field looks consistent.
		run := runFixture(exactLargeRunID, "deploy", db.AgentWorkflowRunStatusRunning)
		retryOf := exactLargeRunID
		run.RetryOfWorkflowRunID = &retryOf
		run.IdempotencyKey = request.IdempotencyKey
		run.CreatedBy = admission.CreatedBy
		run.OriginKind = admission.Origin.Kind
		run.OriginReference = admission.Origin.Reference
		return workflowrun.BindResult{Run: run, Created: true}, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Retry(recorder, jsonRequest(http.MethodPost, "/runs/id/retry", "deploy", exactLargeRunID.String(), nil, `{"idempotency_key":"retry-001"}`))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

func TestRetryRejectsActiveRunsAndOriginalOrMalformedKeys(t *testing.T) {
	tests := []struct {
		name string
		run  db.AgentWorkflowRun
		body string
	}{
		{name: "active source", run: runFixture(exactLargeRunID, "deploy", db.AgentWorkflowRunStatusRunning), body: `{"idempotency_key":"retry-001"}`},
		{name: "same key", run: runFixture(exactLargeRunID, "deploy", db.AgentWorkflowRunStatusFailed), body: `{"idempotency_key":"original-key"}`},
		{name: "unknown field", run: runFixture(exactLargeRunID, "deploy", db.AgentWorkflowRunStatusFailed), body: `{"idempotency_key":"retry-001","inputs":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDeps()
			deps.runs.get = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
				return test.run, true, nil
			}
			bindCalls := 0
			deps.binder.bind = func(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error) {
				bindCalls++
				return workflowrun.BindResult{}, errors.New("unexpected")
			}
			handler := mustHandler(t, deps)
			recorder := httptest.NewRecorder()
			handler.Retry(recorder, jsonRequest(http.MethodPost, "/runs/id/retry", "deploy", exactLargeRunID.String(), nil, test.body))
			if recorder.Code != http.StatusConflict && test.name != "unknown field" || recorder.Code != http.StatusBadRequest && test.name == "unknown field" {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if bindCalls != 0 {
				t.Fatalf("binder called %d times", bindCalls)
			}
		})
	}
}

func TestCancelUsesTeamScopedServiceAndIsIdempotentForCancelingOrAborted(t *testing.T) {
	deps := defaultDeps()
	var gotTeam int
	var gotID snapshot.WorkflowRunID
	status := db.AgentWorkflowRunStatusCanceling
	deps.canceler.cancel = func(_ context.Context, teamID int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		gotTeam, gotID = teamID, id
		return runFixture(id, "deploy", status), true, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, request(http.MethodPost, "/runs/9007199254740993/cancel", "deploy", exactLargeRunID.String(), nil, ""))
	if recorder.Code != http.StatusAccepted || gotTeam != 1 || gotID != exactLargeRunID {
		t.Fatalf("cancel status/team/id = %d/%d/%s", recorder.Code, gotTeam, gotID.String())
	}

	status = db.AgentWorkflowRunStatusAborted
	recorder = httptest.NewRecorder()
	handler.Cancel(recorder, request(http.MethodPost, "/runs/9007199254740993/cancel", "deploy", exactLargeRunID.String(), nil, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("already-aborted cancel status = %d, want 200", recorder.Code)
	}
}

func TestCancelMapsConflictNotFoundAndInternalErrorsWithoutDisclosure(t *testing.T) {
	tests := []struct {
		name       string
		found      bool
		err        error
		status     db.AgentWorkflowRunStatus
		wantStatus int
		wantCode   string
	}{
		{name: "illegal state", found: true, err: fmt.Errorf("%w: secret state", workflowruns.ErrCancelConflict), wantStatus: 409, wantCode: "conflict"},
		{name: "raced deletion", found: false, wantStatus: 404, wantCode: "not_found"},
		{name: "dependency", found: true, err: errors.New("secret abort backend"), wantStatus: 500, wantCode: "internal_error"},
		{name: "invalid service result", found: true, status: db.AgentWorkflowRunStatusRunning, wantStatus: 500, wantCode: "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := defaultDeps()
			deps.canceler.cancel = func(_ context.Context, _ int, id snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
				return runFixture(id, "deploy", test.status), test.found, test.err
			}
			handler := mustHandler(t, deps)
			recorder := httptest.NewRecorder()
			handler.Cancel(recorder, request(http.MethodPost, "/runs/id/cancel", "deploy", exactLargeRunID.String(), nil, ""))
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			response := decodeError(t, recorder)
			if response.Error != test.wantCode || strings.Contains(recorder.Body.String(), "secret") {
				t.Fatalf("response = %+v body=%s", response, recorder.Body.String())
			}
		})
	}

	deps := defaultDeps()
	cancelCalls := 0
	deps.canceler.cancel = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		cancelCalls++
		return db.AgentWorkflowRun{}, false, nil
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Cancel(recorder, request(http.MethodPost, "/runs/id/cancel", "deploy", exactLargeRunID.String(), nil, `{}`))
	if recorder.Code != http.StatusBadRequest || cancelCalls != 0 {
		t.Fatalf("cancel body status/calls = %d/%d, want 400/0", recorder.Code, cancelCalls)
	}
}

func TestStorageFailuresAreBoundedAndDoNotLeak(t *testing.T) {
	deps := defaultDeps()
	deps.runs.get = func(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error) {
		return db.AgentWorkflowRun{}, false, errors.New("postgres secret-password")
	}
	handler := mustHandler(t, deps)
	recorder := httptest.NewRecorder()
	handler.Get(recorder, request(http.MethodGet, "/runs/1", "deploy", "1", nil, ""))
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "secret-password") {
		t.Fatalf("storage error response = %d %s", recorder.Code, recorder.Body.String())
	}
}
