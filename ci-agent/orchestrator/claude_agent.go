package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// ClaudeAgentRunner implements QAAgentRunner using the Claude Code CLI.
type ClaudeAgentRunner struct {
	CLI   string
	Model string
}

// NewClaudeAgentRunner creates a new Claude-based QA agent runner.
func NewClaudeAgentRunner(cli, model string) *ClaudeAgentRunner {
	if cli == "" {
		cli = "claude"
	}
	return &ClaudeAgentRunner{CLI: cli, Model: model}
}

// Run invokes the Claude CLI with the given prompt and returns stdout.
func (r *ClaudeAgentRunner) Run(ctx context.Context, prompt string) (string, error) {
	args := []string{"-p", prompt, "--output-format", "json"}
	if r.Model != "" {
		args = append(args, "--model", r.Model)
	}

	cmd := exec.CommandContext(ctx, r.CLI, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timeout: %w", ctx.Err())
		}
		return "", fmt.Errorf("cli error: %w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}

// Ensure ClaudeAgentRunner satisfies the interface at compile time.
var _ QAAgentRunner = (*ClaudeAgentRunner)(nil)
