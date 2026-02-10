package fix

import (
	"fmt"
	"os/exec"
	"strings"
)

// CreateBranch creates and checks out a new git branch.
func CreateBranch(repoDir, branchName string) error {
	return gitRun(repoDir, "checkout", "-b", branchName)
}

// CommitFiles stages specific files and commits them.
func CommitFiles(repoDir string, files []string, message string) (string, error) {
	args := append([]string{"add"}, files...)
	if err := gitRun(repoDir, args...); err != nil {
		return "", fmt.Errorf("staging files: %w", err)
	}

	if err := gitRun(repoDir, "commit", "-m", message); err != nil {
		return "", fmt.Errorf("committing: %w", err)
	}

	sha, err := GetHeadSHA(repoDir)
	if err != nil {
		return "", err
	}
	return sha, nil
}

// RevertLastCommit reverts the most recent commit.
func RevertLastCommit(repoDir string) error {
	return gitRun(repoDir, "revert", "--no-edit", "HEAD")
}

// GetHeadSHA returns the full SHA of the current HEAD commit.
func GetHeadSHA(repoDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("getting HEAD SHA: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", args[0], string(out))
	}
	return nil
}
