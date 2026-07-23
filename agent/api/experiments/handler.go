package experiments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/pagination"
)

const maximumExperimentRequestBytes int64 = 1 << 20

type IdentityFunc func(*http.Request) (string, error)

type Config struct {
	TeamID          int
	TeamName        string
	Identity        IdentityFunc
	Store           Store
	RunnerAvailable bool
}

type Handler struct {
	teamID          int
	teamName        string
	identity        IdentityFunc
	store           Store
	runnerAvailable bool
}

func NewHandler(config Config) (*Handler, error) {
	if config.TeamID <= 0 || strings.TrimSpace(config.TeamName) == "" {
		return nil, fmt.Errorf("experiments API: trusted team is required")
	}
	if config.Identity == nil || nilInterface(config.Store) {
		return nil, fmt.Errorf("experiments API: identity and store are required")
	}
	return &Handler{
		teamID: config.TeamID, teamName: config.TeamName, identity: config.Identity,
		store: config.Store, runnerAvailable: config.RunnerAvailable,
	}, nil
}

func (handler *Handler) Create(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	var definition experiment.Definition
	if !decodeExperimentJSON(response, request, &definition) {
		return
	}
	if err := validateDraft(definition); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	actor, ok := handler.actor(response, request)
	if !ok {
		return
	}
	stored, err := handler.store.Create(request.Context(), handler.teamID, handler.teamName, actor, definition)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeStoredExperiment(response, http.StatusCreated, stored)
}

type updateRequest struct {
	Revision   int64                 `json:"revision"`
	Definition experiment.Definition `json:"definition"`
}

type revisionRequest struct {
	Revision int64 `json:"revision"`
}

func (handler *Handler) Update(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPut) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	var mutation updateRequest
	if !decodeExperimentJSON(response, request, &mutation) {
		return
	}
	if mutation.Revision <= 0 {
		writeError(response, http.StatusBadRequest, "revision must be positive")
		return
	}
	if err := validateDraft(mutation.Definition); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	actor, ok := handler.actor(response, request)
	if !ok {
		return
	}
	stored, err := handler.store.Update(request.Context(), handler.teamID, id, mutation.Revision, actor, mutation.Definition)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeStoredExperiment(response, http.StatusOK, stored)
}

func (handler *Handler) Validate(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	revision, ok := decodeRevision(response, request)
	if !ok {
		return
	}
	if !handler.requireRunner(response) {
		return
	}
	stored, err := handler.store.PreflightStart(request.Context(), handler.teamID, id, revision)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, ValidationResult{
		Valid: true, Revision: stored.Revision, ExpectedCells: stored.Definition.ExpectedCells(),
	})
}

func (handler *Handler) Start(response http.ResponseWriter, request *http.Request) {
	if !handler.requireRunner(response) {
		return
	}
	handler.transition(response, request, "start", handler.store.Start)
}

func (handler *Handler) requireRunner(response http.ResponseWriter) bool {
	if handler.runnerAvailable {
		return true
	}
	writeError(response, http.StatusServiceUnavailable, "experiment runner is not enabled for this deployment")
	return false
}

func (handler *Handler) Cancel(response http.ResponseWriter, request *http.Request) {
	handler.transition(response, request, "cancel", handler.store.Cancel)
}

func (handler *Handler) transition(
	response http.ResponseWriter,
	request *http.Request,
	_ string,
	transition func(context.Context, int, experiment.ID, int64, string) (StoredExperiment, error),
) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	revision, ok := decodeRevision(response, request)
	if !ok {
		return
	}
	actor, ok := handler.actor(response, request)
	if !ok {
		return
	}
	stored, err := transition(request.Context(), handler.teamID, id, revision, actor)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeStoredExperiment(response, http.StatusOK, stored)
}

