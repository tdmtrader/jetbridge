// Package nodeupgrades exposes exact node consumers and selected workflow
// upgrade requests without granting callers workflow promotion authority.
package nodeupgrades

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
	"github.com/tedsuo/rata"
)

const maxUpgradeRequestBytes int64 = 64 << 10

type ConsumerStore interface {
	Get(string, int) (*workflow.NodeDefinition, bool, error)
	Consumers(context.Context, string, int, workflow.NodeConsumerRequest) (workflow.NodeConsumerPage, error)
}

type UpgradeService interface {
	Upgrade(context.Context, workflow.NodeUpgradeRequest) (workflow.NodeUpgradeResult, error)
}

type IdentityFunc func(*http.Request) (string, error)

type Config struct {
	TeamID   int
	TeamName string
	Store    ConsumerStore
	Upgrader UpgradeService
	Identity IdentityFunc
}

type Handler struct {
	teamID   int
	teamName string
	store    ConsumerStore
	upgrader UpgradeService
	identity IdentityFunc
}

// Consumer is the public, precision-safe projection of a durable node
// consumer. Database identity is quoted so browser and CLI JSON round trips
// cannot lose a signed 64-bit workflow definition ID.
type Consumer struct {
	WorkflowDefinitionID snapshot.DatabaseID `json:"workflow_definition_id"`
	WorkflowName         string              `json:"workflow_name"`
	WorkflowVersion      int                 `json:"workflow_version"`
	Live                 bool                `json:"live"`
	Binding              ConsumerBinding     `json:"binding"`
}

// ConsumerBinding keeps the domain fields of a resolved binding while
// projecting its database identity through the precision-safe public ID type.
type ConsumerBinding struct {
	InstanceName     string              `json:"instance_name"`
	NodeDefinitionID snapshot.DatabaseID `json:"node_definition_id"`
	NodeName         string              `json:"node_name"`
	NodeVersion      int                 `json:"node_version"`
	NodeContentHash  string              `json:"node_content_hash"`
	InputMapping     map[string]string   `json:"input_mapping,omitempty"`
	OutputMapping    map[string]string   `json:"output_mapping,omitempty"`
	Parameters       map[string]string   `json:"parameters,omitempty"`
}

func NewHandler(config Config) (*Handler, error) {
	if config.TeamID <= 0 || config.TeamName != atc.DefaultTeamName || config.Store == nil || config.Upgrader == nil || config.Identity == nil {
		return nil, fmt.Errorf("node upgrades API: trusted main-team dependencies are required")
	}
	return &Handler{teamID: config.TeamID, teamName: config.TeamName, store: config.Store, upgrader: config.Upgrader, identity: config.Identity}, nil
}

func (handler *Handler) Consumers(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) || !requireNoBody(w, r) {
		return
	}
	name, version, query, ok := parseRoute(w, r, map[string]struct{}{"limit": {}, "cursor": {}})
	if !ok {
		return
	}
	request, ok := parseConsumerRequest(w, query)
	if !ok {
		return
	}
	definition, found, err := handler.store.Get(name, version)
	if err != nil {
		writeInternalError(w)
		return
	}
	if !found || definition == nil || definition.Name != name || definition.Version != version {
		writeError(w, http.StatusNotFound, "not_found", "node version was not found")
		return
	}
	page, err := handler.store.Consumers(r.Context(), name, version, request)
	if err != nil {
		writeInternalError(w)
		return
	}
	if !validConsumerPage(page, *definition, request.Limit) {
		writeInternalError(w)
		return
	}
	consumers := make([]Consumer, 0, len(page.Consumers))
	for _, consumer := range page.Consumers {
		consumers = append(consumers, Consumer{
			WorkflowDefinitionID: snapshot.DatabaseID(consumer.WorkflowDefinitionID),
			WorkflowName:         consumer.WorkflowName,
			WorkflowVersion:      consumer.WorkflowVersion,
			Live:                 consumer.Live,
			Binding: ConsumerBinding{
				InstanceName:     consumer.Binding.InstanceName,
				NodeDefinitionID: snapshot.DatabaseID(consumer.Binding.NodeDefinitionID),
				NodeName:         consumer.Binding.NodeName,
				NodeVersion:      consumer.Binding.NodeVersion,
				NodeContentHash:  consumer.Binding.NodeContentHash,
				InputMapping:     cloneStringMap(consumer.Binding.InputMapping),
				OutputMapping:    cloneStringMap(consumer.Binding.OutputMapping),
				Parameters:       cloneStringMap(consumer.Binding.Parameters),
			},
		})
	}
	if page.NextCursor != (workflow.NodeConsumerCursor{}) {
		cursor, err := encodeConsumerCursor(page.NextCursor)
		if err != nil {
			writeInternalError(w)
			return
		}
		w.Header().Set("X-Next-Cursor", cursor)
		next := r.URL.Query()
		for key := range next {
			if strings.HasPrefix(key, ":") {
				next.Del(key)
			}
		}
		next.Set("cursor", cursor)
		next.Set("limit", strconv.Itoa(request.Limit))
		w.Header().Set("Link", "<"+r.URL.EscapedPath()+"?"+next.Encode()+">; rel=\"next\"")
	}
	writeJSON(w, http.StatusOK, consumers)
}

