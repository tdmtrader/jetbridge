package client

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/agent/runclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPOptions is fixed when the local process starts. See runclient.MCPOptions.
type MCPOptions = runclient.MCPOptions

// ResultOutput is what implement_result returns: either the Run's verified
// change, or the validation that attests it.
type ResultOutput struct {
	// Result names which of the Run's results this is.
	Result string `json:"result"`
	// Summary and Patch are the change: summary.json and change.patch as the
	// Run published them, verified against each other and the Run.
	Summary *implement.Summary `json:"summary,omitempty"`
	Patch   *string            `json:"patch,omitempty"`
	// Validation is the template's validation, verified to name exactly the
	// Run's change.
	Validation *implement.Validation `json:"validation,omitempty"`
}

// RegisterTools adds implement_submit, implement_status and implement_result to
// s. They perform the same remote operations as the human CLI.
func RegisterTools(s *mcp.Server, c *Client, options MCPOptions) {
	team, template := options.Team, options.Template
	mcp.AddTool(s, &mcp.Tool{Name: "implement_submit", Description: "Submit a captured implement snapshot using a saved local receipt. Reuse the same receipt after interruption. Only ready=true confirms that the Run's worker accepted credentials. Credentials come from local startup configuration, never tool arguments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input submitInput) (*mcp.CallToolResult, Submission, error) {
			result, err := c.Client.SubmitFromMCP(ctx, Workload(), options, input.Input, input.Receipt)
			return nil, result, err
		})
	runclient.AddStatusTool(s, c.Client, options, "implement_status", "Read an implement Run. A pending Run has no terminal observation. A terminal observation retains result references after build cleanup; it does not contain the change or validation files.")
	mcp.AddTool(s, &mcp.Tool{Name: "implement_result", Description: "Retrieve a completed implement Run's change (the summary and the patch text against the snapshot's base commit) or, with result=validation, the template's validation of that exact change. A failed validation is a result, not an error. Apply a change with `jb implement apply --run`.",
		OutputSchema: resultSchema(), Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcp.CallToolRequest, input resultInput) (*mcp.CallToolResult, ResultOutput, error) {
			name := input.Result
			if name == "" {
				name = ChangeResult
			}
			if name != ChangeResult && name != ValidationResult {
				return nil, ResultOutput{}, fmt.Errorf("result must be %q or %q", ChangeResult, ValidationResult)
			}
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			handle := Handle{Team: team, Template: template, Number: input.Run}
			// A validation is only meaningful for the change it ran against,
			// so it is always read together with, and checked against, the
			// Run's change.
			change, err := c.Result(ctx, handle, ChangeResult)
			if err != nil {
				return nil, ResultOutput{}, err
			}
			if name == ChangeResult {
				patch := change.Patch
				return nil, ResultOutput{Result: ChangeResult, Summary: change.Summary, Patch: &patch}, nil
			}
			validation, err := c.Validation(ctx, handle, change)
			if err != nil {
				return nil, ResultOutput{}, err
			}
			return nil, ResultOutput{Result: ValidationResult, Validation: validation}, nil
		})
}

type resultInput struct {
	Run    int    `json:"run" jsonschema:"Positive Run number returned by submission"`
	Result string `json:"result,omitempty" jsonschema:"change or validation; defaults to change"`
}

type submitInput struct {
	Input   string `json:"input" jsonschema:"Local captured implement snapshot directory"`
	Receipt string `json:"receipt" jsonschema:"Local receipt path outside the snapshot; reuse it for retries"`
}

// resultSchema is implement_result's output schema, built from the published
// implement/v1 and implement-validation/v1 schemas so the tool advertises
// exactly the contracts the CLI verifies. The summary schema's definitions
// move to the root, where its references resolve.
func resultSchema() json.RawMessage {
	var summary, validation map[string]any
	if err := json.Unmarshal(implement.Schema(), &summary); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(implement.ValidationSchema(), &validation); err != nil {
		panic(err)
	}
	draft := summary["$schema"]
	defs := summary["$defs"]
	delete(summary, "$schema")
	delete(summary, "$defs")
	delete(validation, "$schema")
	schema, err := json.Marshal(map[string]any{
		"$schema":              draft,
		"title":                "JetBridge implementation result",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"result"},
		"properties": map[string]any{
			"result":     map[string]any{"enum": []string{ChangeResult, ValidationResult}},
			"summary":    summary,
			"patch":      map[string]any{"type": "string", "description": "change.patch: a unified diff against the snapshot's base commit."},
			"validation": validation,
		},
		"oneOf": []any{
			map[string]any{
				"properties": map[string]any{"result": map[string]any{"const": ChangeResult}},
				"required":   []string{"summary", "patch"},
				"not":        map[string]any{"required": []string{"validation"}},
			},
			map[string]any{
				"properties": map[string]any{"result": map[string]any{"const": ValidationResult}},
				"required":   []string{"validation"},
				"not":        map[string]any{"anyOf": []any{map[string]any{"required": []string{"summary"}}, map[string]any{"required": []string{"patch"}}}},
			},
		},
		"$defs": defs,
	})
	if err != nil {
		panic(err)
	}
	return schema
}
