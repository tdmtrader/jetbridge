// Package noderuns serves direct executions of exact reusable node versions.
package noderuns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
	workflowrunsapi "github.com/concourse/concourse/agent/api/workflowruns"
	"github.com/concourse/concourse/agent/pagination"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

type RunStore interface {
	GetKind(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
	ListKind(context.Context, workflow.DefinitionKind, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error)
	workflowrunsapi.SnapshotBindingStore
}

type Config struct {
	Logger    lager.Logger
	Team      workflowrunsapi.TrustedTeam
	Identity  workflowrunsapi.IdentityFunc
	Binder    workflowrunsapi.Binder
	Runs      RunStore
	Canceler  workflowrunsapi.Canceler
	Manifests workflowrunsapi.ManifestStore
}

type Handler struct {
	logger    lager.Logger
	team      workflowrunsapi.TrustedTeam
	identity  workflowrunsapi.IdentityFunc
	binder    workflowrunsapi.Binder
	runs      RunStore
	canceler  workflowrunsapi.Canceler
	presenter *workflowrunsapi.RunPresenter
}

type CreateRequest struct {
	Inputs         map[string]snapshot.SnapshotID `json:"inputs,omitempty"`
	Params         map[string]string              `json:"params,omitempty"`
	IdempotencyKey string                         `json:"idempotency_key"`
}

func NewHandler(config Config) (*Handler, error) {
	if config.Team.ID <= 0 || config.Team.Name != atc.DefaultTeamName || config.Identity == nil || config.Binder == nil || config.Runs == nil || config.Canceler == nil || config.Manifests == nil {
		return nil, fmt.Errorf("node runs API: trusted dependencies are required")
	}
	presenter, err := workflowrunsapi.NewRunPresenter(config.Team, config.Runs, config.Manifests)
	if err != nil {
		return nil, fmt.Errorf("node runs API: presenter: %w", err)
	}
	logger := config.Logger
	if logger == nil {
		logger = lager.NewLogger("node-runs")
	}
	return &Handler{
		logger: logger, team: config.Team, identity: config.Identity, binder: config.Binder,
		runs: config.Runs, canceler: config.Canceler, presenter: presenter,
	}, nil
}

func (handler *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	name, version, _, ok := parseRoute(w, r, false, false)
	if !ok {
		return
	}
	request, ok := decodeCreateRequest(w, r)
	if !ok {
		return
	}
	creator, err := handler.identity(r)
	if err != nil || workflowrunsapi.ValidateText(creator, 256, false, true) != nil {
		writeInternalError(w)
		return
	}
	// Direct manual node launch: deliberately unattached, same reasoning as the
	// workflow-run endpoint.
	result, err := handler.binder.BindAndCreate(r.Context(), workflowrun.AdmissionContext{
		TeamID: handler.team.ID, TeamName: handler.team.Name, CreatedBy: creator, Origin: workflowrun.Origin{Kind: "manual"},
	}, workflowrun.BindRequest{
		DefinitionKind: workflow.DefinitionKindNode, WorkflowName: name, Version: &version,
		Inputs: cloneSnapshotIDs(request.Inputs), NodeParameters: cloneStrings(request.Params), IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		workflowrunsapi.WriteBinderError(w, handler.logger, err)
		return
	}
	if result.Run.DefinitionKind != workflow.DefinitionKindNode || result.Run.WorkflowName != name || result.Run.WorkflowVersion != version ||
		result.Run.IdempotencyKey != request.IdempotencyKey || result.Run.CreatedBy != creator || result.Run.OriginKind != "manual" ||
		result.Run.OriginReference != "" || result.Run.FunctionID != nil || result.Run.RetryOfWorkflowRunID != nil {
		writeInternalError(w)
		return
	}
	detail, err := handler.presenter.Detail(r, name, result.Run)
	if err != nil {
		writeInternalError(w)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	workflowrunsapi.WriteJSON(w, status, detail)
}

func (handler *Handler) List(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireNoBody(w, r) {
		return
	}
	name, version, _, ok := parseRoute(w, r, false, true)
	if !ok {
		return
	}
	query, _ := url.ParseQuery(r.URL.RawQuery)
	filter, ok := parseListFilter(w, query)
	if !ok {
		return
	}
	filter.TeamID = handler.team.ID
	filter.WorkflowName = name
	filter.WorkflowVersion = &version
	pageLimit := filter.Limit
	filter.Limit = pageLimit + 1
	runs, err := handler.runs.ListKind(r.Context(), workflow.DefinitionKindNode, filter)
	if err != nil || len(runs) > filter.Limit {
		writeInternalError(w)
		return
	}
	hasNext := len(runs) > pageLimit
	if hasNext {
		runs = runs[:pageLimit]
	}
	summaries := make([]workflowrunsapi.RunSummary, 0, len(runs))
	for _, run := range runs {
		if run.DefinitionKind != workflow.DefinitionKindNode || run.WorkflowVersion != version {
			writeInternalError(w)
			return
		}
		summary, err := handler.presenter.Summary(name, run)
		if err != nil {
			writeInternalError(w)
			return
		}
		summaries = append(summaries, summary)
	}
	if hasNext {
		last := runs[len(runs)-1]
		cursor, err := pagination.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: int64(last.ID)})
		if err != nil {
			writeInternalError(w)
			return
		}
		setNextPageHeaders(w, r, cursor, pageLimit)
	}
	workflowrunsapi.WriteJSON(w, http.StatusOK, summaries)
}

func (handler *Handler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireNoBody(w, r) {
		return
	}
	name, _, runID, ok := parseRoute(w, r, true, false)
	if !ok {
		return
	}
	run, ok := handler.loadNodeRun(w, r, name, runID)
	if !ok {
		return
	}
	detail, err := handler.presenter.Detail(r, name, run)
	if err != nil {
		writeInternalError(w)
		return
	}
	workflowrunsapi.WriteJSON(w, http.StatusOK, detail)
}

// Cancel terminates one node run. The kind check inside loadNodeRun is the
// point of this handler: node and workflow runs share the durable run table
// and the ID space, and a node route must never reach a workflow run. Only
// once that check has passed do we hand the run off to the shared kind-blind
// Canceler, which resolves purely by team ID and run ID.
func (handler *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireNoBody(w, r) {
		return
	}
	name, _, runID, ok := parseRoute(w, r, true, false)
	if !ok {
		return
	}
	if _, ok := handler.loadNodeRun(w, r, name, runID); !ok {
		return
	}
	run, found, err := handler.canceler.Cancel(r.Context(), handler.team.ID, runID)
	if errors.Is(err, workflowrunsapi.ErrCancelConflict) {
		workflowrunsapi.WriteError(w, http.StatusConflict, "conflict", "node run cannot be canceled in its current state")
		return
	}
	if err != nil {
		writeInternalError(w)
		return
	}
	if !found {
		workflowrunsapi.WriteNotFound(w)
		return
	}
	status := 0
	switch run.Status {
	case db.AgentWorkflowRunStatusCanceling:
		status = http.StatusAccepted
	case db.AgentWorkflowRunStatusAborted:
		status = http.StatusOK
	default:
		writeInternalError(w)
		return
	}
	detail, err := handler.presenter.Detail(r, name, run)
	if err != nil {
		writeInternalError(w)
		return
	}
	workflowrunsapi.WriteJSON(w, status, detail)
}

// loadNodeRun resolves and validates the exact team-owned node run addressed
// by the route. The DefinitionKind, team, and name comparisons below are the
// safety boundary shared by every node-run route: node and workflow runs
// live in the same durable run table and ID space, so an ID alone is not
// enough to prove a route reached the kind of run it asked for.
func (handler *Handler) loadNodeRun(w http.ResponseWriter, r *http.Request, name string, runID snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool) {
	run, found, err := handler.runs.GetKind(r.Context(), handler.team.ID, workflow.DefinitionKindNode, runID)
	if err != nil {
		writeInternalError(w)
		return db.AgentWorkflowRun{}, false
	}
	if !found || run.ID != runID || run.DefinitionKind != workflow.DefinitionKindNode || run.TeamID != handler.team.ID || run.TeamName != handler.team.Name || run.WorkflowName != name {
		workflowrunsapi.WriteNotFound(w)
		return db.AgentWorkflowRun{}, false
	}
	return run, true
}

func parseRoute(w http.ResponseWriter, r *http.Request, withRunID, allowFilters bool) (string, int, snapshot.WorkflowRunID, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
		return "", 0, 0, false
	}
	allowed := map[string]struct{}{}
	if allowFilters {
		allowed = map[string]struct{}{"status": {}, "limit": {}, "cursor": {}}
	}
	for key, values := range query {
		_, public := allowed[key]
		path := key == ":node_name" || !withRunID && key == ":version" || withRunID && key == ":workflow_run_id"
		if (!public && !path) || len(values) != 1 || values[0] == "" {
			workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_query", "request query contains unsupported fields")
			return "", 0, 0, false
		}
	}
	name := rata.Param(r, "node_name")
	if warning, err := atc.ValidateIdentifier(name); err != nil || warning != nil {
		workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_node", "node name is invalid")
		return "", 0, 0, false
	}
	if !withRunID {
		version, err := parseVersion(rata.Param(r, "version"))
		if err != nil {
			workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_version", "node version is invalid")
			return "", 0, 0, false
		}
		return name, version, 0, true
	}
	runID, err := snapshot.ParseWorkflowRunID(rata.Param(r, "workflow_run_id"))
	if err != nil {
		workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_workflow_run_id", "workflow run ID is invalid")
		return "", 0, 0, false
	}
	return name, 0, runID, true
}

