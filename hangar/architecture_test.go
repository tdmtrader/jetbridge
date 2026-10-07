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

// AC 20's second clause: no output warrant path reaches the daemon without a
// claim/read-lease check.
//
// The clause is about a PATH, and a path is hard to state as an import rule --
// which is exactly why the two previous rounds of the delete guard kept naming
// routes instead of capabilities. So this one is structural, over the two things
// that make the property true:
//
//  1. MaterializeManaged admits BEFORE it opens. A materialization that opened
//     first and asked afterwards would be reading under authority the control
//     plane may already have given away, and the bytes would be on disk by the
//     time it found out.
//  2. Exactly one production type implements OutputReadProfile, and it is the
//     one whose Admit is an HTTP round trip to the control plane's lease
//     endpoints. A second implementation is a second answer to "may I read
//     this", and the one that does not ask is the one somebody will wire.
//
// A caller-provided warrant is not authority in either half: requirement 36 puts
// the authority on the committed lease, and the lease is decided in one
// caller-owned transaction that revalidates the active claim, the registered
// exact lifecycle, a fresh stat proof, and the policy and reclaim exclusion.

// outputReadProfileMethods is the interface, stated here so a rename is a
// visible edit rather than a silently vacuous rule.
var outputReadProfileMethods = []string{"Admit", "Renew", "Release"}

func TestAManagedReadAdmitsBeforeItOpens(t *testing.T) {
	path := filepath.Join(repositoryRoot(), "hangar", "materializer_output.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var body *ast.FuncDecl
	ast.Inspect(file, func(node ast.Node) bool {
		declaration, ok := node.(*ast.FuncDecl)
		if ok && declaration.Name.Name == "MaterializeManaged" {
			body = declaration
		}

		return true
	})
	if body == nil {
		t.Fatal("MaterializeManaged was not found; this rule would pass vacuously")
	}

	// The ORDER of the two calls inside the one function, by source position.
	admit, open, release := -1, -1, -1
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "Admit":
			if admit == -1 {
				admit = int(call.Pos())
			}
		case "Materialize":
			if open == -1 {
				open = int(call.Pos())
			}
		case "Release":
			if release == -1 {
				release = int(call.Pos())
			}
		}

		return true
	})

	for name, position := range map[string]int{
		"profile.Admit": admit, "materializer.Materialize": open, "profile.Release": release,
	} {
		if position == -1 {
			t.Fatalf("MaterializeManaged never calls %s; this rule would pass vacuously", name)
		}
	}
	if admit > open {
		t.Error("MaterializeManaged opens the object BEFORE it admits the lease.\n\n" +
			"A caller-provided warrant is not authority: requirement 36 puts it on the " +
			"committed lease, and the daemon validates the exact lease is still active " +
			"before it opens anything. Asking afterwards means the bytes are on disk by the " +
			"time the answer arrives.")
	}
	if release < open {
		t.Error("MaterializeManaged releases the lease before it opens the object")
	}
}

func TestExactlyOneProductionTypeImplementsTheOutputReadProfile(t *testing.T) {
	root := repositoryRoot()

	// A type implements it when one file declares all three methods on the same
	// receiver. The scan is over non-test production files only: a fake in a
	// _test.go file is a fake, and the rule is about what a binary can link.
	implementers := map[string]map[string]bool{}
	scanned := 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return nil
		}
		scanned++

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || len(function.Recv.List) == 0 {
				continue
			}
			if !hasProfileShape(function) {
				continue
			}
			receiver := receiverTypeName(function.Recv.List[0].Type)
			if receiver == "" {
				continue
			}
			key := filepath.ToSlash(filepath.Dir(relative)) + "." + receiver
			if implementers[key] == nil {
				implementers[key] = map[string]bool{}
			}
			implementers[key][function.Name.Name] = true
		}

		return nil
	})
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if scanned < 500 {
		t.Fatalf("parsed only %d production Go files; the walk failed", scanned)
	}

	var complete []string
	for key, methods := range implementers {
		all := true
		for _, method := range outputReadProfileMethods {
			if !methods[method] {
				all = false
			}
		}
		if all {
			complete = append(complete, key)
		}
	}
	sort.Strings(complete)

	const only = "hangar/output.LeaseReadProfile"
	if len(complete) != 1 || complete[0] != only {
		t.Errorf("the production types implementing OutputReadProfile are %v, and there is "+
			"exactly one: %s.\n\nA second implementation is a second answer to \"may I read "+
			"this\", and the one that does not ask the control plane is the one somebody "+
			"will wire. %s's Admit is an HTTP round trip to the lease endpoints; a profile "+
			"that decided locally would be a warrant authorizing itself.",
			complete, only, only)
	}
}

