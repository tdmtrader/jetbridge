package steps

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Scan imports rather than source text: comments, test input strings, and
// identifiers mentioning "fake" are not clientset dependencies.
func kubernetesDoubleImports(filename string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly)
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

// Scan the entire module, including adapter commands outside steps/.
func TestTheModuleDoesNotImportFakeKubernetesClients(t *testing.T) {
	if _, err := os.Stat("../go.mod"); err != nil {
		t.Fatalf("guard must scan from the module root: %v", err)
	}
	files, outsideSteps := 0, 0
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
		if !strings.HasPrefix(path, "../steps/") {
			outsideSteps++
		}
		// The IMPORT rule holds for test files too, which is how it has always
		// been read in steps/ and there is no reason for the rest of the module
		// to be the exception: a suite that reaches for a fake clientset is
		// exactly the case it was written for.
		imports, err := kubernetesDoubleImports(path)
		if err != nil {
			return err
		}
		for _, imported := range imports {
			t.Errorf("%s imports Kubernetes test double %q; use the real API or live tier", path, imported)
		}
		// Reject locally declared client doubles in non-test sources.
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
	if files == 0 || outsideSteps == 0 {
		t.Fatalf("module guard scanned %d Go sources, %d outside steps; expected a nonempty module-wide scan",
			files, outsideSteps)
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
