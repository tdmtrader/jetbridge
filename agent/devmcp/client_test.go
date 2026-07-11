package devmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
)

// toolTextResult wraps a payload the way MCP servers return tool results:
// a single text content block containing the JSON object.
func toolTextResult(id json.RawMessage, payload string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": payload}},
		},
	}
}

var _ = Describe("HTTPClient", func() {
	Describe("buffered JSON responses", func() {
		It("sends tools/call with arguments and a progress token, and decodes the payload", func() {
			var gotBody map[string]any
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal(http.MethodPost))
				Expect(r.Header.Get("Content-Type")).To(Equal("application/json"))
				Expect(r.Header.Get("Accept")).To(ContainSubstring("text/event-stream"))
				Expect(json.NewDecoder(r.Body).Decode(&gotBody)).To(Succeed())

				w.Header().Set("Content-Type", "application/json")
				id, _ := json.Marshal(gotBody["id"])
				json.NewEncoder(w).Encode(toolTextResult(id,
					`{"status":"ok","summary":"built","duration_seconds":2.5}`))
			}))
			defer ts.Close()

			res, err := devmcp.NewClient(ts.URL).Build(context.Background(), "atc")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Status).To(Equal(devmcp.StatusOK))
			Expect(res.Summary).To(Equal("built"))
			Expect(res.DurationSeconds).To(Equal(2.5))

			Expect(gotBody["method"]).To(Equal("tools/call"))
			params := gotBody["params"].(map[string]any)
			Expect(params["name"]).To(Equal("build"))
			Expect(params["arguments"]).To(Equal(map[string]any{"component": "atc"}))
			Expect(params["_meta"].(map[string]any)["progressToken"]).NotTo(BeEmpty())
		})

		It("returns AffectedComponents ids from the result payload", func() {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				args := body["params"].(map[string]any)["arguments"].(map[string]any)
				Expect(args["changed_paths"]).To(Equal([]any{"atc/api/handler.go"}))

				w.Header().Set("Content-Type", "application/json")
				id, _ := json.Marshal(body["id"])
				json.NewEncoder(w).Encode(toolTextResult(id,
					`{"components":["atc"],"unmapped_paths":[]}`))
			}))
			defer ts.Close()

			ids, err := devmcp.NewClient(ts.URL).AffectedComponents(
				context.Background(), []string{"atc/api/handler.go"})
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(Equal([]string{"atc"}))
		})

		It("sends nil changed_paths as an empty array, never null (the server rejects null with -32602)", func() {
			var gotArgs map[string]any
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				gotArgs = body["params"].(map[string]any)["arguments"].(map[string]any)

				w.Header().Set("Content-Type", "application/json")
				id, _ := json.Marshal(body["id"])
				json.NewEncoder(w).Encode(toolTextResult(id,
					`{"components":[],"unmapped_paths":[]}`))
			}))
			defer ts.Close()

			ids, err := devmcp.NewClient(ts.URL).AffectedComponents(context.Background(), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(ids).To(BeEmpty())
			Expect(gotArgs["changed_paths"]).To(Equal([]any{}))
		})

		It("maps JSON-RPC errors to *devmcp.RPCError", func() {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      body["id"],
					"error":   map[string]any{"code": -32602, "message": "unknown component: nope"},
				})
			}))
			defer ts.Close()

			_, err := devmcp.NewClient(ts.URL).Build(context.Background(), "nope")
			var rpcErr *devmcp.RPCError
			Expect(errors.As(err, &rpcErr)).To(BeTrue(), "got %v", err)
			Expect(rpcErr.Code).To(Equal(-32602))
			Expect(rpcErr.Message).To(ContainSubstring("unknown component"))
		})
	})

	Describe("SSE responses", func() {
		It("forwards progress notifications and returns the final response", func() {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				Expect(json.NewDecoder(r.Body).Decode(&body)).To(Succeed())
				id, _ := json.Marshal(body["id"])

				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				fmt.Fprintf(w, "event: message\ndata: %s\n\n",
					`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"t","message":"running case 3"}}`)
				flusher.Flush()

				final, _ := json.Marshal(toolTextResult(id,
					`{"status":"ok","summary":"10 cases passed","duration_seconds":1.2}`))
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", final)
				flusher.Flush()
			}))
			defer ts.Close()

			var mu sync.Mutex
			var progress []string
			client := devmcp.NewClient(ts.URL, devmcp.WithProgress(func(tool, msg string) {
				mu.Lock()
				progress = append(progress, tool+"|"+msg)
				mu.Unlock()
			}))

			res, err := client.RunTests(context.Background(), "app", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Status).To(Equal(devmcp.StatusOK))
			Expect(res.Summary).To(Equal("10 cases passed"))

			mu.Lock()
			defer mu.Unlock()
			Expect(progress).To(ContainElement("run_tests|running case 3"))
		})
	})
})
