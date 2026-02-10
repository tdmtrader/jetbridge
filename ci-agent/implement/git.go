package implement

import (
	"os/exec"
	"strings"
)

// StageFiles stages specific files for commit.
func StageFiles(repoDir string, files []string) error {
	args := append([]string{"add"}, files...)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	return cmd.Run()
}

// Commit creates a git commit and returns the full SHA.
func Commit(repoDir string, message string) (string, error) {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return CurrentSHA(repoDir)
}

// RevertLast performs a soft reset of the last commit.
func RevertLast(repoDir string) error {
	cmd := exec.Command("git", "reset", "--soft", "HEAD~1")
	cmd.Dir = repoDir
	return cmd.Run()
}

// CurrentSHA returns the full SHA of HEAD.
func CurrentSHA(repoDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateBranch creates and checks out a new branch.
func CreateBranch(repoDir string, branchName string) error {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = repoDir
	return cmd.Run()
}
