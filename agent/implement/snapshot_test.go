package implement

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotBindsBaseTreeAndBrief(t *testing.T) {
	repo, s := snapshotFixture(t)
	if s.Manifest.Version != InputVersion || s.Manifest.BaseCommit != gitTest(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("manifest does not bind the base commit: %+v", s.Manifest)
	}
	modes := map[string]string{}
	for _, f := range s.Manifest.Files {
		if f.Side != "base" {
			t.Fatalf("side %q", f.Side)
		}
		modes[f.Path] = f.Mode
	}
	if modes["parser.go"] != "100644" || modes["bin/tool.sh"] != "100755" || modes["link"] != "120000" {
		t.Fatalf("unexpected inventory %v", modes)
	}
	// git archive would omit the export-ignored file; capture must not.
	if b, err := os.ReadFile(filepath.Join(s.Dir, "base", "parser.go")); err != nil || !strings.Contains(string(b), "s[1]") {
		t.Fatalf("export-ignored file missing: %v", err)
	}
	if b, err := s.Brief(); err != nil || string(b) != "Return the first byte.\n" {
		t.Fatalf("brief: %q %v", b, err)
	}
	// The same base and brief always give the same digest.
	brief := filepath.Join(t.TempDir(), "brief.md")
	writeTest(t, brief, "Return the first byte.\n")
	again, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Brief: brief, Output: filepath.Join(t.TempDir(), "again")})
	if err != nil {
		t.Fatal(err)
	}
	if again.Digest != s.Digest {
		t.Fatal("recapture changed the digest")
	}
	writeTest(t, brief, "Return the second byte.\n")
	other, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Brief: brief, Output: filepath.Join(t.TempDir(), "other")})
	if err != nil {
		t.Fatal(err)
	}
	if other.Digest == s.Digest {
		t.Fatal("a different brief kept the digest")
	}
}

func TestSnapshotRefusesTampering(t *testing.T) {
	for _, tamper := range []string{"extra", "brief", "base", "symlink", "manifest-side"} {
		t.Run(tamper, func(t *testing.T) {
			_, s := snapshotFixture(t)
			switch tamper {
			case "extra":
				writeTest(t, filepath.Join(s.Dir, "auth.json"), "unexpected")
			case "brief":
				writeTest(t, filepath.Join(s.Dir, "brief.md"), "Do something else.\n")
			case "base":
				writeTest(t, filepath.Join(s.Dir, "base", "parser.go"), "tampered\n")
			case "symlink":
				name := filepath.Join(s.Dir, "base", "parser.go")
				os.Remove(name)
				if err := os.Symlink("/etc/passwd", name); err != nil {
					t.Fatal(err)
				}
			case "manifest-side":
				data, err := os.ReadFile(filepath.Join(s.Dir, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				writeTest(t, filepath.Join(s.Dir, "manifest.json"), strings.Replace(string(data), `"side": "base"`, `"side": "head"`, 1))
			}
			if _, err := LoadSnapshot(s.Dir); err == nil {
				t.Fatalf("accepted %s", tamper)
			}
		})
	}
}

func TestSnapshotCaptureRefusals(t *testing.T) {
	repo, base := repoFixture(t)
	dir := t.TempDir()
	capture := func(brief string) error {
		name := filepath.Join(dir, "brief.md")
		writeTest(t, name, brief)
		_, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: base, Brief: name, Output: filepath.Join(t.TempDir(), "out")})
		return err
	}
	for name, brief := range map[string]string{"empty": " \n", "binary": "a\x00b"} {
		if err := capture(brief); err == nil {
			t.Fatalf("accepted %s brief", name)
		}
	}
	writeTest(t, filepath.Join(repo, "untracked"), "x")
	if err := capture("Do it.\n"); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("captured a dirty worktree: %v", err)
	}
	os.Remove(filepath.Join(repo, "untracked"))
	if _, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Brief: filepath.Join(dir, "brief.md"), Output: filepath.Join(repo, "inside")}); err == nil {
		t.Fatal("published a snapshot inside the repository")
	}
}
