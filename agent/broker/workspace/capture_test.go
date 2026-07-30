package workspace_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	if err := os.Remove(filepath.Join(repository, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repository, "new.bin", []byte{0, 1, 2, 255}, 0o644)
	writeFile(t, repository, "ignored.txt", []byte("secret\n"), 0o644)

	indexBefore := gitBytes(t, repository, "show", ":tracked.txt")
	indexFileBefore, err := os.ReadFile(filepath.Join(repository, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
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
	if indexFileAfter, err := os.ReadFile(filepath.Join(repository, ".git", "index")); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(indexFileAfter, indexFileBefore) {
		t.Fatal("caller index bytes changed")
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
	if got := gitBytes(t, verify, "show", ":tracked.txt"); !bytes.Equal(got, []byte("staged\n")) {
		t.Fatalf("staged tracked content = %q, want %q", got, "staged\\n")
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

func TestCaptureRejectsConflictingStagedAndUnstagedContents(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "tracked.txt", []byte("base\n"), 0o644)
	git(t, repository, "add", "tracked.txt")
	git(t, repository, "commit", "-m", "base")

	writeFile(t, repository, "tracked.txt", []byte("staged\n"), 0o644)
	git(t, repository, "add", "tracked.txt")
	writeFile(t, repository, "tracked.txt", []byte("unstaged\n"), 0o644)

	if _, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	}); err == nil {
		t.Fatal("Capture() accepted conflicting staged and unstaged content")
	}
}

func TestCapturePublishesStableFileObservations(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "script", []byte("#!/bin/sh\necho base\n"), 0o644)
	writeFile(t, repository, "delete.txt", []byte("remove\n"), 0o644)
	writeFile(t, repository, ".gitignore", []byte("ignored.txt\n"), 0o644)
	git(t, repository, "add", ".")
	git(t, repository, "commit", "-m", "base")

	if err := os.Chmod(filepath.Join(repository, "script"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repository, "binary.bin", []byte{0, 1, 2, 255}, 0o644)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("external-only-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repository, "ignored.txt", []byte("excluded\n"), 0o644)

	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	})
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	if capture.PolicyRevision != "git-workspace-capture/v2" {
		t.Fatalf("policy revision = %q", capture.PolicyRevision)
	}
	if bytes.Contains(capture.Patch, []byte("external-only-content")) {
		t.Fatal("capture followed a symlink target")
	}
	if len(capture.Observations) != 5 {
		t.Fatalf("observation count = %d, want 5", len(capture.Observations))
	}
	for index := 1; index < len(capture.Observations); index++ {
		left := capture.Observations[index-1].Path
		right := capture.Observations[index].Path
		if left >= right {
			t.Fatalf("observations are not path-sorted: %q then %q", left, right)
		}
	}
	binary := observation(t, capture, "binary.bin")
	if !binary.Included || !binary.Unstaged || !binary.Binary {
		t.Fatalf("binary observation = %#v", binary)
	}
	deleted := observation(t, capture, "delete.txt")
	if !deleted.Included || !deleted.Unstaged || !deleted.Deleted || deleted.Mode != "100644" {
		t.Fatalf("deletion observation = %#v", deleted)
	}
	ignored := observation(t, capture, "ignored.txt")
	if ignored.Included || !ignored.Excluded {
		t.Fatalf("ignored observation = %#v", ignored)
	}
	link := observation(t, capture, "link")
	if !link.Included || !link.Unstaged || !link.Symlink || link.Mode != "120000" {
		t.Fatalf("symlink observation = %#v", link)
	}
	script := observation(t, capture, "script")
	if !script.Included || !script.Unstaged || script.Mode != "100755" {
		t.Fatalf("mode observation = %#v", script)
	}
}

func TestCaptureRejectsDirtySubmoduleContents(t *testing.T) {
	submodule := newRepository(t)
	writeFile(t, submodule, "inside.txt", []byte("base\n"), 0o644)
	git(t, submodule, "add", "inside.txt")
	git(t, submodule, "commit", "-m", "base")

	repository := newRepository(t)
	git(t, repository, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "module")
	git(t, repository, "commit", "-m", "add submodule")
	writeFile(t, repository, "module/inside.txt", []byte("dirty\n"), 0o644)

	if _, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	}); err == nil {
		t.Fatal("Capture() accepted dirty submodule contents")
	}
}

