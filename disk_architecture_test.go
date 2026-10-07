package concourse

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Disk role credentials enforce authorization. These guards also keep storage
// ownership out of accidental command composition: only the store's own binary
// serves the disk. The client half of hangar/disk is linked by every disk-store
// client, so who may OWN the disk is asked of the calls below rather than of the
// link, and who may construct a delete client is fixed by
// hangar/architecture_test.go.
func TestDiskCapabilitiesStayInTheirOwningCommands(t *testing.T) {
	owners := map[string]string{
		"github.com/concourse/concourse/hangar/diskserver": "./cmd/hangar-store",
	}
	roots := commandRoots(t)
	if len(roots) == 0 {
		t.Fatal("no command roots found")
	}
	for capability, owner := range owners {
		if !linksPackage(t, owner, capability) {
			t.Fatalf("%s does not link %s; guard is vacuous", owner, capability)
		}
		for _, root := range roots {
			if root != owner && linksPackage(t, root, capability) {
				t.Errorf("%s unexpectedly links %s", root, capability)
			}
		}
	}
}

func TestDiskWireTransportIsPrivateToAdaptersAndServer(t *testing.T) {
	allowed := map[string]bool{"hangar/disk": true, "hangar/diskserver": true}
	scanned, matched := 0, 0
	err := filepath.WalkDir("hangar", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if name != "github.com/concourse/concourse/hangar/internal/disktransport" {
				continue
			}
			matched++
			if !allowed[filepath.ToSlash(filepath.Dir(path))] {
				t.Errorf("%s imports unrestricted disk wire transport", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 || matched == 0 {
		t.Fatal("disk transport import guard matched nothing")
	}
}

// Opening or initializing the disk itself -- the bbolt index and the object
// files -- is the store binary's alone. A second process that opened the same
// volume would be a second writer to an index that assumes exclusive ownership.
func TestOnlyTheStoreBinaryOwnsTheDisk(t *testing.T) {
	const diskPath = "github.com/concourse/concourse/hangar/disk"
	owners := map[string]bool{"cmd/hangar-store": true}
	scanned, owned := 0, 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", ".claude", "node_modules", "brine":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		scanned++
		alias := ""
		for _, imp := range file.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if name == diskPath {
				alias = "disk"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		if alias == "" {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok || ident.Name != alias || (selector.Sel.Name != "Open" && selector.Sel.Name != "Initialize") {
				return true
			}
			pkg := filepath.ToSlash(filepath.Dir(path))
			if owners[pkg] {
				owned++
				return true
			}
			t.Errorf("%s calls disk.%s; only %v may open or initialize the disk store", path, selector.Sel.Name, owners)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 || owned == 0 {
		t.Fatalf("disk ownership guard scanned %d files and found %d owner calls; it is vacuous", scanned, owned)
	}
}
