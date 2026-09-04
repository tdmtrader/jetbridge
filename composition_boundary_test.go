package concourse

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The boundary this file defends is the other half of architecture_test.go's.
//
// That file bounds which packages the agentic layer may reach. This one
// asserts the two properties that make the reach worth bounding: that run
// admission has exactly one publisher, and that the port really is usable by a
// caller who cannot see atc/db.
//
// Both are source scans rather than import-graph checks, because both are
// claims about where a name appears rather than about what a package depends
// on. Each asserts it matched something, so neither can pass on an empty walk.

// walkGoFiles visits every non-test .go file in the tree, skipping dot
// directories and vendor.
func walkGoFiles(t *testing.T, visit func(relPath, contents string)) int {
	t.Helper()

	visited := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name != "." && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}

			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		visited++
		visit(filepath.ToSlash(path), string(contents))

		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if visited < 100 {
		t.Fatalf("the walk visited only %d non-test .go files; this repo has far more. "+
			"The scan failed and every assertion below would pass vacuously.", visited)
	}

	return visited
}

// TestRunAdmissionHasOneCallerOutsideTheDatabaseLayer is A11's sole-caller scan.
//
// CreateRunInTx is the seam. If a second package calls it directly, the port
// stops being the place authorization, template resolution and refusal
// translation happen, and the next caller gets whichever subset of those its
// author remembered -- which is how mcpserver's authorization drifted from the
// route it shadowed.
//
// It matches the call form ".CreateRunInTx(" rather than the bare identifier,
// deliberately. The port's package doc and its error comments name the seam
// they wrap, and a guard that goes red the moment someone explains what it
// guards is a guard people delete.
func TestRunAdmissionHasOneCallerOutsideTheDatabaseLayer(t *testing.T) {
	const callForm = ".CreateRunInTx("

	var (
		callers    []string
		totalSites int
	)

	walkGoFiles(t, func(path, contents string) {
		count := strings.Count(contents, callForm)
		if count == 0 {
			return
		}
		totalSites += count

		// atc/db is the seam's own home: the factory declares it and
		// CreateRun calls it. Those are not the couplings this guards.
		if strings.HasPrefix(path, "atc/db/") {
			return
		}
		callers = append(callers, path)
	})

	if totalSites == 0 {
		t.Fatalf("the scan found no %q call site anywhere, including inside atc/db. "+
			"Either the seam was renamed or the scan is broken; either way this "+
			"assertion is no longer checking anything.", callForm)
	}

	sort.Strings(callers)
	want := []string{"atc/runs/admitter.go"}
	if len(callers) != len(want) || (len(callers) == 1 && callers[0] != want[0]) {
		t.Errorf("run admission should have exactly one caller outside atc/db, and it "+
			"should be the port.\n  want: %v\n  got:  %v\n"+
			"A second direct caller re-implements authorization, template resolution "+
			"and refusal translation, or silently skips them. Go through atc/runs.",
			want, callers)
	}
}

// TestThePortIsUsableWithoutTheDatabaseLayer is A10's structural half.
//
// The spec's consumer proves the port is reachable without atc/db by
// compiling and passing. This asserts the premise that makes that proof mean
// anything: that the file really does not import atc/db. Without it, someone
// adds one import to make a spec easier and the demonstration quietly stops
// demonstrating.
func TestThePortIsUsableWithoutTheDatabaseLayer(t *testing.T) {
	const (
		consumer  = "atc/runs/db_free_consumer_test.go"
		forbidden = "github.com/concourse/concourse/atc/db"
	)

	file, err := parser.ParseFile(token.NewFileSet(), consumer, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v\n"+
			"This file is A10's consumer. If it moved, move this assertion with it "+
			"rather than deleting it.", consumer, err)
	}
	if len(file.Imports) == 0 {
		t.Fatalf("%s declares no imports at all, so this scan proves nothing. "+
			"It should at least import atc/runs.", consumer)
	}

	sawPort := false
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("unquoting import %s in %s: %v", imported.Path.Value, consumer, err)
		}
		if path == forbidden || strings.HasPrefix(path, forbidden+"/") {
			t.Errorf("%s imports %s.\n"+
				"That file exists to be a consumer the port must serve without it. "+
				"An import here does not make a spec easier -- it deletes the "+
				"demonstration.", consumer, path)
		}
		if path == "github.com/concourse/concourse/atc/runs" {
			sawPort = true
		}
	}

	if !sawPort {
		t.Errorf("%s does not import the port at all, so it is not exercising it.", consumer)
	}
}
