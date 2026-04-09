package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/api/mcpserver"
)

var _ = Describe("Server", func() {
	var (
		server *mcpserver.Server
	)

	BeforeEach(func() {
		server = mcpserver.NewServer()
	})

	Describe("protocol types marshaling", func() {
		It("returns valid JSON-RPC 2.0 responses", func() {
			body := jsonRPCBody("initialize", 1, map[string]any{
				"protocolVersion": "2024-11-05",
				"clientInfo":      map[string]any{"name": "test-client"},
			})
			resp := doMCP(server, body)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))

			var rpcResp jsonRPCResponse
			Expect(json.NewDecoder(resp.Body).Decode(&rpcResp)).To(Succeed())
			Expect(rpcResp.JSONRPC).To(Equal("2.0"))
			Expect(rpcResp.ID).To(BeEquivalentTo(json.RawMessage(`1`)))
			Expect(rpcResp.Error).To(BeNil())
		})

		It("returns error for malformed JSON", func() {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewBufferString(`{not json`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			var rpcResp jsonRPCResponse
			Expect(json.NewDecoder(w.Result().Body).Decode(&rpcResp)).To(Succeed())
			Expect(rpcResp.Error).NotTo(BeNil())
			Expect(rpcResp.Error.Code).To(Equal(-32700))
		})
	})

	Describe("dispatch", func() {
		It("handles initialize", func() {
			body := jsonRPCBody("initialize", 1, map[string]any{
				"protocolVersion": "2024-11-05",
				"clientInfo":      map[string]any{"name": "test"},
			})
			resp := doMCP(server, body)
			result := decodeResult(resp)

			Expect(result).To(HaveKey("protocolVersion"))
			Expect(result["protocolVersion"]).To(Equal("2024-11-05"))
			Expect(result).To(HaveKey("capabilities"))
			Expect(result).To(HaveKey("serverInfo"))
			serverInfo := result["serverInfo"].(map[string]any)
			Expect(serverInfo["name"]).To(Equal("concourse-mcp"))
		})

		It("handles notifications/initialized with 204", func() {
			body := jsonRPCBodyNoID("notifications/initialized", nil)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)

			Expect(w.Result().StatusCode).To(Equal(http.StatusNoContent))
		})

		It("handles ping", func() {
			body := jsonRPCBody("ping", 2, nil)
			resp := doMCP(server, body)
			result := decodeResult(resp)
			Expect(result).To(BeEmpty())
		})

		It("handles tools/list with empty tools", func() {
			body := jsonRPCBody("tools/list", 3, nil)
			resp := doMCP(server, body)
			result := decodeResult(resp)
			Expect(result).To(HaveKey("tools"))
			tools := result["tools"].([]any)
			Expect(tools).To(BeEmpty())
		})

		It("returns method not found for unknown methods", func() {
			body := jsonRPCBody("unknown/method", 4, nil)
			resp := doMCP(server, body)

			var rpcResp jsonRPCResponse
			Expect(json.NewDecoder(resp.Body).Decode(&rpcResp)).To(Succeed())
			Expect(rpcResp.Error).NotTo(BeNil())
			Expect(rpcResp.Error.Code).To(Equal(-32601))
		})

		It("handles tools/call for a registered tool", func() {
			server.AddTool("echo", "Echoes input", json.RawMessage(`{"type":"object"}`),
				func(ctx context.Context, args json.RawMessage) (any, error) {
					return map[string]string{"echoed": string(args)}, nil
				},
			)

			body := jsonRPCBody("tools/call", 5, map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"msg": "hello"},
			})
			resp := doMCP(server, body)
			result := decodeResult(resp)
			Expect(result).To(HaveKey("content"))
			content := result["content"].([]any)
			Expect(content).To(HaveLen(1))
			block := content[0].(map[string]any)
			Expect(block["type"]).To(Equal("text"))

			var echoed map[string]string
			Expect(json.Unmarshal([]byte(block["text"].(string)), &echoed)).To(Succeed())
			Expect(echoed["echoed"]).To(ContainSubstring("hello"))
		})

		It("returns error result for unknown tool", func() {
			body := jsonRPCBody("tools/call", 6, map[string]any{
				"name": "nonexistent",
			})
			resp := doMCP(server, body)
			result := decodeResult(resp)
			Expect(result).To(HaveKey("isError"))
			Expect(result["isError"]).To(BeTrue())
		})

		It("returns error result when tool handler errors", func() {
			server.AddTool("fail", "Always fails", json.RawMessage(`{"type":"object"}`),
				func(ctx context.Context, args json.RawMessage) (any, error) {
					return nil, io.ErrUnexpectedEOF
				},
			)

			body := jsonRPCBody("tools/call", 7, map[string]any{
				"name": "fail",
			})
			resp := doMCP(server, body)
			result := decodeResult(resp)
			Expect(result["isError"]).To(BeTrue())
		})

		It("handles tools/list with registered tools", func() {
			server.AddTool("my_tool", "Does things", json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
				func(ctx context.Context, args json.RawMessage) (any, error) {
					return nil, nil
				},
			)

			body := jsonRPCBody("tools/list", 8, nil)
			resp := doMCP(server, body)
			result := decodeResult(resp)
			tools := result["tools"].([]any)
			Expect(tools).To(HaveLen(1))
			tool := tools[0].(map[string]any)
			Expect(tool["name"]).To(Equal("my_tool"))
			Expect(tool["description"]).To(Equal("Does things"))
			Expect(tool).To(HaveKey("inputSchema"))
		})

		It("rejects non-POST requests with 405", func() {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp", nil)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)
			Expect(w.Result().StatusCode).To(Equal(http.StatusMethodNotAllowed))
		})
	})
})

// helpers

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func jsonRPCBody(method string, id int, params any) io.Reader {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	Expect(err).NotTo(HaveOccurred())
	return bytes.NewReader(data)
}

func jsonRPCBodyNoID(method string, params any) io.Reader {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		msg["params"] = params
	}
	data, err := json.Marshal(msg)
	Expect(err).NotTo(HaveOccurred())
	return bytes.NewReader(data)
}

func doMCP(server *mcpserver.Server, body io.Reader) *http.Response {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	return w.Result()
}

func decodeResult(resp *http.Response) map[string]any {
	var rpcResp jsonRPCResponse
	Expect(json.NewDecoder(resp.Body).Decode(&rpcResp)).To(Succeed())
	Expect(rpcResp.Error).To(BeNil())
	var result map[string]any
	Expect(json.Unmarshal(rpcResp.Result, &result)).To(Succeed())
	return result
}
