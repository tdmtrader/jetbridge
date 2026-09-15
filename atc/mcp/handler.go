// Package mcp adapts the existing, fully authorized API to MCP tools.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/mcpserver"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type principalKey struct{}

// NewHandler requires a resource-bound bearer token even for initialization and
// discovery. api must be the complete API handler (RBAC, policy and audit wraps),
// configured with accessor.NewTrustedTokenVerifier.
// HandlerOptions uses the same access factory and custom-role map as the API.
// DisabledOperations is an optional deployment restriction, never an authority grant.
type HandlerOptions struct {
	AccessFactory      accessor.AccessFactory
	CustomRoles        map[string]string
	DisabledOperations map[string]bool
}

func NewHandler(auth *mcpauth.Server, api http.Handler, options HandlerOptions) http.Handler {
	operations := boundOperations(api)
	return auth.AuthorizeHTTP(func(w http.ResponseWriter, r *http.Request, p mcpauth.Principal) {
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		c := catalog{operations: operations, principal: p, disabled: options.DisabledOperations,
			access: func(ctx context.Context, action string) (AccountEligibility, error) {
				return AccountEligibilityForAction(ctx, options.AccessFactory, p, options.CustomRoles, action)
			},
		}
		needsCatalog := false
		// Authenticate first. Preflight never invokes an API or looks up a target.
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessageBytes))
			if err != nil {
				http.Error(w, "invalid or oversized MCP request", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			// The SDK still accepts JSON-RPC batches for pre-2025-06-18 clients;
			// a batch would skip this preflight, so refuse it outright.
			if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
				http.Error(w, "JSON-RPC batch requests are not supported", http.StatusBadRequest)
				return
			}
			// Maps match keys exactly, as the SDK's decoder does. A struct would
			// match case-insensitively and could challenge a different call.
			var message, params map[string]json.RawMessage
			var method, name string
			decoded := json.Unmarshal(body, &message) == nil && json.Unmarshal(message["method"], &method) == nil
			if decoded {
				_ = json.Unmarshal(message["params"], &params)
				_ = json.Unmarshal(params["name"], &name)
			}
			needsCatalog = decoded && method == "tools/list"
			if decoded && method == "tools/call" {
				if name == "pipeline_status" && !c.disabled["pipeline_get"] && !auth.RequireScope(w, p, mcpauth.ScopeRead) {
					return
				}
				if op, _, err := decodeOperation(name, params["arguments"], operations); err == nil && op.Implemented() && !c.disabled[op.ID] && !auth.RequireScope(w, p, op.Scope) {
					return
				}
			}
		}
		groups := map[string][]Operation{}
		if needsCatalog {
			var err error
			groups, err = c.eligible(ctx)
			if err != nil {
				http.Error(w, "capability access temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		server := mcpserver.NewProtocolServer()
		// Intercept calls after SDK protocol decoding, including groups whose last
		// branch was pruned. Do not register a hidden executable tool to do this.
		server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
			return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
				if method == "tools/call" {
					if call, ok := req.(*sdk.CallToolRequest); ok {
						if call.Params.Name == "pipeline_status" && c.disabled["pipeline_get"] {
							return toolError("DISABLED: this MCP operation is disabled by the operator"), nil
						}
						for _, op := range operations {
							if op.Resource == call.Params.Name {
								return c.dispatch(ctx, call.Params.Name, call.Params.Arguments)
							}
						}
					}
				}
				return next(ctx, method, req)
			}
		})
		closedWorld := false
		for resource, ops := range groups {
			readOnly, idempotent := true, true
			for _, op := range ops {
				readOnly = readOnly && op.ReadOnly
				idempotent = idempotent && op.Idempotent
			}
			server.AddTool(&sdk.Tool{Name: resource, Description: "Perform one precise " + resource + " operation. Only eligible branches are listed. If an action is absent, use capabilities_explain. Target authorization and client approval still apply.", InputSchema: GroupInputSchema(ops), OutputSchema: GroupOutputSchema(ops), Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: idempotent, OpenWorldHint: &closedWorld}}, interceptedByMiddleware)
		}
		server.AddTool(&sdk.Tool{Name: "capabilities_explain", Description: "Explain MCP support and this connection's access when a requested action is absent. Metadata only, no executable schemas or target guarantees. Available without read consent.", InputSchema: explainSchema(), Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			if validateJSON(explainSchema(), req.Params.Arguments) != nil {
				return toolError("INVALID_ARGUMENTS: invalid capability query"), nil
			}
			var args explainArgs
			_ = json.Unmarshal(req.Params.Arguments, &args)
			result, err := c.explain(ctx, args)
			if err != nil {
				return toolError(err.Error()), nil
			}
			return toolSuccess(result), nil
		})
		if p.HasScope(mcpauth.ScopeRead) && !c.disabled["pipeline_get"] {
			sdk.AddTool(server, &sdk.Tool{Name: "pipeline_status", Description: "Compatibility tool: get one non-instanced pipeline's status. Use pipeline_get for exact instance selection.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}}, func(ctx context.Context, _ *sdk.CallToolRequest, args PipelineStatusArgs) (*sdk.CallToolResult, PipelineStatus, error) {
				result, err := pipelineStatus(ctx, api, p, args)
				return nil, result, err
			})
		}
		response := &boundedResponse{header: make(http.Header)}
		mcpserver.NewHTTPHandler(func(*http.Request) *sdk.Server { return server }).ServeHTTP(response, r.WithContext(ctx))
		if response.overflow {
			http.Error(w, "MCP response exceeds limit; do not retry mutations automatically", http.StatusInternalServerError)
			return
		}
		for key, values := range response.header {
			w.Header()[key] = values
		}
		if response.status != 0 {
			w.WriteHeader(response.status)
		}
		_, _ = w.Write(response.body.Bytes())
	})
}

