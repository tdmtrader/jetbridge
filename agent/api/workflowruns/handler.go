package workflowruns

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

const maxRequestBytes int64 = 64 << 10

var originKindPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Handler struct {
	team      TrustedTeam
	identity  IdentityFunc
	binder    Binder
	runs      RunStore
	canceler  Canceler
	manifests ManifestStore
}

func NewHandler(config Config) (*Handler, error) {
	if config.Team.ID <= 0 || config.Team.Name != atc.DefaultTeamName {
		return nil, fmt.Errorf("workflow runs API: a positive main-team identity is required")
	}
	if config.Identity == nil {
		return nil, fmt.Errorf("workflow runs API: request identity is required")
	}
	for name, dependency := range map[string]any{
		"binder": config.Binder, "run store": config.Runs,
		"canceler": config.Canceler, "manifest store": config.Manifests,
	} {
		if interfaceIsNil(dependency) {
			return nil, fmt.Errorf("workflow runs API: %s is required", name)
		}
	}
	return &Handler{
		team: config.Team, identity: config.Identity, binder: config.Binder,
		runs: config.Runs, canceler: config.Canceler, manifests: config.Manifests,
	}, nil
}

func (handler *Handler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	workflowName, _, _, ok := parseRoute(w, r, false, nil)
	if !ok {
		return
	}
	request, ok := decodeCreateRequest(w, r)
	if !ok {
		return
	}
	creator, ok := handler.creator(w, r)
	if !ok {
		return
	}
	result, err := handler.binder.BindAndCreate(r.Context(), workflowrun.AdmissionContext{
		TeamID: handler.team.ID, TeamName: handler.team.Name, CreatedBy: creator,
		Origin: workflowrun.Origin{Kind: "manual"},
	}, workflowrun.BindRequest{
		WorkflowName: workflowName, Version: cloneInt(request.Version),
		Inputs: cloneSnapshotIDs(request.Inputs), IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		writeBinderError(w, err)
		return
	}
	if result.Run.IdempotencyKey != request.IdempotencyKey || result.Run.CreatedBy != creator ||
		result.Run.OriginKind != "manual" || result.Run.OriginReference != "" ||
		result.Run.FunctionID != nil || result.Run.RetryOfWorkflowRunID != nil ||
		request.Version != nil && result.Run.WorkflowVersion != *request.Version {
		writeInternalError(w)
		return
	}
	detail, err := handler.presentDetail(r, workflowName, result.Run)
	if err != nil {
		writeInternalError(w)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, detail)
}

func (handler *Handler) List(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireNoBody(w, r) {
		return
	}
	allowed := map[string]struct{}{
		"status": {}, "origin_kind": {}, "origin_reference": {}, "limit": {},
	}
	workflowName, _, query, ok := parseRoute(w, r, false, allowed)
	if !ok {
		return
	}
	filter, ok := parseListFilter(w, query)
	if !ok {
		return
	}
	filter.TeamID = handler.team.ID
	filter.WorkflowName = workflowName
	runs, err := handler.runs.List(r.Context(), filter)
	if err != nil {
		writeInternalError(w)
		return
	}
	summaries := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		summary, err := handler.presentSummary(workflowName, run)
		if err != nil {
			writeInternalError(w)
			return
		}
		summaries = append(summaries, summary)
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (handler *Handler) Get(w http.ResponseWriter, r *http.Request) {
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
	detail, err := handler.presentDetail(r, workflowName, run)
	if err != nil {
		writeInternalError(w)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (handler *Handler) Outputs(w http.ResponseWriter, r *http.Request) {
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
	outputs, err := handler.presentOutputs(r, run)
	if err != nil {
		writeInternalError(w)
		return
	}
	writeJSON(w, http.StatusOK, OutputsResponse{WorkflowRunID: run.ID, Outputs: outputs})
}

func (handler *Handler) Retry(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	workflowName, sourceID, _, ok := parseRoute(w, r, true, nil)
	if !ok {
		return
	}
	retryRequest, ok := decodeRetryRequest(w, r)
	if !ok {
		return
	}
	source, ok := handler.loadScopedRun(w, r, workflowName, sourceID)
	if !ok {
		return
	}
	if !isTerminal(source.Status) || retryRequest.IdempotencyKey == source.IdempotencyKey {
		writeError(w, http.StatusConflict, "conflict", "workflow run cannot be retried in its current state")
		return
	}
	bindings, err := handler.runs.Snapshots(r.Context(), source.ID)
	if err != nil {
		writeInternalError(w)
		return
	}
	inputs, err := inputIDs(source.ID, bindings)
	if err != nil {
		writeInternalError(w)
		return
	}
	creator, ok := handler.creator(w, r)
	if !ok {
		return
	}
	version := source.WorkflowVersion
	functionID := ""
	if source.FunctionID != nil {
		functionID = *source.FunctionID
	}
	result, err := handler.binder.BindAndCreate(r.Context(), workflowrun.AdmissionContext{
		TeamID: handler.team.ID, TeamName: handler.team.Name, CreatedBy: creator,
		Origin: workflowrun.Origin{Kind: "retry", Reference: source.ID.String()},
	}, workflowrun.BindRequest{
		WorkflowName: workflowName, Version: &version, FunctionID: functionID,
		Inputs: inputs, IdempotencyKey: retryRequest.IdempotencyKey, RetryOf: cloneWorkflowRunID(&source.ID),
	})
	if err != nil {
		writeBinderError(w, err)
		return
	}
	if result.Run.ID == source.ID || result.Run.WorkflowVersion != source.WorkflowVersion ||
		!equalOptionalString(result.Run.FunctionID, source.FunctionID) ||
		result.Run.RetryOfWorkflowRunID == nil || *result.Run.RetryOfWorkflowRunID != source.ID ||
		result.Run.IdempotencyKey != retryRequest.IdempotencyKey || result.Run.CreatedBy != creator ||
		result.Run.OriginKind != "retry" || result.Run.OriginReference != source.ID.String() {
		writeInternalError(w)
		return
	}
	detail, err := handler.presentDetail(r, workflowName, result.Run)
	if err != nil {
		writeInternalError(w)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, detail)
}

func (handler *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) || !requireNoBody(w, r) {
		return
	}
	workflowName, runID, _, ok := parseRoute(w, r, true, nil)
	if !ok {
		return
	}
	if _, ok := handler.loadScopedRun(w, r, workflowName, runID); !ok {
		return
	}
	run, found, err := handler.canceler.Cancel(r.Context(), handler.team.ID, runID)
	if errors.Is(err, ErrCancelConflict) {
		writeError(w, http.StatusConflict, "conflict", "workflow run cannot be canceled in its current state")
		return
	}
	if err != nil {
		writeInternalError(w)
		return
	}
	if !found {
		writeNotFound(w)
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
	detail, err := handler.presentDetail(r, workflowName, run)
	if err != nil {
		writeInternalError(w)
		return
	}
	writeJSON(w, status, detail)
}

func (handler *Handler) creator(w http.ResponseWriter, r *http.Request) (string, bool) {
	creator, err := handler.identity(r)
	if err != nil || validateText(creator, 256, false, true) != nil {
		writeInternalError(w)
		return "", false
	}
	return creator, true
}

func (handler *Handler) loadScopedRun(
	w http.ResponseWriter,
	r *http.Request,
	workflowName string,
	runID snapshot.WorkflowRunID,
) (db.AgentWorkflowRun, bool) {
	run, found, err := handler.runs.Get(r.Context(), handler.team.ID, runID)
	if err != nil {
		writeInternalError(w)
		return db.AgentWorkflowRun{}, false
	}
	if !found || run.ID != runID || run.TeamID != handler.team.ID || run.TeamName != handler.team.Name || run.WorkflowName != workflowName {
		writeNotFound(w)
		return db.AgentWorkflowRun{}, false
	}
	return run, true
}

func (handler *Handler) presentDetail(r *http.Request, workflowName string, run db.AgentWorkflowRun) (RunDetail, error) {
	summary, err := handler.presentSummary(workflowName, run)
	if err != nil {
		return RunDetail{}, err
	}
	bindings, err := handler.runs.Snapshots(r.Context(), run.ID)
	if err != nil {
		return RunDetail{}, err
	}
	inputs, err := presentInputs(run.ID, bindings)
	if err != nil {
		return RunDetail{}, err
	}
	outputs, err := handler.outputsFromBindings(r, run.ID, bindings)
	if err != nil {
		return RunDetail{}, err
	}
	return RunDetail{RunSummary: summary, Inputs: inputs, Outputs: outputs}, nil
}

func (handler *Handler) presentOutputs(r *http.Request, run db.AgentWorkflowRun) ([]OutputManifest, error) {
	bindings, err := handler.runs.Snapshots(r.Context(), run.ID)
	if err != nil {
		return nil, err
	}
	return handler.outputsFromBindings(r, run.ID, bindings)
}

func (handler *Handler) outputsFromBindings(
	r *http.Request,
	runID snapshot.WorkflowRunID,
	bindings []db.AgentWorkflowRunSnapshotBinding,
) ([]OutputManifest, error) {
	outputs := make([]OutputManifest, 0)
	seen := make(map[string]struct{})
	for _, binding := range bindings {
		if err := validateBinding(runID, binding); err != nil {
			return nil, err
		}
		if binding.Direction != db.AgentWorkflowRunSnapshotOutput {
			continue
		}
		if _, duplicate := seen[binding.PortName]; duplicate {
			return nil, fmt.Errorf("duplicate output port")
		}
		seen[binding.PortName] = struct{}{}
		manifest, found, err := handler.manifests.GetAuthorized(r.Context(), handler.team.ID, binding.Snapshot.ID)
		if err != nil || !found {
			return nil, fmt.Errorf("read output manifest")
		}
		if err := manifest.Validate(); err != nil || manifest.ID != binding.Snapshot.ID ||
			manifest.Type != binding.Snapshot.Type || manifest.Digest != binding.Snapshot.Digest {
			return nil, fmt.Errorf("output manifest does not match durable binding")
		}
		outputs = append(outputs, OutputManifest{Port: binding.PortName, Snapshot: manifest.Clone()})
	}
	sort.Slice(outputs, func(left, right int) bool { return outputs[left].Port < outputs[right].Port })
	return outputs, nil
}

func presentInputs(runID snapshot.WorkflowRunID, bindings []db.AgentWorkflowRunSnapshotBinding) ([]InputBinding, error) {
	inputs := make([]InputBinding, 0)
	seen := make(map[string]struct{})
	for _, binding := range bindings {
		if err := validateBinding(runID, binding); err != nil {
			return nil, err
		}
		if binding.Direction != db.AgentWorkflowRunSnapshotInput {
			continue
		}
		if _, duplicate := seen[binding.PortName]; duplicate {
			return nil, fmt.Errorf("duplicate input port")
		}
		seen[binding.PortName] = struct{}{}
		inputs = append(inputs, InputBinding{Port: binding.PortName, Snapshot: binding.Snapshot})
	}
	sort.Slice(inputs, func(left, right int) bool { return inputs[left].Port < inputs[right].Port })
	return inputs, nil
}

func inputIDs(runID snapshot.WorkflowRunID, bindings []db.AgentWorkflowRunSnapshotBinding) (map[string]snapshot.SnapshotID, error) {
	inputs, err := presentInputs(runID, bindings)
	if err != nil {
		return nil, err
	}
	result := make(map[string]snapshot.SnapshotID, len(inputs))
	for _, input := range inputs {
		result[input.Port] = input.Snapshot.ID
	}
	return result, nil
}

func validateBinding(runID snapshot.WorkflowRunID, binding db.AgentWorkflowRunSnapshotBinding) error {
	if binding.WorkflowRunID != runID || binding.Direction != db.AgentWorkflowRunSnapshotInput && binding.Direction != db.AgentWorkflowRunSnapshotOutput ||
		validateIdentifier(binding.PortName) != nil {
		return fmt.Errorf("invalid durable snapshot binding")
	}
	return binding.Snapshot.Validate()
}

func (handler *Handler) presentSummary(workflowName string, run db.AgentWorkflowRun) (RunSummary, error) {
	if run.TeamID != handler.team.ID || run.TeamName != handler.team.Name || run.WorkflowName != workflowName ||
		run.WorkflowDefinitionID <= 0 || run.WorkflowVersion <= 0 || run.SchemaVersion <= 0 || run.SignatureVersion <= 0 ||
		run.ID.Validate() != nil || validateIdentifier(run.WorkflowName) != nil ||
		validateText(run.DefinitionContentHash, 1024, false, true) != nil ||
		validateText(run.ParameterizedConfigHash, 1024, false, true) != nil ||
		validateStatus(run.Status) != nil || run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() ||
		validateText(run.OriginKind, 64, false, true) != nil || !originKindPattern.MatchString(run.OriginKind) ||
		validateText(run.OriginReference, 1024, true, false) != nil ||
		validateText(run.CreatedBy, 256, false, true) != nil {
		return RunSummary{}, fmt.Errorf("invalid durable workflow run")
	}
	if run.FunctionID != nil && validateIdentifier(*run.FunctionID) != nil ||
		run.ExecutionStatus != nil && validateExecutionStatus(*run.ExecutionStatus) != nil ||
		run.RetryOfWorkflowRunID != nil && run.RetryOfWorkflowRunID.Validate() != nil ||
		run.PipelineRunID != nil && *run.PipelineRunID <= 0 ||
		run.PlannedBuildID != nil && *run.PlannedBuildID <= 0 ||
		run.ConcreteConfigHash != nil && validateText(*run.ConcreteConfigHash, 1024, false, true) != nil ||
		run.ActualPlanHash != nil && validateText(*run.ActualPlanHash, 1024, false, true) != nil ||
		run.StartedAt != nil && run.StartedAt.IsZero() || run.CompletedAt != nil && run.CompletedAt.IsZero() {
		return RunSummary{}, fmt.Errorf("invalid optional durable workflow-run state")
	}
	return RunSummary{
		WorkflowRunID: run.ID, PipelineRunID: cloneInt(run.PipelineRunID),
		WorkflowName: run.WorkflowName, WorkflowVersion: run.WorkflowVersion,
		SchemaVersion: run.SchemaVersion, SignatureVersion: run.SignatureVersion,
		DefinitionContentHash: run.DefinitionContentHash, FunctionID: cloneString(run.FunctionID),
		Status: run.Status, ExecutionStatus: cloneExecutionStatus(run.ExecutionStatus),
		OriginKind: run.OriginKind, OriginReference: run.OriginReference, CreatedBy: run.CreatedBy,
		RetryOfWorkflowRunID: cloneWorkflowRunID(run.RetryOfWorkflowRunID),
		CreatedAt:            run.CreatedAt, UpdatedAt: run.UpdatedAt,
		StartedAt: cloneTime(run.StartedAt), CompletedAt: cloneTime(run.CompletedAt),
		ParameterizedConfigHash: run.ParameterizedConfigHash,
		InstanceConfigHash:      cloneString(run.ConcreteConfigHash), ActualPlanHash: cloneString(run.ActualPlanHash),
		PlannedBuildID: cloneInt64(run.PlannedBuildID),
	}, nil
}

func parseRoute(
	w http.ResponseWriter,
	r *http.Request,
	withRunID bool,
	allowedPublic map[string]struct{},
) (string, snapshot.WorkflowRunID, url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
		return "", 0, nil, false
	}
	for key, values := range query {
		_, public := allowedPublic[key]
		path := key == ":workflow_name" || withRunID && key == ":workflow_run_id"
		if !public && !path {
			writeError(w, http.StatusBadRequest, "invalid_query", "request query contains unsupported fields")
			return "", 0, nil, false
		}
		if len(values) != 1 || values[0] == "" {
			writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
			return "", 0, nil, false
		}
	}
	if values := query[":workflow_name"]; len(values) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_workflow", "workflow name is invalid")
		return "", 0, nil, false
	}
	// rata.Param is intentional: path identity must not be read through
	// Request.FormValue, where form/query values can shadow routed identity.
	workflowName := rata.Param(r, "workflow_name")
	if validateIdentifier(workflowName) != nil {
		writeError(w, http.StatusBadRequest, "invalid_workflow", "workflow name is invalid")
		return "", 0, nil, false
	}
	if !withRunID {
		return workflowName, 0, query, true
	}
	if values := query[":workflow_run_id"]; len(values) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_workflow_run_id", "workflow run ID is invalid")
		return "", 0, nil, false
	}
	runID, err := snapshot.ParseWorkflowRunID(rata.Param(r, "workflow_run_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_workflow_run_id", "workflow run ID is invalid")
		return "", 0, nil, false
	}
	return workflowName, runID, query, true
}

func parseListFilter(w http.ResponseWriter, query url.Values) (db.AgentWorkflowRunListFilter, bool) {
	filter := db.AgentWorkflowRunListFilter{Limit: 100}
	if raw, present := query["status"]; present {
		status := db.AgentWorkflowRunStatus(raw[0])
		if validateStatus(status) != nil {
			writeError(w, http.StatusBadRequest, "invalid_status", "workflow run status filter is invalid")
			return db.AgentWorkflowRunListFilter{}, false
		}
		filter.Status = status
	}
	if raw, present := query["origin_kind"]; present {
		if !originKindPattern.MatchString(raw[0]) {
			writeError(w, http.StatusBadRequest, "invalid_origin", "workflow run origin filter is invalid")
			return db.AgentWorkflowRunListFilter{}, false
		}
		filter.OriginKind = raw[0]
	}
	if raw, present := query["origin_reference"]; present {
		if validateText(raw[0], 1024, false, false) != nil {
			writeError(w, http.StatusBadRequest, "invalid_origin", "workflow run origin filter is invalid")
			return db.AgentWorkflowRunListFilter{}, false
		}
		filter.OriginReference = raw[0]
	}
	if raw, present := query["limit"]; present {
		limit, err := parseCanonicalPositiveInt(raw[0], 1000)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a decimal integer from 1 to 1000")
			return db.AgentWorkflowRunListFilter{}, false
		}
		filter.Limit = limit
	}
	return filter, true
}

