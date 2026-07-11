package devmcp_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/agent/devmcp"
)

var _ = Describe("contract types", func() {
	It("marshals Component with the §3.1 field names", func() {
		data, err := json.Marshal(devmcp.Component{
			ID:          "atc",
			Description: "ATC web node",
			Paths:       []string{"atc/"},
			Kind:        "service",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(MatchJSON(`{
			"id": "atc",
			"description": "ATC web node",
			"paths": ["atc/"],
			"kind": "service"
		}`))
	})

	It("marshals ToolResult with the §3.1 field names and omits empty optionals", func() {
		data, err := json.Marshal(devmcp.ToolResult{
			Status:          devmcp.StatusFailed,
			Summary:         "2 specs failed",
			DurationSeconds: 93.5,
			OutputTail:      "FAIL",
			LogPath:         ".dev-mcp/logs/test-atc-1.log",
			Failures: []devmcp.Failure{
				{ID: "TestX", Message: "boom", Path: "atc/x_test.go", Line: 12},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(data).To(MatchJSON(`{
			"status": "failed",
			"summary": "2 specs failed",
			"duration_seconds": 93.5,
			"output_tail": "FAIL",
			"log_path": ".dev-mcp/logs/test-atc-1.log",
			"failures": [{"id": "TestX", "message": "boom", "path": "atc/x_test.go", "line": 12}]
		}`))

		bare, err := json.Marshal(devmcp.ToolResult{
			Status: devmcp.StatusOK, Summary: "ok", DurationSeconds: 0.1,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(bare).To(MatchJSON(`{"status": "ok", "summary": "ok", "duration_seconds": 0.1}`))
	})

	It("pins the taxonomy and transport constants", func() {
		Expect(string(devmcp.StatusOK)).To(Equal("ok"))
		Expect(string(devmcp.StatusFailed)).To(Equal("failed"))
		Expect(string(devmcp.StatusError)).To(Equal("error"))
		Expect(devmcp.DefaultListenAddr).To(Equal(":7780"))
		Expect(devmcp.EndpointPath).To(Equal("/mcp"))
		Expect(devmcp.EnvEndpoint).To(Equal("DEV_MCP_URL"))
		Expect(devmcp.EnvListenAddr).To(Equal("MCP_LISTEN_ADDR"))
	})
})