// hasProfileShape recognises the three methods by NAME AND ARITY rather than by
// name alone, so an unrelated Release(ctx) somewhere does not count as half an
// implementation.
func hasProfileShape(function *ast.FuncDecl) bool {
	switch function.Name.Name {
	case "Admit":
		return countParams(function) == 4 && countResults(function) == 2
	case "Renew":
		return countParams(function) == 1 && countResults(function) == 1
	case "Release":
		return countParams(function) == 2 && countResults(function) == 1
	}

	return false
}

func countParams(function *ast.FuncDecl) int {
	total := 0
	if function.Type.Params == nil {
		return 0
	}
	for _, field := range function.Type.Params.List {
		if len(field.Names) == 0 {
			total++

			continue
		}
		total += len(field.Names)
	}

	return total
}

func countResults(function *ast.FuncDecl) int {
	if function.Type.Results == nil {
		return 0
	}
	total := 0
	for _, field := range function.Type.Results.List {
		if len(field.Names) == 0 {
			total++

			continue
		}
		total += len(field.Names)
	}

	return total
}

func receiverTypeName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.StarExpr:
		return receiverTypeName(typed.X)
	case *ast.Ident:
		return typed.Name
	}

	return ""
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
var deleteConstructorCallers = map[string]string{
	"cmd/hangar-output-reclaimer": "the output reclaimer, over the output namespace",
	"cmd/artifact-daemon/durable": "the fail-open cache tier, over the cache namespace only",
}

var deleteConstructors = map[string]string{
	"github.com/concourse/concourse/hangar/gcs":  "NewDeleteClient",
	"github.com/concourse/concourse/hangar/disk": "NewDeleteClient",
}

type goFile struct {
	pkg  string // slash-separated directory relative to the repository root
	path string
	file *ast.File
}

// deleteConstructorReferences returns "pkg: file" for every reference to a
// delete constructor, and the packages that made one.
func deleteConstructorReferences(files []goFile) (map[string][]string, int) {
	found := map[string][]string{}
	for _, f := range files {
		aliases := map[string]string{}
		for _, spec := range f.file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			name, ok := deleteConstructors[path]
			if !ok {
				continue
			}
			alias := filepath.Base(path)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			aliases[alias] = name
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
			if name, ok := aliases[ident.Name]; ok && selector.Sel.Name == name {
				found[f.pkg] = append(found[f.pkg], f.path)
			}
			return true
		})
	}
	return found, len(files)
}

func deleteConstructorProblems(found map[string][]string, scanned int, allowed map[string]string) []string {
	if scanned == 0 {
		return []string{"no Go file was scanned; this rule would pass vacuously"}
	}
	var problems []string
	for pkg, paths := range found {
		if _, ok := allowed[pkg]; ok {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s names a delete constructor (%s). Only %v may: "+
			"a binary that constructs a DeleteClient holds an exact delete over whatever namespace it "+
			"names.", pkg, strings.Join(paths, ", "), sortedKeys(allowed)))
	}
	for pkg := range allowed {
		if len(found[pkg]) == 0 {
			problems = append(problems, pkg+" is allowed to construct a delete client and does not; "+
				"the exemption is stale")
		}
	}
	sort.Strings(problems)
	return problems
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

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
			return nil
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
	for pkg, paths := range found {
		t.Logf("%s constructs a delete client in %v", pkg, paths)
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
	allowed := map[string]string{"cmd/reclaimer": "because"}

	if problems := deleteConstructorProblems(map[string][]string{}, 0, allowed); len(problems) == 0 {
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
		// The read/create client is not the capability.
		parse("cmd/daemon", `package main
import "github.com/concourse/concourse/hangar/gcs"
var _, _, _ = gcs.NewClient(nil, "")`),
	}
	found, scanned := deleteConstructorReferences(files)
	problems := deleteConstructorProblems(found, scanned, allowed)
	if len(problems) != 1 || !strings.Contains(problems[0], "cmd/web") {
		t.Fatalf("expected exactly cmd/web to be reported, got %v", problems)
	}

	stale := deleteConstructorProblems(map[string][]string{"cmd/reclaimer": {"x"}}, 1,
		map[string]string{"cmd/reclaimer": "because", "cmd/gone": "because"})
	if len(stale) != 1 || !strings.Contains(stale[0], "cmd/gone") {
		t.Fatalf("expected the stale exemption to be reported, got %v", stale)
	}
}
