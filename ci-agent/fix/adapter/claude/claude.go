package claude

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/concourse/ci-agent/fix"
	"github.com/concourse/ci-agent/schema"
)

// Adapter implements fix.FixAdapter using the Claude Code CLI.
type Adapter struct {
	CLI   string
	Model string
}

// New creates a new Claude Code fix adapter.
func New(cli, model string) *Adapter {
	if cli == "" {
		cli = "claude"
	}
	return &Adapter{CLI: cli, Model: model}
}

// Fix invokes Claude to generate patches for the given issue.
func (a *Adapter) Fix(ctx context.Context, issue schema.ProvenIssue, fileContent, testCode string) ([]fix.FilePatch, error) {
	prompt := fix.BuildFixPrompt(issue, fileContent, testCode)

	args := []string{"-p", prompt, "--output-format", "json"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}

	cmd := exec.CommandContext(ctx, a.CLI, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timeout: %w", ctx.Err())
		}
		return nil, fmt.Errorf("cli error: %w: %s", err, stderr.String())
	}

	return fix.ParseFixPatches(stdout.Bytes())
}

// Ensure Adapter satisfies the interface at compile time.
var _ fix.FixAdapter = (*Adapter)(nil)
