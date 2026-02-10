package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/schema"
)

// Adapter implements the adapter.Adapter interface using Claude Code CLI.
type Adapter struct {
	CLI string // path to claude CLI binary
}

// New creates a new Claude Code adapter.
func New(cli string) *Adapter {
	if cli == "" {
		cli = "claude"
	}
	return &Adapter{CLI: cli}
}

// GenerateSpec invokes the Claude CLI to generate a spec.
func (a *Adapter) GenerateSpec(ctx context.Context, input *schema.PlanningInput, opts adapter.SpecOpts) (*adapter.SpecOutput, error) {
	prompt := adapter.BuildSpecPrompt(input, opts)
	output, err := a.invoke(ctx, prompt, opts.Model)
	if err != nil {
		return nil, fmt.Errorf("generate spec: %w", err)
	}
	var spec adapter.SpecOutput
	if err := json.Unmarshal(output, &spec); err != nil {
		return nil, fmt.Errorf("parse spec output: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("invalid spec output: %w", err)
	}
	return &spec, nil
}

// GeneratePlan invokes the Claude CLI to generate a plan.
func (a *Adapter) GeneratePlan(ctx context.Context, input *schema.PlanningInput, specMarkdown string, opts adapter.PlanOpts) (*adapter.PlanOutput, error) {
	prompt := adapter.BuildPlanPrompt(input, specMarkdown, opts)
	output, err := a.invoke(ctx, prompt, opts.Model)
	if err != nil {
		return nil, fmt.Errorf("generate plan: %w", err)
	}
	var plan adapter.PlanOutput
	if err := json.Unmarshal(output, &plan); err != nil {
		return nil, fmt.Errorf("parse plan output: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("invalid plan output: %w", err)
	}
	return &plan, nil
}

func (a *Adapter) invoke(ctx context.Context, prompt string, model string) ([]byte, error) {
	args := []string{"-p", prompt, "--output-format", "json"}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, a.CLI, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timeout: %w", ctx.Err())
		}
		return nil, fmt.Errorf("cli error (exit %v): %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
