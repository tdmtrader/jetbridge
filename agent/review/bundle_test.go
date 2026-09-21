package review

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	gitTest(t, repo, "init", "-q")
	gitTest(t, repo, "config", "user.email", "review@example.test")
	gitTest(t, repo, "config", "user.name", "Review fixture")
	writeTest(t, filepath.Join(repo, "parser.go"), "package parser\nfunc First(s string) byte { return s[0] }\n")
	writeTest(t, filepath.Join(repo, "deleted.txt"), "removed line\n")
	// git archive would silently omit this tracked file; capture must not.
	writeTest(t, filepath.Join(repo, ".gitattributes"), "parser.go export-ignore\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "base")
	base := gitTest(t, repo, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(repo, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(repo, "parser.go"), "package parser\nfunc First(s string) byte { return s[1] }\n")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "head")
	return repo, base, gitTest(t, repo, "rev-parse", "HEAD")
}

func gitTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	c.Env = gitEnvironment()
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.Bytes())
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

func captureTest(t *testing.T) *Bundle {
	t.Helper()
	repo, base, head := gitFixture(t)
	b, err := Capture(context.Background(), CaptureOptions{Repo: repo, Base: base, Head: head, Output: filepath.Join(t.TempDir(), "bundle")})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBundleRejectsSymlinkAndExtraPayload(t *testing.T) {
	for _, kind := range []string{"extra", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			b := captureTest(t)
			if kind == "extra" {
				writeTest(t, filepath.Join(b.Dir, "auth.json"), "unexpected")
			} else {
				name := filepath.Join(b.Dir, "head", "parser.go")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/passwd", name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := LoadBundle(b.Dir); err == nil {
				t.Fatal("accepted " + kind)
			}
		})
	}
}
