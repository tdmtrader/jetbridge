package implement

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
)

func gitTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
		"-c", "user.name=Implement fixture", "-c", "user.email=implement@example.test"}, args...)...)
	c.Env = capture.GitEnvironment()
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func writeTest(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// repoFixture commits a base tree with the shapes a patch must handle: an
// export-ignored file, a file without a trailing newline, a name with a
// space, an executable, a symlink and a file to delete. The repository has a
// local identity so Apply can commit.
func repoFixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitTest(t, repo, "init", "-q")
	gitTest(t, repo, "config", "user.name", "Implement fixture")
	gitTest(t, repo, "config", "user.email", "implement@example.test")
	gitTest(t, repo, "config", "commit.gpgsign", "false")
	writeTest(t, filepath.Join(repo, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	writeTest(t, filepath.Join(repo, ".gitattributes"), "parser.go export-ignore\n")
	writeTest(t, filepath.Join(repo, "deleted.txt"), "removed line\n")
	writeTest(t, filepath.Join(repo, "noeol.txt"), "one\ntwo")
	writeTest(t, filepath.Join(repo, "docs", "with space.md"), "# Title\n\nBody.\n")
	writeTest(t, filepath.Join(repo, "bin", "tool.sh"), "#!/bin/sh\necho tool\n")
	if err := os.Chmod(filepath.Join(repo, "bin", "tool.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("parser.go", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	var long strings.Builder
	for i := 1; i <= 40; i++ {
		long.WriteString("line " + string(rune('a'+i%26)) + "\n")
	}
	writeTest(t, filepath.Join(repo, "long.txt"), long.String())
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "base")
	return repo, gitTest(t, repo, "rev-parse", "HEAD")
}

func snapshotFixture(t *testing.T) (string, *Snapshot) {
	t.Helper()
	repo, base := repoFixture(t)
	brief := filepath.Join(t.TempDir(), "brief.md")
	writeTest(t, brief, "Return the first byte.\n")
	s, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: base, Brief: brief, Output: filepath.Join(t.TempDir(), "snapshot")})
	if err != nil {
		t.Fatal(err)
	}
	return repo, s
}

// edit applies the canonical fixture edit to a copy of base.
func edit(base Tree) Tree {
	edited := Tree{}
	for p, f := range base {
		edited[p] = f
	}
	edited["parser.go"] = TreeFile{Data: []byte("package parser\nfunc First(s string) byte { return s[0] }\n"), Mode: "100644"}
	edited["parser_test.go"] = TreeFile{Data: []byte("package parser\n"), Mode: "100644"}
	delete(edited, "deleted.txt")
	return edited
}
