package snapshots

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

const maxResourceCaptureRequestBytes int64 = 64 * 1024

type ResourceCaptureRequest struct {
	Pipeline           atc.PipelineRef `json:"pipeline"`
	Resource           string          `json:"resource"`
	Version            atc.Version     `json:"version"`
	Type               string          `json:"type,omitempty"`
	RetryPipelineRunID string          `json:"retry_pipeline_run_id,omitempty"`
}

func (factory *HandlerFactory) CaptureResource(team TrustedTeam, capturer ResourceCapturer) http.Handler {
	return factory.endpoint(team, http.MethodPost, func(w http.ResponseWriter, r *http.Request, team TrustedTeam) {
		factory.captureResource(w, r, team, capturer)
	})
}

func (factory *HandlerFactory) captureResource(w http.ResponseWriter, r *http.Request, team TrustedTeam, capturer ResourceCapturer) {
	if interfaceIsNil(capturer) {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "resource capture is not enabled")
		return
	}
	query, ok := strictQuery(w, r)
	if !ok {
		return
	}
	for key, values := range query {
		if key != ":team_name" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_query", "resource capture query is invalid")
			return
		}
	}
	if !requireMediaType(w, r, "application/json") {
		return
	}
	if len(r.Header.Values("Content-Encoding")) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_encoding", "request content encoding is not supported")
		return
	}
	if r.ContentLength > maxResourceCaptureRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "resource capture request exceeds the configured limit")
		return
	}
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	defer body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, body, maxResourceCaptureRequestBytes))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "limit_exceeded", "resource capture request exceeds the configured limit")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "resource capture request is invalid")
		}
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 || !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, "invalid_request", "resource capture request is invalid")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire ResourceCaptureRequest
	if err := decoder.Decode(&wire); err != nil || requireJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "resource capture request is invalid")
		return
	}
	identity, err := factory.identity(r)
	if err != nil || validateAuthorityString(identity.Actor, 500) != nil || validateAuthorityString(identity.DisplayName, 500) != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "snapshot service failed")
		return
	}
	retryPipelineRunID, err := parseResourceCapturePipelineRunID(wire.RetryPipelineRunID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "resource capture request is invalid")
		return
	}
	request := resourcecapture.Request{
		TeamID: team.ID, TeamName: team.Name, Pipeline: wire.Pipeline, Resource: wire.Resource,
		Version: wire.Version, Type: snapshot.TypeRef(wire.Type), CreatedBy: identity.DisplayName, Actor: identity.Actor,
		RetryPipelineRunID: retryPipelineRunID,
	}
	if err := request.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "resource capture request is invalid")
		return
	}
	result, err := capturer.Capture(r.Context(), request)
	if err != nil {
		factory.writeResourceCaptureError(w, err)
		return
	}
	if len(result.OperationKey) != 64 || result.Execution.PipelineRunID <= 0 || result.Execution.TemplatePipelineID <= 0 || result.Execution.InstancePipelineID <= 0 {
		writeError(w, http.StatusInternalServerError, "internal_error", "resource capture failed")
		return
	}
	if result.Snapshot != nil {
		if err := result.Snapshot.Validate(); err != nil || result.Snapshot.Type != request.Type && request.Type != "" {
			writeError(w, http.StatusInternalServerError, "internal_error", "resource capture failed")
			return
		}
		status := http.StatusOK
		if result.Created {
			status = http.StatusCreated
		}
		writeJSON(w, status, result)
		return
	}
	status := http.StatusOK
	if result.Execution.Status == db.PipelineRunRunning {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func parseResourceCapturePipelineRunID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	if strings.TrimSpace(raw) != raw || raw[0] == '+' || len(raw) > 1 && raw[0] == '0' {
		return 0, errors.New("invalid resource capture pipeline run ID")
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != raw {
		return 0, errors.New("invalid resource capture pipeline run ID")
	}
	return value, nil
}

func (factory *HandlerFactory) writeResourceCaptureError(w http.ResponseWriter, err error) {
	factory.logger.Error("resource-capture-request-failed", err)
	switch {
	case errors.Is(err, resourcecapture.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "resource or exact version was not found")
	case errors.Is(err, resourcecapture.ErrDisabled):
		writeError(w, http.StatusConflict, "disabled", "resource version is disabled")
	case errors.Is(err, resourcecapture.ErrTypeRequired):
		writeError(w, http.StatusUnprocessableEntity, "type_required", "snapshot type is required for this resource")
	case errors.Is(err, resourcecapture.ErrTemplateConflict):
		writeError(w, http.StatusConflict, "conflict", "resource capture conflicts with immutable state")
	case errors.Is(err, resourcecapture.ErrUnavailable), errors.Is(err, resourcecapture.ErrOutputUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "resource capture is unavailable")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout", "resource capture timed out")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusRequestTimeout, "canceled", "resource capture was canceled")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "resource capture failed")
	}
}
