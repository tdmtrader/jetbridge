package devmcp_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/devmcp"
)

func post(url, body string) map[string]any {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	var decoded map[string]any
	Expect(json.NewDecoder(resp.Body).Decode(&decoded)).To(Succeed())
	return decoded
}

var _ = Describe("Server", func() {
	var ts *httptest.Server

	newServer := func(heartbeat time.Duration, handler devmcp.ToolHandler) {
		s := devmcp.NewServer(heartbeat)
		s.AddTool("echo", "echoes back", json.RawMessage(`{"type":"object"}`), handler)
		ts = httptest.NewServer(s)
		DeferCleanup(ts.Close)
	}

	It("answers initialize with the tools capability", func() {
		newServer(0, nil)
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
		result := resp["result"].(map[string]any)
		Expect(result["protocolVersion"]).NotTo(BeEmpty())
		caps := result["capabilities"].(map[string]any)
		Expect(caps).To(HaveKey("tools"))
	})

	It("lists registered tools with schemas", func() {
		newServer(0, nil)
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		tools := resp["result"].(map[string]any)["tools"].([]any)
		Expect(tools).To(HaveLen(1))
		tool := tools[0].(map[string]any)
		Expect(tool["name"]).To(Equal("echo"))
		Expect(tool["description"]).To(Equal("echoes back"))
		Expect(tool["inputSchema"]).To(HaveKeyWithValue("type", "object"))
	})

	It("returns the tool payload as a single text content block", func() {
		newServer(0, func(_ context.Context, _ json.RawMessage, _ devmcp.ProgressFunc) (any, error) {
			return map[string]any{"status": "ok", "summary": "done"}, nil
		})
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
		Expect(resp).NotTo(HaveKey("error"))
		content := resp["result"].(map[string]any)["content"].([]any)
		Expect(content).To(HaveLen(1))
		block := content[0].(map[string]any)
		Expect(block["type"]).To(Equal("text"))
		Expect(block["text"]).To(MatchJSON(`{"status":"ok","summary":"done"}`))
	})

	It("maps handler errors to JSON-RPC -32602 (malformed input only)", func() {
		newServer(0, func(_ context.Context, _ json.RawMessage, _ devmcp.ProgressFunc) (any, error) {
			return nil, devmcp.ErrInvalidParams("unknown component: nope")
		})
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
		rpcErr := resp["error"].(map[string]any)
		Expect(rpcErr["code"]).To(BeEquivalentTo(-32602))
		Expect(rpcErr["message"]).To(ContainSubstring("unknown component"))
	})

	It("returns -32602 for an unknown tool and -32601 for an unknown method", func() {
		newServer(0, nil)
		resp := post(ts.URL, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32602))

		resp = post(ts.URL, `{"jsonrpc":"2.0","id":6,"method":"bogus/method"}`)
		Expect(resp["error"].(map[string]any)["code"]).To(BeEquivalentTo(-32601))
	})

	It("streams progress notifications over SSE when the client asks for them", func() {
		newServer(50*time.Millisecond, func(_ context.Context, _ json.RawMessage, progress devmcp.ProgressFunc) (any, error) {
			progress("halfway there")
			time.Sleep(150 * time.Millisecond)
			return map[string]any{"status": "ok"}, nil
		})

		req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(
			`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"progressToken":"tok-1"}}}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring(`"notifications/progress"`))
		Expect(string(body)).To(ContainSubstring(`"tok-1"`))
		Expect(string(body)).To(ContainSubstring("halfway there"))
		Expect(string(body)).To(ContainSubstring(`"id":7`))
	})

	It("survives a panicking tool handler on the SSE path and keeps serving", func() {
		newServer(50*time.Millisecond, func(_ context.Context, _ json.RawMessage, _ devmcp.ProgressFunc) (any, error) {
			panic("tool exploded")
		})

		req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(
			`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"echo","arguments":{},"_meta":{"progressToken":"tok-2"}}}`))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")

		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.Header.Get("Content-Type")).To(Equal("text/event-stream"))

		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(ContainSubstring(`"code":-32603`))
		Expect(string(body)).To(ContainSubstring("panicked"))
		Expect(string(body)).To(ContainSubstring(`"id":8`))

		// the sidecar is still alive after the panic
		pong := post(ts.URL, `{"jsonrpc":"2.0","id":9,"method":"ping"}`)
		Expect(pong).NotTo(HaveKey("error"))
		Expect(pong).To(HaveKey("result"))
	})
})