func (handler *Handler) List(response http.ResponseWriter, request *http.Request) {
	if !requireRead(response, request) {
		return
	}
	filter, pageSize, err := parseExperimentListFilter(request)
	if err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return
	}
	pagedStore, ok := handler.store.(experiment.PagedStore)
	if !ok {
		writeError(response, http.StatusInternalServerError, "experiment store does not support durable pagination")
		return
	}
	values, err := pagedStore.ListPage(request.Context(), handler.teamID, filter)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	if len(values) > pageSize+1 {
		writeError(response, http.StatusInternalServerError, "experiment service returned too many experiments")
		return
	}
	if len(values) > pageSize {
		values = values[:pageSize]
		last := values[len(values)-1]
		cursor, cursorErr := pagination.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: int64(last.ID)})
		if cursorErr != nil {
			writeError(response, http.StatusInternalServerError, "experiment service returned an invalid page boundary")
			return
		}
		response.Header().Set("X-Next-Cursor", cursor)
		query := request.URL.Query()
		for key := range query {
			if strings.HasPrefix(key, ":") {
				query.Del(key)
			}
		}
		query.Set("cursor", cursor)
		query.Set("limit", strconv.Itoa(pageSize))
		response.Header().Set("Link", "<"+request.URL.EscapedPath()+"?"+query.Encode()+`>; rel="next"`)
	}
	if values == nil {
		values = []StoredExperiment{}
	}
	writeJSON(response, http.StatusOK, values)
}

func parseExperimentListFilter(request *http.Request) (experiment.ListFilter, int, error) {
	pageSize := experiment.MaxListedExperiments
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return experiment.ListFilter{}, 0, fmt.Errorf("experiment list query is malformed: %w", err)
	}
	for key := range query {
		if strings.HasPrefix(key, ":") {
			continue
		}
		if key != "limit" && key != "cursor" {
			return experiment.ListFilter{}, 0, fmt.Errorf("unknown experiment list query parameter %q", key)
		}
	}
	if raw, present := query["limit"]; present {
		if len(raw) != 1 {
			return experiment.ListFilter{}, 0, fmt.Errorf("limit must be specified once")
		}
		parsed, err := strconv.Atoi(raw[0])
		if err != nil || parsed <= 0 || parsed > experiment.MaxListedExperiments {
			return experiment.ListFilter{}, 0, fmt.Errorf(
				"limit must be an integer between 1 and %d", experiment.MaxListedExperiments,
			)
		}
		pageSize = parsed
	}
	filter := experiment.ListFilter{Limit: pageSize + 1}
	if raw, present := query["cursor"]; present {
		if len(raw) != 1 {
			return experiment.ListFilter{}, 0, fmt.Errorf("cursor must be specified once")
		}
		cursor, err := pagination.Decode(raw[0])
		if err != nil {
			return experiment.ListFilter{}, 0, fmt.Errorf("cursor is invalid: %w", err)
		}
		filter.Before = &cursor
	}
	return filter, pageSize, nil
}

func (handler *Handler) Get(response http.ResponseWriter, request *http.Request) {
	if !requireRead(response, request) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	stored, found, err := handler.store.Get(request.Context(), handler.teamID, id)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	if !found {
		writeStoreError(response, ErrNotFound)
		return
	}
	writeStoredExperiment(response, http.StatusOK, stored)
}

func (handler *Handler) ListCells(response http.ResponseWriter, request *http.Request) {
	if !requireRead(response, request) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	values, err := handler.store.ListCells(request.Context(), handler.teamID, id)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	if len(values) > experiment.MaxMaterializedCells {
		writeError(response, http.StatusInternalServerError, "experiment service returned too many cells")
		return
	}
	if values == nil {
		values = []StoredCell{}
	}
	writeJSON(response, http.StatusOK, values)
}

