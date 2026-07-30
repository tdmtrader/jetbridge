package workspace_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker/workspace"
)

func TestCaptureIncludesCompleteDirtyWorkspaceAndPreservesIndex(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "tracked.txt", []byte("base\n"), 0o644)
	writeFile(t, repository, "delete.txt", []byte("remove\n"), 0o644)
	writeFile(t, repository, ".gitignore", []byte("ignored.txt\n"), 0o644)
	git(t, repository, "add", ".")
	git(t, repository, "commit", "-m", "base")

	writeFile(t, repository, "tracked.txt", []byte("staged\n"), 0o644)
	git(t, repository, "add", "tracked.txt")
	writeFile(t, repository, "tracked.txt", []byte("working\n"), 0o644)
	if err := os.Remove(filepath.Join(repository, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repository, "new.bin", []byte{0, 1, 2, 255}, 0o644)
	writeFile(t, repository, "ignored.txt", []byte("secret\n"), 0o644)

	indexBefore := gitBytes(t, repository, "show", ":tracked.txt")
	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 2,
	})
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	if capture.BaseCommit == "" || capture.BaseTree == "" || capture.ResultTree == "" {
		t.Fatalf("capture misses Git identity: %#v", capture)
	}
	if capture.ResultTree == capture.BaseTree {
		t.Fatal("dirty workspace produced the base tree")
	}
	if !bytes.Contains(capture.Patch, []byte("tracked.txt")) ||
		!bytes.Contains(capture.Patch, []byte("delete.txt")) ||
		!bytes.Contains(capture.Patch, []byte("new.bin")) {
		t.Fatalf("patch does not contain full dirty state:\n%s", capture.Patch)
	}
	if bytes.Contains(capture.Patch, []byte("ignored.txt")) {
		t.Fatal("ignored file was captured")
	}
	if got := gitBytes(t, repository, "show", ":tracked.txt"); !bytes.Equal(got, indexBefore) {
		t.Fatalf("caller index changed: got %q want %q", got, indexBefore)
	}

	verify := t.TempDir()
	git(t, verify, "init")
	git(t, verify, "remote", "add", "origin", repository)
	git(t, verify, "fetch", "origin", capture.BaseCommit)
	git(t, verify, "checkout", "--detach", capture.BaseCommit)
	command := exec.Command("git", "apply", "--index", "--binary", "-")
	command.Dir = verify
	command.Stdin = bytes.NewReader(capture.Patch)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("apply captured patch: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(git(t, verify, "write-tree")); got != capture.ResultTree {
		t.Fatalf("applied tree = %s, want %s", got, capture.ResultTree)
	}
}

func TestCaptureRejectsNonGitAndOversizedChanges(t *testing.T) {
	if _, err := workspace.Capture(t.TempDir(), workspace.Limits{
		MaxPatchBytes: 1024, MaxEntries: 10, StabilityAttempts: 1,
	}); err == nil || !strings.Contains(err.Error(), "Git worktree") {
		t.Fatalf("non-Git error = %v", err)
	}

	repository := newRepository(t)
	writeFile(t, repository, "base", []byte("base"), 0o644)
	git(t, repository, "add", ".")
	git(t, repository, "commit", "-m", "base")
	writeFile(t, repository, "large", bytes.Repeat([]byte("x"), 4096), 0o644)
	if _, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 32, MaxEntries: 10, StabilityAttempts: 1,
	}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("size error = %v", err)
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	git(t, directory, "init")
	git(t, directory, "config", "user.email", "broker@example.invalid")
	git(t, directory, "config", "user.name", "Broker Test")
	return directory
}

func writeFile(t *testing.T, root, name string, contents []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func gitBytes(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return output
}