func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (CreateRequest, bool) {
	raw, ok := readStrictJSONBody(w, r)
	if !ok {
		return CreateRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		writeInvalidBody(w)
		return CreateRequest{}, false
	}
	request := CreateRequest{Inputs: map[string]snapshot.SnapshotID{}}
	seen := map[string]struct{}{}
	keyPresent := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, keyOK := keyToken.(string)
		if err != nil || !keyOK {
			writeInvalidBody(w)
			return CreateRequest{}, false
		}
		if _, duplicate := seen[key]; duplicate {
			writeInvalidBody(w)
			return CreateRequest{}, false
		}
		seen[key] = struct{}{}
		switch key {
		case "version":
			var value int
			if err := decoder.Decode(&value); err != nil || value <= 0 {
				writeInvalidBody(w)
				return CreateRequest{}, false
			}
			request.Version = &value
		case "inputs":
			inputs, err := decodeInputs(decoder)
			if err != nil {
				writeInvalidBody(w)
				return CreateRequest{}, false
			}
			request.Inputs = inputs
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
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || requireJSONEOF(decoder) != nil ||
		!keyPresent || validateText(request.IdempotencyKey, 256, false, true) != nil {
		writeInvalidBody(w)
		return CreateRequest{}, false
	}
	return request, true
}

func decodeInputs(decoder *json.Decoder) (map[string]snapshot.SnapshotID, error) {
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("inputs must be an object")
	}
	inputs := map[string]snapshot.SnapshotID{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		port, ok := keyToken.(string)
		if err != nil || !ok || validateIdentifier(port) != nil {
			return nil, fmt.Errorf("invalid input port")
		}
		if _, duplicate := inputs[port]; duplicate {
			return nil, fmt.Errorf("duplicate input port")
		}
		var id snapshot.SnapshotID
		if err := decoder.Decode(&id); err != nil {
			return nil, err
		}
		inputs[port] = id
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("inputs object is not terminated")
	}
	return inputs, nil
}

