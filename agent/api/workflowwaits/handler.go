package workflowwaits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/tedsuo/rata"
)

const (
	maximumRequestBytes  int64 = 64 << 10
	maximumDocumentBytes int64 = 1 << 20
)

var workflowNamePattern = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]{0,127}$`)

type TrustedTeam struct {
	ID   int
	Name string
}

type RequestIdentity struct {
	Actor       string
	DisplayName string
}

type IdentityFunc func(*http.Request) (RequestIdentity, error)

type RunStore interface {
	Get(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
}

type ManifestStore interface {
	GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
}

type ContentStore interface {
	Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error)
}

type SnapshotCreator interface {
	Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error)
}

type Config struct {
	Team          TrustedTeam
	Identity      IdentityFunc
	Runs          RunStore
	Waits         workflowwait.Store
	Manifests     ManifestStore
	Content       ContentStore
	Creator       SnapshotCreator
	ArchiveLimits snapshot.ArchiveLimits
	TempDir       string
}

type Handler struct {
	team          TrustedTeam
	identity      IdentityFunc
	runs          RunStore
	waits         workflowwait.Store
	manifests     ManifestStore
	content       ContentStore
	creator       SnapshotCreator
	canonicalizer snapshot.Canonicalizer
}

func NewHandler(config Config) (*Handler, error) {
	if config.Team.ID <= 0 || config.Team.Name != atc.DefaultTeamName {
		return nil, fmt.Errorf("workflow waits API: a positive main-team identity is required")
	}
	if config.Identity == nil || interfaceIsNil(config.Runs) || interfaceIsNil(config.Waits) ||
		interfaceIsNil(config.Manifests) {
		return nil, fmt.Errorf("workflow waits API: identity, run store, wait store, and manifest store are required")
	}
	return &Handler{
		team: config.Team, identity: config.Identity, runs: config.Runs,
		waits: config.Waits, manifests: config.Manifests, content: config.Content, creator: config.Creator,
		canonicalizer: snapshot.Canonicalizer{
			MaxEntries:      config.ArchiveLimits.MaxEntries,
			MaxContentBytes: config.ArchiveLimits.MaxContentBytes,
			TempDir:         config.TempDir,
		},
	}, nil
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type PublicWait struct {
	ID                    workflowwait.ID            `json:"id"`
	OutputName            string                     `json:"output_name"`
	QuestionName          string                     `json:"question_name"`
	Question              snapshot.SnapshotRef       `json:"question"`
	Prompt                string                     `json:"prompt"`
	Context               string                     `json:"context"`
	Options               []string                   `json:"options,omitempty"`
	Default               string                     `json:"default,omitempty"`
	ExpectedType          snapshot.TypeRef           `json:"expected_type"`
	Deadline              time.Time                  `json:"deadline"`
	TimeoutPolicy         workflowwait.TimeoutPolicy `json:"timeout_policy"`
	HasDefault            bool                       `json:"has_default"`
	WorkflowPort          string                     `json:"workflow_port,omitempty"`
	Status                workflowwait.Status        `json:"status"`
	Answer                *snapshot.SnapshotRef      `json:"answer,omitempty"`
	ResolvedBy            string                     `json:"resolved_by,omitempty"`
	ResolvedByDisplayName string                     `json:"resolved_by_display_name,omitempty"`
	ResolutionSource      string                     `json:"resolution_source,omitempty"`
	CreatedAt             time.Time                  `json:"created_at"`
	UpdatedAt             time.Time                  `json:"updated_at"`
	ResolvedAt            *time.Time                 `json:"resolved_at,omitempty"`
}

type ListResponse struct {
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id"`
	Waits         []PublicWait           `json:"waits"`
}

type ResolveRequest struct {
	Answer     *string              `json:"answer,omitempty"`
	SnapshotID *snapshot.SnapshotID `json:"snapshot_id,omitempty"`
}

func (handler *Handler) List(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if !requireNoBody(response, request) {
		return
	}
	workflowName, runID, _, ok := parseRoute(response, request, false)
	if !ok || !handler.loadRun(response, request, workflowName, runID) {
		return
	}
	waits, err := handler.waits.List(request.Context(), handler.team.ID, runID)
	if err != nil {
		writeInternalError(response)
		return
	}
	public := make([]PublicWait, 0, len(waits))
	for _, wait := range waits {
		question, err := handler.readQuestion(request.Context(), wait)
		if err != nil {
			writeInternalError(response)
			return
		}
		projected, err := presentWait(handler.team.ID, runID, wait, question)
		if err != nil {
			writeInternalError(response)
			return
		}
		public = append(public, projected)
	}
	writeJSON(response, http.StatusOK, ListResponse{WorkflowRunID: runID, Waits: public})
}