// Group tools exist for listing only; the receiving middleware dispatches
// every call by resource name, including groups with no listed branch.
func interceptedByMiddleware(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
	return toolError("INTERNAL: tool call bypassed the MCP middleware"), nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 256 && !strings.ContainsAny(name, "/\\\x00\r\n")
}

type PipelineStatusArgs struct {
	Team     string `json:"team" jsonschema:"Team containing the pipeline"`
	Pipeline string `json:"pipeline" jsonschema:"Exact pipeline name"`
}

// PipelineStatus deliberately omits configuration, credentials and build output.
type PipelineStatus struct {
	ID       int    `json:"id"`
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Paused   bool   `json:"paused"`
	Archived bool   `json:"archived"`
	Public   bool   `json:"public"`
}

func pipelineStatus(ctx context.Context, api http.Handler, principal mcpauth.Principal, args PipelineStatusArgs) (PipelineStatus, error) {
	for _, name := range []string{args.Team, args.Pipeline} {
		if name == "" || name == "." || name == ".." || len(name) > 256 || strings.ContainsAny(name, "/\\\x00\r\n") {
			return PipelineStatus{}, errors.New("team and pipeline must be valid nonempty names")
		}
	}
	path := "/api/v1/teams/" + url.PathEscape(args.Team) + "/pipelines/" + url.PathEscape(args.Pipeline)
	// A fresh request carries no client-supplied cookies, Authorization header or
	// routing headers. Only the authenticated identity crosses this private seam.
	r, err := http.NewRequestWithContext(accessor.WithTrustedClaims(ctx, principal.Claims), http.MethodGet, path, nil)
	if err != nil {
		return PipelineStatus{}, err
	}
	w := &boundedResponse{header: make(http.Header)}
	api.ServeHTTP(w, r)
	if w.status != http.StatusOK {
		if w.status == 401 || w.status == 403 || w.status == 404 {
			return PipelineStatus{}, errors.New("pipeline unavailable or not permitted")
		}
		return PipelineStatus{}, errors.New("pipeline status could not be retrieved")
	}
	var p atc.Pipeline
	if w.overflow || json.Unmarshal(w.body.Bytes(), &p) != nil {
		return PipelineStatus{}, errors.New("invalid pipeline status response")
	}
	return PipelineStatus{ID: p.ID, Team: p.TeamName, Pipeline: p.Name, Paused: p.Paused, Archived: p.Archived, Public: p.Public}, nil
}

type boundedResponse struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *boundedResponse) Header() http.Header { return w.header }
func (w *boundedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *boundedResponse) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if w.body.Len()+len(p) > 1024*1024 {
		w.overflow = true
		return len(p), nil
	}
	return w.body.Write(p)
}
