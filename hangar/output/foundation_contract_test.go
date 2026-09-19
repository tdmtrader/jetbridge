package output

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This file is the dependency manifest for the reviewed Hangar strict-input
// foundation that this track extends.
//
// None of TreeRef, Store, Canonicalizer, WarrantSigner or Materializer carries a
// version field, so there is no number to compare and "the accepted foundation"
// cannot be asserted by asking the code what version it is. It is asserted by
// three concrete checks instead:
//
//	(a) a golden of the exported symbol set and signatures of package hangar,
//	    so an incompatible foundation change fails here — in one file, with the
//	    diff in front of the reader — rather than at whichever call site Phase 2
//	    happens to add first;
//	(b) the integration commit pinned in the track's verification report,
//	    asserted as an ancestor of HEAD; and
//	(c) the presence of the daemon composition, the runtime input seam and the
//	    strict-input chart capability.
//
// A failure here is a diagnosis, not an invitation to copy the missing piece
// into this track. The foundation is a prerequisite; this package extends it.
//
// Reqs 19, 24, 37.

// integrationCommit is the commit this track pins, recorded in the track's
// verification report and in plan.md ("the integration commit this track pins
// is f2a42f9988, the current core").
const integrationCommit = "f2a42f9988"

// hangarSymbolGolden is the checked-in exported surface of package hangar.
// Regenerate deliberately, never reflexively: a diff here means the foundation
// this track builds on changed shape.
const hangarSymbolGolden = "testdata/hangar-exported-symbols.golden"

// repoRoot walks up from the test's working directory to the module root. It
// does not shell out to git, because clause (c) must still work in a source
// tree extracted without history — only clause (b) needs a repository.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s; cannot locate the module root", dir)
		}
		dir = parent
	}
}

// exportedSurface renders the exported declarations of the Go package in dir,
// one per line, sorted.
//
// Unexported fields and methods are deliberately dropped: this golden exists to
// notice a change to what callers can depend on, and would otherwise churn on
// every internal refactor until nobody read it.
func exportedSurface(t *testing.T, dir string) []string {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}

	var lines []string
	render := func(node ast.Node) string {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, node); err != nil {
			t.Fatalf("rendering a declaration in %s: %v", dir, err)
		}

		return strings.Join(strings.Fields(buf.String()), " ")
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					recv := ""
					if d.Recv != nil && len(d.Recv.List) > 0 {
						base := d.Recv.List[0].Type
						if star, ok := base.(*ast.StarExpr); ok {
							base = star.X
						}
						ident, ok := base.(*ast.Ident)
						if !ok || !ident.IsExported() {
							continue
						}
						recv = "(" + render(d.Recv.List[0].Type) + ") "
					}
					lines = append(lines, "func "+recv+d.Name.Name+render(d.Type)[len("func"):])
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						switch s := spec.(type) {
						case *ast.TypeSpec:
							if !s.Name.IsExported() {
								continue
							}
							lines = append(lines, "type "+s.Name.Name+" "+render(exportedOnly(s.Type)))
						case *ast.ValueSpec:
							for _, name := range s.Names {
								if !name.IsExported() {
									continue
								}
								kind := "var"
								if d.Tok == token.CONST {
									kind = "const"
								}
								typed := ""
								if s.Type != nil {
									typed = " " + render(s.Type)
								}
								lines = append(lines, kind+" "+name.Name+typed)
							}
						}
					}
				}
			}
		}
	}

	sort.Strings(lines)

	return lines
}

// exportedOnly strips unexported struct fields and interface methods so the
// golden describes the surface rather than the implementation.
func exportedOnly(expr ast.Expr) ast.Expr {
	switch t := expr.(type) {
	case *ast.StructType:
		return &ast.StructType{Fields: exportedFields(t.Fields), Incomplete: t.Incomplete}
	case *ast.InterfaceType:
		return &ast.InterfaceType{Methods: exportedFields(t.Methods), Incomplete: t.Incomplete}
	default:
		return expr
	}
}

