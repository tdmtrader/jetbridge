package hangaroutput

// What this package is allowed to SAY.
//
// A capture's state is full of things that must not be repeated: a one-shot
// control warrant, a receipt-signing key id, an absolute hostPath, an object key,
// an opaque consumer reference. None of them is secret in the sense of a
// password, and all of them are things an operator's log aggregator, a metrics
// store or a support bundle should not accumulate.
//
// The rule here is structural rather than a value scan, and the difference is
// the point: "this log line did not contain a key" is true of every line that
// happened not to, and "this package cannot say more than these words" is true
// of all of them.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// permittedLogKeys is everything this package may put in a structured log.
//
// It is EMPTY. The one field this package ever logged -- the count of read
// leases a cleanup pass closed -- went with the read-lease protocol, and the
// package now says nothing structured at all: a directory, a scope, a digest, a
// warrant, a key id and an opaque consumer reference are each either an
// identity a consumer may treat as sensitive or unbounded, and nothing here has
// a bounded word left to say. A field added later is named here with its
// reason, or the guard fails.
var permittedLogKeys = map[string]string{}

func TestThisPackageSaysNothingItShouldNot(t *testing.T) {
	set := token.NewFileSet()
	files, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	found, scanned := 0, 0
	for _, pkg := range files {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				literal, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				selector, ok := literal.Type.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Data" {
					return true
				}

				for _, element := range literal.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := pair.Key.(*ast.BasicLit)
					if !ok || key.Kind != token.STRING {
						continue
					}
					found++
					word := strings.Trim(key.Value, `"`)
					if _, permitted := permittedLogKeys[word]; !permitted {
						t.Errorf("%s logs %q. This package may say %v and nothing else: a "+
							"warrant, a key id, a raw path, a scope, a digest or a consumer "+
							"reference in a log is an accumulation nobody audits. If %q is "+
							"genuinely bounded and safe, name it in permittedLogKeys with the "+
							"reason.", name, word, keysOf(permittedLogKeys), word)
					}
				}

				return true
			})
		}
	}

	// The guard proves it scanned the package rather than an empty directory:
	// a scan that parsed nothing would pass vacuously. "Nothing structured" is
	// the expectation, and it is only an expectation over files that were read.
	if scanned == 0 {
		t.Fatal("this guard parsed no file of this package; the rule would pass vacuously")
	}
	t.Logf("scanned %d files and inventoried %d structured log fields", scanned, found)
}

func keysOf(m map[string]string) []string {
	var keys []string
	for key := range m {
		keys = append(keys, key)
	}

	return keys
}
