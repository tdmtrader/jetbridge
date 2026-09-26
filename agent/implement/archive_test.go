package implement

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extract writes a Run input archive the way input materialization lays it
// out, and returns the input root.
func extract(t *testing.T, archive []byte) string {
	t.Helper()
	root := t.TempDir()
	r := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return root
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg || h.Mode != 0600 || !strings.HasPrefix(h.Name, RunInputSnapshotDir+"/") {
			t.Fatalf("unexpected archive entry %q type %v mode %o", h.Name, h.Typeflag, h.Mode)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		writeTest(t, filepath.Join(root, filepath.FromSlash(h.Name)), string(data))
	}
}

func TestRunInputArchiveCarriesTheSnapshotUnchanged(t *testing.T) {
	_, s := snapshotFixture(t)
	var archive bytes.Buffer
	if err := s.WriteRunInputArchive(context.Background(), &archive); err != nil {
		t.Fatal(err)
	}
	received, err := LoadSnapshot(filepath.Join(extract(t, archive.Bytes()), RunInputSnapshotDir))
	if err != nil {
		t.Fatal(err)
	}
	if received.Digest != s.Digest {
		t.Fatal("the uploaded snapshot has a different digest")
	}
	var again bytes.Buffer
	if err := s.WriteRunInputArchive(context.Background(), &again); err != nil || !bytes.Equal(again.Bytes(), archive.Bytes()) {
		t.Fatalf("the archive is not deterministic: %v", err)
	}
}

func TestRunInputArchiveRefusesAChangedSnapshot(t *testing.T) {
	for _, tamper := range []string{"brief", "base", "extra"} {
		t.Run(tamper, func(t *testing.T) {
			_, s := snapshotFixture(t)
			switch tamper {
			case "brief":
				writeTest(t, filepath.Join(s.Dir, "brief.md"), "Do something else.\n")
			case "base":
				writeTest(t, filepath.Join(s.Dir, "base", "parser.go"), "tampered\n")
			case "extra":
				writeTest(t, filepath.Join(s.Dir, "auth.json"), "synthetic")
			}
			var archive bytes.Buffer
			if err := s.WriteRunInputArchive(context.Background(), &archive); err == nil {
				t.Fatal("a changed snapshot was archived")
			}
			if bytes.Contains(archive.Bytes(), []byte("synthetic")) || bytes.Contains(archive.Bytes(), []byte("tampered")) {
				t.Fatal("changed content reached the archive")
			}
		})
	}
	t.Run("replaced after load", func(t *testing.T) {
		repo, s := snapshotFixture(t)
		brief := filepath.Join(t.TempDir(), "brief.md")
		writeTest(t, brief, "Return the second byte.\n")
		// A valid snapshot of the same base with another brief.
		other, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: s.Manifest.BaseCommit, Brief: brief, Output: filepath.Join(t.TempDir(), "other")})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(s.Dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(other.Dir, s.Dir); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteRunInputArchive(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "changed before upload") {
			t.Fatalf("a replaced snapshot was archived: %v", err)
		}
	})
}
