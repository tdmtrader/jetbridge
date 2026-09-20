package steps

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Scan imports rather than source text: comments, test input strings, and
// identifiers mentioning "fake" are not clientset dependencies.
func kubernetesDoubleImports(filename string, source any) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filename, source, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var forbidden []string
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, err
		}
		double := path == "sigs.k8s.io/controller-runtime/pkg/client/fake" ||
			strings.HasPrefix(path, "sigs.k8s.io/controller-runtime/pkg/client/fake/")
		if strings.HasPrefix(path, "k8s.io/client-go/") {
			for _, part := range strings.Split(strings.TrimPrefix(path, "k8s.io/client-go/"), "/") {
				double = double || part == "fake" || part == "testing"
			}
		}
		if double {
			forbidden = append(forbidden, path)
		}
	}
	return forbidden, nil
}

// The guard reads THE WHOLE MODULE, not the package it happens to live in.
//
// It walked "." — steps/ — which is where its author was working, and said
// nothing about cmd/, features/ or live/. A rule enforced over one directory of
// a four-directory module is a rule a fake can be introduced beside: the adapter
// binary, the census tool and the live tier are all places a clientset gets
// built, and none of them was being read.
func TestTheModuleDoesNotImportFakeKubernetesClients(t *testing.T) {
	files := 0
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// A vendor tree or a build output is somebody else's source. The
			// root of the walk is spelled ".." and is emphatically not a hidden
			// directory: skipping it on that reading scanned zero files and
			// reported a clean module, which is the vacuity the count below
			// exists to catch.
			name := entry.Name()
			if path != ".." && (name == "vendor" || strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		files++
		// The IMPORT rule holds for test files too, which is how it has always
		// been read in steps/ and there is no reason for the rest of the module
		// to be the exception: a suite that reaches for a fake clientset is
		// exactly the case it was written for.
		imports, err := kubernetesDoubleImports(path, nil)
		if err != nil {
			return err
		}
		for _, imported := range imports {
			t.Errorf("%s imports Kubernetes test double %q; use the real API or live tier", path, imported)
		}
		// The NAME rule is for non-test files only. A spec about doubles talks
		// about doubles -- this very file has a table of the names it must
		// recognise -- and a guard that reported its own vocabulary would be
		// unusable.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		declarations, err := kubernetesDoubleDeclarations(path)
		if err != nil {
			return err
		}
		for _, declared := range declarations {
			t.Errorf("%s declares %s; a hand-written Kubernetes client double is the thing the "+
				"import rule forbids, spelled locally", path, declared)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The module has four source directories; a walk that found a handful of
	// files found one of them and stopped, which is the failure this spec was
	// rewritten for.
	if files < 100 {
		t.Fatalf("only %d Go sources scanned across the module; the Kubernetes client guard "+
			"is not reading what it claims to", files)
	}
}

// fakeKubernetesName is the shape of a hand-written double: FakeClientset,
// fakeClient, FakeDynamicClient and the rest, in any capitalisation.
//
// It is deliberately about CLIENTS. "fake" alone would catch a scenario's
// fixture data and a comment-shaped identifier; this catches the thing the
// import rule is about — something standing in for the API server — written
// out by hand instead of imported, which is the obvious way around a guard that
// only reads import paths.
var fakeKubernetesName = regexp.MustCompile(`(?i)fake(clientset|client|dynamic)`)

// kubernetesDoubleDeclarations reports type and function declarations whose
// names say they are a Kubernetes client double.
func kubernetesDoubleDeclarations(filename string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		return nil, err
	}

	var found []string
	for _, declaration := range file.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if fakeKubernetesName.MatchString(typed.Name.Name) {
				found = append(found, "func "+typed.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range typed.Specs {
				named, ok := spec.(*ast.TypeSpec)
				if ok && fakeKubernetesName.MatchString(named.Name.Name) {
					found = append(found, "type "+named.Name.Name)
				}
			}
		}
	}

	return found, nil
}

// The name guard recognises the doubles it is for, and leaves alone the words
// that merely contain "fake".
func TestKubernetesClientDeclarationGuard(t *testing.T) {
	for _, name := range []string{
		"fakeClientset", "FakeClientset", "newFakeClient", "FakeDynamicClient",
		"fakeclient", "recordingFakeClientSet",
	} {
		if !fakeKubernetesName.MatchString(name) {
			t.Errorf("%s would pass as a real client", name)
		}
	}
	for _, name := range []string{
		"fakeArtifactBytes", "FakePod", "clientFor", "DynamicScenario", "makeFakeTarball",
	} {
		if fakeKubernetesName.MatchString(name) {
			t.Errorf("%s is not a Kubernetes client double", name)
		}
	}

	declarations, err := kubernetesDoubleDeclarations("kubernetes_clients_test.go")
	if err != nil {
		t.Fatalf("parsing this file: %v", err)
	}
	if len(declarations) != 0 {
		t.Errorf("this file declares %v, which the module walk would report if it read "+
			"test files", declarations)
	}
}

func TestKubernetesClientImportGuard(t *testing.T) {
	for _, path := range []string{
		"k8s.io/client-go/kubernetes/fake",
		"k8s.io/client-go/dynamic/fake",
		"k8s.io/client-go/metadata/fake",
		"k8s.io/client-go/testing",
		"sigs.k8s.io/controller-runtime/pkg/client/fake",
	} {
		t.Run(path, func(t *testing.T) {
			for _, alias := range []string{"", "client ", "_ ", ". "} {
				imports, err := kubernetesDoubleImports("probe.go", "package probe\nimport "+alias+strconv.Quote(path))
				if err != nil || len(imports) != 1 || imports[0] != path {
					t.Fatalf("missed double import %s%s: %v, %v", alias, path, imports, err)
				}
			}
		})
	}
	for _, path := range []string{
		"k8s.io/client-go/kubernetes",
		"k8s.io/client-go/rest",
		"sigs.k8s.io/controller-runtime/pkg/envtest",
	} {
		imports, err := kubernetesDoubleImports("probe.go", "package probe\nimport "+strconv.Quote(path))
		if err != nil || len(imports) != 0 {
			t.Fatalf("rejected real API import %s: %v, %v", path, imports, err)
		}
	}
	imports, err := kubernetesDoubleImports("probe.go", "package probe\n// k8s.io/client-go/kubernetes/fake\nconst example = \"k8s.io/client-go/kubernetes/fake\"")
	if err != nil || len(imports) != 0 {
		t.Fatalf("mistook documentation or a test string for an import: %v, %v", imports, err)
	}
}
