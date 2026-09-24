package concourse

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Disk role credentials enforce authorization. These guards also keep storage
// ownership and the deletion constructor out of accidental command composition.
func TestDiskCapabilitiesStayInTheirOwningCommands(t *testing.T) {
	owners := map[string]string{
		"github.com/concourse/concourse/hangar/diskdelete": "./cmd/hangar-output-reclaimer",
		"github.com/concourse/concourse/hangar/disk":       "./cmd/hangar-store",
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
	allowed := map[string]bool{"hangar/diskclient": true, "hangar/diskdelete": true, "hangar/diskserver": true}
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
			if name == "github.com/concourse/concourse/hangar/internal/disktransport" {
				matched++
				if !allowed[filepath.ToSlash(filepath.Dir(path))] {
					t.Errorf("%s imports unrestricted disk wire transport", path)
				}
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