func TestCaptureRecordsRepresentableSubmoduleGitlink(t *testing.T) {
	submodule := newRepository(t)
	writeFile(t, submodule, "inside.txt", []byte("base\n"), 0o644)
	git(t, submodule, "add", "inside.txt")
	git(t, submodule, "commit", "-m", "base")

	repository := newRepository(t)
	git(t, repository, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "module")
	git(t, repository, "commit", "-m", "add submodule")
	git(t, filepath.Join(repository, "module"), "config", "user.email", "broker@example.invalid")
	git(t, filepath.Join(repository, "module"), "config", "user.name", "Broker Test")
	writeFile(t, repository, "module/inside.txt", []byte("next\n"), 0o644)
	git(t, filepath.Join(repository, "module"), "add", "inside.txt")
	git(t, filepath.Join(repository, "module"), "commit", "-m", "next")

	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	})
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	gitlink := observation(t, capture, "module")
	if !gitlink.Included || !gitlink.Unstaged || !gitlink.Submodule || gitlink.Mode != "160000" {
		t.Fatalf("submodule observation = %#v", gitlink)
	}
}

func TestCaptureRejectsEntryLimit(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "one", []byte("1"), 0o644)
	writeFile(t, repository, "two", []byte("2"), 0o644)
	git(t, repository, "add", ".")
	git(t, repository, "commit", "-m", "base")

	if _, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 1, StabilityAttempts: 1,
	}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("entry limit error = %v", err)
	}
}

func TestCapturePreservesRenamedResultTree(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "old.txt", []byte("base\n"), 0o644)
	git(t, repository, "add", "old.txt")
	git(t, repository, "commit", "-m", "base")
	git(t, repository, "mv", "old.txt", "new.txt")

	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	})
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	if !observation(t, capture, "old.txt").Deleted || !observation(t, capture, "new.txt").Included {
		t.Fatalf("rename observations = %#v", capture.Observations)
	}
}

func TestMaterializeCreatesDisposableCapturedWorkspace(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "tracked.txt", []byte("base\n"), 0o644)
	git(t, repository, "add", "tracked.txt")
	git(t, repository, "commit", "-m", "base")
	writeFile(t, repository, "tracked.txt", []byte("captured\n"), 0o644)

	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	workdir, cleanup, err := workspace.Materialize(scratch, capture)
	if err != nil {
		t.Fatalf("Materialize(): %v", err)
	}
	if workdir == repository || !strings.HasPrefix(workdir, filepath.Clean(scratch)+string(filepath.Separator)) {
		t.Fatalf("workdir = %q, must be a private disposable directory", workdir)
	}
	contents, err := os.ReadFile(filepath.Join(workdir, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "captured\n" {
		t.Fatalf("materialized contents = %q", contents)
	}
	if got := strings.TrimSpace(git(t, workdir, "write-tree")); got != capture.ResultTree {
		t.Fatalf("materialized tree = %s, want %s", got, capture.ResultTree)
	}
	assertNoSharedObjectInodes(t, repository, workdir)
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup(): %v", err)
	}
	if _, err := os.Stat(workdir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workdir remains after cleanup: %v", err)
	}
}

func assertNoSharedObjectInodes(t *testing.T, source, clone string) {
	t.Helper()
	objects := make(map[[2]uint64]string)
	for _, root := range []string{filepath.Join(source, ".git", "objects"), filepath.Join(clone, ".git", "objects")} {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || len(entry.Name()) != 2 {
				continue
			}
			files, err := os.ReadDir(filepath.Join(root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				stat, ok := info.Sys().(*syscall.Stat_t)
				if !ok {
					t.Fatalf("object stat = %T", info.Sys())
				}
				key := [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
				if prior, found := objects[key]; found {
					t.Fatalf("shared object inode %s and %s", prior, filepath.Join(root, entry.Name(), file.Name()))
				}
				objects[key] = filepath.Join(root, entry.Name(), file.Name())
			}
		}
	}
}

func TestCaptureRejectsWorkspaceMutationThatChangesTheManifest(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, ".gitignore", []byte("ignored-*\n"), 0o644)
	git(t, repository, "add", ".gitignore")
	git(t, repository, "commit", "-m", "base")

	mutationFile := filepath.Join(repository, "ignored-during-capture")
	installGitWrapper(t, `
if [ "$1" = clone ] && [ ! -e "$CAPTURE_MUTATION_DONE" ]; then
  : > "$CAPTURE_MUTATION_DONE"
  : > "$CAPTURE_MUTATION_FILE"
fi
exec "$CAPTURE_REAL_GIT" "$@"
`)
	t.Setenv("CAPTURE_MUTATION_FILE", mutationFile)
	t.Setenv("CAPTURE_MUTATION_DONE", filepath.Join(t.TempDir(), "done"))

	if _, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	}); !errors.Is(err, workspace.ErrUnstable) {
		t.Fatalf("mutation error = %v, want ErrUnstable", err)
	}
}

