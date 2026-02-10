package adapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
)

// Adapter defines the interface for dispatching code review to an AI agent.
type Adapter interface {
	Review(ctx context.Context, repoDir string, cfg *config.ReviewConfig) ([]runner.AgentFinding, error)
}

// rawFinding is the JSON wire format from the agent.
type rawFinding struct {
	Title        string          `json:"title"`
	Description  string          `json:"description,omitempty"`
	File         string          `json:"file"`
	Line         int             `json:"line"`
	SeverityHint schema.Severity `json:"severity_hint"`
	Category     schema.Category `json:"category"`
	TestCode     string          `json:"test_code,omitempty"`
	TestFile     string          `json:"test_file,omitempty"`
	TestName     string          `json:"test_name,omitempty"`
}

// ParseFindings parses the agent's raw structured JSON output into AgentFindings.
func ParseFindings(raw []byte) ([]runner.AgentFinding, error) {
	var raws []rawFinding
	if err := json.Unmarshal(raw, &raws); err != nil {
		return nil, fmt.Errorf("parsing agent findings: %w", err)
	}

	findings := make([]runner.AgentFinding, len(raws))
	for i, r := range raws {
		findings[i] = runner.AgentFinding{
			Title:        r.Title,
			Description:  r.Description,
			File:         r.File,
			Line:         r.Line,
			SeverityHint: r.SeverityHint,
			Category:     r.Category,
			TestCode:     r.TestCode,
			TestFile:     r.TestFile,
			TestName:     r.TestName,
		}
	}
	return findings, nil
}
