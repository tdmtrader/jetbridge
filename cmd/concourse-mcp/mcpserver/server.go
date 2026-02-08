package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const protocolVersion = "2024-11-05"

// ToolHandler is a function that handles an MCP tool call.
type ToolHandler func(ctx context.Context, args json.RawMessage) (any, error)

// Server is the MCP server that dispatches tool calls to handlers.
type Server struct {
	tools    []toolDef
	handlers map[string]ToolHandler
	reader   io.Reader
	writer   io.Writer
}

// New creates a Server wired to the given Concourse client and team.
func New(client ClientAPI, team TeamAPI, apiURL string) *Server {
	s := &Server{
		handlers: make(map[string]ToolHandler),
		reader:   os.Stdin,
		writer:   os.Stdout,
	}
	registerTools(s, client, team, apiURL)
	return s
}

// NewWithIO creates a Server with custom reader/writer (for testing).
func NewWithIO(client ClientAPI, team TeamAPI, apiURL string, r io.Reader, w io.Writer) *Server {
	s := &Server{
		handlers: make(map[string]ToolHandler),
		reader:   r,
		writer:   w,
	}
	registerTools(s, client, team, apiURL)
	return s
}

func (s *Server) addTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.tools = append(s.tools, toolDef{
		Name:        name,
		Description: description,
		InputSchema: schema,
	})
	s.handlers[name] = handler
}

// Run reads JSON-RPC messages from the reader and writes responses to the writer.
func (s *Server) Run(ctx context.Context) error {
	scanner := bufio.NewScanner(s.reader)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max message
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "parse error")
			continue
		}

		resp := s.dispatch(ctx, &req)
		if resp != nil {
			s.writeJSON(resp)
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, req *jsonRPCRequest) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized":
		return nil // notification, no response
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
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  toolsListResult{Tools: s.tools},
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

func (s *Server) writeJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		// If we can't marshal the response, send a JSON-RPC internal error.
		fallback := fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal: marshal error: %s"}}`, err.Error())
		fmt.Fprintf(s.writer, "%s\n", fallback)
		return
	}
	fmt.Fprintf(s.writer, "%s\n", data)
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	s.writeJSON(&jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	})
}