func (handler *Handler) GetCell(response http.ResponseWriter, request *http.Request) {
	if !requireRead(response, request) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	cellID, ok := parseCellID(response, request)
	if !ok {
		return
	}
	value, found, err := handler.store.GetCell(request.Context(), handler.teamID, id, cellID)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	if !found {
		writeStoreError(response, ErrNotFound)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (handler *Handler) Scorecard(response http.ResponseWriter, request *http.Request) {
	if !requireRead(response, request) {
		return
	}
	id, ok := parseExperimentID(response, request)
	if !ok {
		return
	}
	value, err := handler.store.Scorecard(request.Context(), handler.teamID, id)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func validateDraft(definition experiment.Definition) error {
	if definition.State != experiment.StateDraft {
		return fmt.Errorf("experiment state must be draft")
	}
	return definition.Validate()
}

func decodeRevision(response http.ResponseWriter, request *http.Request) (int64, bool) {
	var mutation revisionRequest
	if !decodeExperimentJSON(response, request, &mutation) {
		return 0, false
	}
	if mutation.Revision <= 0 {
		writeError(response, http.StatusBadRequest, "revision must be positive")
		return 0, false
	}
	return mutation.Revision, true
}

func (handler *Handler) actor(response http.ResponseWriter, request *http.Request) (string, bool) {
	actor, err := handler.identity(request)
	if err != nil || actor != strings.TrimSpace(actor) || actor == "" || len(actor) > 256 {
		writeError(response, http.StatusUnauthorized, "verified identity required")
		return "", false
	}
	return actor, true
}

func parseExperimentID(response http.ResponseWriter, request *http.Request) (experiment.ID, bool) {
	value, err := parsePositiveID(request.FormValue(":experiment_id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid experiment ID")
		return 0, false
	}
	return experiment.ID(value), true
}

func parseCellID(response http.ResponseWriter, request *http.Request) (experiment.CellID, bool) {
	value, err := parsePositiveID(request.FormValue(":cell_id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid experiment cell ID")
		return 0, false
	}
	return experiment.CellID(value), true
}

func parsePositiveID(raw string) (int64, error) {
	if raw == "" || raw[0] == '+' || (len(raw) > 1 && raw[0] == '0') {
		return 0, errors.New("invalid ID")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid ID")
	}
	return value, nil
}

func decodeExperimentJSON(response http.ResponseWriter, request *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "content type must be application/json")
		return false
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumExperimentRequestBytes+1))
	if err != nil || int64(len(payload)) > maximumExperimentRequestBytes {
		writeError(response, http.StatusBadRequest, "invalid experiment request")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(response, http.StatusBadRequest, err.Error())
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, "request must contain one JSON value")
		return false
	}
	return true
}

func requireMethod(response http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	response.Header().Set("Allow", method)
	writeError(response, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func requireRead(response http.ResponseWriter, request *http.Request) bool {
	if !requireMethod(response, request, http.MethodGet) {
		return false
	}
	if request.Body == nil {
		return true
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil || len(payload) != 0 {
		writeError(response, http.StatusBadRequest, "request body must be empty")
		return false
	}
	return true
}

func writeStoredExperiment(response http.ResponseWriter, status int, stored StoredExperiment) {
	response.Header().Set("ETag", fmt.Sprintf(`"%d"`, stored.Revision))
	writeJSON(response, status, stored)
}

func writeStoreError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(response, http.StatusNotFound, ErrNotFound.Error())
	case errors.Is(err, ErrRevisionConflict), errors.Is(err, ErrImmutable), errors.Is(err, ErrScorecardUnavailable):
		writeError(response, http.StatusConflict, err.Error())
	case errors.Is(err, ErrInvalidDefinition):
		writeError(response, http.StatusBadRequest, err.Error())
	default:
		writeError(response, http.StatusInternalServerError, "experiment service failed")
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, map[string]string{"error": http.StatusText(status), "message": message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":"Internal Server Error","message":"experiment service failed"}`)
		status = http.StatusInternalServerError
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(append(payload, '\n'))
}

func nilInterface(value any) bool {
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
