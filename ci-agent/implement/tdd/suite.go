package tdd

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// SuiteResult captures the outcome of running the full test suite.
type SuiteResult struct {
	Pass        bool     `json:"pass"`
	Output      string   `json:"output"`
	FailedTests []string `json:"failed_tests,omitempty"`
}

// RunSuite executes the full test suite in the given repo directory.
func RunSuite(ctx context.Context, repoDir string, testCmd string) (*SuiteResult, error) {
	parts := strings.Fields(testCmd)
	if len(parts) == 0 {
		parts = []string{"go", "test", "./..."}
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = repoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	output := stdout.String() + stderr.String()

	if runErr != nil {
		return &SuiteResult{
			Pass:        false,
			Output:      output,
			FailedTests: extractFailedTests(output),
		}, nil
	}

	return &SuiteResult{
		Pass:   true,
		Output: output,
	}, nil
}

func extractFailedTests(output string) []string {
	var failed []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--- FAIL:") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				failed = append(failed, parts[2])
			}
		}
	}
	return failed
}