func (handler *Handler) Resolve(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		response.Header().Set("Allow", http.MethodPut)
		writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	workflowName, runID, waitID, ok := parseRoute(response, request, true)
	if !ok || !handler.loadRun(response, request, workflowName, runID) {
		return
	}
	wait, found, err := handler.waits.Get(request.Context(), handler.team.ID, runID, waitID)
	if err != nil {
		writeInternalError(response)
		return
	}
	if !found || wait.ID != waitID || wait.Key.TeamID != handler.team.ID || wait.Key.WorkflowRunID != runID {
		writeNotFound(response)
		return
	}
	resolve, ok := decodeResolveRequest(response, request)
	if !ok {
		return
	}
	identity, err := handler.identity(request)
	if err != nil || !validActor(identity.Actor) || !validDisplayName(identity.DisplayName) {
		writeError(response, http.StatusUnauthorized, "unauthorized", "verified identity required")
		return
	}
	question, err := handler.readQuestion(request.Context(), wait)
	if err != nil {
		writeInternalError(response)
		return
	}
	answerValue := ""
	if resolve.Answer != nil {
		answerValue = *resolve.Answer
	} else {
		manifest, found, err := handler.manifests.GetAuthorized(
			request.Context(),
			handler.team.ID,
			*resolve.SnapshotID,
		)
		if err != nil {
			writeInternalError(response)
			return
		}
		if !found || manifest.Validate() != nil || manifest.ID != *resolve.SnapshotID ||
			manifest.Type != snapshot.TypeRef("human-answer/v1") ||
			manifest.ContentState != snapshot.ContentStateAvailable {
			writeNotFound(response)
			return
		}
		document, err := handler.readHumanAnswer(request.Context(), manifest)
		if err != nil || document.TimedOut || document.AnsweredBy != identity.Actor {
			writeError(response, http.StatusBadRequest, "invalid_answer", "answer snapshot is not valid for this identity")
			return
		}
		answerValue = document.Answer
	}
	if !questionAcceptsAnswer(question, answerValue) {
		writeError(response, http.StatusBadRequest, "invalid_answer", "answer is not allowed by the sealed question")
		return
	}
	reservedWait, intent, found, err := handler.waits.ReserveResolution(
		request.Context(),
		workflowwait.ReserveResolutionRequest{
			TeamID:        handler.team.ID,
			WorkflowRunID: runID,
			WaitID:        waitID,
			AnswerValue:   answerValue,
			Actor:         identity.Actor,
			DisplayName:   identity.DisplayName,
		},
	)
	if !handler.handleResolutionError(response, found, err) {
		return
	}
	if reservedWait.Status == workflowwait.StatusResolved {
		projected, err := presentWait(handler.team.ID, runID, reservedWait, question)
		if err != nil {
			writeInternalError(response)
			return
		}
		writeJSON(response, http.StatusOK, projected)
		return
	}
	manifest, err := workflowwait.MaterializeAnswer(
		request.Context(),
		handler.creator,
		handler.team.ID,
		handler.team.Name,
		runID,
		waitID,
		intent,
	)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "snapshot_unavailable", "answer snapshot could not be sealed")
		return
	}
	answerRef := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	resolved, found, err := handler.waits.Resolve(request.Context(), workflowwait.ResolveRequest{
		TeamID:        handler.team.ID,
		WorkflowRunID: runID,
		WaitID:        waitID,
		Answer:        answerRef,
		AnswerValue:   intent.AnswerValue,
		Actor:         intent.Actor,
		DisplayName:   intent.DisplayName,
		ReservedAt:    intent.ReservedAt,
	})
	if !handler.handleResolutionError(response, found, err) {
		return
	}
	projected, err := presentWait(handler.team.ID, runID, resolved, question)
	if err != nil {
		writeInternalError(response)
		return
	}
	writeJSON(response, http.StatusOK, projected)
}

func (handler *Handler) handleResolutionError(response http.ResponseWriter, found bool, err error) bool {
	if errors.Is(err, workflowwait.ErrConflict) {
		writeError(response, http.StatusConflict, "conflict", "workflow wait has already reached a terminal state")
		return false
	}
	if errors.Is(err, workflowwait.ErrExpired) {
		writeError(response, http.StatusConflict, "expired", "workflow wait deadline has passed")
		return false
	}
	if errors.Is(err, workflowwait.ErrUnavailable) || !found {
		writeNotFound(response)
		return false
	}
	if err != nil {
		writeInternalError(response)
		return false
	}
	return true
}

