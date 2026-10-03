package capture

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// WriteArchive streams the manifest and the verified inventory below its
// directory, and refuses a file that changed after the inventory was taken.
func TestWriteArchiveStreamsTheInventoryAndRefusesChanges(t *testing.T) {
	repo, commit := fixture(t)
	for _, tamper := range []string{"none", "modified", "grown", "missing", "manifest"} {
		t.Run(tamper, func(t *testing.T) {
			dest, files, err := captureInto(t, repo, commit)
			if err != nil {
				t.Fatal(err)
			}
			inv := Inventory{Files: map[string]Entry{}, Dirs: []string{"base"}}
			for _, f := range files {
				inv.Files["base/"+f.Path] = Entry{Digest: f.Digest, Size: f.Size}
			}
			parser := filepath.Join(dest, "base", "parser.go")
			original, err := os.ReadFile(parser)
			if err != nil {
				t.Fatal(err)
			}
			switch tamper {
			case "modified":
				write(t, parser, string(bytes.ToUpper(original)))
			case "grown":
				write(t, parser, string(original)+"\n")
			case "missing":
				os.Remove(parser)
			case "manifest":
				inv.Files[manifestFile] = Entry{Digest: Digest(nil), Size: 0}
			}
			root, err := os.OpenRoot(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			manifest := []byte(`{"version":"fixture"}`)
			var archive bytes.Buffer
			err = WriteArchive(context.Background(), &archive, root, "sealed", manifest, inv)
			switch tamper {
			case "none":
				if err != nil {
					t.Fatal(err)
				}
			case "modified", "grown":
				if !errors.Is(err, ErrChangedDuringUpload) {
					t.Fatalf("got %v, want ErrChangedDuringUpload", err)
				}
				return
			default:
				if err == nil {
					t.Fatal("archive written")
				}
				return
			}
			got := map[string][]byte{}
			var order []string
			r := tar.NewReader(&archive)
			for {
				h, err := r.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if h.Typeflag != tar.TypeReg || h.Mode != 0600 {
					t.Fatalf("%s: type %c mode %o", h.Name, h.Typeflag, h.Mode)
				}
				data, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				got[h.Name], order = data, append(order, h.Name)
			}
			if !bytes.Equal(got["sealed/"+manifestFile], manifest) {
				t.Fatalf("manifest %q", got["sealed/"+manifestFile])
			}
			if !bytes.Equal(got["sealed/base/parser.go"], original) {
				t.Fatalf("parser.go %q", got["sealed/base/parser.go"])
			}
			if len(got) != len(inv.Files)+1 {
				t.Fatalf("archive holds %v", order)
			}
			for i := 1; i < len(order); i++ {
				if order[i-1] >= order[i] {
					t.Fatalf("archive is not in name order: %v", order)
				}
			}
		})
	}
}

func TestWriteArchiveStopsWhenCancelled(t *testing.T) {
	repo, commit := fixture(t)
	dest, files, err := captureInto(t, repo, commit)
	if err != nil {
		t.Fatal(err)
	}
	inv := Inventory{Files: map[string]Entry{}}
	for _, f := range files {
		inv.Files["base/"+f.Path] = Entry{Digest: f.Digest, Size: f.Size}
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteArchive(ctx, io.Discard, root, "sealed", []byte("{}"), inv); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
