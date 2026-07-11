package devmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// Client is the code-callable dev-mcp interface (00-shared-contracts.md §3.1).
// The harvest step invokes gates exclusively through this interface.
//
//counterfeiter:generate . Client
type Client interface {
	ListComponents(ctx context.Context) ([]Component, error)
	Build(ctx context.Context, component string) (*ToolResult, error)
	RunTests(ctx context.Context, component, focus string) (*ToolResult, error)
	Lint(ctx context.Context, component string) (*ToolResult, error)
	AffectedComponents(ctx context.Context, paths []string) ([]string, error)
}

// RPCError is a JSON-RPC-level error from the server. Per the contract
// taxonomy this means MALFORMED INPUT (unknown tool/component, bad args);
// run failures come back as ToolResult.Status, never as RPCError.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// HTTPClient speaks MCP streamable HTTP: a single POST endpoint with SSE
// responses for long calls. It applies NO transport timeout — per §3.1 the
// caller applies a per-gate timeout through ctx.
type HTTPClient struct {
	endpoint   string
	httpClient *http.Client
	onProgress func(tool, message string)
	nextID     atomic.Int64
}

// Option configures an HTTPClient.
type Option func(*HTTPClient)

// WithProgress registers a callback invoked for every notifications/progress
// message received during a tool call.
func WithProgress(fn func(tool, message string)) Option {
	return func(c *HTTPClient) { c.onProgress = fn }
}

// WithHTTPClient overrides the underlying *http.Client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *HTTPClient) { c.httpClient = hc }
}

// NewClient returns a Client for the dev-mcp server at endpoint, e.g.
// os.Getenv(EnvEndpoint) == "http://127.0.0.1:7780/mcp".
func NewClient(endpoint string, opts ...Option) *HTTPClient {
	c := &HTTPClient{
		endpoint:   endpoint,
		httpClient: &http.Client{}, // deliberately no Timeout: ctx governs
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type callToolParams struct {
	Name      string    `json:"name"`
	Arguments any       `json:"arguments,omitempty"`
	Meta      *callMeta `json:"_meta,omitempty"`
}

type callMeta struct {
	ProgressToken string `json:"progressToken"`
}

type progressParams struct {
	ProgressToken json.RawMessage `json:"progressToken"`
	Message       string          `json:"message"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (c *HTTPClient) callTool(ctx context.Context, tool string, args any, out any) error {
	id := c.nextID.Add(1)
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params: callToolParams{
			Name:      tool,
			Arguments: args,
			Meta:      &callMeta{ProgressToken: fmt.Sprintf("devmcp-%d", id)},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call %s: %w", tool, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("call %s: unexpected HTTP status %d", tool, resp.StatusCode)
	}

	var final rpcMessage
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		final, err = c.readSSE(resp.Body, tool)
	} else {
		err = json.NewDecoder(resp.Body).Decode(&final)
	}
	if err != nil {
		return fmt.Errorf("read %s response: %w", tool, err)
	}
	if final.Error != nil {
		return final.Error
	}

	var ctr callToolResult
	if err := json.Unmarshal(final.Result, &ctr); err != nil {
		return fmt.Errorf("decode %s result: %w", tool, err)
	}
	if ctr.IsError {
		text := ""
		if len(ctr.Content) > 0 {
			text = ctr.Content[0].Text
		}
		return fmt.Errorf("%s: tool error: %s", tool, text)
	}
	if len(ctr.Content) == 0 {
		return fmt.Errorf("%s: empty result content", tool)
	}
	if err := json.Unmarshal([]byte(ctr.Content[0].Text), out); err != nil {
		return fmt.Errorf("decode %s payload: %w", tool, err)
	}
	return nil
}

// readSSE consumes an SSE stream, forwarding notifications/progress to the
// progress callback and returning the final JSON-RPC response message.
func (c *HTTPClient) readSSE(body io.Reader, tool string) (rpcMessage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		if line != "" || data.Len() == 0 {
			continue // event-name lines, comments, or blank keepalives
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(data.String()), &msg); err != nil {
			return rpcMessage{}, fmt.Errorf("bad SSE frame: %w", err)
		}
		data.Reset()
		if msg.Method == "notifications/progress" {
			if c.onProgress != nil {
				var p progressParams
				_ = json.Unmarshal(msg.Params, &p)
				c.onProgress(tool, p.Message)
			}
			continue
		}
		if len(msg.ID) > 0 {
			return msg, nil // the response for our call
		}
	}
	if err := scanner.Err(); err != nil {
		return rpcMessage{}, err
	}
	return rpcMessage{}, fmt.Errorf("SSE stream ended without a response")
}

type listComponentsResult struct {
	Components []Component `json:"components"`
}

func (c *HTTPClient) ListComponents(ctx context.Context) ([]Component, error) {
	var res listComponentsResult
	if err := c.callTool(ctx, ToolListComponents, struct{}{}, &res); err != nil {
		return nil, err
	}
	return res.Components, nil
}

type componentOnlyArgs struct {
	Component string `json:"component,omitempty"`
}

func (c *HTTPClient) Build(ctx context.Context, component string) (*ToolResult, error) {
	var res ToolResult
	if err := c.callTool(ctx, ToolBuild, componentOnlyArgs{Component: component}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type runTestsArgs struct {
	Component string `json:"component,omitempty"`
	Focus     string `json:"focus,omitempty"`
}

func (c *HTTPClient) RunTests(ctx context.Context, component, focus string) (*ToolResult, error) {
	var res ToolResult
	if err := c.callTool(ctx, ToolRunTests, runTestsArgs{Component: component, Focus: focus}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *HTTPClient) Lint(ctx context.Context, component string) (*ToolResult, error) {
	var res ToolResult
	if err := c.callTool(ctx, ToolLint, componentOnlyArgs{Component: component}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

type affectedArgs struct {
	ChangedPaths []string `json:"changed_paths"`
}

func (c *HTTPClient) AffectedComponents(ctx context.Context, paths []string) ([]string, error) {
	if paths == nil {
		// nil marshals as JSON null, which the server rejects (-32602:
		// changed_paths is required); normalize to an empty array.
		paths = []string{}
	}
	var res AffectedResult
	if err := c.callTool(ctx, ToolAffectedComponents, affectedArgs{ChangedPaths: paths}, &res); err != nil {
		return nil, err
	}
	return res.Components, nil
}
