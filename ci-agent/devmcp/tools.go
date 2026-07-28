package devmcp

import (
	"context"
	"encoding/json"
	"strings"
)

// Component is the wire shape of one list_components entry (§3.1).
type Component struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Kind        string   `json:"kind"`
}

// AffectedResult is the affected_components result payload.
type AffectedResult struct {
	Components    []string `json:"components"`
	UnmappedPaths []string `json:"unmapped_paths"`
}

// Input schemas: copied verbatim from 00-shared-contracts.md §3.1.
var (
	emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)

	componentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "component": {"type": "string", "description": "component id; omitted = whole repo"}
  },
  "additionalProperties": false
}`)

	runTestsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "component": {"type": "string"},
    "focus": {"type": "string", "description": "test-name filter, implementation-defined semantics (e.g. ginkgo --focus)"}
  },
  "additionalProperties": false
}`)

	affectedSchema = json.RawMessage(`{
  "type": "object",
  "required": ["changed_paths"],
  "properties": {
    "changed_paths": {"type": "array", "items": {"type": "string"}}
  },
  "additionalProperties": false
}`)
)

// RegisterTools wires the five retained MCP tools onto the server through the
// shared transport-neutral core. MCP callers are untrusted adapters; command
// resolution and execution happen only in Core.
func RegisterTools(s *Server, core Core) {
	registerListComponents(s, core)
	registerCommandTool(s, core, OperationBuild,
		"Build a component (or the whole repo when component is omitted).")
	registerRunTests(s, core)
	registerCommandTool(s, core, OperationLint,
		"Lint a component (or the whole repo when component is omitted).")
	registerAffectedComponents(s, core)
}

func registerListComponents(s *Server, core Core) {
	s.AddTool("list_components", "List this repo's buildable/testable components.", emptyObjectSchema,
		func(ctx context.Context, _ json.RawMessage, _ ProgressFunc) (any, error) {
			components, err := core.ListComponents(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"components": components}, nil
		})
}

type componentArgs struct {
	Component string `json:"component"`
	Focus     string `json:"focus"`
}

func parseComponentArgs(raw json.RawMessage, allowFocus bool) (componentArgs, error) {
	var args componentArgs
	if len(raw) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&args); err != nil {
			return componentArgs{}, ErrInvalidParams("invalid arguments: %s", err)
		}
	}
	if !allowFocus && args.Focus != "" {
		return componentArgs{}, ErrInvalidParams("invalid arguments: focus is not accepted by this tool")
	}
	return args, nil
}

func registerCommandTool(s *Server, core Core, operation Operation, description string) {
	s.AddTool(string(operation), description, componentSchema,
		func(ctx context.Context, raw json.RawMessage, progress ProgressFunc) (any, error) {
			args, err := parseComponentArgs(raw, false)
			if err != nil {
				return nil, err
			}
			return core.Execute(ctx, Request{Operation: operation, Component: args.Component}, progress)
		})
}

func registerRunTests(s *Server, core Core) {
	s.AddTool("run_tests", "Run a component's tests (or the whole repo's when component is omitted).", runTestsSchema,
		func(ctx context.Context, raw json.RawMessage, progress ProgressFunc) (any, error) {
			args, err := parseComponentArgs(raw, true)
			if err != nil {
				return nil, err
			}
			return core.Execute(ctx, Request{
				Operation: OperationTest,
				Component: args.Component,
				Focus:     args.Focus,
			}, progress)
		})
}

func registerAffectedComponents(s *Server, core Core) {
	s.AddTool("affected_components", "Map changed file paths to the component ids that own them.", affectedSchema,
		func(ctx context.Context, raw json.RawMessage, _ ProgressFunc) (any, error) {
			var args struct {
				ChangedPaths *[]string `json:"changed_paths"`
			}
			if err := json.Unmarshal(raw, &args); err != nil || args.ChangedPaths == nil {
				return nil, ErrInvalidParams("invalid arguments: changed_paths is required")
			}
			return core.AffectedComponents(ctx, *args.ChangedPaths)
		})
}