func parseVersion(raw string) (int, error) {
	if raw == "" || raw[0] == '0' {
		return 0, fmt.Errorf("invalid version")
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("invalid version")
		}
	}
	return strconv.Atoi(raw)
}

func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (CreateRequest, bool) {
	raw, ok := workflowrunsapi.ReadStrictJSONBody(w, r)
	if !ok {
		return CreateRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		writeInvalidBody(w)
		return CreateRequest{}, false
	}
	request := CreateRequest{Inputs: map[string]snapshot.SnapshotID{}, Params: map[string]string{}}
	seen, keyPresent := map[string]struct{}{}, false
	for decoder.More() {
		token, err := decoder.Token()
		key, isString := token.(string)
		if err != nil || !isString {
			writeInvalidBody(w)
			return CreateRequest{}, false
		}
		if _, duplicate := seen[key]; duplicate {
			writeInvalidBody(w)
			return CreateRequest{}, false
		}
		seen[key] = struct{}{}
		switch key {
		case "inputs":
			inputs, err := workflowrunsapi.DecodeInputs(decoder)
			if err != nil {
				writeInvalidBody(w)
				return CreateRequest{}, false
			}
			request.Inputs = inputs
		case "params":
			params, err := decodeParams(decoder)
			if err != nil {
				writeInvalidBody(w)
				return CreateRequest{}, false
			}
			request.Params = params
		case "idempotency_key":
			if err := decoder.Decode(&request.IdempotencyKey); err != nil {
				writeInvalidBody(w)
				return CreateRequest{}, false
			}
			keyPresent = true
		default:
			writeInvalidBody(w)
			return CreateRequest{}, false
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || workflowrunsapi.RequireJSONEOF(decoder) != nil ||
		!keyPresent || workflowrunsapi.ValidateText(request.IdempotencyKey, 256, false, true) != nil {
		writeInvalidBody(w)
		return CreateRequest{}, false
	}
	return request, true
}

func decodeParams(decoder *json.Decoder) (map[string]string, error) {
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("params must be an object")
	}
	params := map[string]string{}
	for decoder.More() {
		token, err := decoder.Token()
		name, isString := token.(string)
		if err != nil || !isString {
			return nil, fmt.Errorf("invalid parameter")
		}
		if workflowrunsapi.ValidateText(name, 256, false, true) != nil {
			return nil, fmt.Errorf("invalid parameter")
		}
		if _, duplicate := params[name]; duplicate {
			return nil, fmt.Errorf("duplicate parameter")
		}
		var value string
		if err := decoder.Decode(&value); err != nil || workflowrunsapi.ValidateText(value, 4096, true, false) != nil {
			return nil, fmt.Errorf("invalid parameter")
		}
		params[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("params object is not terminated")
	}
	return params, nil
}

func parseListFilter(w http.ResponseWriter, query url.Values) (db.AgentWorkflowRunListFilter, bool) {
	filter := db.AgentWorkflowRunListFilter{Limit: 100}
	if raw, found := query["status"]; found {
		status := db.AgentWorkflowRunStatus(raw[0])
		switch status {
		case db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed, db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusCanceling, db.AgentWorkflowRunStatusAborted:
			filter.Status = status
		default:
			workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_status", "workflow run status filter is invalid")
			return db.AgentWorkflowRunListFilter{}, false
		}
	}
	if raw, found := query["limit"]; found {
		limit, err := parseLimit(raw[0])
		if err != nil {
			workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_limit", "limit must be a decimal integer from 1 to 1000")
			return db.AgentWorkflowRunListFilter{}, false
		}
		filter.Limit = limit
	}
	if raw, found := query["cursor"]; found {
		cursor, err := pagination.Decode(raw[0])
		if err != nil {
			workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_cursor", "workflow run cursor is invalid")
			return db.AgentWorkflowRunListFilter{}, false
		}
		filter.Before = &cursor
	}
	return filter, true
}

func parseLimit(raw string) (int, error) {
	value, err := parseVersion(raw)
	if err != nil || value > 1000 {
		return 0, fmt.Errorf("invalid limit")
	}
	return value, nil
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	workflowrunsapi.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
	return false
}

func requireNoBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 || r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody {
		workflowrunsapi.WriteError(w, http.StatusBadRequest, "unexpected_body", "request body is not allowed")
		return false
	}
	return true
}

func writeInvalidBody(w http.ResponseWriter) {
	workflowrunsapi.WriteError(w, http.StatusBadRequest, "invalid_request", "workflow run request body is invalid")
}

func writeInternalError(w http.ResponseWriter) {
	workflowrunsapi.WriteError(w, http.StatusInternalServerError, "internal_error", "workflow run service failed")
}

func setNextPageHeaders(w http.ResponseWriter, r *http.Request, cursor string, limit int) {
	w.Header().Set("X-Next-Cursor", cursor)
	query := r.URL.Query()
	for key := range query {
		if len(key) > 0 && key[0] == ':' {
			query.Del(key)
		}
	}
	query.Set("cursor", cursor)
	query.Set("limit", strconv.Itoa(limit))
	w.Header().Set("Link", "<"+r.URL.EscapedPath()+"?"+query.Encode()+">; rel=\"next\"")
}

func cloneSnapshotIDs(values map[string]snapshot.SnapshotID) map[string]snapshot.SnapshotID {
	cloned := make(map[string]snapshot.SnapshotID, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneStrings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
