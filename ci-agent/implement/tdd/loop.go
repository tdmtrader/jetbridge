package tdd

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/ci-agent/implement/adapter"
)

// TaskInfo describes a task to execute (decoupled from implement.PlanTask to avoid import cycle).
type TaskInfo struct {
	ID          string
	Description string
	Phase       string
	Files       []string
}

// TaskStatusResult is the outcome status of a task execution.
type TaskStatusResult string

const (
	TaskCommitted TaskStatusResult = "committed"
	TaskSkipped   TaskStatusResult = "skipped"
	TaskFailed    TaskStatusResult = "failed"
)

// TaskLoopOpts configures the single-task TDD loop.
type TaskLoopOpts struct {
	RepoDir     string
	Task        TaskInfo
	SpecContext string
	Adapter     adapter.Adapter
	TestCmd     string
	MaxRetries  int
}

// TaskResult captures the outcome of executing a single task.
type TaskResult struct {
	Status       TaskStatusResult `json:"status"`
	CommitSHA    string           `json:"commit_sha,omitempty"`
	TestFile     string           `json:"test_file,omitempty"`
	FilesChanged []string         `json:"files_changed,omitempty"`
	Attempts     int              `json:"attempts"`
	Duration     time.Duration    `json:"duration"`
	Reason       string           `json:"reason,omitempty"`
}

// ExecuteTask runs the full TDD cycle for a single task.
func ExecuteTask(ctx context.Context, opts TaskLoopOpts) (*TaskResult, error) {
	start := time.Now()
	result := &TaskResult{}

	req := adapter.CodeGenRequest{
		TaskDescription: opts.Task.Description,
		SpecContext:     opts.SpecContext,
		RepoDir:         opts.RepoDir,
		TargetFiles:     opts.Task.Files,
	}

	// RED PHASE: Generate and verify failing test.
	testResp, err := opts.Adapter.GenerateTest(ctx, req)
	if err != nil {
		result.Status = TaskFailed
		result.Reason = "agent_error: " + err.Error()
		result.Duration = time.Since(start)
		return result, nil
	}

	if err := WriteTestFile(opts.RepoDir, testResp); err != nil {
		result.Status = TaskFailed
		result.Reason = "write_test_error: " + err.Error()
		result.Duration = time.Since(start)
		return result, nil
	}

	testFilePath := filepath.Join(opts.RepoDir, testResp.TestFilePath)
	result.TestFile = testResp.TestFilePath

	redResult, err := VerifyRed(ctx, opts.RepoDir, testFilePath)
	if err != nil {
		result.Status = TaskFailed
		result.Reason = "red_verify_error: " + err.Error()
		result.Duration = time.Since(start)
		return result, nil
	}

	if !redResult.Confirmed {
		result.Status = TaskSkipped
		result.Reason = "already_satisfied: " + redResult.Reason
		result.Duration = time.Since(start)
		return result, nil
	}

	// GREEN PHASE: Generate implementation and verify test passes.
	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		result.Attempts = attempt + 1

		implResp, err := opts.Adapter.GenerateImpl(ctx, req, testResp.TestContent)
		if err != nil {
			result.Status = TaskFailed
			result.Reason = "agent_error: " + err.Error()
			result.Duration = time.Since(start)
			return result, nil
		}

		modified, err := ApplyPatches(opts.RepoDir, implResp.Patches)
		if err != nil {
			result.Status = TaskFailed
			result.Reason = "patch_error: " + err.Error()
			result.Duration = time.Since(start)
			return result, nil
		}
		result.FilesChanged = modified

		greenResult, err := VerifyGreen(ctx, opts.RepoDir, testFilePath)
		if err != nil {
			result.Status = TaskFailed
			result.Reason = "green_verify_error: " + err.Error()
			result.Duration = time.Since(start)
			return result, nil
		}

		if !greenResult.Confirmed {
			RevertFiles(opts.RepoDir, modified)
			if attempt == opts.MaxRetries {
				result.Status = TaskFailed
				result.Reason = "green_failed_after_retries"
				result.Duration = time.Since(start)
				return result, nil
			}
			continue
		}

		// REGRESSION CHECK.
		regResult, err := CheckRegression(ctx, opts.RepoDir, opts.TestCmd)
		if err != nil {
			result.Status = TaskFailed
			result.Reason = "regression_check_error: " + err.Error()
			result.Duration = time.Since(start)
			return result, nil
		}

		if !regResult.Clean {
			RevertFiles(opts.RepoDir, modified)
			if attempt == opts.MaxRetries {
				result.Status = TaskFailed
				result.Reason = "regression_after_retries"
				result.Duration = time.Since(start)
				return result, nil
			}
			continue
		}

		// COMMIT: stage files and commit.
		allFiles := append(modified, testResp.TestFilePath)
		if err := gitStageFiles(opts.RepoDir, allFiles); err != nil {
			result.Status = TaskFailed
			result.Reason = "stage_error: " + err.Error()
			result.Duration = time.Since(start)
			return result, nil
		}

		msg := "feat: " + opts.Task.Description
		sha, err := gitCommit(opts.RepoDir, msg)
		if err != nil {
			result.Status = TaskFailed
			result.Reason = "commit_error: " + err.Error()
			result.Duration = time.Since(start)
			return result, nil
		}

		result.Status = TaskCommitted
		result.CommitSHA = sha
		result.Duration = time.Since(start)
		return result, nil
	}

	result.Status = TaskFailed
	result.Reason = "exhausted_retries"
	result.Duration = time.Since(start)
	return result, nil
}

// git helpers (inline to avoid import cycle with implement package).
func gitStageFiles(repoDir string, files []string) error {
	args := append([]string{"add"}, files...)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	return cmd.Run()
}

func gitCommit(repoDir string, message string) (string, error) {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return gitCurrentSHA(repoDir)
}

func gitCurrentSHA(repoDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
