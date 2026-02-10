package fix

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
)

// SuiteResult captures the outcome of running the full test suite.
type SuiteResult struct {
	Pass   bool
	Output string
}

// RunFullTestSuite runs the project's full test suite and returns the result.
func RunFullTestSuite(ctx context.Context, repoDir, testCommand string) (*SuiteResult, error) {
	if testCommand == "" {
		testCommand = "go test ./..."
	}

	parts := strings.Fields(testCommand)
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = repoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String() + stderr.String()

	if ctx.Err() != nil {
		return &SuiteResult{Pass: false, Output: "test suite timed out: " + output}, nil
	}

	if err != nil {
		return &SuiteResult{Pass: false, Output: output}, nil
	}

	return &SuiteResult{Pass: true, Output: output}, nil
}
