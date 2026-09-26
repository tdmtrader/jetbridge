package capture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false",
		"-c", "user.name=Capture fixture", "-c", "user.email=capture@example.test"}, args...)...)
	c.Env = GitEnvironment()
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

func write(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

// fixture commits a tree that git archive would not reproduce faithfully: an
// export-ignored file, an executable and a symlink.
func fixture(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	write(t, filepath.Join(repo, "parser.go"), "package parser\n")
	write(t, filepath.Join(repo, ".gitattributes"), "parser.go export-ignore\n")
	write(t, filepath.Join(repo, "bin", "tool.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(repo, "bin", "tool.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "base")
	return repo, git(t, repo, "rev-parse", "HEAD")
}

func captureInto(t *testing.T, repo, commit string) (string, []File, error) {
	t.Helper()
	dest := t.TempDir()
	var total int64
	files, err := CaptureTree(context.Background(), repo, commit, "base", dest, &total)
	return dest, files, err
}

func TestCaptureTreeKeepsExportIgnoredFilesAndInertLinks(t *testing.T) {
	repo, commit := fixture(t)
	dest, files, err := captureInto(t, repo, commit)
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, f := range files {
		if f.Side != "base" {
			t.Fatalf("side %q", f.Side)
		}
		modes[f.Path] = f.Mode
	}
	if modes["parser.go"] != "100644" || modes["bin/tool.sh"] != "100755" || modes["link"] != "120000" || modes[".gitattributes"] != "100644" {
		t.Fatalf("unexpected inventory %v", modes)
	}
	// The export-ignore trap: git archive would silently omit parser.go.
	if b, err := os.ReadFile(filepath.Join(dest, "base", "parser.go")); err != nil || string(b) != "package parser\n" {
		t.Fatalf("export-ignored file not captured: %v", err)
	}
	// A symlink is captured as its target text, never as a link.
	st, err := os.Lstat(filepath.Join(dest, "base", "link"))
	if err != nil || !st.Mode().IsRegular() {
		t.Fatalf("symlink materialized as %v: %v", st.Mode(), err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "base", "link")); string(b) != "/etc/passwd" {
		t.Fatalf("symlink target text %q", b)
	}
	// Nothing is materialized executable.
	st, err = os.Stat(filepath.Join(dest, "base", "bin", "tool.sh"))
	if err != nil || st.Mode().Perm()&0111 != 0 {
		t.Fatalf("executable materialized: %v %v", st.Mode(), err)
	}
}

func TestCaptureTreeRefusesSubmodulesAndLFS(t *testing.T) {
	for _, kind := range []string{"submodule", "lfs"} {
		t.Run(kind, func(t *testing.T) {
			repo, commit := fixture(t)
			if kind == "submodule" {
				git(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sub")
			} else {
				write(t, filepath.Join(repo, "asset"), "version https://git-lfs.github.com/spec/v1\noid sha256:"+strings.Repeat("a", 64)+"\nsize 12\n")
				git(t, repo, "add", ".")
			}
			git(t, repo, "commit", "-qm", kind)
			_, _, err := captureInto(t, repo, git(t, repo, "rev-parse", "HEAD"))
			want := map[string]string{"submodule": "submodules", "lfs": "Git LFS"}[kind]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("captured %s: %v", kind, err)
			}
		})
	}
}

func TestRequireCleanAndResolve(t *testing.T) {
	repo, commit := fixture(t)
	if err := RequireClean(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveCommit(context.Background(), repo, "HEAD"); err != nil || got != commit {
		t.Fatalf("resolve HEAD: %s %v", got, err)
	}
	if _, err := ResolveCommit(context.Background(), repo, "no-such-ref"); err == nil {
		t.Fatal("resolved a missing ref")
	}
	write(t, filepath.Join(repo, "untracked"), "x")
	if err := RequireClean(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty worktree accepted: %v", err)
	}
	top, err := Repository(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Output(top, filepath.Join(repo, "inside")); err == nil {
		t.Fatal("accepted output inside the repository")
	}
}

func TestVerifyRequiresTheExactInventory(t *testing.T) {
	repo, commit := fixture(t)
	for _, tamper := range []string{"none", "extra", "symlink", "modified", "missing", "size"} {
		t.Run(tamper, func(t *testing.T) {
			dest, files, err := captureInto(t, repo, commit)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dest, "manifest.json"), "{}")
			inv := Inventory{Files: map[string]Entry{}, Dirs: []string{"base"}}
			for _, f := range files {
				inv.Files["base/"+f.Path] = Entry{Digest: f.Digest, Size: f.Size}
			}
			parser := filepath.Join(dest, "base", "parser.go")
			switch tamper {
			case "extra":
				write(t, filepath.Join(dest, "auth.json"), "unexpected")
			case "symlink":
				os.Remove(parser)
				if err := os.Symlink("/etc/passwd", parser); err != nil {
					t.Fatal(err)
				}
			case "modified":
				write(t, parser, "tampered\n")
			case "missing":
				os.Remove(parser)
			case "size":
				e := inv.Files["base/parser.go"]
				e.Size++
				inv.Files["base/parser.go"] = e
			}
			root, err := os.OpenRoot(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = Verify(root, inv)
			if (err == nil) != (tamper == "none") {
				t.Fatalf("%s: %v", tamper, err)
			}
		})
	}
}

func TestSafePath(t *testing.T) {
	for name, ok := range map[string]bool{
		"a/b.go": true, "a b": true, "": false, ".": false, "..": false, "../x": false, "/etc/passwd": false,
		"a/../b": false, "a//b": false, "a\\b": false, "a\nb": false, "a\x00b": false, string([]byte{0xff}): false,
	} {
		if SafePath(name) != ok {
			t.Errorf("SafePath(%q) != %v", name, ok)
		}
	}
}