func decodeRetryRequest(w http.ResponseWriter, r *http.Request) (RetryRequest, bool) {
	raw, ok := readStrictJSONBody(w, r)
	if !ok {
		return RetryRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		writeInvalidBody(w)
		return RetryRequest{}, false
	}
	request := RetryRequest{}
	seenKey := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, keyOK := keyToken.(string)
		if err != nil || !keyOK || key != "idempotency_key" || seenKey {
			writeInvalidBody(w)
			return RetryRequest{}, false
		}
		seenKey = true
		if err := decoder.Decode(&request.IdempotencyKey); err != nil {
			writeInvalidBody(w)
			return RetryRequest{}, false
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || requireJSONEOF(decoder) != nil ||
		!seenKey || validateText(request.IdempotencyKey, 256, false, true) != nil {
		writeInvalidBody(w)
		return RetryRequest{}, false
	}
	return request, true
}

func readStrictJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if !requireJSONMediaType(w, r) {
		return nil, false
	}
	if len(r.Header.Values("Content-Encoding")) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding", "request content encoding is not supported")
		return nil, false
	}
	if r.ContentLength > maxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "workflow run request exceeds the configured limit")
		return nil, false
	}
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxRequestBytes+1))
	if err != nil {
		writeInvalidBody(w)
		return nil, false
	}
	if int64(len(raw)) > maxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "workflow run request exceeds the configured limit")
		return nil, false
	}
	if len(raw) == 0 || !utf8.Valid(raw) {
		writeInvalidBody(w)
		return nil, false
	}
	return raw, true
}

