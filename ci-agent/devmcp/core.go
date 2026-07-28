package devmcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Operation is one repository-development action shared by the untrusted MCP
// adapter and deterministic validation. Its value is not sourced from a
// candidate workspace at execution time.
type Operation string

const (
	OperationBuild Operation = "build"
	OperationTest  Operation = "test"
	OperationLint  Operation = "lint"
)

// Request selects an operation and either a configured component or the
// configured whole-repository command. Focus applies only to test commands.
type Request struct {
	Operation Operation
	Component string
	Focus     string
}

// Core is the transport-neutral dev capability. Command outcomes are carried
// by ToolResult; errors denote invalid requests or a cancelled caller context.
type Core interface {
	ListComponents(context.Context) ([]Component, error)
	AffectedComponents(context.Context, []string) (AffectedResult, error)
	Execute(context.Context, Request, ProgressFunc) (ToolResult, error)
}

type coreImpl struct {
	config  Config
	workdir string
}

// NewCore freezes a validated configuration and a real workspace directory.
// The defensive clone prevents a caller from changing a resolved executable
// after the core has been constructed.
func NewCore(config Config, workdir string) (Core, error) {
	cloned := cloneCoreConfig(config)
	if err := validateConfig(cloned); err != nil {
		return nil, fmt.Errorf("invalid dev-mcp config: %w", err)
	}
	if workdir == "" {
		return nil, fmt.Errorf("dev-mcp workdir is required")
	}
	absolute, err := filepath.Abs(workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve dev-mcp workdir: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect dev-mcp workdir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("dev-mcp workdir %q is not a directory", absolute)
	}
	return &coreImpl{config: cloned, workdir: absolute}, nil
}

func (core *coreImpl) ListComponents(ctx context.Context) ([]Component, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	components := make([]Component, len(core.config.Components))
	for index, component := range core.config.Components {
		components[index] = Component{
			ID:          component.ID,
			Description: component.Description,
			Paths:       append([]string(nil), component.Paths...),
			Kind:        component.Kind,
		}
	}
	return components, nil
}

func (core *coreImpl) AffectedComponents(ctx context.Context, changedPaths []string) (AffectedResult, error) {
	if err := ctx.Err(); err != nil {
		return AffectedResult{}, err
	}

	hit := map[string]bool{}
	unmapped := make([]string, 0)
	for _, changed := range changedPaths {
		if err := ctx.Err(); err != nil {
			return AffectedResult{}, err
		}
		clean := filepath.ToSlash(filepath.Clean(changed))
		matched := false
		for _, component := range core.config.Components {
			for _, prefix := range component.Paths {
				pathPrefix := strings.TrimSuffix(filepath.ToSlash(prefix), "/")
				if clean == pathPrefix || strings.HasPrefix(clean, pathPrefix+"/") {
					hit[component.ID] = true
					matched = true
				}
			}
		}
		if !matched {
			unmapped = append(unmapped, changed)
		}
	}

	components := make([]string, 0, len(hit))
	for id := range hit {
		components = append(components, id)
	}
	sort.Strings(components)
	return AffectedResult{Components: components, UnmappedPaths: unmapped}, nil
}

func (core *coreImpl) Execute(ctx context.Context, request Request, progress ProgressFunc) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if request.Focus != "" && request.Operation != OperationTest {
		return ToolResult{}, ErrInvalidParams("focus is supported only for %s operations", OperationTest)
	}

	spec, label, err := core.resolve(request.Operation, request.Component)
	if err != nil {
		return ToolResult{}, err
	}
	var extraArguments []string
	if request.Focus != "" {
		if spec.FocusFlag == "" {
			return ToolResult{}, ErrInvalidParams("component %q does not support focus", request.Component)
		}
		extraArguments = []string{fmt.Sprintf("%s=%s", spec.FocusFlag, request.Focus)}
	}
	return runCommand(ctx, core.workdir, label, spec, extraArguments, progress), nil
}

func (core *coreImpl) resolve(operation Operation, component string) (CommandSpec, string, error) {
	if !operation.valid() {
		return CommandSpec{}, "", ErrInvalidParams("unknown operation: %s", operation)
	}
	if component == "" {
		if core.config.Repo != nil {
			if spec := operation.pick(core.config.Repo.Build, core.config.Repo.Test, core.config.Repo.Lint); spec != nil {
				return *spec, string(operation) + "-repo", nil
			}
		}
		// Preserve the MCP's existing malformed whole-repository error exactly.
		return CommandSpec{}, "", ErrInvalidParams(
			"whole-repo %s is not configured (no repo: section in dev-mcp.yml); pass a component",
			operation,
		)
	}

	componentConfig, found := core.config.Component(component)
	if !found {
		return CommandSpec{}, "", ErrInvalidParams("unknown component: %s", component)
	}
	spec := operation.pick(componentConfig.Build, componentConfig.Test, componentConfig.Lint)
	if spec == nil {
		return CommandSpec{}, "", ErrInvalidParams("component %s does not support %s", component, operation)
	}
	return *spec, fmt.Sprintf("%s-%s", operation, component), nil
}

func (operation Operation) valid() bool {
	switch operation {
	case OperationBuild, OperationTest, OperationLint:
		return true
	default:
		return false
	}
}

func (operation Operation) pick(build, test, lint *CommandSpec) *CommandSpec {
	switch operation {
	case OperationBuild:
		return build
	case OperationTest:
		return test
	case OperationLint:
		return lint
	default:
		return nil
	}
}

func cloneCoreConfig(config Config) Config {
	cloned := Config{
		SchemaVersion: config.SchemaVersion,
		Repo:          cloneToolCommands(config.Repo),
		Components:    make([]ComponentConfig, len(config.Components)),
	}
	for index, component := range config.Components {
		cloned.Components[index] = ComponentConfig{
			ID:          component.ID,
			Description: component.Description,
			Paths:       append([]string(nil), component.Paths...),
			Kind:        component.Kind,
			Build:       cloneCommandSpec(component.Build),
			Test:        cloneCommandSpec(component.Test),
			Lint:        cloneCommandSpec(component.Lint),
		}
	}
	return cloned
}

func cloneToolCommands(commands *ToolCommands) *ToolCommands {
	if commands == nil {
		return nil
	}
	return &ToolCommands{
		Build: cloneCommandSpec(commands.Build),
		Test:  cloneCommandSpec(commands.Test),
		Lint:  cloneCommandSpec(commands.Lint),
	}
}

func cloneCommandSpec(spec *CommandSpec) *CommandSpec {
	if spec == nil {
		return nil
	}
	return &CommandSpec{
		Cmd:             append([]string(nil), spec.Cmd...),
		Dir:             spec.Dir,
		FocusFlag:       spec.FocusFlag,
		FailedExitCodes: append([]int(nil), spec.FailedExitCodes...),
	}
}
