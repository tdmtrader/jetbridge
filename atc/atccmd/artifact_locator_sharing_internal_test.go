package atccmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactLocatorIsCreatedOnceAndShared(t *testing.T) {
	cmd := &RunCommand{}

	first := cmd.artifactLocator()
	require.NotNil(t, first)
	require.Same(t, first, cmd.artifactLocator(), "the locator must be created once, not per call")
	require.Same(t, first, cmd.k8sArtifactLocator)
}

// The Reaper reads and reclaims the artifact locations the workers record, so
// both must hold the same *ArtifactLocator. That invariant used to be stated in
// a comment and broken by the very next line: constructPool captured the
// instance the workers write to, then `cmd.k8sArtifactLocator =
// NewArtifactLocator()` handed the Reaper a fresh empty one. The two components
// ran on disjoint maps, so DaemonSet artifact cleanup never ran and no locator
// entry was ever reclaimed.
//
// Assigning the field anywhere but the accessor can reintroduce exactly that,
// silently, so the assignment sites are pinned rather than the comment.
func TestArtifactLocatorFieldIsAssignedOnlyByItsAccessor(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "command.go", nil, 0)
	require.NoError(t, err)

	assignsField := func(expr ast.Expr) bool {
		selector, ok := expr.(*ast.SelectorExpr)
		return ok && selector.Sel != nil && selector.Sel.Name == "k8sArtifactLocator"
	}

	var offenders []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name == "artifactLocator" {
			continue
		}
		ast.Inspect(fn, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if assignsField(lhs) {
					offenders = append(offenders,
						fn.Name.Name+" at "+fset.Position(assign.Pos()).String())
				}
			}
			return true
		})
	}

	require.Emptyf(t, offenders,
		"k8sArtifactLocator must only be assigned by artifactLocator(); assigning it elsewhere "+
			"can hand the Reaper a different instance than the workers write to, which silently "+
			"disables DaemonSet artifact cleanup")
}
