package tdd

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
)

// GreenResult describes the outcome of the green-phase verification.
type GreenResult struct {
	Confirmed bool   `json:"confirmed"`
	Output    string `json:"output,omitempty"`
}

// VerifyGreen runs the test and confirms it passes (TDD green phase).
func VerifyGreen(ctx context.Context, repoDir string, testFilePath string) (*GreenResult, error) {
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

	if runErr != nil {
		return &GreenResult{
			Confirmed: false,
			Output:    output,
		}, nil
	}

	return &GreenResult{
		Confirmed: true,
		Output:    output,
	}, nil
}
