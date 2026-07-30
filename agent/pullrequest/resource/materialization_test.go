package resource

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

func TestMaterializationBoundsRejectOversizedRepository(t *testing.T) {
	repository := t.TempDir()
	packDirectory := filepath.Join(repository, ".git", "objects", "pack")
	if err := os.MkdirAll(packDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(packDirectory, "oversized.pack")
	file, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(snapshot.DefaultMaxSnapshotContentBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateMaterializationBounds(context.Background(), repository, snapshot.DefaultMaxSnapshotEntries, snapshot.DefaultMaxSnapshotContentBytes, snapshot.MaxSnapshotSymlinkTargetBytes); err == nil {
		t.Fatal("oversized repository was accepted")
	}
}

func TestRepositoryEvidenceRejectsLateGitlinkBeyondDirectRunnerCapture(t *testing.T) {
	repository := t.TempDir()
	runGitFixture(t, repository, "init", "--quiet")
	runGitFixture(t, repository, "config", "user.name", "Concourse Test")
	runGitFixture(t, repository, "config", "user.email", "test@example.invalid")
	for index := 0; index < 1000; index++ {
		name := fmt.Sprintf("%04d-%s.txt", index, strings.Repeat("x", 32))
		if err := os.WriteFile(filepath.Join(repository, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runGitFixture(t, repository, "add", ".")
	runGitFixture(t, repository, "commit", "--quiet", "-m", "large index")
	head := strings.TrimSpace(runGitFixture(t, repository, "rev-parse", "HEAD"))
	runGitFixture(t, repository, "update-index", "--add", "--cacheinfo", "160000,"+head+",zzzz-late-gitlink")
	runGitFixture(t, repository, "commit", "--quiet", "-m", "late gitlink")
	listing := runGitFixture(t, repository, "ls-files", "--stage")
	if len(listing) <= 64<<10 {
		t.Fatalf("fixture index is only %d bytes; it must cross directgit's capture limit", len(listing))
	}
	if err := validateRepositoryEvidence(context.Background(), repository); err == nil || !strings.Contains(err.Error(), "gitlink") {
		t.Fatalf("late gitlink validation error = %v", err)
	}
}

func runGitFixture(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func TestMaterializationBoundsCountEveryEntryAndByte(t *testing.T) {
	for _, test := range []struct {
		name       string
		maxEntries int64
		maxBytes   int64
		maxSymlink int64
		build      func(*testing.T, string)
	}{
		{"entries", 2, 1024, 1024, func(t *testing.T, root string) {
			for _, name := range []string{"a", "b", "c"} {
				if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"aggregate bytes", 10, 5, 1024, func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "a"), []byte("abc"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "b"), []byte("def"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink target", 10, 1024, 4, func(t *testing.T, root string) {
			if err := os.Symlink(strings.Repeat("x", 5), filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.build(t, root)
			if err := validateMaterializationBounds(context.Background(), root, test.maxEntries, test.maxBytes, test.maxSymlink); err == nil {
				t.Fatal("out-of-bounds materialization was accepted")
			}
		})
	}
}
