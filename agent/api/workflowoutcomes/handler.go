package workflowoutcomes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/tedsuo/rata"
)

const maximumRequestBytes int64 = 64 << 10

// Match Concourse's established identifier alphabet so every valid workflow
// identity can be addressed through this API. The length bound is API-specific
// and prevents an unbounded path segment from reaching persistence.
var workflowNamePattern = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]{0,127}$`)

type IdentityFunc func(*http.Request) (string, error)

// Authorizer binds the team-scoped route identity to a durable run, one of
// its exact output bindings, and any optional human-modification snapshot.
type Authorizer interface {
	AuthorizeRun(context.Context, int, string, snapshot.WorkflowRunID) (bool, error)
	AuthorizeOutput(context.Context, int, snapshot.WorkflowRunID, snapshot.SnapshotID) (bool, error)
}

type HandlerConfig struct {
	TeamID     int
	TeamName   string
	Identity   IdentityFunc
	Store      Store
	Authorizer Authorizer
}

type Handler struct {
	teamID     int
	teamName   string
	identity   IdentityFunc
	store      Store
	authorizer Authorizer
}

func NewHandler(config HandlerConfig) (*Handler, error) {
	if config.TeamID <= 0 || strings.TrimSpace(config.TeamName) == "" {
		return nil, fmt.Errorf("workflow outcomes API: trusted team is required")
	}
	if config.Identity == nil || nilInterface(config.Store) || nilInterface(config.Authorizer) {
		return nil, fmt.Errorf("workflow outcomes API: identity, store, and authorizer are required")
	}
	return &Handler{
		teamID: config.TeamID, teamName: config.TeamName, identity: config.Identity,
		store: config.Store, authorizer: config.Authorizer,
	}, nil
}

type MutationRequest struct {
	Disposition            Disposition          `json:"disposition"`
	HumanModified          bool                 `json:"human_modified"`
	ModificationSnapshotID *snapshot.SnapshotID `json:"modification_snapshot_id,omitempty"`
	Labels                 []string             `json:"labels,omitempty"`
}

func (handler *Handler) List(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		writeOutcomeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if request.Body != nil {
		data, err := io.ReadAll(io.LimitReader(request.Body, 1))
		if err != nil || len(data) != 0 {
			writeOutcomeError(response, http.StatusBadRequest, "request body must be empty")
			return
		}
	}
	workflowName, runID, ok := parseOutcomeRoute(response, request, false)
	if !ok {
		return
	}
	authorized, err := handler.authorizer.AuthorizeRun(request.Context(), handler.teamID, workflowName, runID)
	if err != nil {
		writeOutcomeError(response, http.StatusInternalServerError, "internal error")
		return
	}
	if !authorized {
		writeOutcomeError(response, http.StatusNotFound, "workflow run not found")
		return
	}
	outcomes, err := handler.store.ListByRun(request.Context(), handler.teamID, runID)
	if err != nil {
		writeOutcomeError(response, http.StatusInternalServerError, "internal error")
		return
	}
	if outcomes == nil {
		outcomes = []Outcome{}
	}
	writeOutcomeJSON(response, http.StatusOK, outcomes)
}

func (handler *Handler) Record(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		response.Header().Set("Allow", http.MethodPut)
		writeOutcomeError(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	workflowName, runID, ok := parseOutcomeRoute(response, request, true)
	if !ok {
		return
	}
	outputID, err := snapshot.ParseSnapshotID(rata.Param(request, "snapshot_id"))
	if err != nil {
		writeOutcomeError(response, http.StatusBadRequest, "invalid output snapshot ID")
		return
	}
	if !requireJSONContentType(request) {
		writeOutcomeError(response, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
	var mutation MutationRequest
	if err := decodeMutation(request, &mutation); err != nil {
		writeOutcomeError(response, http.StatusBadRequest, "invalid outcome request")
		return
	}
	if err := mutation.Disposition.Validate(); err != nil {
		writeOutcomeError(response, http.StatusBadRequest, "invalid disposition")
		return
	}
	authorized, err := handler.authorizer.AuthorizeRun(request.Context(), handler.teamID, workflowName, runID)
	if err != nil {
		writeOutcomeError(response, http.StatusInternalServerError, "internal error")
		return
	}
	if !authorized {
		writeOutcomeError(response, http.StatusNotFound, "workflow run not found")
		return
	}
	authorized, err = handler.authorizer.AuthorizeOutput(request.Context(), handler.teamID, runID, outputID)
	if err != nil {
		writeOutcomeError(response, http.StatusInternalServerError, "internal error")
		return
	}
	if !authorized {
		writeOutcomeError(response, http.StatusNotFound, "workflow output not found")
		return
	}
	actor, err := handler.identity(request)
	if err != nil || !validBoundedText(actor, 256) {
		writeOutcomeError(response, http.StatusUnauthorized, "verified identity required")
		return
	}

	modification := ModifyRequest{
		WorkflowRunID: runID, OutputSnapshotID: outputID,
		Disposition:            mutation.Disposition,
		HumanModified:          mutation.HumanModified,
		ModificationSnapshotID: cloneSnapshotID(mutation.ModificationSnapshotID),
		Labels:                 append([]string(nil), mutation.Labels...), Actor: actor,
	}
	outcome, created, err := handler.store.Modify(request.Context(), handler.teamID, modification)
	if err != nil {
		if errors.Is(err, ErrInvalidOutcome) {
			writeOutcomeError(response, http.StatusBadRequest, "invalid outcome request")
			return
		}
		if errors.Is(err, ErrOutcomeNotFound) {
			writeOutcomeError(response, http.StatusNotFound, "modification snapshot not found")
			return
		}
		writeOutcomeError(response, http.StatusInternalServerError, "internal error")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeOutcomeJSON(response, status, outcome)
}

func parseOutcomeRoute(response http.ResponseWriter, request *http.Request, withOutput bool) (string, snapshot.WorkflowRunID, bool) {
	workflowName := rata.Param(request, "workflow_name")
	if !workflowNamePattern.MatchString(workflowName) {
		writeOutcomeError(response, http.StatusBadRequest, "invalid workflow name")
		return "", 0, false
	}
	runID, err := snapshot.ParseWorkflowRunID(rata.Param(request, "workflow_run_id"))
	if err != nil {
		writeOutcomeError(response, http.StatusBadRequest, "invalid workflow run ID")
		return "", 0, false
	}
	if withOutput && rata.Param(request, "snapshot_id") == "" {
		writeOutcomeError(response, http.StatusBadRequest, "output snapshot ID is required")
		return "", 0, false
	}
	return workflowName, runID, true
}

func decodeMutation(request *http.Request, destination *MutationRequest) error {
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > maximumRequestBytes {
		return errors.New("request body is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func requireJSONContentType(request *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func writeOutcomeJSON(response http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		writeOutcomeError(response, http.StatusInternalServerError, "internal error")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(append(payload, '\n'))
}

func writeOutcomeError(response http.ResponseWriter, status int, message string) {
	writeOutcomeJSONDirect(response, status, map[string]string{"error": http.StatusText(status), "message": message})
}

func writeOutcomeJSONDirect(response http.ResponseWriter, status int, value any) {
	payload, _ := json.Marshal(value)
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
