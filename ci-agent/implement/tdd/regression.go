package tdd

import (
	"context"
	"os"
	"path/filepath"
)

// RegressionResult captures whether the full suite is clean after a change.
type RegressionResult struct {
	Clean  bool   `json:"clean"`
	Output string `json:"output,omitempty"`
}

// CheckRegression runs the full test suite and reports whether regressions exist.
func CheckRegression(ctx context.Context, repoDir string, testCmd string) (*RegressionResult, error) {
	suiteResult, err := RunSuite(ctx, repoDir, testCmd)
	if err != nil {
		return nil, err
	}

	return &RegressionResult{
		Clean:  suiteResult.Pass,
		Output: suiteResult.Output,
	}, nil
}

// RevertFiles removes specified files from the working tree (used for rollback).
func RevertFiles(repoDir string, files []string) error {
	for _, f := range files {
		path := filepath.Join(repoDir, f)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