func exportedFields(list *ast.FieldList) *ast.FieldList {
	if list == nil {
		return nil
	}
	kept := &ast.FieldList{}
	for _, field := range list.List {
		if len(field.Names) == 0 {
			// Embedded field or an embedded interface: keep it, it is part of
			// the surface.
			kept.List = append(kept.List, &ast.Field{Type: field.Type})
			continue
		}
		var names []*ast.Ident
		for _, name := range field.Names {
			if name.IsExported() {
				names = append(names, ast.NewIdent(name.Name))
			}
		}
		if len(names) == 0 {
			continue
		}
		kept.List = append(kept.List, &ast.Field{Names: names, Type: field.Type})
	}

	return kept
}

// TestFoundationExportedSurfaceMatchesTheGolden is clause (a).
func TestFoundationExportedSurfaceMatchesTheGolden(t *testing.T) {
	root := repoRoot(t)

	got := exportedSurface(t, filepath.Join(root, "hangar"))
	if len(got) == 0 {
		t.Fatal("package hangar exported nothing; the golden comparison below would pass vacuously")
	}

	// A foundation that lost any of these is not the reviewed one, whatever the
	// golden says. Naming them separately means a regenerated golden cannot
	// quietly bless their removal.
	for _, required := range []string{
		"type TreeRef ",
		"type Store ",
		"type Canonicalizer ",
		"type WarrantSigner ",
		"type Materializer ",
		"type TreeAttributes ",
		"type Scope ",
		"type Digest ",
	} {
		found := false
		for _, line := range got {
			if strings.HasPrefix(line, required) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("package hangar no longer declares %q. This track extends the reviewed "+
				"strict-input foundation; it does not recreate it. Diagnose the mixed or missing "+
				"prerequisite rather than copying it here.", strings.TrimSpace(required))
		}
	}

	want, err := os.ReadFile(hangarSymbolGolden)
	if err != nil {
		t.Fatalf("reading %s: %v\n\nThe current surface is:\n%s", hangarSymbolGolden, err,
			strings.Join(got, "\n"))
	}

	gotText := strings.Join(got, "\n") + "\n"
	if gotText != string(want) {
		t.Errorf("the exported surface of package hangar no longer matches %s.\n"+
			"An incompatible foundation change must be diagnosed here, not discovered at a later "+
			"call site. If the change is intended, regenerate the golden in the same commit.\n\n"+
			"--- got ---\n%s\n--- want ---\n%s", hangarSymbolGolden, gotText, want)
	}
}

// TestIntegrationCommitIsAnAncestorOfHEAD is clause (b).
func TestIntegrationCommitIsAnAncestorOfHEAD(t *testing.T) {
	root := repoRoot(t)

	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		// Loud, because a silent skip on the one check that pins the base
		// commit is exactly how a track ends up built on the wrong tree.
		t.Skipf("SKIPPED, AND THIS CHECK IS LOAD-BEARING: %s has no .git, so the pinned "+
			"integration commit %s cannot be proven to be an ancestor of this source. "+
			"Every other assertion in this file still ran; this one did not.",
			root, integrationCommit)
	}

	// A clone that does not carry the object cannot answer the question, and
	// `merge-base` reports that the same way it reports a real negative: exit
	// 128. Ask whether the object is here before asking where it sits, or a
	// shallow CI checkout fails this test for having a short history rather
	// than a wrong foundation. That is not hypothetical: the pipeline's repo
	// resource clones shallow, `.git` exists so the skip above does not fire,
	// and the first run of this test on `core` failed with "Not a valid object
	// name". `hack/ci-check.sh` could not have caught it — it builds from a
	// `git archive`, so there is no `.git` at all and the skip above fires.
	probe := exec.Command("git", "cat-file", "-e", integrationCommit+"^{commit}")
	probe.Dir = root
	if err := probe.Run(); err != nil {
		shallow := exec.Command("git", "rev-parse", "--is-shallow-repository")
		shallow.Dir = root
		depth, _ := shallow.Output()
		t.Skipf("SKIPPED, AND THIS CHECK IS LOAD-BEARING: the pinned integration commit %s "+
			"is not present in this clone (shallow=%s), so its ancestry cannot be proven "+
			"here. This is a truncated history, not a wrong foundation — a clone with the "+
			"object answers the question. Every other assertion in this file still ran; "+
			"this one did not.",
			integrationCommit, strings.TrimSpace(string(depth)))
	}

	cmd := exec.Command("git", "merge-base", "--is-ancestor", integrationCommit, "HEAD")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the pinned integration commit %s is not an ancestor of HEAD: %v\n%s\n\n"+
			"This track pins the commit that carries the reviewed Hangar strict-input "+
			"foundation. Work built on a tree without it is built on a different foundation.",
			integrationCommit, err, out)
	}
}

