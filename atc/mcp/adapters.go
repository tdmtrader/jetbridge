package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/skymarshal/mcpauth"
)

type pipelineArguments struct {
	Team         string           `json:"team"`
	Pipeline     string           `json:"pipeline"`
	InstanceVars atc.InstanceVars `json:"instance_vars"`
}

func (a pipelineArguments) path() string {
	return "/api/v1/teams/" + url.PathEscape(a.Team) + "/pipelines/" + url.PathEscape(a.Pipeline)
}
func (a pipelineArguments) query() url.Values {
	return atc.PipelineRef{InstanceVars: a.InstanceVars}.QueryParams()
}

// decodeArguments decodes a tool's arguments into args. A payload that does
// not decode is refused as INVALID_ARGUMENTS naming the field or the JSON
// problem, never read as zero values (a bad build_id must not address build
// 0). Accepted payloads decode exactly as json.Unmarshal decodes them.
func decodeArguments(input json.RawMessage, args any) error {
	err := json.Unmarshal(input, args)
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &typeErr) && typeErr.Field != "":
		// Tool arguments are flat objects; the path's earlier segments are
		// only embedded Go struct names (pipelineArguments), not JSON keys.
		field := typeErr.Field[strings.LastIndex(typeErr.Field, ".")+1:]
		return fmt.Errorf("INVALID_ARGUMENTS: field %q must be %s, not a JSON %s", field, typeErr.Type, typeErr.Value)
	case errors.As(err, &typeErr):
		return fmt.Errorf("INVALID_ARGUMENTS: arguments must be a JSON object, not a JSON %s", typeErr.Value)
	case errors.As(err, &syntaxErr):
		return fmt.Errorf("INVALID_ARGUMENTS: malformed JSON arguments at byte %d", syntaxErr.Offset)
	default:
		return fmt.Errorf("INVALID_ARGUMENTS: malformed JSON arguments: %v", err)
	}
}

// Bind every registered operation to its API adapter.
func boundOperations(api http.Handler) []Operation {
	ops := Operations()
	for i := range ops {
		ops[i].handler = coreAdapter(api, ops[i].ID)
		if page := pageAdapter(api, ops[i].ID); page != nil {
			ops[i].handler = page
		}
		switch ops[i].ID {
		case "pipeline_get":
			ops[i].handler = func(ctx context.Context, input json.RawMessage) (any, error) {
				var args pipelineArguments
				if err := decodeArguments(input, &args); err != nil {
					return nil, err
				}
				response, err := apiRequest(ctx, api, http.MethodGet, args.path(), args.query(), nil, nil)
				if err != nil {
					return nil, err
				}
				if err = responseError(response); err != nil {
					return nil, err
				}
				var pipeline atc.Pipeline
				if json.Unmarshal(response.body.Bytes(), &pipeline) != nil {
					return nil, errors.New("INVALID_RESULT: invalid pipeline response")
				}
				return pipelineView(pipeline), nil
			}
		}
	}
	return ops
}

func pipelineView(p atc.Pipeline) map[string]any {
	vars := p.InstanceVars
	if vars == nil {
		vars = atc.InstanceVars{}
	}
	return map[string]any{"id": p.ID, "team": p.TeamName, "pipeline": p.Name, "instance_vars": vars, "paused": p.Paused, "archived": p.Archived, "public": p.Public}
}

func apiRequest(ctx context.Context, api http.Handler, method, path string, query url.Values, body io.Reader, headers http.Header) (*boundedResponse, error) {
	principal, ok := ctx.Value(principalKey{}).(mcpauth.Principal)
	if !ok {
		return nil, errors.New("AUTHENTICATION_REQUIRED")
	}
	// No caller cookies, Authorization, forwarding or routing headers cross this
	// seam. The API verifies trusted identity and all current target/policy rules.
	r, err := http.NewRequestWithContext(accessor.WithTrustedClaims(ctx, principal.Claims), method, path, body)
	if err != nil {
		return nil, err
	}
	r.URL.RawQuery = query.Encode()
	if headers != nil {
		r.Header = headers.Clone()
	}
	response := &boundedResponse{header: make(http.Header)}
	api.ServeHTTP(response, r)
	if response.overflow {
		return nil, errors.New("RESULT_TOO_LARGE: API response exceeded the bounded adapter limit")
	}
	return response, nil
}
func responseError(r *boundedResponse) error {
	if r.status >= 200 && r.status < 300 {
		return nil
	}
	switch r.status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return errors.New("TARGET_UNAVAILABLE: target unavailable or not permitted")
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return configValidationError(r)
	case http.StatusConflict:
		// The archived-pipeline guard answers 409 before authorization, so an
		// untyped conflict must read like any other unavailable target.
		var envelope atc.ErrorResponse
		if json.Unmarshal(r.body.Bytes(), &envelope) == nil && envelope.Code != "" {
			return configValidationError(r)
		}
		return errors.New("TARGET_UNAVAILABLE: target unavailable or not permitted")
	default:
		return errors.New("API_FAILURE: operation failed; do not retry a mutation automatically")
	}
}
