package hangaroutput

// What this package is allowed to SAY.
//
// A capture's state is full of things that must not be repeated: a one-shot
// control grant, a receipt-signing key id, an absolute hostPath, an object key,
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
// A handoff id is here and an incarnation, a directory, a scope, a digest, a
// grant and a key id are not. The handoff is the one identity an operator needs
// to find a capture, it is opaque by construction, and Hangar attaches no
// meaning to it -- which is exactly the property that makes it safe to say.
var permittedLogKeys = map[string]string{
	"handoff":    "the one identity an operator needs to find a capture, and opaque by construction",
	"transition": "a member of a closed set",
	"class":      "a bounded word derived from the leaf's sentinels, never an error's own text",
	"closed": "a COUNT of read leases a recovery pass closed. It is an integer and it names " +
		"nobody: a log line naming the lease would put an opaque id a consumer may treat as " +
		"sensitive into a system nobody thinks of as a log, and a metric keyed by one would be " +
		"a series per reader forever",
}

func TestThisPackageSaysNothingItShouldNot(t *testing.T) {
	set := token.NewFileSet()
	files, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	found := 0
	for _, pkg := range files {
		for name, file := range pkg.Files {
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
							"grant, a key id, a raw path, a scope, a digest or a consumer "+
							"reference in a log is an accumulation nobody audits. If %q is "+
							"genuinely bounded and safe, name it in permittedLogKeys with the "+
							"reason.", name, word, keysOf(permittedLogKeys), word)
					}
				}

				return true
			})
		}
	}

	if found == 0 {
		t.Fatal("this package logs nothing structured at all; the rule would pass vacuously")
	}
	t.Logf("inventoried %d structured log fields", found)
}

func keysOf(m map[string]string) []string {
	var keys []string
	for key := range m {
		keys = append(keys, key)
	}

	return keys
}
