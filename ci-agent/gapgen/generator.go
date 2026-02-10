package gapgen

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/concourse/ci-agent/mapper"
)

// AgentRunner is a general-purpose agent invocation interface.
type AgentRunner interface {
	Run(ctx context.Context, prompt string) (string, error)
}

// GeneratedTestFile represents a test generated for an uncovered requirement.
type GeneratedTestFile struct {
	RequirementID string `json:"requirement_id"`
	FilePath      string `json:"file_path"`
	TestName      string `json:"test_name"`
	TestCode      string `json:"test_code"`
}

// GenerateGapTests generates test files for uncovered requirements.
func GenerateGapTests(ctx context.Context, agent AgentRunner, repoDir string, gaps []mapper.RequirementMapping) ([]GeneratedTestFile, error) {
	var tests []GeneratedTestFile

	for _, gap := range gaps {
		if gap.Status != "uncovered" {
			continue
		}

		if agent == nil {
			continue
		}

		prompt := buildGapTestPrompt(gap)
		output, err := agent.Run(ctx, prompt)
		if err != nil {
			continue // graceful: skip this gap
		}

		test, err := parseGeneratedTest(output, gap.SpecItem.ID)
		if err != nil || test.TestCode == "" {
			continue
		}

		tests = append(tests, *test)
	}

	return tests, nil
}

func buildGapTestPrompt(gap mapper.RequirementMapping) string {
	var sb strings.Builder
	sb.WriteString("Generate a Go test that verifies the following requirement:\n\n")
	sb.WriteString(fmt.Sprintf("Requirement %s: %s\n\n", gap.SpecItem.ID, gap.SpecItem.Text))
	sb.WriteString(`Respond with JSON:
{"test_name": "TestXxx", "test_code": "package ...\n\nfunc TestXxx(t *testing.T) { ... }"}
`)
	return sb.String()
}

type generatedTestJSON struct {
	TestName string `json:"test_name"`
	TestCode string `json:"test_code"`
}

func parseGeneratedTest(output string, reqID string) (*GeneratedTestFile, error) {
	var parsed generatedTestJSON
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		return nil, fmt.Errorf("parse generated test: %w", err)
	}
	slug := strings.ToLower(strings.ReplaceAll(reqID, " ", "_"))
	return &GeneratedTestFile{
		RequirementID: reqID,
		FilePath:      fmt.Sprintf("qa/tests/%s_%s_test.go", strings.ToLower(reqID), slug),
		TestName:      parsed.TestName,
		TestCode:      parsed.TestCode,
	}, nil
}
