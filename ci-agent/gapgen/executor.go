package gapgen

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/concourse/ci-agent/schema"
)

// TestResult represents the outcome of running a generated test.
type TestResult struct {
	Passed  bool   `json:"passed"`
	Output  string `json:"output"`
	CompErr bool   `json:"compilation_error"`
}

// ExecuteGapTests writes and runs generated tests, returning results.
func ExecuteGapTests(ctx context.Context, repoDir string, tests []GeneratedTestFile) (map[string]*TestResult, error) {
	results := make(map[string]*TestResult)

	for _, t := range tests {
		absPath := filepath.Join(repoDir, t.FilePath)
		os.MkdirAll(filepath.Dir(absPath), 0755)
		if err := os.WriteFile(absPath, []byte(t.TestCode), 0644); err != nil {
			results[t.RequirementID] = &TestResult{CompErr: true, Output: err.Error()}
			continue
		}

		cmd := exec.CommandContext(ctx, "go", "test", "-run", t.TestName, "-count=1", "-timeout", "30s", "./...")
		cmd.Dir = filepath.Dir(absPath)
		out, err := cmd.CombinedOutput()

		result := &TestResult{
			Output: string(out),
		}
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
				result.CompErr = true
			}
			result.Passed = false
		} else {
			result.Passed = true
		}
		results[t.RequirementID] = result
	}

	return results, nil
}

// ClassifyGapResults converts test results into RequirementResults with appropriate status.
func ClassifyGapResults(reqID string, reqText string, result *TestResult) schema.RequirementResult {
	if result == nil {
		return schema.RequirementResult{
			ID:             reqID,
			Text:           reqText,
			Status:         schema.CoverageUncoveredBroken,
			CoveragePoints: 0.0,
		}
	}

	if result.CompErr {
		return schema.RequirementResult{
			ID:             reqID,
			Text:           reqText,
			Status:         schema.CoverageUncoveredBroken,
			CoveragePoints: 0.0,
			Notes:          fmt.Sprintf("compilation error: %s", result.Output),
		}
	}

	if result.Passed {
		return schema.RequirementResult{
			ID:             reqID,
			Text:           reqText,
			Status:         schema.CoverageUncoveredImplemented,
			CoveragePoints: 0.75,
		}
	}

	return schema.RequirementResult{
		ID:             reqID,
		Text:           reqText,
		Status:         schema.CoverageUncoveredBroken,
		CoveragePoints: 0.0,
	}
}
