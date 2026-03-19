package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
)

// Client is the interface for invoking an LLM.
type Client interface {
	Call(ctx context.Context, prompt string, opts CallOpts) (CallResult, error)
}

// CallOpts configures a single LLM call.
type CallOpts struct {
	Model  string
	Dir    string // working directory for the CLI process
}

// ClaudeClient invokes the Claude Code CLI.
type ClaudeClient struct {
	CLI string
}

// NewClaudeClient creates a new Claude CLI client.
func NewClaudeClient(cli string) *ClaudeClient {
	if cli == "" {
		cli = "claude"
	}
	return &ClaudeClient{CLI: cli}
}

// Call invokes the Claude CLI with the given prompt and returns a CallResult
// with both the extracted content and usage metadata.
func (c *ClaudeClient) Call(ctx context.Context, prompt string, opts CallOpts) (CallResult, error) {
	args := []string{"-p", prompt, "--output-format", "json"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	cmd := exec.CommandContext(ctx, c.CLI, args...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return CallResult{}, fmt.Errorf("timeout: %w", ctx.Err())
		}
		return CallResult{}, fmt.Errorf("cli error (exit %v): %s", err, stderr.String())
	}

	return ParseCLIEnvelope(stdout.Bytes()), nil
}

var jsonBlockRe = regexp.MustCompile("(?s)```json\\s*\\n(.+?)\\n```")

// ExtractJSON extracts JSON from raw output, handling markdown code block wrapping.
func ExtractJSON(data []byte) json.RawMessage {
	if m := jsonBlockRe.FindSubmatch(data); m != nil {
		return json.RawMessage(m[1])
	}
	return json.RawMessage(data)
}
