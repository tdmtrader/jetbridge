// Package mcpserver provides MCP transport without dependencies on the CI platform.
package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
)

type ToolHandler func(context.Context, json.RawMessage) (any, error)
type Server struct {
	server  *mcp.Server
	handler http.Handler
}

func NewServer() *Server {
	s := &Server{server: NewProtocolServer()}
	s.handler = NewHTTPHandler(func(*http.Request) *mcp.Server { return s.server })
	return s
}
func NewProtocolServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "jetbridge-mcp", Version: "0.1.0"},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
}

// NewHTTPHandler serves the 2025-11-25 Streamable HTTP compatibility profile.
// Each POST is independent: tokens are checked by the caller on every request,
// and no in-memory session affinity is needed across replicas or restarts.
// The initial read-only surface has no server-initiated event stream.
func NewHTTPHandler(selectServer func(*http.Request) *mcp.Server) http.Handler {
	sdk := mcp.NewStreamableHTTPHandler(selectServer, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		sdk.ServeHTTP(w, r)
	}))
}

// AddTool preserves the generic registration interface; product adapters should
// prefer the SDK's typed AddTool for generated and validated argument schemas.
func (s *Server) AddTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.server.AddTool(&mcp.Tool{Name: name, Description: description, InputSchema: schema},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			value, err := handler(ctx, req.Params.Arguments)
			if err != nil {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
			}
			data, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
		})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }
func MustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
