package devmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
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

// RegisterTools wires the five §3.1 tools onto the server, backed by cfg's
// commands executed under workdir.
func RegisterTools(s *Server, cfg Config, workdir string) {
	registerListComponents(s, cfg)
	registerCommandTool(s, cfg, workdir, "build",
		"Build a component (or the whole repo when component is omitted).")
	registerRunTests(s, cfg, workdir)
	registerCommandTool(s, cfg, workdir, "lint",
		"Lint a component (or the whole repo when component is omitted).")
	registerAffectedComponents(s, cfg)
}

func registerListComponents(s *Server, cfg Config) {
	s.AddTool("list_components", "List this repo's buildable/testable components.", emptyObjectSchema,
		func(_ context.Context, _ json.RawMessage, _ ProgressFunc) (any, error) {
			comps := make([]Component, len(cfg.Components))
			for i, c := range cfg.Components {
				comps[i] = Component{ID: c.ID, Description: c.Description, Paths: c.Paths, Kind: c.Kind}
			}
			return map[string]any{"components": comps}, nil
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

func pickSpec(tool string, build, test, lint *CommandSpec) *CommandSpec {
	switch tool {
	case "build":
		return build
	case "test":
		return test
	case "lint":
		return lint
	}
	return nil
}

// resolveSpec picks the CommandSpec for tool ("build"|"test"|"lint") and
// component (empty = whole-repo scope, which requires the repo: section).
// All misses are malformed input (-32602 at the server layer).
func resolveSpec(cfg Config, tool, component string) (CommandSpec, string, error) {
	if component == "" {
		if cfg.Repo != nil {
			if spec := pickSpec(tool, cfg.Repo.Build, cfg.Repo.Test, cfg.Repo.Lint); spec != nil {
				return *spec, tool + "-repo", nil
			}
		}
		return CommandSpec{}, "", ErrInvalidParams(
			"whole-repo %s is not configured (no repo: section in dev-mcp.yml); pass a component", tool)
	}
	comp, found := cfg.Component(component)
	if !found {
		return CommandSpec{}, "", ErrInvalidParams("unknown component: %s", component)
	}
	spec := pickSpec(tool, comp.Build, comp.Test, comp.Lint)
	if spec == nil {
		return CommandSpec{}, "", ErrInvalidParams("component %s does not support %s", component, tool)
	}
	return *spec, fmt.Sprintf("%s-%s", tool, component), nil
}

func registerCommandTool(s *Server, cfg Config, workdir, tool, description string) {
	s.AddTool(tool, description, componentSchema,
		func(ctx context.Context, raw json.RawMessage, progress ProgressFunc) (any, error) {
			args, err := parseComponentArgs(raw, false)
			if err != nil {
				return nil, err
			}
			spec, label, err := resolveSpec(cfg, tool, args.Component)
			if err != nil {
				return nil, err
			}
			return runCommand(ctx, workdir, label, spec, nil, progress), nil
		})
}

func registerRunTests(s *Server, cfg Config, workdir string) {
	s.AddTool("run_tests", "Run a component's tests (or the whole repo's when component is omitted).", runTestsSchema,
		func(ctx context.Context, raw json.RawMessage, progress ProgressFunc) (any, error) {
			args, err := parseComponentArgs(raw, true)
			if err != nil {
				return nil, err
			}
			spec, label, err := resolveSpec(cfg, "test", args.Component)
			if err != nil {
				return nil, err
			}
			var extra []string
			if args.Focus != "" {
				if spec.FocusFlag == "" {
					return nil, ErrInvalidParams("component %q does not support focus", args.Component)
				}
				extra = []string{fmt.Sprintf("%s=%s", spec.FocusFlag, args.Focus)}
			}
			return runCommand(ctx, workdir, label, spec, extra, progress), nil
		})
}

func registerAffectedComponents(s *Server, cfg Config) {
	s.AddTool("affected_components", "Map changed file paths to the component ids that own them.", affectedSchema,
		func(_ context.Context, raw json.RawMessage, _ ProgressFunc) (any, error) {
			var args struct {
				ChangedPaths *[]string `json:"changed_paths"`
			}
			if err := json.Unmarshal(raw, &args); err != nil || args.ChangedPaths == nil {
				return nil, ErrInvalidParams("invalid arguments: changed_paths is required")
			}
			hit := map[string]bool{}
			unmapped := []string{}
			for _, changed := range *args.ChangedPaths {
				clean := filepath.ToSlash(filepath.Clean(changed))
				matched := false
				for _, comp := range cfg.Components {
					for _, prefix := range comp.Paths {
						p := strings.TrimSuffix(filepath.ToSlash(prefix), "/")
						if clean == p || strings.HasPrefix(clean, p+"/") {
							hit[comp.ID] = true
							matched = true
						}
					}
				}
				if !matched {
					unmapped = append(unmapped, changed)
				}
			}
			ids := make([]string, 0, len(hit))
			for id := range hit {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			return AffectedResult{Components: ids, UnmappedPaths: unmapped}, nil
		})
}
