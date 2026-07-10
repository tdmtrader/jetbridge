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

// ExtractJSON extracts JSON from raw LLM output. Models don't reliably return
// pure JSON: sometimes it's fenced in a ```json block, sometimes it's a bare
// object wrapped in prose ("Here is my review:\n{...}"). Downstream consumers
// (artifact validation, publish) require the extracted bytes to be valid JSON,
// so try progressively: use the data as-is if it already parses, else a fenced
// block, else the span from the first bracket to the last.
func ExtractJSON(data []byte) json.RawMessage {
	if json.Valid(data) {
		return json.RawMessage(data)
	}
	if m := jsonBlockRe.FindSubmatch(data); m != nil {
		if inner := ExtractJSON(m[1]); json.Valid(inner) {
			return inner
		}
	}
	if span := jsonSpan(data); span != nil {
		return span
	}
	return json.RawMessage(data)
}

// jsonSpan returns the substring from the first opening bracket to the last
// closing bracket when that span is valid JSON, or nil when no such span
// exists. This recovers a bare JSON value embedded in surrounding prose.
func jsonSpan(data []byte) json.RawMessage {
	start := bytes.IndexAny(data, "{[")
	if start < 0 {
		return nil
	}
	end := bytes.LastIndexAny(data, "}]")
	if end < start {
		return nil
	}
	span := data[start : end+1]
	if json.Valid(span) {
		return json.RawMessage(span)
	}
	return nil
}
