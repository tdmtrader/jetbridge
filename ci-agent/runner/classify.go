package runner

import (
	"fmt"

	"github.com/concourse/ci-agent/schema"
)

// AgentFinding is an intermediate type representing what the AI agent produced
// before test verification.
type AgentFinding struct {
	Title        string
	Description  string
	File         string
	Line         int
	SeverityHint schema.Severity
	Category     schema.Category
	TestCode     string
	TestFile     string
	TestName     string
}

// ClassifyResults separates agent findings into proven issues and observations
// based on test results.
func ClassifyResults(findings []AgentFinding, results map[string]*TestResult) ([]schema.ProvenIssue, []schema.Observation) {
	var proven []schema.ProvenIssue
	var observations []schema.Observation

	for i, f := range findings {
		id := fmt.Sprintf("ISS-%03d", i+1)

		// No test generated → observation.
		if f.TestFile == "" || f.TestCode == "" {
			observations = append(observations, schema.Observation{
				ID:          id,
				Title:       f.Title,
				Description: f.Description,
				File:        f.File,
				Line:        f.Line,
				Category:    f.Category,
			})
			continue
		}

		result, ok := results[f.TestFile]
		if !ok {
			// No result for this test file → observation.
			observations = append(observations, schema.Observation{
				ID:          id,
				Title:       f.Title,
				Description: f.Description,
				File:        f.File,
				Line:        f.Line,
				Category:    f.Category,
			})
			continue
		}

		if result.Error {
			// Compilation error → demote to observation.
			observations = append(observations, schema.Observation{
				ID:          id,
				Title:       f.Title,
				Description: f.Description + " (test could not compile)",
				File:        f.File,
				Line:        f.Line,
				Category:    f.Category,
			})
			continue
		}

		if result.Pass {
			// Test passed → agent's concern was unfounded, discard.
			continue
		}

		// Test failed → proven issue.
		proven = append(proven, schema.ProvenIssue{
			ID:          id,
			Severity:    f.SeverityHint,
			Title:       f.Title,
			Description: f.Description,
			File:        f.File,
			Line:        f.Line,
			TestFile:    f.TestFile,
			TestName:    f.TestName,
			TestOutput:  result.Output,
			Category:    f.Category,
		})
	}

	return proven, observations
}
