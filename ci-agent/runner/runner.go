package runner

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TestResult captures the outcome of running a single test file.
type TestResult struct {
	Pass     bool
	Error    bool
	Output   string
	Duration time.Duration
}

// RunTest executes a single Go test file and returns the result.
// The test file must already be in the correct package directory within repoDir.
func RunTest(ctx context.Context, repoDir, testFile string) (*TestResult, error) {
	start := time.Now()

	// Determine the package directory from the test file path.
	pkgDir := filepath.Dir(testFile)

	// Build the relative package path from repoDir.
	relPkg, err := filepath.Rel(repoDir, pkgDir)
	if err != nil {
		return &TestResult{Error: true, Output: err.Error(), Duration: time.Since(start)}, nil
	}

	pkgPath := "./" + relPkg
	if relPkg == "." {
		pkgPath = "./"
	}

	// Extract test function name from file for targeted execution.
	testName := extractTestName(testFile)

	args := []string{"test", "-count=1", "-timeout", "30s"}
	if testName != "" {
		args = append(args, "-run", testName)
	}
	args = append(args, pkgPath)

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = repoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	duration := time.Since(start)

	output := stdout.String() + stderr.String()

	// Check for context cancellation (timeout).
	if ctx.Err() != nil {
		return &TestResult{
			Error:    true,
			Output:   "test timed out: " + output,
			Duration: duration,
		}, nil
	}

	if runErr != nil {
		// Distinguish compilation errors from test failures.
		if isCompileError(output) {
			return &TestResult{
				Error:    true,
				Output:   output,
				Duration: duration,
			}, nil
		}
		// Test failure (exit code 1 from go test).
		return &TestResult{
			Pass:     false,
			Output:   output,
			Duration: duration,
		}, nil
	}

	return &TestResult{
		Pass:     true,
		Output:   output,
		Duration: duration,
	}, nil
}

// RunTests executes multiple Go test files independently and returns results keyed by file path.
func RunTests(ctx context.Context, repoDir string, testFiles []string) (map[string]*TestResult, error) {
	results := make(map[string]*TestResult, len(testFiles))
	for _, tf := range testFiles {
		result, err := RunTest(ctx, repoDir, tf)
		if err != nil {
			return nil, err
		}
		results[tf] = result
	}
	return results, nil
}

// isCompileError checks if the go test output indicates a build/compile failure.
func isCompileError(output string) bool {
	return strings.Contains(output, "build failed") ||
		strings.Contains(output, "cannot ") ||
		strings.Contains(output, "undefined") ||
		strings.Contains(output, "syntax error") ||
		strings.Contains(output, "[build failed]")
}

// extractTestName parses the test file path to derive a test function name filter.
// Returns empty string if no specific name can be extracted.
func extractTestName(testFile string) string {
	// We run the whole package's tests scoped to the file — go test handles it.
	// Returning empty means no -run filter.
	return ""
}
