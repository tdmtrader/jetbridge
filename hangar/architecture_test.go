package hangar

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const hangarImportPath = "github.com/concourse/concourse/hangar"

func TestArchitectureHasNoAgentImports(t *testing.T) {
	var paths []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".go" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("finding Go files: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("architecture check matched no Go files")
	}

	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse imports in %s: %v", path, err)
			continue
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", path, err)
				continue
			}
			if strings.Contains(importPath, "/agent/") || strings.HasSuffix(importPath, "/agent") {
				t.Errorf("%s imports prohibited agent package %q", path, importPath)
			}
		}
	}
}

// hangar is imported by atc/runtime, atc/atccmd and atc/worker/jetbridge, so
// every package hangar imports is linked into the web binary too. While the
// GCS store lived in this package that cost ./cmd/concourse 168 extra packages
// and 20 MB; the store now lives in hangar/gcs, which only cmd/artifact-daemon
// imports. This asserts the shape that keeps it that way: hangar itself is a
// leaf — no cloud client, and no first-party import but its own path, which is
// what "go list -deps ./hangar/ | grep concourse prints only hangar" means.
//
// Deliberately NOT the recursive walk above: hangar/gcs is expected to import
// both, and scanning it here would make this guard assert the opposite of what
// it exists for.
func TestArchitectureHangarPackageIsALeaf(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the hangar package directory: %v", err)
	}
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Errorf("parse imports in %s: %v", entry.Name(), err)
			continue
		}
		scanned++
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Errorf("unquote import in %s: %v", entry.Name(), err)
				continue
			}
			if strings.HasPrefix(importPath, "cloud.google.com/") || strings.HasPrefix(importPath, "google.golang.org/api") {
				t.Errorf("%s imports %q: a cloud client belongs in a leaf package the daemon imports, "+
					"such as hangar/gcs, not in the package the web binary links", entry.Name(), importPath)
			}
			if strings.HasPrefix(importPath, "github.com/concourse/concourse/") && importPath != hangarImportPath {
				t.Errorf("%s imports first-party package %q; hangar must stay importable from anywhere", entry.Name(), importPath)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no Go files in the hangar package — this guard cannot fail")
	}
}
