package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const protocolVersion = "2024-11-05"

// ToolHandler is a function that handles an MCP tool call.
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

// Server is an MCP server that dispatches tool calls over HTTP.
// It implements http.Handler using the MCP Streamable HTTP transport.
type Server struct {
	tools    []ToolDef
	handlers map[string]ToolHandler
}

// NewServer creates an MCP server with no tools registered.
func NewServer() *Server {
	return &Server{
		handlers: make(map[string]ToolHandler),
	}
}

// AddTool registers a tool with the server.
func (s *Server) AddTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.tools = append(s.tools, ToolDef{
		Name:        name,
		Description: description,
		InputSchema: schema,
	})
	s.handlers[name] = handler
}

// ServeHTTP implements http.Handler for the MCP Streamable HTTP transport.
// POST requests contain JSON-RPC messages; responses are JSON.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeHTTPError(w, -32700, "failed to read request body")
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeHTTPError(w, -32700, "parse error")
		return
	}

	resp := s.dispatch(r.Context(), &req)
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) dispatch(ctx context.Context, req *jsonRPCRequest) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "notifications/initialized":
		return nil
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "ping":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func (s *Server) handleInitialize(req *jsonRPCRequest) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: initializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities:    serverCapability{Tools: &toolCapability{}},
			ServerInfo:      entityInfo{Name: "concourse-mcp", Version: "0.1.0"},
		},
	}
}

func (s *Server) handleToolsList(req *jsonRPCRequest) *jsonRPCResponse {
	tools := s.tools
	if tools == nil {
		tools = []ToolDef{}
	}
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  toolsListResult{Tools: tools},
	}
}

func (s *Server) handleToolsCall(ctx context.Context, req *jsonRPCRequest) *jsonRPCResponse {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: "invalid params"},
		}
	}

	handler, ok := s.handlers[params.Name]
	if !ok {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: callToolResult{
				Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("unknown tool: %s", params.Name)}},
				IsError: true,
			},
		}
	}

	result, err := handler(ctx, params.Arguments)
	if err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: callToolResult{
				Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("error: %s", err.Error())}},
				IsError: true,
			},
		}
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: callToolResult{
				Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("error marshaling result: %s", err.Error())}},
				IsError: true,
			},
		}
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: callToolResult{
			Content: []contentBlock{{Type: "text", Text: string(resultJSON)}},
		},
	}
}

func writeHTTPError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(&jsonRPCResponse{
		JSONRPC: "2.0",
		Error:   &jsonRPCError{Code: code, Message: message},
	})
}

// MustJSON marshals v to JSON or panics. Used for tool schema definitions.
func MustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
