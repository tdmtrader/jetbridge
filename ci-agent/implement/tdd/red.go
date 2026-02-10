package tdd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/concourse/ci-agent/implement/adapter"
)

// RedResult describes the outcome of the red-phase verification.
type RedResult struct {
	Confirmed bool   `json:"confirmed"`
	Reason    string `json:"reason,omitempty"`
	Output    string `json:"output,omitempty"`
}

// WriteTestFile writes the generated test to disk inside the repo.
func WriteTestFile(repoDir string, resp *adapter.TestGenResponse) error {
	fullPath := filepath.Join(repoDir, resp.TestFilePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(resp.TestContent), 0644)
}

// VerifyRed runs the test and confirms it fails (TDD red phase).
func VerifyRed(ctx context.Context, repoDir string, testFilePath string) (*RedResult, error) {
	pkgDir := filepath.Dir(testFilePath)
	relPkg, err := filepath.Rel(repoDir, pkgDir)
	if err != nil {
		return nil, err
	}

	pkgPath := "./" + relPkg
	if relPkg == "." {
		pkgPath = "./"
	}

	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout", "30s", pkgPath)
	cmd.Dir = repoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	output := stdout.String() + stderr.String()

	if runErr == nil {
		// Test passed — not a valid red phase.
		return &RedResult{
			Confirmed: false,
			Reason:    "test already passes",
			Output:    output,
		}, nil
	}

	// Check if it's a compilation error vs a test failure.
	if isCompileError(output) {
		return &RedResult{
			Confirmed: false,
			Reason:    "compilation error: " + firstError(output),
			Output:    output,
		}, nil
	}

	// Test failed — valid red phase.
	return &RedResult{
		Confirmed: true,
		Output:    output,
	}, nil
}

func isCompileError(output string) bool {
	return strings.Contains(output, "build failed") ||
		strings.Contains(output, "[build failed]") ||
		strings.Contains(output, "cannot ") ||
		strings.Contains(output, "undefined") ||
		strings.Contains(output, "syntax error")
}

func firstError(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "undefined") || strings.Contains(line, "cannot ") || strings.Contains(line, "syntax error") {
			return line
		}
	}
	return "see output"
}
