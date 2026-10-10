package steps

// The guard that stops the class from regrowing.
//
// Fifty-three of these registrations released a daemon, a route, a namespace, a
// Postgres role or a lock with `rec.RegisterDisposer(func(){...})` -- outside
// the process-level set, so a signal or a panic left them running -- and
// fourteen of them ended in `panic(err)`, which the recorder's drain turns into
// a message nobody reads AND which skips every disposer registered before it.
// Converting them once fixes the fifty-three that exist. It does nothing about
// the fifty-fourth, and the whole point of TrackDisposer is that it is not
// optional, so the rule is read out of the source.
//
// The rule: a recorder registration may take a context cancellation and
// nothing else. A CancelFunc is already idempotent, releases no process and no
// bytes, and cannot fail, so there is nothing for the process drain to do with
// it and nothing for it to report. Everything else -- anything that stops,
// closes, kills or deletes -- goes through TrackDisposer, which registers the
// release twice and records its failure.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestGuardEveryRecorderDisposerReleasesOnlyAContext(t *testing.T) {
	sources := packageSourceFiles(t, ".")
	if len(sources) == 0 {
		t.Fatal("no non-test sources scanned; the disposer guard would pass vacuously")
	}

	registrations := 0
	for _, source := range sources {
		found, offenders := disposerRegistrations(t, source)
		registrations += found
		for _, offender := range offenders {
			t.Errorf("steps/%s registers %s directly on the recorder, so it is released only "+
				"on an ordinary scenario exit -- a signal or a panic leaves it running -- and "+
				"a failure to release it is discarded. Call TrackDisposer(rec, name, func() error) "+
				"instead, and return the error rather than panicking: a panicking disposer skips "+
				"every disposer registered before it.", source, offender)
		}
	}
	if registrations == 0 {
		t.Fatal("no recorder registrations found at all; the guard is measuring nothing")
	}
}

// disposerRegistrations parses one file and reports how many recorder
// registrations it makes and which of them release something a cancellation
// does not.
func disposerRegistrations(t *testing.T, source string) (int, []string) {
	t.Helper()

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, source, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", source, err)
	}

	found := 0
	var offenders []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "RegisterDisposer" || len(call.Args) != 1 {
			return true
		}
		found++
		// tracked_disposers.go IS the tracking, and TrackDisposer's own
		// registration is the one that cannot go through itself. The exemption
		// is the shape of the argument in that file and not the file alone, so
		// a daemon stop smuggled in beside it is still reported.
		if source == "tracked_disposers.go" && releasesATrackedEntry(call.Args[0]) {
			return true
		}
		if reason, ok := releasesSomethingOtherThanAContext(call.Args[0]); ok {
			offenders = append(offenders, fmt.Sprintf("%s (line %d)",
				reason, fileSet.Position(call.Args[0].Pos()).Line))
		}

		return true
	})

	return found, offenders
}

// releasesSomethingOtherThanAContext answers whether a registration's argument
// is anything but a context cancellation, and says what it is.
func releasesSomethingOtherThanAContext(argument ast.Expr) (string, bool) {
	if name, ok := calledName(argument); ok {
		if namesACancellation(name) {
			return "", false
		}
		return types.ExprString(argument), true
	}

	literal, ok := argument.(*ast.FuncLit)
	if !ok {
		return "a disposer this guard cannot read", true
	}
	if len(literal.Body.List) != 1 {
		return fmt.Sprintf("a func literal of %d statements", len(literal.Body.List)), true
	}
	statement, ok := literal.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return "a func literal whose body is not a call", true
	}
	inner, ok := statement.X.(*ast.CallExpr)
	if !ok {
		return "a func literal whose body is not a call", true
	}
	name, ok := calledName(inner.Fun)
	if !ok || !namesACancellation(name) || len(inner.Args) != 0 {
		if !ok {
			return "a func literal calling something this guard cannot read", true
		}
		return "a func literal calling " + types.ExprString(inner.Fun), true
	}

	return "", false
}

// releasesATrackedEntry answers whether an argument is TrackDisposer's own
// `func() { tracked.run(true) }`.
func releasesATrackedEntry(argument ast.Expr) bool {
	literal, ok := argument.(*ast.FuncLit)
	if !ok || len(literal.Body.List) != 1 {
		return false
	}
	statement, ok := literal.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := statement.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "run" {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)

	return ok && receiver.Name == "tracked"
}

// calledName is the trailing name of an identifier or a selector, which is what
// says whether a release cancels a context: `cancel`, `core.Cancel`,
// `cancelOwner`. A more complicated expression has no name to read.
func calledName(expr ast.Expr) (string, bool) {
	switch named := expr.(type) {
	case *ast.Ident:
		return named.Name, true
	case *ast.SelectorExpr:
		return named.Sel.Name, true
	}

	return "", false
}

func namesACancellation(name string) bool {
	return strings.HasSuffix(name, "Cancel") || strings.HasSuffix(name, "cancel")
}
