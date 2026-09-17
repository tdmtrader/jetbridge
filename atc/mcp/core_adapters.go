package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"sigs.k8s.io/yaml"
)

// boundChars truncates to the schema's character bound rather than letting a
// long value fail validation: the caller has already committed a mutation by
// the time the receipt is built, and a truncated warning beats INVALID_RESULT.
func boundChars(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "\u2026"
}

func coreAdapter(api http.Handler, id string) mcpserver.ToolHandler {
	switch id {
	case "build_logs_read":
		return func(ctx context.Context, input json.RawMessage) (any, error) {
			var a struct {
				BuildID  int    `json:"build_id"`
				Cursor   string `json:"cursor"`
				MaxBytes int    `json:"max_bytes"`
			}
			_ = json.Unmarshal(input, &a)
			query := url.Values{"format": {"json"}}
			if a.Cursor != "" {
				query.Set("cursor", a.Cursor)
			}
			if a.MaxBytes > 0 {
				query.Set("max_bytes", strconv.Itoa(a.MaxBytes))
			}
			r, err := apiRequest(ctx, api, "GET", "/api/v1/builds/"+strconv.Itoa(a.BuildID)+"/events", query, nil, nil)
			if err != nil {
				return nil, err
			}
			if r.status != http.StatusOK {
				var envelope atc.ErrorResponse
				if json.Unmarshal(r.body.Bytes(), &envelope) == nil {
					switch envelope.Code {
					case "INVALID_CURSOR", "STREAM_CHANGED", "OUTPUT_RETAINED_AWAY", "EVENT_TOO_LARGE", "STORED_EVENT_TOO_LARGE", "PAGE_TOO_SMALL", "TEMPORARY_OUTPUT_FAILURE":
						message := envelope.Code
						if len(envelope.Errors) > 0 {
							message += ": " + envelope.Errors[0]
						}
						return nil, errors.New(message)
					}
				}
				return nil, responseError(r)
			}
			var page atc.BuildEventPage
			if json.Unmarshal(r.body.Bytes(), &page) != nil {
				return nil, errors.New("INVALID_RESULT: invalid event page")
			}
			return page, nil
		}

	case "pipeline_config_get":
		return func(ctx context.Context, input json.RawMessage) (any, error) {
			var a pipelineArguments
			_ = json.Unmarshal(input, &a)
			r, err := apiRequest(ctx, api, "GET", a.path()+"/config", a.query(), nil, nil)
			if err != nil {
				return nil, err
			}
			if err = responseError(r); err != nil {
				return nil, err
			}
			var value atc.ConfigResponse
			if json.Unmarshal(r.body.Bytes(), &value) != nil {
				return nil, errors.New("INVALID_RESULT: invalid config response")
			}
			config, err := yaml.Marshal(value.Config)
			if err != nil {
				return nil, err
			}
			if len(config) > 128*1024 {
				return nil, errors.New("RESULT_TOO_LARGE: pipeline configuration exceeds 131072 UTF-8 bytes")
			}
			version := r.header.Get(atc.ConfigVersionHeader)
			if _, err = atc.ParseConfigVersion(version); err != nil {
				return nil, errors.New("INVALID_RESULT: invalid config version")
			}
			return map[string]any{"config_yaml": string(config), "version": version}, nil
		}
	case "pipeline_config_set":
		return func(ctx context.Context, input json.RawMessage) (any, error) {
			var a struct {
				pipelineArguments
				ConfigYAML string `json:"config_yaml"`
				Version    string `json:"version"`
			}
			_ = json.Unmarshal(input, &a)
			headers := http.Header{"Content-Type": {"application/x-yaml"}, atc.ConfigVersionHeader: {a.Version}}
			r, err := apiRequest(ctx, api, "PUT", a.path()+"/config/conditional", a.query(), bytes.NewBufferString(a.ConfigYAML), headers)
			if err != nil {
				return nil, errors.New("OUTCOME_UNKNOWN: config write response unavailable; do not retry automatically")
			}
			var envelope atc.SaveConfigResponse
			decodeErr := json.Unmarshal(r.body.Bytes(), &envelope)
			if r.status == http.StatusConflict && decodeErr == nil && envelope.Code == atc.ConfigVersionConflictCode {
				return nil, errors.New("VERSION_CONFLICT: configuration changed or the create/update precondition no longer holds; review the current version before retrying")
			}
			if r.status == http.StatusNotFound || r.status == http.StatusMethodNotAllowed {
				return nil, errors.New("STRICT_WRITE_UNAVAILABLE: strict route or target unavailable; no legacy write was attempted")
			}
			if r.status >= 500 || r.status == 0 {
				return nil, errors.New("OUTCOME_UNKNOWN: config write may have committed; do not retry automatically")
			}
			if err = responseError(r); err != nil {
				return nil, err
			}
			version := r.header.Get(atc.ConfigVersionHeader)
			n, parseErr := atc.ParseConfigVersion(version)
			expected := http.StatusOK
			if a.Version == "0" {
				expected = http.StatusCreated
			}
			if parseErr != nil || n == 0 || version == a.Version || r.status != expected || decodeErr != nil {
				return nil, errors.New("OUTCOME_UNKNOWN: missing or invalid committed config receipt; do not retry automatically")
			}
			warnings := []string{}
			for _, warning := range envelope.Warnings {
				warnings = append(warnings, boundChars(warning.Message, configWarningMaxChars))
			}
			return map[string]any{"version": version, "created": r.status == http.StatusCreated, "warnings": warnings}, nil
		}
	case "pipeline_pause", "pipeline_unpause":
		return func(ctx context.Context, input json.RawMessage) (any, error) {
			var a pipelineArguments
			_ = json.Unmarshal(input, &a)
			suffix := "pause"
			if id == "pipeline_unpause" {
				suffix = "unpause"
			}
			r, err := apiRequest(ctx, api, "PUT", a.path()+"/"+suffix, a.query(), nil, nil)
			if err != nil {
				return nil, err
			}
			if err = responseError(r); err != nil {
				return nil, err
			}
			return map[string]any{"accepted": true}, nil
		}
	case "build_get", "build_abort":
		return func(ctx context.Context, input json.RawMessage) (any, error) {
			var a struct {
				BuildID int `json:"build_id"`
			}
			_ = json.Unmarshal(input, &a)
			method, path := "GET", "/api/v1/builds/"+strconv.Itoa(a.BuildID)
			if id == "build_abort" {
				method = "PUT"
				path += "/abort"
			}
			r, err := apiRequest(ctx, api, method, path, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			if err = responseError(r); err != nil {
				return nil, err
			}
			if id == "build_abort" {
				return map[string]any{"accepted": true}, nil
			}
			var value atc.Build
			if json.Unmarshal(r.body.Bytes(), &value) != nil {
				return nil, errors.New("INVALID_RESULT: invalid build response")
			}
			return buildView(value), nil
		}
	case "job_trigger":
		return func(ctx context.Context, input json.RawMessage) (any, error) {
			var a struct {
				pipelineArguments
				Job string `json:"job"`
			}
			_ = json.Unmarshal(input, &a)
			r, err := apiRequest(ctx, api, "POST", a.path()+"/jobs/"+url.PathEscape(a.Job)+"/builds", a.query(), nil, nil)
			if err != nil || ctx.Err() != nil || r.status == 0 || r.status >= 500 {
				return nil, errors.New("OUTCOME_UNKNOWN: a build may have been created; inspect job builds before any retry")
			}
			if err = responseError(r); err != nil {
				return nil, err
			}
			var value atc.Build
			if json.Unmarshal(r.body.Bytes(), &value) != nil || value.ID < 1 {
				return nil, errors.New("OUTCOME_UNKNOWN: build creation returned no valid build ID; do not retry automatically")
			}
			return map[string]any{"build_id": value.ID, "accepted": true}, nil
		}
	}
	return nil
}
func buildView(b atc.Build) map[string]any {
	var pipeline, job any
	if b.PipelineName != "" {
		pipeline = b.PipelineName
	}
	if b.JobName != "" {
		job = b.JobName
	}
	vars := b.PipelineInstanceVars
	if vars == nil {
		vars = atc.InstanceVars{}
	}
	return map[string]any{"build_id": b.ID, "name": b.Name, "status": b.Status, "team": b.TeamName, "pipeline": pipeline, "job": job, "instance_vars": vars}
}

// Keep upstream validation errors compact and useful without exposing raw HTTP
// pages or infrastructure details. References in user-supplied YAML stay intact.
func configValidationError(r *boundedResponse) error {
	var envelope atc.ErrorResponse
	if json.Unmarshal(r.body.Bytes(), &envelope) == nil && len(envelope.Errors) > 0 {
		return fmt.Errorf("INVALID_ARGUMENTS: %s", envelope.Errors[0])
	}
	return errors.New("INVALID_ARGUMENTS: API rejected the request")
}