func requireJSONMediaType(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request media type is not supported")
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") || len(parameters) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request media type is not supported")
		return false
	}
	return true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "the request method is not allowed")
	return false
}

func requireNoBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 ||
		r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody {
		writeError(w, http.StatusBadRequest, "unexpected_body", "request body is not allowed")
		return false
	}
	return true
}

func writeBinderError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workflowrun.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", "workflow run request is invalid")
	case errors.Is(err, workflowrun.ErrDefinitionOrTargetNotFound):
		writeError(w, http.StatusNotFound, "not_found", "workflow or workflow version was not found")
	case errors.Is(err, workflowrun.ErrLegacyDefinition):
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "workflow version is not executable")
	case errors.Is(err, workflowrun.ErrSnapshotUnavailable), errors.Is(err, workflowrun.ErrSnapshotTypeMismatch):
		writeError(w, http.StatusUnprocessableEntity, "inputs_unavailable", "one or more inputs are unavailable or unauthorized")
	case errors.Is(err, workflowrun.ErrBudgetDenied):
		writeError(w, http.StatusUnprocessableEntity, "admission_denied", "workflow run admission was denied")
	case errors.Is(err, workflowrun.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "conflict", "idempotency key conflicts with immutable workflow-run state")
	default:
		writeInternalError(w)
	}
}

