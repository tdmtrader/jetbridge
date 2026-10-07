package hangar

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repositoryRoot is this package's parent, found from this file rather than
// from the working directory: `go test ./...` runs each package in its own
// directory, and a relative walk would scan a different tree depending on
// where it was invoked from.
func repositoryRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)

	return filepath.Dir(filepath.Dir(thisFile))
}

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
// and 20 MB; the GCS adapter now lives in hangar/gcs, which only daemon-side binaries
// import. This asserts the shape that keeps it that way: hangar itself is a
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

// THE DELETE CONSTRUCTORS HAVE A FIXED SET OF CALLERS.
//
// Deletion is objectstore.DeleteClient, and the only constructors of one over a
// real backend are hangar/gcs.NewDeleteClient and hangar/disk.NewDeleteClient.
// They live beside the read/create clients rather than in packages of their
// own, so "which binary links the package" no longer says who can delete --
// every storage-facing binary links both packages. What says it is who names
// the constructor, and that is what this scans: every non-test Go file in the
// module, for any reference to either constructor through its import, called
// or not (a function value handed elsewhere is the same capability).
//
// Two callers, each with the namespace it may delete in:
//
//   - the output reclaimer, over the output namespace, after the control plane
//     admitted the exact generation;
//   - the artifact daemon's fail-open cache tier (cmd/artifact-daemon/durable),
//     over the cache namespace only. The cache is re-derivable and the daemon
//     expires its own objects. It never holds delete over input or output:
//     the daemon refuses to start with the cache namespace equal to either
//     (objectstore.Namespaces), and the disk store authorizes the cache role
//     in the cache namespace alone.
//
// Credentials remain the real boundary (IAM, the disk store's role table); this
// keeps the composition from handing the capability to a third place by
// accident.
// deleteConstructors are the functions whose call yields a delete capability:
// the two backend constructors, and the cache tier's own constructors, which
// build (Open) or accept (New) a delete client for the cache namespace.
var deleteConstructors = map[string][]string{
	"github.com/concourse/concourse/hangar/gcs":                  {"NewDeleteClient"},
	"github.com/concourse/concourse/hangar/disk":                 {"NewDeleteClient"},
	"github.com/concourse/concourse/cmd/artifact-daemon/durable": {"Open", "New"},
}

// deleteConstructorCallers are the only production packages that may name a
// delete constructor, and which ones each may name.
var deleteConstructorCallers = map[string]allowedCaller{
	"cmd/hangar-output-reclaimer": {
		why: "the output reclaimer, over the output namespace",
		may: []string{"hangar/gcs.NewDeleteClient", "hangar/disk.NewDeleteClient"},
	},
	"cmd/artifact-daemon/durable": {
		why: "the fail-open cache tier, over the cache namespace only",
		may: []string{"hangar/gcs.NewDeleteClient", "hangar/disk.NewDeleteClient"},
	},
	"cmd/artifact-daemon": {
		why: "the daemon opens its cache tier, whose bucket it has checked is neither input nor output",
		may: []string{"cmd/artifact-daemon/durable.Open"},
	},
}

type allowedCaller struct {
	why string
	may []string // "<module-relative package>.<function>"
}

type goFile struct {
	pkg  string // slash-separated directory relative to the repository root
	path string
	file *ast.File
}

const modulePrefix = "github.com/concourse/concourse/"

// deleteConstructorReferences returns, per package, every delete constructor it
// names ("<module-relative package>.<function>" -> files), called or not.
func deleteConstructorReferences(files []goFile) (map[string]map[string][]string, int) {
	found := map[string]map[string][]string{}
	for _, f := range files {
		aliases := map[string]string{} // alias -> import path
		for _, spec := range f.file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if _, ok := deleteConstructors[path]; !ok {
				continue
			}
			alias := filepath.Base(path)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			aliases[alias] = path
		}
		if len(aliases) == 0 {
			continue
		}
		ast.Inspect(f.file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, ok := aliases[ident.Name]
			if !ok || !slices.Contains(deleteConstructors[path], selector.Sel.Name) {
				return true
			}
			name := strings.TrimPrefix(path, modulePrefix) + "." + selector.Sel.Name
			if found[f.pkg] == nil {
				found[f.pkg] = map[string][]string{}
			}
			found[f.pkg][name] = append(found[f.pkg][name], f.path)
			return true
		})
	}
	return found, len(files)
}

