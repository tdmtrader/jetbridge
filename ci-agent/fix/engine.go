package fix

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
)

// FilePatch represents a file to be written as part of a fix.
type FilePatch struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// FixAdapter generates code patches for a given issue.
type FixAdapter interface {
	Fix(ctx context.Context, issue schema.ProvenIssue, fileContent, testCode string) ([]FilePatch, error)
}

// FixResult captures the outcome of attempting to fix a single issue.
type FixResult struct {
	IssueID      string
	Status       string // "fixed" or "skipped"
	CommitSHA    string
	FilesChanged []string
	Attempts     int
	Reason       schema.SkipReason
	LastError    string
}

// Engine orchestrates the fix attempt for individual issues.
type Engine struct {
	adapter    FixAdapter
	maxRetries int
}

// NewEngine creates a new fix engine.
func NewEngine(adapter FixAdapter, maxRetries int) *Engine {
	return &Engine{adapter: adapter, maxRetries: maxRetries}
}

// FixSingleIssue attempts to fix a single proven issue by:
// 1. Asking the adapter for a patch
// 2. Applying the patch
// 3. Running the proving test
// 4. If test passes, commit; if fails, revert and retry
func (e *Engine) FixSingleIssue(ctx context.Context, repoDir string, issue schema.ProvenIssue, testCode string) FixResult {
	fileContent := readFileContent(filepath.Join(repoDir, issue.File))

	for attempt := 1; attempt <= e.maxRetries; attempt++ {
		// Ask adapter for fix.
		patches, err := e.adapter.Fix(ctx, issue, fileContent, testCode)
		if err != nil {
			return FixResult{
				IssueID:   issue.ID,
				Status:    "skipped",
				Attempts:  attempt,
				Reason:    schema.SkipAgentError,
				LastError: err.Error(),
			}
		}

		// Apply patches.
		var filesChanged []string
		for _, p := range patches {
			fullPath := filepath.Join(repoDir, p.Path)
			if err := os.WriteFile(fullPath, []byte(p.Content), 0644); err != nil {
				return FixResult{
					IssueID:   issue.ID,
					Status:    "skipped",
					Attempts:  attempt,
					Reason:    schema.SkipAgentError,
					LastError: fmt.Sprintf("writing patch: %v", err),
				}
			}
			filesChanged = append(filesChanged, p.Path)
		}

		// Run proving test.
		testPath := filepath.Join(repoDir, issue.TestFile)
		result, err := runner.RunTest(ctx, repoDir, testPath)
		if err != nil {
			// Revert patches.
			revertPatches(repoDir, filesChanged, fileContent, issue.File)
			continue
		}

		if result.Error {
			// Compilation error — revert.
			revertPatches(repoDir, filesChanged, fileContent, issue.File)
			if attempt == e.maxRetries {
				return FixResult{
					IssueID:   issue.ID,
					Status:    "skipped",
					Attempts:  attempt,
					Reason:    schema.SkipCompilationError,
					LastError: result.Output,
				}
			}
			continue
		}

		if result.Pass {
			// Test passes — commit the fix.
			msg := fmt.Sprintf("fix(%s): %s [%s]", issue.Category, issue.Title, issue.ID)
			sha, err := CommitFiles(repoDir, filesChanged, msg)
			if err != nil {
				return FixResult{
					IssueID:   issue.ID,
					Status:    "skipped",
					Attempts:  attempt,
					Reason:    schema.SkipAgentError,
					LastError: fmt.Sprintf("committing: %v", err),
				}
			}

			return FixResult{
				IssueID:      issue.ID,
				Status:       "fixed",
				CommitSHA:    sha,
				FilesChanged: filesChanged,
				Attempts:     attempt,
			}
		}

		// Test failed — revert patches and retry.
		revertPatches(repoDir, filesChanged, fileContent, issue.File)
	}

	return FixResult{
		IssueID:   issue.ID,
		Status:    "skipped",
		Attempts:  e.maxRetries,
		Reason:    schema.SkipFailedVerification,
		LastError: fmt.Sprintf("test still fails after %d attempts", e.maxRetries),
	}
}

func readFileContent(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func revertPatches(repoDir string, files []string, originalContent, originalFile string) {
	// Restore the original file content.
	for _, f := range files {
		if f == originalFile {
			os.WriteFile(filepath.Join(repoDir, f), []byte(originalContent), 0644)
		} else {
			os.Remove(filepath.Join(repoDir, f))
		}
	}
}