func (handler *Handler) loadRun(response http.ResponseWriter, request *http.Request, workflowName string, runID snapshot.WorkflowRunID) bool {
	run, found, err := handler.runs.Get(request.Context(), handler.team.ID, runID)
	if err != nil {
		writeInternalError(response)
		return false
	}
	if !found || run.ID != runID || run.TeamID != handler.team.ID || run.TeamName != handler.team.Name || run.WorkflowName != workflowName {
		writeNotFound(response)
		return false
	}
	return true
}

func presentWait(
	teamID int,
	runID snapshot.WorkflowRunID,
	wait workflowwait.Wait,
	question contracts.QuestionDocument,
) (PublicWait, error) {
	if err := wait.Validate(); err != nil || wait.Key.TeamID != teamID ||
		wait.Key.WorkflowRunID != runID || question.Validate() != nil {
		return PublicWait{}, fmt.Errorf("workflow waits API: invalid durable wait")
	}
	return PublicWait{
		ID: wait.ID, OutputName: wait.Key.OutputName, QuestionName: wait.QuestionName,
		Question: wait.Question, Prompt: question.Prompt, Context: question.Context,
		Options: append([]string(nil), question.Options...), Default: question.Default,
		ExpectedType: wait.ExpectedType, Deadline: wait.Deadline,
		TimeoutPolicy: wait.TimeoutPolicy, HasDefault: wait.Default != nil, WorkflowPort: wait.WorkflowPort,
		Status: wait.Status, Answer: cloneRef(wait.Answer), ResolvedBy: wait.ResolvedBy,
		ResolvedByDisplayName: wait.ResolvedByDisplayName,
		ResolutionSource:      wait.ResolutionSource, CreatedAt: wait.CreatedAt, UpdatedAt: wait.UpdatedAt,
		ResolvedAt: cloneTime(wait.ResolvedAt),
	}, nil
}

func (handler *Handler) readQuestion(
	ctx context.Context,
	wait workflowwait.Wait,
) (contracts.QuestionDocument, error) {
	manifest, found, err := handler.manifests.GetAuthorized(ctx, handler.team.ID, wait.Question.ID)
	if err != nil {
		return contracts.QuestionDocument{}, err
	}
	if !found || manifest.Validate() != nil || manifest.ContentState != snapshot.ContentStateAvailable ||
		manifest.ID != wait.Question.ID || manifest.Type != wait.Question.Type ||
		manifest.Digest != wait.Question.Digest || manifest.Type != snapshot.TypeRef("question/v1") {
		return contracts.QuestionDocument{}, fmt.Errorf("workflow waits API: sealed question is unavailable")
	}
	var document contracts.QuestionDocument
	if err := handler.readExactDocument(ctx, manifest, "question.json", &document); err != nil {
		return contracts.QuestionDocument{}, err
	}
	if err := document.Validate(); err != nil {
		return contracts.QuestionDocument{}, fmt.Errorf("workflow waits API: invalid sealed question: %w", err)
	}
	return document, nil
}

func (handler *Handler) readHumanAnswer(
	ctx context.Context,
	manifest snapshot.Snapshot,
) (contracts.HumanAnswerDocument, error) {
	var document contracts.HumanAnswerDocument
	if err := handler.readExactDocument(ctx, manifest, "human-answer.json", &document); err != nil {
		return contracts.HumanAnswerDocument{}, err
	}
	if err := document.Validate(); err != nil {
		return contracts.HumanAnswerDocument{}, fmt.Errorf("workflow waits API: invalid sealed answer: %w", err)
	}
	return document, nil
}

func (handler *Handler) readExactDocument(
	ctx context.Context,
	manifest snapshot.Snapshot,
	name string,
	target any,
) (err error) {
	if interfaceIsNil(handler.content) {
		return fmt.Errorf("workflow waits API: snapshot content is unavailable")
	}
	reader, err := handler.content.Open(ctx, manifest)
	if err != nil || interfaceIsNil(reader) {
		if !interfaceIsNil(reader) {
			_ = reader.Close()
		}
		if err == nil {
			err = fmt.Errorf("workflow waits API: content store returned no reader")
		}
		return err
	}
	tree, captureErr := handler.canonicalizer.Capture(ctx, reader)
	closeErr := reader.Close()
	if captureErr != nil || closeErr != nil {
		if tree != nil {
			_ = tree.Close()
		}
		return errors.Join(captureErr, closeErr)
	}
	if tree == nil {
		return fmt.Errorf("workflow waits API: canonicalizer returned no tree")
	}
	defer func() { err = errors.Join(err, tree.Close()) }()
	if tree.Digest != manifest.Digest || tree.ByteSize != manifest.ByteSize ||
		tree.FileCount != manifest.FileCount {
		return fmt.Errorf("workflow waits API: snapshot content does not match its sealed manifest")
	}
	root, err := os.OpenRoot(tree.Root)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumDocumentBytes {
		_ = file.Close()
		if err == nil {
			err = fmt.Errorf("workflow waits API: document is not a bounded regular file")
		}
		return err
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, maximumDocumentBytes+1))
	closeErr = file.Close()
	if readErr != nil || closeErr != nil || int64(len(contents)) > maximumDocumentBytes {
		return errors.Join(readErr, closeErr, fmt.Errorf("workflow waits API: document is unreadable or too large"))
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("workflow waits API: document contains trailing JSON")
		}
		return err
	}
	return nil
}