func deleteConstructorProblems(found map[string]map[string][]string, scanned int, allowed map[string]allowedCaller) []string {
	if scanned == 0 {
		return []string{"no Go file was scanned; this rule would pass vacuously"}
	}
	var problems []string
	for pkg, names := range found {
		for name, paths := range names {
			if slices.Contains(allowed[pkg].may, name) {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s names the delete constructor %s (%s). "+
				"A package that constructs a DeleteClient holds an exact delete over whatever "+
				"namespace it names; only the callers in deleteConstructorCallers may, and only "+
				"the constructors listed for each.", pkg, name, strings.Join(paths, ", ")))
		}
	}
	for pkg, caller := range allowed {
		for _, name := range caller.may {
			if len(found[pkg][name]) == 0 {
				problems = append(problems, pkg+" is allowed to name "+name+" and does not; "+
					"the exemption is stale")
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// productionFiles parses every non-test Go file in the module. A file that
// does not parse FAILS the scan: skipping it would be a guard that goes quiet
// over exactly the file it cannot read.
func productionFiles(t *testing.T) []goFile {
	t.Helper()
	root := repositoryRoot()
	var files []goFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", ".git", ".claude", "node_modules", "brine", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, goFile{pkg: filepath.ToSlash(filepath.Dir(relative)), path: filepath.ToSlash(relative), file: file})
		return nil
	})
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	return files
}

func TestOnlyTheReclaimerAndTheCacheTierConstructADeleteClient(t *testing.T) {
	files := productionFiles(t)
	if len(files) < 500 {
		t.Fatalf("parsed only %d production Go files; the walk failed", len(files))
	}
	found, scanned := deleteConstructorReferences(files)
	for pkg, names := range found {
		for name, paths := range names {
			t.Logf("%s names %s in %v", pkg, name, paths)
		}
	}
	for _, problem := range deleteConstructorProblems(found, scanned, deleteConstructorCallers) {
		t.Error(problem)
	}
}

func TestTheDeleteConstructorGuardIsNotVacuous(t *testing.T) {
	parse := func(pkg, source string) goFile {
		file, err := parser.ParseFile(token.NewFileSet(), pkg+"/f.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		return goFile{pkg: pkg, path: pkg + "/f.go", file: file}
	}
	allowed := map[string]allowedCaller{
		"cmd/reclaimer": {why: "because", may: []string{"hangar/gcs.NewDeleteClient"}},
		"cmd/daemon":    {why: "because", may: []string{"cmd/artifact-daemon/durable.Open"}},
	}

	if problems := deleteConstructorProblems(map[string]map[string][]string{}, 0, allowed); len(problems) == 0 {
		t.Fatal("an empty scan passed")
	}

	files := []goFile{
		parse("cmd/reclaimer", `package main
import "github.com/concourse/concourse/hangar/gcs"
var _, _, _ = gcs.NewDeleteClient(nil, "")`),
		// An aliased import, and a function value rather than a call.
		parse("cmd/web", `package main
import store "github.com/concourse/concourse/hangar/disk"
var open = store.NewDeleteClient`),
		// The daemon may open its cache tier, and that does not license it to
		// build a backend delete client directly.
		parse("cmd/daemon", `package main
import (
	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
	"github.com/concourse/concourse/hangar/gcs"
)
var _, _, _ = durable.Open(nil, durable.Config{})
var _, _, _ = gcs.NewDeleteClient(nil, "")
var _, _, _ = gcs.NewClient(nil, "")`),
		// The cache tier's other constructor is guarded too.
		parse("cmd/other", `package main
import "github.com/concourse/concourse/cmd/artifact-daemon/durable"
var _ = durable.New(nil, nil, "b", 0)`),
	}
	found, scanned := deleteConstructorReferences(files)
	problems := deleteConstructorProblems(found, scanned, allowed)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"cmd/web names", "cmd/daemon names the delete constructor hangar/gcs.NewDeleteClient", "cmd/other names"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected a problem containing %q, got:\n%s", want, joined)
		}
	}
	if len(problems) != 3 {
		t.Fatalf("expected exactly three problems, got %v", problems)
	}

	stale := deleteConstructorProblems(map[string]map[string][]string{"cmd/reclaimer": {"hangar/gcs.NewDeleteClient": {"x"}}}, 1,
		map[string]allowedCaller{
			"cmd/reclaimer": {may: []string{"hangar/gcs.NewDeleteClient"}},
			"cmd/gone":      {may: []string{"hangar/gcs.NewDeleteClient"}},
		})
	if len(stale) != 1 || !strings.Contains(stale[0], "cmd/gone") {
		t.Fatalf("expected the stale exemption to be reported, got %v", stale)
	}
}
