package concourse

import (
	"go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A cache or build artefact checked in by accident is never noticed in review:
// hack/__pycache__/*.pyc rode along for weeks. .gitignore keeps new ones out of
// `git add .`, but not out of `git add -f` or a file tracked before the ignore
// existed, so check what is tracked.
//
// In a checkout that means asking git. CI runs this against a `git archive` of
// the ref (hack/ci-check.sh, the pipeline's unit-tests task), which has no .git
// but contains exactly the tracked files, so there the tree itself is the list.
func TestNoBuildArtefactOrCacheIsTracked(t *testing.T) {
	files := trackedFiles(t)
	if len(files) < 1000 {
		t.Fatalf("only %d tracked files listed; the listing failed and this rule would pass vacuously", len(files))
	}

	for _, f := range files {
		segments := strings.Split(f, "/")
		if strings.HasSuffix(f, ".pyc") ||
			slices.Contains(segments, "__pycache__") ||
			slices.Contains(segments, "elm-stuff") ||
			slices.Contains(segments, "node_modules") {
			t.Errorf("%s is tracked; it is a cache or build artefact. `git rm --cached` it.", f)
		}
	}
}

// trackedFiles lists the repository's tracked files: `git ls-files` when the
// root is a checkout (.git is a directory, or a file in a worktree), otherwise
// a walk of the archive. The walk skips node_modules and elm-stuff, which a
// task may install or build into the archive before the tests run; a tracked
// copy of either is still caught by every local run.
func trackedFiles(t *testing.T) []string {
	t.Helper()

	if _, err := os.Stat(".git"); err == nil {
		out, err := exec.Command("git", "ls-files").Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Fatalf("git ls-files failed: %v\n%s", err, ee.Stderr)
			}
			t.Fatalf("git ls-files failed: %v", err)
		}
		return strings.Split(strings.TrimSpace(string(out)), "\n")
	}

	var files []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "elm-stuff":
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the archive: %v", err)
	}
	return files
}

// atc/imageresolver turns an image reference into a pinned digest for the web,
// the engine, lidar and the Kubernetes runtime alike. It can serve all of them
// only while it depends on none of them: one import of atc/db or a runtime
// package and the resolver joins whichever cycle that package is in. Today it
// imports `atc` (the shared types) and nothing else in the module.
func TestTheImageResolverStaysALeaf(t *testing.T) {
	const resolver = "atc/imageresolver"
	allowed := map[string]bool{"atc": true}

	graph := loadImportGraph(t)
	imports, ok := graph.prod[resolver]
	if !ok {
		t.Fatalf("%s is not in the import graph; this rule would pass vacuously. "+
			"If it moved, move the rule with it.", resolver)
	}

	for _, imp := range imports {
		if !allowed[imp] {
			t.Errorf("%s imports %s. The resolver is a leaf every layer imports; "+
				"pass what it needs in instead of importing it.", resolver, imp)
		}
	}
	for imp := range allowed {
		if !slices.Contains(imports, imp) {
			t.Errorf("%s no longer imports %s; drop it from the allowed set so the rule "+
				"keeps describing the package.", resolver, imp)
		}
	}
}

// Counterfeiter's generated fakes were removed with tools.go and the module
// requirement; the tree's doubles are real servers and real Postgres now. A
// go:generate line or a tools import would bring the generator back quietly,
// so ask the build graph, tests included, whether anything links it.
func TestNothingDependsOnCounterfeiter(t *testing.T) {
	out, err := exec.Command("go", "list", "-e", "-deps", "-test", "./...").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list -deps -test failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list -deps -test failed: %v", err)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(deps) < 500 {
		t.Fatalf("go list -deps -test listed only %d packages; the listing failed and "+
			"this rule would pass vacuously", len(deps))
	}

	for _, dep := range deps {
		if strings.Contains(dep, "counterfeiter") {
			t.Errorf("%s is in the build graph. Counterfeiter fakes were removed; "+
				"use a real server or real Postgres instead.", dep)
		}
	}
}

// A lager.Logger built mid-wiring has no sink, or a sink of its own at a fixed
// level: its lines vanish (the migrator's did) or ignore --log-level and the
// atc.* session chain (the daemon client's did). A logger is made once, at a
// composition root, and passed down. This rule pins that: outside package
// main and test files, nothing calls lager.NewLogger. Nested modules (the
// brine suite) are other modules and are not scanned.
//
// newLoggerExceptions are test-support packages that build a whole web for a
// spec and so are its composition root.
var newLoggerExceptions = map[string]string{
	"atc/atctest/atctest.go": "the in-process ATC platform for integration specs is its own composition root",
}

func TestOnlyCompositionRootsBuildALagerLogger(t *testing.T) {
	var offenders []string
	inMain := 0
	for _, source := range productionGoFiles(t, func(string) bool { return true }) {
		lagerName := ""
		for _, spec := range source.file.Imports {
			if path, _ := strconv.Unquote(spec.Path.Value); path == "code.cloudfoundry.org/lager/v3" {
				lagerName = "lager"
				if spec.Name != nil {
					lagerName = spec.Name.Name
				}
			}
		}
		if lagerName == "" {
			continue
		}
		ast.Inspect(source.file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "NewLogger" {
				return true
			}
			if pkg, ok := selector.X.(*ast.Ident); !ok || pkg.Name != lagerName {
				return true
			}
			switch {
			case source.file.Name.Name == "main":
				inMain++
			case newLoggerExceptions[source.rel] != "":
			default:
				offenders = append(offenders, source.rel)
			}
			return true
		})
	}
	// cmd/artifact-daemon/main.go builds one: a scan that finds none in a
	// main package has stopped recognising the call and would pass vacuously.
	if inMain == 0 {
		t.Fatal("found no lager.NewLogger in any main package; the scan is not recognising the call")
	}
	if len(offenders) != 0 {
		t.Fatalf("lager.NewLogger outside a composition root: %v\n\nTake the caller's "+
			"logger (logger.Session(...)) instead; a logger built here has no sink or "+
			"ignores --log-level.", offenders)
	}
}
