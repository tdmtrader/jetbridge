package hangaroutput

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/concourse/concourse/hangar/output"
)

// Every retry offers the same identity, and different branches offer different
// ones.
//
// The first half is the whole reason these are derived: a fresh id on a retry
// after a lost answer is a different Stage 2 wearing an old idempotency key, or
// a second release of one source, and both turn a recoverable ambiguity into a
// handoff that can never complete.
//
// The second half is why the branch is in the derivation: three branches
// colliding on one release intent would let a no_capture release be
// acknowledged as a cancellation's.
func TestEveryFingerprintIsStableAndBranchDistinct(t *testing.T) {
	first := output.HandoffID("11111111-1111-4111-8111-111111111111")
	second := output.HandoffID("22222222-2222-4222-8222-222222222222")

	if checkpointFor(first) != checkpointFor(first) {
		t.Error("two derivations of one handoff's checkpoint differ")
	}
	if checkpointFor(first) == checkpointFor(second) {
		t.Error("two handoffs derive one checkpoint")
	}

	seen := map[output.ReleaseIntentID]output.Disposition{}
	for _, branch := range output.Dispositions() {
		intent := releaseIntentFor(first, branch)
		if intent != releaseIntentFor(first, branch) {
			t.Errorf("two derivations of %s's release intent differ", branch)
		}
		if err := intent.Validate(); err != nil {
			t.Errorf("%s derives an unusable intent id: %v", branch, err)
		}
		if other, clash := seen[intent]; clash {
			t.Errorf("%s and %s derive one release intent", branch, other)
		}
		seen[intent] = branch

		if releaseIntentFor(second, branch) == intent {
			t.Errorf("two handoffs derive one %s release intent", branch)
		}
	}
}

// Nothing outside identity.go mints one.
//
// A fingerprint minted at a call site is invisible until the day a retry after
// a lost answer conflicts forever, and no test over the happy path can see it.
// So the rule is structural: `uuid.New` belongs to the one file whose whole
// subject is why it must not be used for these, and to the process's own owner
// id, which is not a fingerprint -- it names a process, and a new process
// SHOULD get a new one.
func TestNoCallSiteMintsAnIdempotencyFingerprint(t *testing.T) {
	set := token.NewFileSet()
	files, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}

	scanned := 0
	for _, pkg := range files {
		for name, file := range pkg.Files {
			scanned++
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkgName, ok := selector.X.(*ast.Ident)
				if !ok || pkgName.Name != "uuid" {
					return true
				}
				if strings.HasPrefix(selector.Sel.Name, "New") &&
					!strings.HasSuffix(name, "identity.go") {
					t.Errorf("%s calls uuid.%s. Every idempotency fingerprint in this plane is "+
						"DERIVED in identity.go, because a fresh id on a retry after a lost "+
						"answer conflicts forever; if this one names a process rather than a "+
						"fact, say so there.", name, selector.Sel.Name)
				}

				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("no production file was scanned; this rule would pass vacuously")
	}
	t.Logf("scanned %d production files", scanned)
}
