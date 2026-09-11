package hangar

import (
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
// and 20 MB; the store now lives in hangar/gcsstore, which only cmd/artifact-daemon
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

// AC 20's second clause: no output grant path reaches the daemon without a
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
// A caller-provided grant is not authority in either half: requirement 36 puts
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
			"A caller-provided grant is not authority: requirement 36 puts it on the " +
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
			case "vendor", ".git", "node_modules", "brine":
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
			"that decided locally would be a grant authorizing itself.",
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
