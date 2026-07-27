package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const protocolVersion = "2024-11-05"

// DefaultHeartbeat is half the contract's "progress at least every 30s" bound
// (contracts §3.1), leaving 4x margin under the claude CLI's empirical 60s
// abandonment of progress-free tools/call requests (F13).
const DefaultHeartbeat = 15 * time.Second

// ToolHandler is a function that handles an MCP tool call. progress reports
// the latest human-readable progress line for a long-running call; it is
// never nil — buffered (non-SSE) calls receive a no-op func(string) {}.
type ToolHandler func(ctx context.Context, args json.RawMessage, progress func(string)) (any, error)

// Server is an MCP server that dispatches tool calls over HTTP.
// It implements http.Handler using the MCP Streamable HTTP transport,
// answering progress-bearing tools/call requests over SSE with coalescing
// heartbeat notifications.
type Server struct {
	tools     []ToolDef
	handlers  map[string]ToolHandler
	heartbeat time.Duration
}

// NewServer creates an MCP server with no tools registered and the default
// 15s progress heartbeat (existing ATC callers compile unchanged).
func NewServer() *Server {
	return NewServerWithHeartbeat(0)
}

// NewServerWithHeartbeat creates a server with the given progress-heartbeat
// interval; d <= 0 uses DefaultHeartbeat. Sidecars construct via
// NewServerWithHeartbeat(cfg.ProgressInterval).
func NewServerWithHeartbeat(d time.Duration) *Server {
	if d <= 0 {
		d = DefaultHeartbeat
	}
	return &Server{
		handlers:  make(map[string]ToolHandler),
		heartbeat: d,
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
// POST requests contain JSON-RPC messages; responses are JSON, or SSE for
// progress-bearing tools/call requests.
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

	if req.Method == "tools/call" {
		s.handleToolsCall(w, r, &req)
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

// handleToolsCall answers a tools/call request: buffered JSON by default, SSE
// with heartbeat progress when the client opts in via Accept: text/event-stream
// AND params._meta.progressToken.
// Error mapping is UNCHANGED from the buffered-only server: a handler error is
// an isError=true tool result — never -32602 — in both modes.
func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req *jsonRPCRequest) {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32602, Message: "invalid params"},
		})
		return
	}

	handler, ok := s.handlers[params.Name]
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.toolErrorResponse(req.ID, fmt.Sprintf("unknown tool: %s", params.Name)))
		return
	}

	flusher, canFlush := w.(http.Flusher)
	wantSSE := canFlush &&
		strings.Contains(r.Header.Get("Accept"), "text/event-stream") &&
		params.Meta != nil && len(params.Meta.ProgressToken) > 0

	if !wantSSE {
		result, err := handler(r.Context(), params.Arguments, func(string) {})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.toolResponse(req.ID, result, err))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progressCh := make(chan string, 64)
	done := make(chan *jsonRPCResponse, 1)
	go func() {
		result, err := handler(r.Context(), params.Arguments, func(msg string) {
			select {
			case progressCh <- msg:
			default: // never block the running tool on a slow consumer
			}
		})
		done <- s.toolResponse(req.ID, result, err)
	}()

	emit := func(msg string) {
		writeSSE(w, &jsonRPCNotification{
			JSONRPC: "2.0",
			Method:  "notifications/progress",
			Params: map[string]any{
				"progressToken": params.Meta.ProgressToken,
				"message":       msg,
			},
		})
		flusher.Flush()
	}

	ticker := time.NewTicker(s.heartbeat)
	defer ticker.Stop()
	lastMsg := fmt.Sprintf("running %s", params.Name)
	for {
		select {
		case msg := <-progressCh:
			lastMsg = msg // coalesce: remember, emit on the next tick
		case <-ticker.C:
			emit(lastMsg)
		case resp := <-done:
			writeSSE(w, resp)
			flusher.Flush()
			return
		}
	}
}

// toolResponse builds the tools/call response with the buffered path's exact
// (frozen) error mapping: handler error => isError=true tool result.
func (s *Server) toolResponse(id json.RawMessage, result any, err error) *jsonRPCResponse {
	if err != nil {
		return s.toolErrorResponse(id, fmt.Sprintf("error: %s", err.Error()))
	}
	resultJSON, merr := json.Marshal(result)
	if merr != nil {
		return s.toolErrorResponse(id, fmt.Sprintf("error marshaling result: %s", merr.Error()))
	}
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: callToolResult{
			Content: []contentBlock{{Type: "text", Text: string(resultJSON)}},
		},
	}
}

func (s *Server) toolErrorResponse(id json.RawMessage, msg string) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: callToolResult{
			Content: []contentBlock{{Type: "text", Text: msg}},
			IsError: true,
		},
	}
}

func writeSSE(w io.Writer, msg any) {
	data, _ := json.Marshal(msg)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
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
