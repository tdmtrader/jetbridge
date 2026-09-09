package jetbridge

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The live tier runs against the *deployed* cluster -- the same theborg
// namespace that serves concourse.home -- under a namespaced service account.
// A live test that reaches for a cluster-scoped object is therefore wrong twice
// over: it cannot work (the account has no such permission) and, if it ever did
// work, it would mutate shared production state. That is not hypothetical:
// 269c4bf239 had to move the Hangar generated-Pod contract off the `live` tag
// because it relabels a node to schedule its Pod.
//
// The Mac cannot see any of this. `live` is skipped on darwin, so the mistake
// surfaces only when CI runs the live tier -- which, on this pipeline, is at
// the very end. So the check is static and it lives in the plain unit tier:
// this file carries no build tag on purpose, and `go test ./atc/worker/jetbridge/`
// with no tags at all is enough to catch a live test that goes cluster-scope.
//
// It parses; it does not run. A live suite that grew a cluster-scope call would
// otherwise compile perfectly well in build-and-vet's `go vet -tags live`.

// liveGuardTags models the tag set the live tier compiles with. `live` is the
// tag the pipeline passes; the rest describe the CI machine, so that a
// constraint like `live && linux` is evaluated the way CI would evaluate it and
// not skipped as unsatisfiable.
var liveGuardTags = map[string]bool{
	"live":  true,
	"linux": true,
	"unix":  true,
	"amd64": true,
	"arm64": true,
}

// clusterScopeGroups are client-go accessors that return a client for a
// cluster-scoped resource. Calling one from a namespaced service account fails;
// calling one successfully means the test is mutating something shared.
var clusterScopeGroups = map[string]bool{
	"Nodes":                           true,
	"PersistentVolumes":               true,
	"StorageClasses":                  true,
	"ClusterRoles":                    true,
	"ClusterRoleBindings":             true,
	"CustomResourceDefinitions":       true,
	"Namespaces":                      true,
	"PriorityClasses":                 true,
	"ValidatingWebhookConfigurations": true,
	"MutatingWebhookConfigurations":   true,
}

// Reading a namespace is the one cluster-scope operation a live test may
// legitimately need: the suite is handed a namespace name by configuration and
// wants to confirm it exists before it starts creating Pods in it. That is a
// read of an object the deployed cluster already has, it changes nothing, and
// the alternative -- failing deep inside a Pod create with a NotFound -- is
// strictly worse diagnostics. Creating, deleting or listing namespaces is a
// different act and stays banned.
var clusterScopeReadOnlyAllowed = map[string]map[string]bool{
	"Namespaces": {"Get": true},
}

// liveGuardFileFloor is a non-vacuity floor, not a count. There are 10 files
// under `//go:build live` in this package today. If a refactor drops the scan
// below this, the guard has stopped guarding and should fail loudly rather than
// pass over an empty set.
const liveGuardFileFloor = 8

func TestLiveTestsStayNamespaceScoped(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	fset := token.NewFileSet()
	var scanned []string
	var findings []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, "_test.go") {
			continue
		}

		expr, err := goBuildConstraint(name)
		if err != nil {
			t.Fatalf("reading build constraint of %s: %v", name, err)
		}
		if expr == nil {
			// No constraint means the file is in the plain unit tier, which
			// runs nowhere near a cluster. Not this guard's business.
			continue
		}
		if !expr.Eval(func(tag string) bool { return liveGuardTags[tag] }) {
			continue
		}

		scanned = append(scanned, name)

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		findings = append(findings, clusterScopeCalls(fset, file)...)
	}

	sort.Strings(scanned)

	if len(scanned) < liveGuardFileFloor {
		t.Fatalf("scanned only %d live-tagged files (%v); expected at least %d. "+
			"Either the live suite shrank drastically or the constraint evaluation "+
			"broke -- an empty scan must not read as a pass.",
			len(scanned), scanned, liveGuardFileFloor)
	}
	t.Logf("scanned %d live-tagged files: %v", len(scanned), scanned)

	if len(findings) > 0 {
		sort.Strings(findings)
		t.Errorf("live-tagged tests reach cluster-scoped Kubernetes APIs:\n  %s\n\n"+
			"The live tier runs against the deployed cluster under a namespaced "+
			"service account. Move the test behind its own tag (see "+
			"live_hangar_flow_test.go and hangar_live) or rewrite it to stay "+
			"inside its namespace.",
			strings.Join(findings, "\n  "))
	}
}

// goBuildConstraint returns the file's //go:build expression, or nil if it has
// none. Only the header is read: a //go:build line must precede the package
// clause, so anything after it is a comment about something else.
func goBuildConstraint(path string) (constraint.Expr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "package ") {
			return nil, nil
		}
		if !constraint.IsGoBuild(line) {
			continue
		}
		return constraint.Parse(line)
	}

	return nil, scanner.Err()
}

// clusterScopeCalls reports every call that selects a cluster-scoped resource
// client, except the reads explicitly allowed above.
//
// Two shapes matter. The common one is a chained call --
// `client.CoreV1().Nodes().List(ctx, ...)` -- where the operation is legible
// from the AST and can be allowed or refused on its own merits. The other is a
// client held in a variable (`nodes := client.CoreV1().Nodes()`), where the
// operation is out of reach; those are always reported, since a test that keeps
// a cluster-scope client around is doing more than one read with it.
func clusterScopeCalls(fset *token.FileSet, file *ast.File) []string {
	// Selector calls whose receiver is a cluster-scope accessor, e.g. the
	// `.Get` in `Namespaces().Get(...)`. Keyed by the accessor call so the
	// second pass can tell an allowed read from a bare accessor.
	operationOn := map[ast.Node]string{}

	ast.Inspect(file, func(n ast.Node) bool {
		outer, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := outer.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		if groupName(inner) == "" {
			return true
		}
		operationOn[inner] = sel.Sel.Name
		return true
	})

	var findings []string

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		group := groupName(call)
		if group == "" {
			return true
		}

		op, chained := operationOn[call]
		if chained && clusterScopeReadOnlyAllowed[group][op] {
			return true
		}

		where := fset.Position(call.Pos())
		if chained {
			findings = append(findings, fmt.Sprintf("%s:%d: %s().%s(...)",
				filepath.Base(where.Filename), where.Line, group, op))
		} else {
			findings = append(findings, fmt.Sprintf("%s:%d: %s()",
				filepath.Base(where.Filename), where.Line, group))
		}
		return true
	})

	return findings
}

// groupName returns the cluster-scope accessor a call selects, or "".
//
// The match is on the method name alone -- there is no type information here,
// and loading one would make this guard slower than the suite it protects. The
// names are specific enough that a false positive would have to be a
// zero-argument method called Nodes() or PersistentVolumes() on something that
// is not a Kubernetes client, which is a name worth a second look anyway.
func groupName(call *ast.CallExpr) string {
	if len(call.Args) != 0 {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if !clusterScopeGroups[sel.Sel.Name] {
		return ""
	}
	return sel.Sel.Name
}
