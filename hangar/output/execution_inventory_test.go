package output

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// There is one exact-execution state machine in this repository and it is
// hangar/executioncontrol. DurableOutputCapture is an optional extension that
// references the same base identity; it does not fork it, restate it, or carry
// an activation epoch of its own.
//
// The guards here are the Phase 0 half of that rule. They inventory the
// contract rather than the wiring, because the wiring for capture-selected
// executions lands in Phases 3 and 4 — but they are written so that widening
// the population is a change of what the walk finds, not a change of shape. The
// sibling track `exact_execution_control` (plan decision F13) widens the same
// inventory to every controlled non-capture execution.
//
// Reqs 1, 3-6; ACs 1, 20.

// baseClassificationVocabulary is the closed set of exact-execution outcomes.
// A second state machine anywhere in the tree would have to spell at least one
// of these, because they are the vocabulary the protocol is written in.
var baseClassificationVocabulary = []string{
	"never_started",
	"authoritative_finish",
	"authoritative_stop",
}

// classificationOwners are the packages, relative to the module root, allowed
// to declare that vocabulary or a Classify operation.
//
// Pinning is the whole mechanism: a second declaration in a new package is a
// second state machine, and this is where it becomes a visible edit rather than
// a quiet divergence. The map must stay non-empty — an empty pin would let the
// scan below match nothing and report success forever.
var classificationOwners = map[string]string{
	"hangar/executioncontrol": "the one base exact-execution protocol; every other package " +
		"references its Classification rather than spelling one",
}

// skippedTreeDirs are directories the repository-wide walk does not enter.
var skippedTreeDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "testdata": true,
	"web": true, "release": true,
}

type classificationSite struct {
	Package string
	File    string
	What    string
}

// scanForExactExecutionStateMachines walks the module for Go sources that
// declare the base classification vocabulary or a Classify operation.
func scanForExactExecutionStateMachines(t *testing.T, root string) (sites []classificationSite, scanned int) {
	t.Helper()

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && (skippedTreeDirs[entry.Name()] || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}

			return nil
		}
		// Production sources only. The rule is about what the tree *is*, and a
		// test that names a classification to assert something about it -- this
		// file included -- is not a second state machine.
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(filepath.Dir(rel))

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			// A file this walk cannot parse is a hole in the guard, not a
			// detail to skip past.
			t.Errorf("parsing %s: %v", rel, err)

			return nil
		}

		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(n.Value)
				if err != nil {
					return true
				}
				for _, term := range baseClassificationVocabulary {
					if value == term {
						sites = append(sites, classificationSite{pkg, rel, "the literal " + strconv.Quote(term)})
					}
				}
			case *ast.InterfaceType:
				for _, method := range n.Methods.List {
					if _, ok := method.Type.(*ast.FuncType); !ok {
						continue
					}
					for _, name := range method.Names {
						if name.Name == "Classify" {
							sites = append(sites, classificationSite{pkg, rel, "a Classify operation"})
						}
					}
				}
			}

			return true
		})

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}

		return sites[i].What < sites[j].What
	})

	return sites, scanned
}

func TestOnlyOneExactExecutionStateMachineExists(t *testing.T) {
	if len(classificationOwners) == 0 {
		t.Fatal("classificationOwners is empty; the pin describes nothing and the scan below " +
			"would report success over anything it found")
	}

	root := repoRoot(t)
	sites, scanned := scanForExactExecutionStateMachines(t, root)

	if scanned < 100 {
		t.Fatalf("the walk visited only %d Go files; this repository has well over a thousand. "+
			"The scan failed and every assertion below would pass vacuously.", scanned)
	}
	if len(sites) == 0 {
		t.Fatalf("the walk found no exact-execution classification anywhere in %d Go files. "+
			"Either the protocol was renamed or the scan is broken; either way this guard "+
			"is asserting nothing.", scanned)
	}
	t.Logf("scanned %d Go files, found %d classification sites", scanned, len(sites))

	owned := map[string]bool{}
	for _, site := range sites {
		if _, ok := classificationOwners[site.Package]; ok {
			owned[site.Package] = true

			continue
		}
		t.Errorf("%s declares %s. There is one exact-execution state machine and it is "+
			"hangar/executioncontrol; capture, cancellation and consumer code reference its "+
			"Classification rather than spelling a second one. If this package is genuinely the "+
			"adapter, pin it in classificationOwners with the reason.", site.File, site.What)
	}

	for pkg, reason := range classificationOwners {
		if !owned[pkg] {
			t.Errorf("classificationOwners pins %q (%s), but nothing there declares the "+
				"vocabulary any more. Remove the pin so it keeps describing reality.", pkg, reason)
		}
	}
}

