package implement

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
)

func treeOf(t *testing.T, s *Snapshot) Tree {
	t.Helper()
	base, err := s.tree()
	if err != nil {
		t.Fatal(err)
	}
	return base
}

// gitApply applies patch to a fresh checkout of base with the real git, and
// returns the resulting worktree bytes of every path the test names.
func gitApply(t *testing.T, repo, base string, patch []byte) {
	t.Helper()
	gitTest(t, repo, "checkout", "-q", "--detach", base)
	c := exec.Command("git", "-C", repo, "apply", "--index", "-")
	c.Env = capture.GitEnvironment()
	c.Stdin = bytes.NewReader(patch)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git apply refused the patch: %v: %s\n%s", err, out, patch)
	}
}

func TestPatchRoundTripsThroughGitApply(t *testing.T) {
	repo, s := snapshotFixture(t)
	base := treeOf(t, s)
	long := string(base["long.txt"].Data)
	cases := map[string]func(Tree){
		"modify-add-delete": func(e Tree) {
			for p, f := range edit(base) {
				e[p] = f
			}
			delete(e, "deleted.txt")
		},
		"no-trailing-newline":   func(e Tree) { e["noeol.txt"] = TreeFile{Data: []byte("one\n2"), Mode: "100644"} },
		"gain-trailing-newline": func(e Tree) { e["noeol.txt"] = TreeFile{Data: []byte("one\ntwo\n"), Mode: "100644"} },
		"space-in-name": func(e Tree) {
			e["docs/with space.md"] = TreeFile{Data: []byte("# Title\n\nChanged.\n"), Mode: "100644"}
		},
		"new-name-with-space": func(e Tree) { e["docs/new file.md"] = TreeFile{Data: []byte("new\n"), Mode: "100644"} },
		"executable-content":  func(e Tree) { e["bin/tool.sh"] = TreeFile{Data: []byte("#!/bin/sh\necho changed\n"), Mode: "100755"} },
		"two-hunks": func(e Tree) {
			changed := strings.Replace(strings.Replace(long, "line c\n", "line C\n", 1), "line x\n", "line X\n", 1)
			e["long.txt"] = TreeFile{Data: []byte(changed), Mode: "100644"}
		},
		"empty-new-file": func(e Tree) { e["empty"] = TreeFile{Data: []byte{}, Mode: "100644"} },
		"emptied-file":   func(e Tree) { e["deleted.txt"] = TreeFile{Data: []byte{}, Mode: "100644"} },
		"delete-exec":    func(e Tree) { delete(e, "bin/tool.sh") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			edited := Tree{}
			for p, f := range base {
				edited[p] = f
			}
			// A workspace cannot hold a symlink: its target text is a regular file.
			edited["link"] = TreeFile{Data: base["link"].Data, Mode: "100644"}
			change(edited)
			patch, changes, err := Diff(base, edited)
			if err != nil {
				t.Fatal(err)
			}
			if len(changes) == 0 {
				t.Fatal("no change detected")
			}
			verified, err := VerifyPatch(base, edited, patch)
			if err != nil {
				t.Fatalf("%v\n%s", err, patch)
			}
			if len(verified) != len(changes) {
				t.Fatalf("verify saw %v, diff saw %v", verified, changes)
			}
			gitApply(t, repo, s.Manifest.BaseCommit, patch)
			for p, f := range edited {
				if base[p].Mode == "120000" {
					continue
				}
				got, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(p)))
				if err != nil || !bytes.Equal(got, f.Data) {
					t.Fatalf("%s after git apply: %q %v", p, got, err)
				}
			}
			for p := range base {
				if _, ok := edited[p]; !ok {
					if _, err := os.Lstat(filepath.Join(repo, filepath.FromSlash(p))); !os.IsNotExist(err) {
						t.Fatalf("%s survived deletion", p)
					}
				}
			}
			gitTest(t, repo, "reset", "-q", "--hard")
		})
	}
}

func TestDiffRefusesWhatEditOnlyCannotExpress(t *testing.T) {
	_, s := snapshotFixture(t)
	base := treeOf(t, s)
	for name, change := range map[string]func(Tree){
		"binary":         func(e Tree) { e["blob.bin"] = TreeFile{Data: []byte{0, 1, 2}, Mode: "100644"} },
		"symlink-edit":   func(e Tree) { e["link"] = TreeFile{Data: []byte("/etc/passwd"), Mode: "100644"} },
		"symlink-delete": func(e Tree) { delete(e, "link") },
		"mode-change":    func(e Tree) { e["parser.go"] = TreeFile{Data: base["parser.go"].Data, Mode: "100755"} },
		"new-executable": func(e Tree) { e["run.sh"] = TreeFile{Data: []byte("#!/bin/sh\n"), Mode: "100755"} },
		"git-dir":        func(e Tree) { e[".git/hooks/pre-commit"] = TreeFile{Data: []byte("#!/bin/sh\n"), Mode: "100644"} },
		"quoted-name":    func(e Tree) { e[`a"b`] = TreeFile{Data: []byte("x\n"), Mode: "100644"} },
		"tab-name":       func(e Tree) { e["a\tb"] = TreeFile{Data: []byte("x\n"), Mode: "100644"} },
	} {
		t.Run(name, func(t *testing.T) {
			edited := Tree{}
			for p, f := range base {
				edited[p] = f
			}
			change(edited)
			if _, _, err := Diff(base, edited); err == nil {
				t.Fatalf("diff accepted %s", name)
			}
		})
	}
}

func TestParsePatchIsStrict(t *testing.T) {
	_, s := snapshotFixture(t)
	base := treeOf(t, s)
	edited := edit(base)
	edited["link"] = TreeFile{Data: base["link"].Data, Mode: "100644"}
	patch, _, err := Diff(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPatch(base, edited, patch); err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	for name, bad := range map[string]string{
		"traversal":     strings.Replace(text, "diff --git a/parser.go b/parser.go", "diff --git a/../parser.go b/../parser.go", 1),
		"mismatch":      strings.Replace(text, "diff --git a/parser.go b/parser.go", "diff --git a/parser.go b/other.go", 1),
		"count":         strings.Replace(text, "@@ -1,2 +1,2 @@", "@@ -1,3 +1,2 @@", 1),
		"stray":         text + "garbage\n",
		"truncated":     text[:len(text)-1],
		"mode":          strings.Replace(text, "new file mode 100644", "new file mode 100755", 1),
		"order":         strings.Replace(text, "diff --git a/deleted.txt", "diff --git a/zzz.txt", 1),
		"binary-marker": strings.Replace(text, "@@ -1,2 +1,2 @@", "GIT binary patch", 1),
	} {
		if bad == text {
			t.Fatalf("%s: tampering did not change the patch:\n%s", name, text)
		}
		if _, err := VerifyPatch(base, edited, []byte(bad)); err == nil {
			t.Errorf("accepted %s patch", name)
		}
	}
	// A patch whose context does not match the base never applies.
	stale := strings.Replace(text, " package parser\n", " package other\n", 1)
	if stale == text {
		t.Fatalf("no context line to tamper:\n%s", text)
	}
	if _, err := VerifyPatch(base, edited, []byte(stale)); err == nil {
		t.Error("applied a patch against the wrong base")
	}
}