// foundationPrerequisite is one clause-(c) presence check.
type foundationPrerequisite struct {
	name     string
	path     string
	contains []string
	why      string
}

// TestFoundationCompositionIsPresent is clause (c).
func TestFoundationCompositionIsPresent(t *testing.T) {
	root := repoRoot(t)

	prerequisites := []foundationPrerequisite{
		{
			name:     "daemon composition",
			path:     "cmd/artifact-daemon/hangar.go",
			contains: []string{"HangarReadyLabel", "concourse.dev/hangar-v1"},
			why:      "the strict-input daemon composition and the capability label it advertises",
		},
		{
			name:     "daemon handlers",
			path:     "cmd/artifact-daemon/hangar_handlers.go",
			contains: []string{"hangar."},
			why:      "the daemon HTTP surface this track's output daemon is modelled on and must not reuse",
		},
		{
			name:     "runtime input seam",
			path:     "atc/runtime/types.go",
			contains: []string{"HangarTree *hangar.TreeRef"},
			why:      "the read-only task-input seam; the output plane is its counterpart, not a second copy",
		},
		{
			name:     "chart capability",
			path:     "deploy/chart/templates/artifact-daemon-daemonset.yaml",
			contains: []string{"concourse.dev/hangar-v1"},
			why:      "the strict-input capability label, which the output capability must never reuse (Req 56)",
		},
	}

	if len(prerequisites) == 0 {
		t.Fatal("no prerequisite is declared; this check would pass vacuously")
	}

	checked := 0
	for _, prerequisite := range prerequisites {
		full := filepath.Join(root, prerequisite.path)
		body, err := os.ReadFile(full)
		if err != nil {
			t.Errorf("%s: cannot read %s: %v\nThis track requires %s.",
				prerequisite.name, prerequisite.path, err, prerequisite.why)
			continue
		}
		checked++
		if len(prerequisite.contains) == 0 {
			t.Errorf("%s declares no substring to look for; presence of a file proves nothing",
				prerequisite.name)
		}
		for _, want := range prerequisite.contains {
			if !bytes.Contains(body, []byte(want)) {
				t.Errorf("%s: %s does not contain %q.\nThis track requires %s.",
					prerequisite.name, prerequisite.path, want, prerequisite.why)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no prerequisite file was readable; this check passed over nothing")
	}
	if checked != len(prerequisites) {
		t.Errorf("checked %d of %d prerequisites", checked, len(prerequisites))
	}
}

// TestFoundationSurfaceRendererIsNotVacuous drives exportedSurface with a
// fixture whose expected rendering is written out by hand, so a renderer that
// silently produced nothing — or that stopped stripping unexported members —
// cannot make the golden above agree with itself forever.
func TestFoundationSurfaceRendererIsNotVacuous(t *testing.T) {
	dir := t.TempDir()
	source := `package fixture

type Exported struct {
	Kept    string
	dropped int
}

type Hidden struct{ X int }

type Contract interface {
	Kept(int) error
	dropped()
}

func Exposed(a int) (string, error) { return "", nil }

func hidden() {}

func (e Exported) Method() error { return nil }

func (h *Hidden) AlsoOnAnExportedType() {}

const Limit = 3

var Sentinel error
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	// A _test.go file in the same directory must not contribute.
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte("package fixture\n\nfunc NotInTheSurface() {}\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture test file: %v", err)
	}

	got := exportedSurface(t, dir)
	// Receiver *names* are absent by design: `func (e Exported) Method()` and
	// `func (x Exported) Method()` are the same signature, and a golden that
	// churned on a renamed receiver would be regenerated without being read.
	want := []string{
		"const Limit",
		"func (*Hidden) AlsoOnAnExportedType()",
		"func (Exported) Method() error",
		"func Exposed(a int) (string, error)",
		"type Contract interface { Kept(int) error }",
		"type Exported struct { Kept string }",
		"type Hidden struct { X int }",
		"var Sentinel error",
	}

	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the exported-surface renderer does not describe a known fixture correctly.\n"+
			"--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
