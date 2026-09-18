package steps

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
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

func TestStepsDoNotImportFakeKubernetesClients(t *testing.T) {
	files := 0
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		files++
		imports, err := kubernetesDoubleImports(path, nil)
		if err != nil {
			return err
		}
		for _, imported := range imports {
			t.Errorf("%s imports Kubernetes test double %q; use the real API or live tier", path, imported)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no Go sources scanned; the Kubernetes client guard would pass vacuously")
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
