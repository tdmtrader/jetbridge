package runclient

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPOptions is fixed when the local process starts. Tool calls cannot replace
// the destination, its authentication or the owner's credentials.
type MCPOptions struct {
	// Team and Template select the workload's installed template.
	Team, Template string
	// AuthFile is the owner-selected local Codex auth.json. Without it a
	// workload's submit tool refuses.
	AuthFile string
}

// SubmitFromMCP is the body of every workload's submit tool: it submits the
// input under the receipt, to the destination and with the credentials fixed
// at startup. Once a Run is admitted, a later failure is reported in the
// submission's message rather than as a tool error, so the caller still
// learns which Run to resume.
func (c *Client) SubmitFromMCP(ctx context.Context, workload Workload, options MCPOptions, input, receipt string) (Submission, error) {
	if options.AuthFile == "" {
		return Submission{}, errors.New("submission requires --auth-file when starting the local MCP server")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := c.Submit(ctx, workload, SubmitOptions{Team: options.Team, Template: options.Template, Input: input, Receipt: receipt, AuthFile: options.AuthFile})
	if err != nil && result.RunID == 0 {
		return Submission{}, err
	}
	if err != nil {
		result.Message = err.Error()
	}
	return result, nil
}

// StatusInput is every workload's status tool input.
type StatusInput struct {
	Run int `json:"run" jsonschema:"Positive Run number returned by submission"`
}

// AddStatusTool adds a workload's read-only status tool, which observes a Run
// of the template fixed at startup.
func AddStatusTool(s *mcp.Server, c *Client, options MCPOptions, name, description string) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input StatusInput) (*mcp.CallToolResult, RunObservation, error) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			run, err := c.Observe(ctx, Handle{Team: options.Team, Template: options.Template, Number: input.Run})
			return nil, run, err
		})
}
