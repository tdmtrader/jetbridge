package client

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/concourse/concourse/agent/review"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type statusInput struct {
	Run int `json:"run" jsonschema:"Positive Run number returned by submission"`
}

// A compact projection has a schema matching its JSON representation. The
// larger PipelineRun HTTP type has custom Unix timestamp encoding and is not
// suitable for reflection-derived MCP schemas.
type statusOutput struct {
	ID              int                      `json:"id"`
	Number          int                      `json:"number"`
	ContractVersion atc.RunContractVersion   `json:"run_contract_version"`
	Status          atc.RunStatus            `json:"status"`
	Reclaimed       bool                     `json:"reclaimed"`
	Terminal        *atc.RunTerminalResult   `json:"terminal,omitempty"`
	Captures        []atc.RunCaptureProgress `json:"captures,omitempty"`
}

// MCPServer exposes the same remote operations as the human CLI. The target,
// team and template are selected when starting the local process; tool calls
// cannot replace its authentication or redirect it to another server.
func (c *Client) MCPServer(team, template string, options ...MCPOptions) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "jetbridge-review", Version: "1"}, nil)
	var local MCPOptions
	if len(options) > 0 {
		local = options[0]
	}
	mcp.AddTool(s, &mcp.Tool{Name: "review_submit", Description: "Submit a captured review bundle using a saved local receipt. Reuse the same receipt after interruption. Only ready=true confirms that the detached worker accepted credentials. Credentials come from local startup configuration, never tool arguments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input submitInput) (*mcp.CallToolResult, Submission, error) {
			if local.AuthFile == "" {
				return nil, Submission{}, errors.New("submission requires --auth-file when starting the local review MCP")
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			result, err := c.Submit(ctx, SubmitOptions{Team: team, Template: template, Input: input.Input, Receipt: input.Receipt, AuthFile: local.AuthFile})
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
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input statusInput) (*mcp.CallToolResult, statusOutput, error) {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		run, err := c.Status(ctx, Handle{Team: team, Template: template, Number: input.Run})
		if err != nil {
			return nil, statusOutput{}, err
		}
		return nil, statusOutput{
			ID: run.ID, Number: run.Number, ContractVersion: run.ContractVersion,
			Status: run.Status, Reclaimed: run.Reclaimed, Terminal: run.Terminal,
			Captures: run.Captures,
		}, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "review_result", Description: "Retrieve the typed review report for a completed Run, including after build cleanup.",
		OutputSchema: json.RawMessage(review.Schema()), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input resultInput) (*mcp.CallToolResult, review.Report, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			name := input.Result
			if name == "" {
				name = "findings"
			}
			report, err := c.Result(ctx, Handle{Team: team, Template: template, Number: input.Run}, name)
			if err != nil {
				return nil, review.Report{}, err
			}
			return nil, *report, nil
		})
	return s
}

type resultInput struct {
	Run    int    `json:"run" jsonschema:"Positive Run number returned by submission"`
	Result string `json:"result,omitempty" jsonschema:"Named result; defaults to findings"`
}

type MCPOptions struct{ AuthFile string }
type submitInput struct {
	Input   string `json:"input" jsonschema:"Local captured review bundle directory"`
	Receipt string `json:"receipt" jsonschema:"Local receipt path outside the bundle; reuse it for retries"`
}