func validConsumerPage(page workflow.NodeConsumerPage, definition workflow.NodeDefinition, limit int) bool {
	if definition.ID <= 0 || definition.ContentHash == "" || len(page.Consumers) > limit {
		return false
	}
	for index, consumer := range page.Consumers {
		if consumer.WorkflowDefinitionID <= 0 ||
			consumer.WorkflowVersion <= 0 ||
			consumer.WorkflowVersion > workflow.MaxWorkflowVersion ||
			validateIdentifier(consumer.WorkflowName) != nil ||
			validateText(consumer.Binding.InstanceName, 256, false, true) != nil ||
			consumer.Binding.NodeDefinitionID != definition.ID ||
			consumer.Binding.NodeName != definition.Name ||
			consumer.Binding.NodeVersion != definition.Version ||
			consumer.Binding.NodeContentHash != definition.ContentHash {
			return false
		}
		if index > 0 {
			previous := page.Consumers[index-1]
			if consumer.WorkflowDefinitionID > previous.WorkflowDefinitionID ||
				consumer.WorkflowDefinitionID == previous.WorkflowDefinitionID &&
					consumer.Binding.InstanceName <= previous.Binding.InstanceName {
				return false
			}
		}
	}
	if page.NextCursor != (workflow.NodeConsumerCursor{}) {
		if len(page.Consumers) == 0 {
			return false
		}
		last := page.Consumers[len(page.Consumers)-1]
		if page.NextCursor != (workflow.NodeConsumerCursor{
			WorkflowDefinitionID: last.WorkflowDefinitionID,
			InstanceName:         last.Binding.InstanceName,
		}) {
			return false
		}
	}
	return true
}

func (handler *Handler) Upgrade(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	name, version, _, ok := parseRoute(w, r, nil)
	if !ok {
		return
	}
	workflows, ok := decodeUpgradeRequest(w, r)
	if !ok {
		return
	}
	createdBy, err := handler.identity(r)
	if err != nil || validateText(createdBy, 256, false, true) != nil {
		writeInternalError(w)
		return
	}
	result, err := handler.upgrader.Upgrade(r.Context(), workflow.NodeUpgradeRequest{
		NodeName: name, Version: version, Workflows: append([]string(nil), workflows...), CreatedBy: createdBy,
	})
	if err != nil {
		if errors.Is(err, workflow.ErrNodeUpgradeResponseTooLarge) {
			writeResponseLimitError(w)
			return
		}
		writeInternalError(w)
		return
	}
	if result.NodeName != name || result.Version != version || !validUpgradeResults(workflows, result.Workflows) {
		writeInternalError(w)
		return
	}
	if err := workflow.ValidateNodeUpgradeResultResponseBudget(result); err != nil {
		if errors.Is(err, workflow.ErrNodeUpgradeResponseTooLarge) {
			writeResponseLimitError(w)
			return
		}
		writeInternalError(w)
		return
	}
	result.Workflows = append([]workflow.NodeUpgradeWorkflowResult(nil), result.Workflows...)
	sort.Slice(result.Workflows, func(left, right int) bool { return result.Workflows[left].Workflow < result.Workflows[right].Workflow })
	writeJSON(w, http.StatusOK, result)
}

func parseRoute(w http.ResponseWriter, r *http.Request, allowedPublic map[string]struct{}) (string, int, url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "request query is invalid")
		return "", 0, nil, false
	}
	for key, values := range query {
		_, public := allowedPublic[key]
		path := key == ":node_name" || key == ":version"
		if (!public && !path) || len(values) != 1 || values[0] == "" {
			writeError(w, http.StatusBadRequest, "invalid_query", "request query contains unsupported fields")
			return "", 0, nil, false
		}
	}
	name := rata.Param(r, "node_name")
	if warning, err := atc.ValidateIdentifier(name); err != nil || warning != nil {
		writeError(w, http.StatusBadRequest, "invalid_node", "node name is invalid")
		return "", 0, nil, false
	}
	version, err := parsePositiveInt(rata.Param(r, "version"), workflow.MaxWorkflowVersion)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_version", "node version is invalid")
		return "", 0, nil, false
	}
	return name, version, query, true
}

func parseConsumerRequest(w http.ResponseWriter, query url.Values) (workflow.NodeConsumerRequest, bool) {
	request := workflow.NodeConsumerRequest{Limit: workflow.DefaultVersionPageSize}
	if raw, found := query["limit"]; found {
		limit, err := parsePositiveInt(raw[0], workflow.MaxVersionPageSize)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit must be an integer from 1 to %d", workflow.MaxVersionPageSize))
			return workflow.NodeConsumerRequest{}, false
		}
		request.Limit = limit
	}
	if raw, found := query["cursor"]; found {
		cursor, err := decodeConsumerCursor(raw[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "node consumer cursor is invalid")
			return workflow.NodeConsumerRequest{}, false
		}
		request.Cursor = cursor
	}
	return request, true
}

