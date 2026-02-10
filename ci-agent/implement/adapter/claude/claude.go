package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"

	"github.com/concourse/ci-agent/implement/adapter"
)

// Adapter implements the adapter.Adapter interface using Claude Code CLI.
type Adapter struct {
	CLI   string
	Model string
}

// New creates a new Claude Code adapter.
func New(cli string, model ...string) *Adapter {
	a := &Adapter{CLI: cli}
	if len(model) > 0 {
		a.Model = model[0]
	}
	return a
}

// GenerateTest invokes the agent to produce a failing test.
func (a *Adapter) GenerateTest(ctx context.Context, req adapter.CodeGenRequest) (*adapter.TestGenResponse, error) {
	prompt := a.BuildTestGenPrompt(req)

	output, err := a.run(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("test generation failed: %w", err)
	}

	return ParseTestGenResponse(output)
}

// GenerateImpl invokes the agent to produce implementation code.
func (a *Adapter) GenerateImpl(ctx context.Context, req adapter.CodeGenRequest, testCode string) (*adapter.ImplGenResponse, error) {
	prompt := a.BuildImplGenPrompt(req, testCode, "")

	output, err := a.run(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("impl generation failed: %w", err)
	}

	return ParseImplGenResponse(output)
}

// BuildTestGenPrompt builds the prompt for test generation.
func (a *Adapter) BuildTestGenPrompt(req adapter.CodeGenRequest) string {
	return adapter.BuildTestPrompt(req)
}

// BuildImplGenPrompt builds the prompt for implementation generation.
func (a *Adapter) BuildImplGenPrompt(req adapter.CodeGenRequest, testCode string, testOutput string) string {
	return adapter.BuildImplPrompt(req, testCode, testOutput)
}

func (a *Adapter) run(ctx context.Context, prompt string) ([]byte, error) {
	args := []string{"-p", prompt, "--output-format", "json"}
	if a.Model != "" {
		args = append(args, "--model", a.Model)
	}
	cmd := exec.CommandContext(ctx, a.CLI, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("CLI error: %w, stderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

var jsonBlockRe = regexp.MustCompile("(?s)```json\\s*\\n(.+?)\\n```")

// ParseTestGenResponse extracts TestGenResponse from agent output.
func ParseTestGenResponse(data []byte) (*adapter.TestGenResponse, error) {
	cleaned := extractJSON(data)
	var resp adapter.TestGenResponse
	if err := json.Unmarshal(cleaned, &resp); err != nil {
		return nil, fmt.Errorf("parse test gen response: %w", err)
	}
	return &resp, nil
}

// ParseImplGenResponse extracts ImplGenResponse from agent output.
func ParseImplGenResponse(data []byte) (*adapter.ImplGenResponse, error) {
	cleaned := extractJSON(data)
	var resp adapter.ImplGenResponse
	if err := json.Unmarshal(cleaned, &resp); err != nil {
		return nil, fmt.Errorf("parse impl gen response: %w", err)
	}
	return &resp, nil
}

func extractJSON(data []byte) []byte {
	// Try to extract from markdown code blocks first.
	if m := jsonBlockRe.FindSubmatch(data); m != nil {
		return m[1]
	}
	return data
}
