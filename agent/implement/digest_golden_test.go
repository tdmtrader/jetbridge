package implement

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
)

// The implement-input/v1 digest is a published contract: receipts, summaries,
// the validate task and the worker's before/after check all compare it. These
// goldens were recorded before snapshots could carry a prior change or review
// findings, and a snapshot without either must keep them exactly: the
// optional manifest fields are omitted, not null, when absent. Commit
// identities are fixed by author, committer and dates, so the goldens cover
// the Git capture path, the manifest canonicalization and the uploaded
// archive bytes.
const (
	goldenSnapshotDigest = "1ada55258343e424e8b8c10a07614b850925c54d1de5d39076081e919e4cf4fc"
	goldenArchiveDigest  = "fc9a98d48fc9a5df3acd8d3db4570470895b581a0439f458602b8fff3f467399"
)

func deterministicSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
			"-c", "user.name=Implement fixture", "-c", "user.email=implement@example.test", "-c", "init.defaultBranch=main"}, args...)...)
		c.Env = append(capture.GitEnvironment(), "GIT_AUTHOR_DATE=2026-09-12T00:00:00Z", "GIT_COMMITTER_DATE=2026-09-12T00:00:00Z")
		b, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-q")
	writeTest(t, filepath.Join(repo, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	writeTest(t, filepath.Join(repo, "deleted.txt"), "removed line\n")
	writeTest(t, filepath.Join(repo, "bin", "tool.sh"), "#!/bin/sh\necho tool\n")
	if err := os.Chmod(filepath.Join(repo, "bin", "tool.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("parser.go", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "base")
	brief := filepath.Join(t.TempDir(), "brief.md")
	writeTest(t, brief, "Return the first byte.\n")
	s, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: git("rev-parse", "HEAD"), Brief: brief, Output: filepath.Join(t.TempDir(), "snapshot")})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnapshotDigestIsStable(t *testing.T) {
	s := deterministicSnapshot(t)
	if s.Digest != goldenSnapshotDigest {
		t.Fatalf("implement-input/v1 digest changed: got %s, want %s", s.Digest, goldenSnapshotDigest)
	}
	loaded, err := LoadSnapshot(s.Dir)
	if err != nil || loaded.Digest != goldenSnapshotDigest {
		t.Fatalf("reloaded digest differs: %v", err)
	}
	var archive bytes.Buffer
	if err := s.WriteRunInputArchive(context.Background(), &archive); err != nil {
		t.Fatal(err)
	}
	if got := capture.Digest(archive.Bytes()); got != goldenArchiveDigest {
		t.Fatalf("implement-input/v1 run input archive changed: got %s, want %s", got, goldenArchiveDigest)
	}
}