func TestCaptureRetriesOneWorkspaceMutation(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "tracked", []byte("base\n"), 0o644)
	git(t, repository, "add", "tracked")
	git(t, repository, "commit", "-m", "base")

	mutationFile := filepath.Join(repository, "created-during-capture")
	installGitWrapper(t, `
if [ "$1" = clone ] && [ ! -e "$CAPTURE_MUTATION_DONE" ]; then
  : > "$CAPTURE_MUTATION_DONE"
  printf 'captured\n' > "$CAPTURE_MUTATION_FILE"
fi
exec "$CAPTURE_REAL_GIT" "$@"
`)
	t.Setenv("CAPTURE_MUTATION_FILE", mutationFile)
	t.Setenv("CAPTURE_MUTATION_DONE", filepath.Join(t.TempDir(), "done"))

	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 2,
	})
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	if !observation(t, capture, "created-during-capture").Included {
		t.Fatalf("mutation result observations = %#v", capture.Observations)
	}
}

func TestCaptureBindsBaseTreeToItsResolvedBaseCommit(t *testing.T) {
	repository := newRepository(t)
	writeFile(t, repository, "tracked", []byte("base\n"), 0o644)
	git(t, repository, "add", "tracked")
	git(t, repository, "commit", "-m", "base")
	base := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	writeFile(t, repository, "tracked", []byte("next\n"), 0o644)
	git(t, repository, "commit", "-am", "next")
	next := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD"))
	git(t, repository, "reset", "--hard", base)

	installGitWrapper(t, `
if [ "$1" = rev-parse ] && [ "$2" = --verify ]; then
  if [ ! -e "$CAPTURE_FIRST_REV_PARSE" ]; then
    : > "$CAPTURE_FIRST_REV_PARSE"
  elif [ ! -e "$CAPTURE_BASE_SWITCHED" ]; then
    : > "$CAPTURE_BASE_SWITCHED"
    "$CAPTURE_REAL_GIT" -C "$CAPTURE_REPOSITORY" reset --hard "$CAPTURE_NEW_HEAD" >/dev/null
    output="$("$CAPTURE_REAL_GIT" "$@")"
    status=$?
    "$CAPTURE_REAL_GIT" -C "$CAPTURE_REPOSITORY" reset --hard "$CAPTURE_OLD_HEAD" >/dev/null
    printf '%s' "$output"
    exit "$status"
  fi
fi
exec "$CAPTURE_REAL_GIT" "$@"
`)
	t.Setenv("CAPTURE_REPOSITORY", repository)
	t.Setenv("CAPTURE_OLD_HEAD", base)
	t.Setenv("CAPTURE_NEW_HEAD", next)
	t.Setenv("CAPTURE_FIRST_REV_PARSE", filepath.Join(t.TempDir(), "first"))
	t.Setenv("CAPTURE_BASE_SWITCHED", filepath.Join(t.TempDir(), "switched"))

	capture, err := workspace.Capture(repository, workspace.Limits{
		MaxPatchBytes: 1 << 20, MaxEntries: 100, StabilityAttempts: 1,
	})
	if err != nil {
		t.Fatalf("Capture(): %v", err)
	}
	if capture.BaseCommit != base {
		t.Fatalf("base commit = %s, want %s", capture.BaseCommit, base)
	}
	if want := strings.TrimSpace(git(t, repository, "rev-parse", "--verify", capture.BaseCommit+"^{tree}")); capture.BaseTree != want {
		t.Fatalf("base tree = %s, want %s", capture.BaseTree, want)
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

func observation(t *testing.T, capture workspace.Result, path string) workspace.Observation {
	t.Helper()
	for _, observation := range capture.Observations {
		if observation.Path == path {
			return observation
		}
	}
	t.Fatalf("missing observation for %q: %#v", path, capture.Observations)
	return workspace.Observation{}
}

func installGitWrapper(t *testing.T, script string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_REAL_GIT", realGit)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}