func writeInvalidBody(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_request", "workflow run request body is invalid")
}

func writeNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "not_found", "workflow run was not found")
}

func writeInternalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "internal_error", "workflow run service failed")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":"internal_error","message":"workflow run service failed"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func validateStatus(status db.AgentWorkflowRunStatus) error {
	switch status {
	case db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning,
		db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusCanceling,
		db.AgentWorkflowRunStatusAborted:
		return nil
	default:
		return fmt.Errorf("invalid status")
	}
}

func validateExecutionStatus(status db.AgentWorkflowRunExecutionStatus) error {
	switch status {
	case db.AgentWorkflowRunExecutionStatusSucceeded, db.AgentWorkflowRunExecutionStatusFailed,
		db.AgentWorkflowRunExecutionStatusErrored, db.AgentWorkflowRunExecutionStatusAborted:
		return nil
	default:
		return fmt.Errorf("invalid execution status")
	}
}

func isTerminal(status db.AgentWorkflowRunStatus) bool {
	switch status {
	case db.AgentWorkflowRunStatusSucceeded, db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored, db.AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

func validateIdentifier(value string) error {
	warning, err := atc.ValidateIdentifier(value)
	if err != nil || warning != nil && warning.Type == "invalid_identifier" {
		return fmt.Errorf("invalid identifier")
	}
	return nil
}

func validateText(value string, maximum int, allowEmpty, requireTrimmed bool) error {
	if !utf8.ValidString(value) || len(value) > maximum || !allowEmpty && strings.TrimSpace(value) == "" ||
		requireTrimmed && strings.TrimSpace(value) != value {
		return fmt.Errorf("invalid text")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("invalid text")
		}
	}
	return nil
}

func parseCanonicalPositiveInt(raw string, maximum int) (int, error) {
	if raw == "" || raw[0] == '0' {
		return 0, fmt.Errorf("not a canonical positive integer")
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("not a canonical positive integer")
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("outside allowed range")
	}
	return value, nil
}

func cloneSnapshotIDs(values map[string]snapshot.SnapshotID) map[string]snapshot.SnapshotID {
	cloned := make(map[string]snapshot.SnapshotID, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneWorkflowRunID(value *snapshot.WorkflowRunID) *snapshot.WorkflowRunID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneExecutionStatus(value *db.AgentWorkflowRunExecutionStatus) *db.AgentWorkflowRunExecutionStatus {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