func encodeConsumerCursor(cursor workflow.NodeConsumerCursor) (string, error) {
	if cursor.WorkflowDefinitionID <= 0 || validateText(cursor.InstanceName, 256, false, true) != nil {
		return "", fmt.Errorf("invalid node consumer cursor")
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeConsumerCursor(raw string) (workflow.NodeConsumerCursor, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return workflow.NodeConsumerCursor{}, fmt.Errorf("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != raw {
		return workflow.NodeConsumerCursor{}, fmt.Errorf("invalid cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor workflow.NodeConsumerCursor
	if err := decoder.Decode(&cursor); err != nil || requireEOF(decoder) != nil || cursor.WorkflowDefinitionID <= 0 || validateText(cursor.InstanceName, 256, false, true) != nil {
		return workflow.NodeConsumerCursor{}, fmt.Errorf("invalid cursor")
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, payload) {
		return workflow.NodeConsumerCursor{}, fmt.Errorf("invalid cursor")
	}
	return cursor, nil
}

func decodeUpgradeRequest(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if !requireJSONMediaType(w, r) {
		return nil, false
	}
	if len(r.Header.Values("Content-Encoding")) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding", "request content encoding is not supported")
		return nil, false
	}
	if r.ContentLength > maxUpgradeRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "node upgrade request exceeds the configured limit")
		return nil, false
	}
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxUpgradeRequestBytes+1))
	if err != nil || len(raw) == 0 || !utf8.Valid(raw) {
		writeInvalidBody(w)
		return nil, false
	}
	if int64(len(raw)) > maxUpgradeRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "node upgrade request exceeds the configured limit")
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		writeInvalidBody(w)
		return nil, false
	}
	var workflows []string
	seenField := false
	for decoder.More() {
		token, err := decoder.Token()
		key, isString := token.(string)
		if err != nil || !isString || key != "workflows" || seenField {
			writeInvalidBody(w)
			return nil, false
		}
		seenField = true
		if err := decoder.Decode(&workflows); err != nil {
			writeInvalidBody(w)
			return nil, false
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || requireEOF(decoder) != nil || !seenField || !validWorkflowSelection(workflows) {
		writeInvalidBody(w)
		return nil, false
	}
	return append([]string(nil), workflows...), true
}

func validWorkflowSelection(workflows []string) bool {
	if len(workflows) == 0 || len(workflows) > 1000 {
		return false
	}
	seen := make(map[string]struct{}, len(workflows))
	for _, name := range workflows {
		if warning, err := atc.ValidateIdentifier(name); err != nil || warning != nil {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
	}
	return true
}

func validateIdentifier(value string) error {
	warning, err := atc.ValidateIdentifier(value)
	if err != nil {
		return err
	}
	if warning != nil {
		return fmt.Errorf("identifier is not canonical")
	}
	return nil
}

func validUpgradeResults(selected []string, results []workflow.NodeUpgradeWorkflowResult) bool {
	if len(selected) != len(results) {
		return false
	}
	remaining := make(map[string]struct{}, len(selected))
	for _, name := range selected {
		remaining[name] = struct{}{}
	}
	for _, result := range results {
		if _, found := remaining[result.Workflow]; !found || result.OldVersion < 0 || result.NewVersion < 0 {
			return false
		}
		delete(remaining, result.Workflow)
		switch result.Status {
		case workflow.NodeUpgradeCreated, workflow.NodeUpgradeUnchanged:
			if result.Error != "" || result.Obligations != nil || result.NewVersion <= 0 {
				return false
			}
		case workflow.NodeUpgradeFailed:
			if validateText(result.Error, 4096, false, true) != nil || result.Obligations != nil {
				return false
			}
		case workflow.NodeUpgradeRecompositionRequired:
			if result.Error != "" || result.Obligations == nil {
				return false
			}
		default:
			return false
		}
	}
	return len(remaining) == 0
}

func parsePositiveInt(raw string, maximum int) (int, error) {
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
		return 0, fmt.Errorf("integer is outside its bound")
	}
	return value, nil
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

func requireEOF(decoder *json.Decoder) error {
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
	if r.ContentLength > 0 || len(r.TransferEncoding) != 0 || r.ContentLength < 0 && r.Body != nil && r.Body != http.NoBody {
		writeError(w, http.StatusBadRequest, "unexpected_body", "request body is not allowed")
		return false
	}
	return true
}

func validateText(value string, maximum int, allowEmpty, requireTrimmed bool) error {
	if !utf8.ValidString(value) || len(value) > maximum || !allowEmpty && strings.TrimSpace(value) == "" || requireTrimmed && strings.TrimSpace(value) != value {
		return fmt.Errorf("invalid text")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("invalid text")
		}
	}
	return nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func writeInvalidBody(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_request", "node upgrade request body is invalid")
}

func writeResponseLimitError(w http.ResponseWriter) {
	writeError(
		w,
		http.StatusUnprocessableEntity,
		"response_limit_exceeded",
		"node upgrade result exceeds the 4 MiB response limit; select fewer workflows",
	)
}

func writeInternalError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "internal_error", "node upgrade service failed")
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":"internal_error","message":"node upgrade service failed"}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
