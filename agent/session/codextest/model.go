package codextest

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// Model is a scripted model behind a local HTTP server speaking the backend
// protocol the pinned Codex uses with ChatGPT subscription auth: Responses
// over server-sent events, after its WebSocket transport is declined. Each
// model request is answered by the next Turn; Codex's other backend calls
// (model catalog, settings, analytics, metrics) are answered as a real
// backend without them would. Any other request, a request without Auth's
// bearer token, or a request past the end of the script fails the test.
type Model struct {
	URL string

	mu       sync.Mutex
	turns    []Turn
	requests []Request
	faults   []string
}

// Turn answers one model request with the items the model outputs.
type Turn func(Request) []Item

// Steps is a script in which each item is one turn.
func Steps(items ...Item) []Turn {
	turns := make([]Turn, len(items))
	for i, item := range items {
		turns[i] = func(Request) []Item { return []Item{item} }
	}
	return turns
}

// Item is one Responses output item.
type Item map[string]any

var calls atomic.Int64

func callID() string { return fmt.Sprintf("call_%d", calls.Add(1)) }

// Say is an assistant message. A session's last turn says its final answer,
// which Codex writes to the --output-last-message file.
func Say(text string) Item {
	return Item{"type": "message", "role": "assistant", "id": "msg_" + callID(), "content": []any{map[string]any{"type": "output_text", "text": text}}}
}

// ToolCall calls tool on the configured MCP server, as a model sees it: a
// function in the mcp__<server> namespace.
func ToolCall(server, tool string, args any) Item {
	item := FunctionCall(tool, args)
	item["namespace"] = "mcp__" + server
	return item
}

// FunctionCall calls any function tool by name, offered or not.
func FunctionCall(name string, args any) Item {
	b, _ := json.Marshal(args)
	return Item{"type": "function_call", "call_id": callID(), "name": name, "arguments": string(b)}
}

// Patch calls Codex's apply_patch tool with a patch in its own format.
func Patch(patch string) Item {
	return Item{"type": "custom_tool_call", "call_id": callID(), "name": "apply_patch", "input": patch}
}

// Fail ends the turn the way the backend reports a refused request, such as
// a subscription whose plan does not include the model (usage_not_included).
// Nothing after it in the turn is sent.
func Fail(code, message string) Item {
	return Item{"type": "response.failed", "response": map[string]any{"status": "failed", "error": map[string]any{"code": code, "message": message}}}
}

// Request is one model request, decoded.
type Request struct {
	// Done closes when Codex abandons the request, for a turn that waits.
	Done  <-chan struct{}   `json:"-"`
	Model string            `json:"model"`
	Input []json.RawMessage `json:"input"`
	Tools []struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	} `json:"tools"`
}

// Offered lists the tools Codex offered the model, a namespaced tool as
// namespace.name.
func (r Request) Offered() []string {
	var names []string
	for _, t := range r.Tools {
		if t.Type == "namespace" {
			for _, n := range t.Tools {
				names = append(names, t.Name+"."+n.Name)
			}
			continue
		}
		names = append(names, t.Name)
	}
	return names
}

// Output is what Codex returned to the model for call, if it has yet.
func (r Request) Output(call Item) (string, bool) {
	for _, raw := range r.Input {
		var in struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &in) != nil || in.CallID != call["call_id"] || !strings.HasSuffix(in.Type, "_output") {
			continue
		}
		var text string
		if json.Unmarshal(in.Output, &text) == nil {
			return text, true
		}
		var parts []struct{ Text string }
		json.Unmarshal(in.Output, &parts)
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String(), true
	}
	return "", false
}

// NewModel starts a model that answers with turns, in order.
func NewModel(t testing.TB, turns ...Turn) *Model {
	t.Helper()
	m := &Model{turns: turns}
	srv := httptest.NewServer(http.HandlerFunc(m.serve))
	m.URL = srv.URL
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, f := range m.faults {
			t.Error("scripted model: " + f)
		}
	})
	return m
}

// Requests are the model requests served so far.
func (m *Model) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.requests...)
}

// Output is what Codex returned to the model for call, in any request.
func (m *Model) Output(call Item) (string, bool) {
	for _, r := range m.Requests() {
		if out, ok := r.Output(call); ok {
			return out, true
		}
	}
	return "", false
}

func (m *Model) fault(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.faults = append(m.faults, fmt.Sprintf(format, args...))
}

func (m *Model) serve(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	switch {
	case r.Header.Get("Upgrade") != "":
		// Declining the WebSocket upgrade is how a backend without it makes
		// Codex fall back to server-sent events.
		w.WriteHeader(http.StatusUpgradeRequired)
	case route == "POST /backend-api/codex/responses":
		m.respond(w, r)
	case route == "GET /backend-api/codex/models", route == "GET /backend-api/wham/settings/user", route == "POST /backend-api/wham/usage/thread-estimates/query":
		// Codex then uses the model catalog bundled with the pinned release.
		http.NotFound(w, r)
	case route == "POST /backend-api/codex/analytics-events/events", route == "POST /otlp/v1/metrics":
		io.Copy(io.Discard, r.Body)
		w.Write([]byte("{}"))
	default:
		m.fault("Codex called an unscripted endpoint: %s", route)
		http.NotFound(w, r)
	}
}

func (m *Model) respond(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+AccessToken {
		// Never 401: that would send Codex to refresh the token upstream.
		m.fault("a model request did not present the staged credential: %v", r.Header)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body io.Reader = r.Body
	switch r.Header.Get("Content-Encoding") {
	case "zstd":
		d, err := zstd.NewReader(r.Body)
		if err != nil {
			m.fault("undecodable request: %v", err)
			return
		}
		defer d.Close()
		body = d
	case "gzip":
		g, err := gzip.NewReader(r.Body)
		if err != nil {
			m.fault("undecodable request: %v", err)
			return
		}
		body = g
	}
	req := Request{Done: r.Context().Done()}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		m.fault("undecodable request: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	n := len(m.requests)
	m.requests = append(m.requests, req)
	var turn Turn
	if n < len(m.turns) {
		turn = m.turns[n]
	}
	m.mu.Unlock()
	id := fmt.Sprintf("resp_%d", n+1)
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(e map[string]any) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], b)
	}
	if turn == nil {
		m.fault("Codex made model request %d; the script has %d turns", n+1, len(m.turns))
		event(map[string]any{"type": "response.failed", "response": map[string]any{"id": id, "error": map[string]any{"code": "server_error", "message": "unscripted turn"}}})
		return
	}
	event(map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
	for _, item := range turn(req) {
		if item["type"] == "response.failed" {
			item["response"].(map[string]any)["id"] = id
			event(item)
			return
		}
		event(map[string]any{"type": "response.output_item.done", "item": item})
	}
	event(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]any{
		"input_tokens": 1, "input_tokens_details": nil, "output_tokens": 1, "output_tokens_details": nil, "total_tokens": 2}}})
}
