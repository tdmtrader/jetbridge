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
func NewHandler(auth *mcpauth.Server, api http.Handler) http.Handler {
	readServer := mcpserver.NewProtocolServer()
	emptyServer := mcpserver.NewProtocolServer()
	closedWorld := false
	sdk.AddTool(readServer, &sdk.Tool{
		Name:        "pipeline_status",
		Description: "Get the paused, archived and public status of one pipeline. Requires read consent and the account's normal pipeline access.",
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args PipelineStatusArgs) (*sdk.CallToolResult, PipelineStatus, error) {
		principal, ok := ctx.Value(principalKey{}).(mcpauth.Principal)
		if !ok || !AllowsAction(principal, atc.GetPipeline) {
			return nil, PipelineStatus{}, errors.New("read scope required")
		}
		result, err := pipelineStatus(ctx, api, principal, args)
		return nil, result, err
	})
	transport := mcpserver.NewHTTPHandler(func(r *http.Request) *sdk.Server {
		p, _ := r.Context().Value(principalKey{}).(mcpauth.Principal)
		if AllowsAction(p, atc.GetPipeline) {
			return readServer
		}
		return emptyServer
	})
	return auth.AuthorizeHTTP(func(w http.ResponseWriter, r *http.Request, p mcpauth.Principal) {
		// A recognized tool without its consent scope gets the OAuth challenge
		// a client needs to request additional consent. The SDK still validates
		// the complete message and arguments, and the tool rechecks authority.
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
			if err != nil {
				http.Error(w, "invalid or oversized MCP request", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var call struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if json.Unmarshal(body, &call) == nil && call.Method == "tools/call" && call.Params.Name == "pipeline_status" && !auth.RequireScope(w, p, mcpauth.ScopeRead) {
				return
			}
		}
		transport.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
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
