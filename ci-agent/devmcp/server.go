// Package devmcp implements a generic, config-driven dev-mcp server
// (docs/superpowers/plans/agentic-platform/00-shared-contracts.md §3.1):
// a streamable-HTTP MCP server whose five tools are backed by per-component
// commands declared in dev-mcp.yml.
//
// The JSON-RPC plumbing mirrors the main module's atc/api/mcpserver
// precedent (which this standalone module cannot import) and adds SSE
// progress streaming, which the precedent lacks.
package devmcp

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

// DefaultHeartbeat is half the contract's "progress at least every 30s"
// bound, leaving margin for scheduling jitter.
const DefaultHeartbeat = 15 * time.Second

// ProgressFunc reports the latest human-readable progress line for a
// running tool call.
type ProgressFunc func(message string)

// ToolHandler handles one MCP tool call. Returning a non-nil error signals
// MALFORMED INPUT ONLY (mapped to JSON-RPC -32602); run outcomes — including
// tooling breakage — are expressed in the returned payload's status field.
type ToolHandler func(ctx context.Context, args json.RawMessage, progress ProgressFunc) (any, error)

// ErrInvalidParams builds the error a ToolHandler returns for malformed
// input; the server maps it to JSON-RPC -32602.
func ErrInvalidParams(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// ToolDef describes one registered tool for tools/list.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Server is a streamable-HTTP MCP server.
type Server struct {
	tools     []ToolDef
	handlers  map[string]ToolHandler
	heartbeat time.Duration
}

// NewServer creates a server. heartbeat <= 0 uses DefaultHeartbeat.
func NewServer(heartbeat time.Duration) *Server {
	if heartbeat <= 0 {
		heartbeat = DefaultHeartbeat
	}
	return &Server{handlers: map[string]ToolHandler{}, heartbeat: heartbeat}
}

// AddTool registers a tool (atc/api/mcpserver registration style).
func (s *Server) AddTool(name, description string, schema json.RawMessage, handler ToolHandler) {
	s.tools = append(s.tools, ToolDef{Name: name, Description: description, InputSchema: schema})
	s.handlers[name] = handler
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      *struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta,omitempty"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ServeHTTP implements the MCP streamable HTTP transport: POST requests
// carry JSON-RPC messages; responses are JSON, or SSE for progress-bearing
// tools/call requests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "failed to read request body"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}

	switch req.Method {
	case "initialize":
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "dev-mcp", "version": "1"},
		}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		tools := s.tools
		if tools == nil {
			tools = []ToolDef{}
		}
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}})
	case "ping":
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/call":
		s.handleToolsCall(w, r, &req)
	default:
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)}})
	}
}

func (s *Server) handleToolsCall(w http.ResponseWriter, r *http.Request, req *rpcRequest) {
	var params callToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		return
	}
	handler, ok := s.handlers[params.Name]
	if !ok {
		writeJSON(w, &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool: %s", params.Name)}})
		return
	}

	flusher, canFlush := w.(http.Flusher)
	wantSSE := canFlush &&
		strings.Contains(r.Header.Get("Accept"), "text/event-stream") &&
		params.Meta != nil && len(params.Meta.ProgressToken) > 0

	if !wantSSE {
		result, err := handler(r.Context(), params.Arguments, func(string) {})
		writeJSON(w, s.toolResponse(req.ID, result, err))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	progressCh := make(chan string, 64)
	done := make(chan *rpcResponse, 1)
	go func() {
		// a panicking handler must not kill the sidecar: recover and
		// surface it as the internal-error frame (-32603, the code
		// toolResponse already uses for marshal failures).
		defer func() {
			if rec := recover(); rec != nil {
				done <- &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32603, Message: fmt.Sprintf("tool handler panicked: %v", rec)}}
			}
		}()
		result, err := handler(r.Context(), params.Arguments, func(msg string) {
			select {
			case progressCh <- msg:
			default: // never block the running tool on a slow consumer
			}
		})
		done <- s.toolResponse(req.ID, result, err)
	}()

	emit := func(msg string) {
		writeSSE(w, &rpcResponse{
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

func (s *Server) toolResponse(id json.RawMessage, result any, err error) *rpcResponse {
	if err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32602, Message: err.Error()}}
	}
	payload, merr := json.Marshal(result)
	if merr != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: -32603, Message: fmt.Sprintf("marshal result: %s", merr)}}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: id, Result: callToolResult{
		Content: []contentBlock{{Type: "text", Text: string(payload)}},
	}}
}

func writeJSON(w http.ResponseWriter, resp *rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeSSE(w io.Writer, resp *rpcResponse) {
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
}