func questionAcceptsAnswer(question contracts.QuestionDocument, answer string) bool {
	if strings.TrimSpace(answer) == "" || len(answer) > 16<<10 || strings.ContainsRune(answer, '\x00') {
		return false
	}
	if len(question.Options) == 0 {
		return true
	}
	for _, option := range question.Options {
		if answer == option {
			return true
		}
	}
	return false
}

func parseRoute(response http.ResponseWriter, request *http.Request, withWait bool) (string, snapshot.WorkflowRunID, workflowwait.ID, bool) {
	workflowName := rata.Param(request, "workflow_name")
	if !workflowNamePattern.MatchString(workflowName) {
		writeError(response, http.StatusBadRequest, "invalid_workflow", "workflow name is invalid")
		return "", 0, 0, false
	}
	runID, err := snapshot.ParseWorkflowRunID(rata.Param(request, "workflow_run_id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_workflow_run_id", "workflow run ID is invalid")
		return "", 0, 0, false
	}
	if !withWait {
		return workflowName, runID, 0, true
	}
	waitID, err := workflowwait.ParseID(rata.Param(request, "workflow_wait_id"))
	if err != nil {
		writeError(response, http.StatusBadRequest, "invalid_workflow_wait_id", "workflow wait ID is invalid")
		return "", 0, 0, false
	}
	return workflowName, runID, waitID, true
}

func decodeResolveRequest(response http.ResponseWriter, request *http.Request) (ResolveRequest, bool) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be application/json")
		return ResolveRequest{}, false
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maximumRequestBytes+1))
	if err != nil || int64(len(payload)) > maximumRequestBytes {
		writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
		return ResolveRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
		return ResolveRequest{}, false
	}
	var result ResolveRequest
	seen := ""
	for decoder.More() {
		token, err := decoder.Token()
		key, valid := token.(string)
		if err != nil || !valid || seen != "" {
			writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
			return ResolveRequest{}, false
		}
		switch key {
		case "answer":
			var answer string
			if decoder.Decode(&answer) != nil {
				writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
				return ResolveRequest{}, false
			}
			result.Answer = &answer
		case "snapshot_id":
			var id snapshot.SnapshotID
			if decoder.Decode(&id) != nil {
				writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
				return ResolveRequest{}, false
			}
			result.SnapshotID = &id
		default:
			writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
			return ResolveRequest{}, false
		}
		seen = key
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || seen == "" ||
		(result.Answer == nil) == (result.SnapshotID == nil) ||
		result.SnapshotID != nil && result.SnapshotID.Validate() != nil {
		writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
		return ResolveRequest{}, false
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		writeError(response, http.StatusBadRequest, "invalid_body", "request body is invalid")
		return ResolveRequest{}, false
	}
	return result, true
}

func requireNoBody(response http.ResponseWriter, request *http.Request) bool {
	if request.Body == nil {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 1))
	if err != nil || len(data) != 0 {
		writeError(response, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return false
	}
	return true
}

func validActor(actor string) bool {
	return actor == strings.TrimSpace(actor) && actor != "" && len(actor) <= 256 && !strings.ContainsAny(actor, "\x00\r\n")
}

func validDisplayName(displayName string) bool {
	return strings.TrimSpace(displayName) != "" && len(displayName) <= 500 &&
		!strings.ContainsAny(displayName, "\x00\r\n")
}

func writeNotFound(response http.ResponseWriter) {
	writeError(response, http.StatusNotFound, "not_found", "workflow wait not found")
}

func writeInternalError(response http.ResponseWriter) {
	writeError(response, http.StatusInternalServerError, "internal_error", "internal error")
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, ErrorResponse{Error: code, Message: message})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func cloneRef(value *snapshot.SnapshotRef) *snapshot.SnapshotRef {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
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
