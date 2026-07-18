package mcpserver

import "encoding/json"

// JSON-RPC 2.0 message types for the MCP protocol.

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol types

type initializeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Capabilities    serverCapability `json:"capabilities"`
	ServerInfo      entityInfo       `json:"serverInfo"`
}

type entityInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type serverCapability struct {
	Tools *toolCapability `json:"tools,omitempty"`
}

type toolCapability struct{}

type toolsListResult struct {
	Tools []ToolDef `json:"tools"`
}

type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Meta carries the MCP progress opt-in: a client that wants SSE progress
	// sends params._meta.progressToken; it is echoed verbatim in every
	// notifications/progress frame (04 Task 1 wire spec).
	Meta *struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta,omitempty"`
}

// jsonRPCNotification is a server-initiated message (no id) — the
// notifications/progress frames on the SSE path.
type jsonRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
