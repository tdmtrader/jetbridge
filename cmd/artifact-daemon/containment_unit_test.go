package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The symlink-target rule, exercised directly. The end-to-end behaviour lives
// in containment_test.go; this pins the decision boundary itself, including the
// cases that are easy to get wrong by one level.
func TestValidateSymlinkTarget(t *testing.T) {
	for _, tc := range []struct {
		name      string
		entryName string
		linkname  string
		wantErr   bool
	}{
		{"sibling file", "link", "file.txt", false},
		{"into subdirectory", "link", "sub/file.txt", false},
		{"shared dep between trees", "app/node_modules", "../shared", false},
		{"up then back down, still inside", "a/b/link", "../../c/d", false},
		{"dot target", "link", ".", false},
		{"explicit relative", "link", "./file.txt", false},
		{"deep entry climbing to root of dest", "a/b/c/link", "../../..", false},

		{"escapes by one level", "link", "..", true},
		{"escapes from nested entry", "a/link", "../..", true},
		{"escapes far", "link", "../../../../etc/passwd", true},
		{"absolute unix path", "link", "/etc/passwd", true},
		{"absolute root", "link", "/", true},
		{"empty target", "link", "", true},
		{"climbs out through the middle", "a/link", "../../b/../..", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSymlinkTarget(tc.entryName, tc.linkname)
			if tc.wantErr && err == nil {
				t.Errorf("validateSymlinkTarget(%q, %q) = nil, want error", tc.entryName, tc.linkname)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateSymlinkTarget(%q, %q) = %v, want nil", tc.entryName, tc.linkname, err)
			}
		})
	}
}

// The proposal claims: "if every link is individually contained relative to its
// own location, following any sequence of them stays inside" — and adds that
// this "is a claim the tests must actually exercise, not an argument to be
// trusted."
//
// This is that test. For each candidate link, ask validateSymlinkTarget for a
// verdict, then create the link for real and ask the FILESYSTEM where it lands.
// A lexical rule that says "contained" while the kernel resolves outside the
// root is a hole; that disagreement is the entire defect class this track
// exists to remove.
func TestValidatorAgreesWithTheFilesystem(t *testing.T) {
	entryNames := []string{
		"link", "a/link", "a/b/link", "a/b/c/link",
	}
	targets := []string{
		"file.txt", "./file.txt", ".", "..", "../..", "../../..", "../../../..",
		"sub/file", "../sib", "../../sib", "a/../b", "../a/../..",
		"/etc", "/", "/tmp/x",
		"", "...", "..foo", "foo..", ".../..",
		"a/b/../../..", "./../..",
	}

	for _, entryName := range entryNames {
		for _, target := range targets {
			t.Run(strings.ReplaceAll(entryName+"__"+target, "/", "_"), func(t *testing.T) {
				root := t.TempDir()

				verdict := validateSymlinkTarget(entryName, target)

				// Build the link for real, inside the root.
				linkPath := filepath.Join(root, entryName)
				if err := os.MkdirAll(filepath.Dir(linkPath), 0755); err != nil {
					t.Skip("cannot stage entry dir")
				}
				if target == "" {
					// The filesystem refuses an empty target outright; the
					// validator refusing it is trivially in agreement.
					if verdict == nil {
						t.Error("validator allowed an empty symlink target")
					}
					return
				}
				if err := os.Symlink(target, linkPath); err != nil {
					t.Skipf("cannot create link: %v", err)
				}

				// Where does the kernel actually put it?
				realRoot, _ := filepath.EvalSymlinks(root)
				if realRoot == "" {
					realRoot = root
				}

				resolved, err := filepath.EvalSymlinks(linkPath)
				if err != nil {
					// Dangling link: nothing exists at the target. Resolve
					// lexically from the link's REAL directory — both sides must
					// be resolved the same way or a platform where /var is a
					// symlink to /private/var reports every contained link as an
					// escape.
					if filepath.IsAbs(target) {
						// An absolute target resolves to itself; Join would
						// wrongly graft it onto the link's directory.
						resolved = filepath.Clean(target)
					} else {
						realDir, derr := filepath.EvalSymlinks(filepath.Dir(linkPath))
						if derr != nil {
							realDir = filepath.Dir(linkPath)
						}
						resolved = filepath.Clean(filepath.Join(realDir, target))
					}
				}

				escapes := resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator))

				switch {
				case escapes && verdict == nil:
					t.Errorf("HOLE: validator ALLOWED %q -> %q, but it resolves to %q, outside %q",
						entryName, target, resolved, realRoot)
				case !escapes && verdict != nil:
					t.Errorf("OVER-STRICT: validator REFUSED %q -> %q, but it resolves to %q, inside %q (%v)",
						entryName, target, resolved, realRoot, verdict)
				}
			})
		}
	}
}
