package client

import (
	"context"
	"encoding/json"
	"time"

	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/agent/runclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPOptions is fixed when the local process starts. See runclient.MCPOptions.
type MCPOptions = runclient.MCPOptions

// RegisterTools adds review_submit, review_status and review_result to s. They
// perform the same remote operations as the human CLI, and are the same
// whether s serves only review or every workload.
func RegisterTools(s *mcp.Server, c *Client, options MCPOptions) {
	team, template := options.Team, options.Template
	mcp.AddTool(s, &mcp.Tool{Name: "review_submit", Description: "Submit a captured review bundle using a saved local receipt. Reuse the same receipt after interruption. Only ready=true confirms that the Run's worker accepted credentials. Credentials come from local startup configuration, never tool arguments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input submitInput) (*mcp.CallToolResult, Submission, error) {
			result, err := c.Client.SubmitFromMCP(ctx, workload, options, input.Input, input.Receipt)
			return nil, result, err
		})
	runclient.AddStatusTool(s, c.Client, options, "review_status", "Read a review Run. A pending Run has no terminal observation. A terminal observation retains result references after build cleanup; it does not contain the report files.")
	mcp.AddTool(s, &mcp.Tool{Name: "review_result", Description: "Retrieve the typed review report for a completed Run, including after build cleanup.",
		OutputSchema: json.RawMessage(review.Schema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input resultInput) (*mcp.CallToolResult, review.Report, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			name := input.Result
			if name == "" {
				name = FindingsResult
			}
			report, err := c.Result(ctx, Handle{Team: team, Template: template, Number: input.Run}, name)
			if err != nil {
				return nil, review.Report{}, err
			}
			return nil, *report, nil
		})
}

type resultInput struct {
	Run    int    `json:"run" jsonschema:"Positive Run number returned by submission"`
	Result string `json:"result,omitempty" jsonschema:"Named result; defaults to findings"`
}

type submitInput struct {
	Input   string `json:"input" jsonschema:"Local captured review bundle directory"`
	Receipt string `json:"receipt" jsonschema:"Local receipt path outside the bundle; reuse it for retries"`
}
