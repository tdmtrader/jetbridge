package client

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/concourse/concourse/agent/detached"
	"github.com/concourse/concourse/agent/review"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPOptions is fixed when the local process starts. Tool calls cannot replace
// the destination, its authentication or the owner's credentials.
type MCPOptions struct {
	// Team and Template select the installed review template.
	Team, Template string
	// AuthFile is the owner-selected local Codex auth.json. Without it
	// review_submit refuses.
	AuthFile string
}

// RegisterTools adds review_submit, review_status and review_result to s. They
// perform the same remote operations as the human CLI, and are the same
// whether s serves only review or every detached workload.
func RegisterTools(s *mcp.Server, c *Client, options MCPOptions) {
	team, template := options.Team, options.Template
	mcp.AddTool(s, &mcp.Tool{Name: "review_submit", Description: "Submit a captured review bundle using a saved local receipt. Reuse the same receipt after interruption. Only ready=true confirms that the detached worker accepted credentials. Credentials come from local startup configuration, never tool arguments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input submitInput) (*mcp.CallToolResult, Submission, error) {
			if options.AuthFile == "" {
				return nil, Submission{}, errors.New("submission requires --auth-file when starting the local review MCP")
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			result, err := c.Submit(ctx, SubmitOptions{Team: team, Template: template, Input: input.Input, Receipt: input.Receipt, AuthFile: options.AuthFile})
			if err != nil && result.RunID == 0 {
				return nil, Submission{}, err
			}
			if err != nil {
				result.Message = err.Error()
			}
			return nil, result, nil
		})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "review_status",
		Description: "Read a detached review Run. A pending Run has no terminal observation. A terminal observation retains result references after build cleanup; it does not contain the report files.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input statusInput) (*mcp.CallToolResult, detached.RunObservation, error) {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		run, err := c.Observe(ctx, Handle{Team: team, Template: template, Number: input.Run})
		return nil, run, err
	})
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

type statusInput struct {
	Run int `json:"run" jsonschema:"Positive Run number returned by submission"`
}

type resultInput struct {
	Run    int    `json:"run" jsonschema:"Positive Run number returned by submission"`
	Result string `json:"result,omitempty" jsonschema:"Named result; defaults to findings"`
}

type submitInput struct {
	Input   string `json:"input" jsonschema:"Local captured review bundle directory"`
	Receipt string `json:"receipt" jsonschema:"Local receipt path outside the bundle; reuse it for retries"`
}