// The two contract packages, as this file's walks name them. They are
// constants because the vocabulary rules below are stated per package, and a
// typo in one of two string literals would silently describe nothing.
const (
	outputPackageDir = "."
	basePackageDir   = "../executioncontrol"
)

// declaredField is one field of one exported struct in the two contract
// packages.
//
// JSONName is the wire spelling from the field's `json` tag, empty when there
// is none. It is inventoried separately from the Go name because a field has
// two names and only one of them is what another implementation reads:
// `Origin string \`json:"run_id"\“ is innocent in Go and a product word on the
// wire.
type declaredField struct {
	Package  string
	Owner    string
	Name     string
	JSONName string
	Type     string
}

// jsonTagName reads the wire name out of a struct tag. It returns "" for an
// absent tag and for `json:"-"`, and it drops the options after the comma, so
// `json:",omitempty"` reports no rename rather than an empty wire name.
func jsonTagName(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	value, err := strconv.Unquote(tag.Value)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(reflect.StructTag(value).Get("json"), ",")
	if name == "-" {
		return ""
	}

	return name
}

func contractFields(t *testing.T, dirs []string) []declaredField {
	t.Helper()

	var fields []declaredField
	fset := token.NewFileSet()

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			for _, decl := range file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok || !typeSpec.Name.IsExported() {
						continue
					}
					structType, ok := typeSpec.Type.(*ast.StructType)
					if !ok || structType.Fields == nil {
						continue
					}
					for _, field := range structType.Fields.List {
						rendered := render(fset, field.Type)
						wire := jsonTagName(field.Tag)
						if len(field.Names) == 0 {
							fields = append(fields, declaredField{dir, typeSpec.Name.Name, "", wire, rendered})

							continue
						}
						for _, fieldName := range field.Names {
							if !fieldName.IsExported() {
								continue
							}
							fields = append(fields, declaredField{dir, typeSpec.Name.Name, fieldName.Name, wire, rendered})
						}
					}
				}
			}
		}
	}

	return fields
}

// TestCaptureExtensionDoesNotForkTheBaseIdentity is the other direction: the
// extension must reference the base identity and epoch, never redeclare them.
func TestCaptureExtensionDoesNotForkTheBaseIdentity(t *testing.T) {
	fields := contractFields(t, []string{outputPackageDir})
	if len(fields) == 0 {
		t.Fatal("hangar/output declares no exported struct field; this guard would pass vacuously")
	}

	carriers := 0
	for _, field := range fields {
		switch field.Name {
		case "Execution":
			carriers++
			if field.Type != "executioncontrol.Identity" {
				t.Errorf("hangar/output: %s.Execution is a %s, not an executioncontrol.Identity. "+
					"The capture extension references the base identity; a second exact identity "+
					"is a second execution.", field.Owner, field.Type)
			}
		case "ExecutionID", "Fence":
			// SourceIncarnation is the one place an execution id appears
			// outside an Identity, because a server-issued source incarnation
			// is defined as (execution, node, handle generation, output) and
			// carries no fence at all.
			if field.Owner != "SourceIncarnation" {
				t.Errorf("hangar/output: %s.%s redeclares part of the base identity. Reference "+
					"executioncontrol.Identity instead; splitting the identity is how the "+
					"extension quietly becomes a second state machine.", field.Owner, field.Name)

				continue
			}
			carriers++
			if field.Name == "ExecutionID" && field.Type != "executioncontrol.ExecutionID" {
				t.Errorf("hangar/output: SourceIncarnation.ExecutionID is a %s, not an "+
					"executioncontrol.ExecutionID.", field.Type)
			}
		case "ActivationEpoch":
			carriers++
			if field.Type != "executioncontrol.ActivationEpoch" {
				t.Errorf("hangar/output: %s.ActivationEpoch is a %s, not an "+
					"executioncontrol.ActivationEpoch. One activation epoch attests both facets; "+
					"a second type is a second epoch.", field.Owner, field.Type)
			}
		}
	}

	if carriers == 0 {
		t.Fatal("no exported struct in hangar/output carries an execution identity or activation " +
			"epoch. This guard exists because the capture extension references the base identity; " +
			"an extension that references none satisfies it for the wrong reason.")
	}
	t.Logf("checked %d identity/epoch carriers across %d exported fields", carriers, len(fields))
}
