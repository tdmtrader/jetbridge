package output

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The seams this file defends, all of them stated once here rather than
// re-derived at each call site in Phases 2 through 9:
//
//  1. Neither hangar/output nor hangar/executioncontrol may name a Run,
//     workflow, ticket, agent, Anvil or playbook. Hangar is the product-neutral
//     durable result plane; the moment it learns why an output matters, the
//     consumer track owns Hangar rather than composing with it. (Reqs 1, 57)
//  2. There is exactly one TreeRef, and it is the foundation's. A second exact
//     reference type is how two object models start diverging. (Req 20)
//  3. Output code is never routed through the durable *cache* tier, which is a
//     fail-open, name-keyed cache whose every method swallows its errors.
//     (Reqs 20, 59)
//  4. Exactly one interface can delete a published object, its method requires
//     an exact ref and a generation precondition, and nothing else in the
//     package offers a delete. GCS IAM cannot require a caller to send a
//     generation precondition once delete permission exists, so the boundary
//     has to be the code. (Reqs 47, 55; AC 20)
//  5. No production API accepts a bucket, object key, absolute path, hostPath
//     or caller-chosen scope. A handle string alone is never an identity.
//     (Reqs 7, 20, 24; AC 7)
//  6. Both packages stay leaves, for the reason hangar/architecture_test.go
//     already states about package hangar.
//
// Every guard below is a pure function over an injected inventory, and every
// one of them is driven twice: once over the real packages, and once — in
// TestArchitectureGuardsAreNotVacuous — over a fixture that violates it. A
// structural test that silently matches zero files passes forever.

// scannedPackages are the directories this file inventories.
var scannedPackages = []string{".", "../executioncontrol"}

// scannedFile is one file in the inventory. Extensionless files are included
// as well as Go sources: a shell fragment materialized into a pod is production
// code, and it is exactly where a forbidden name would otherwise hide.
type scannedFile struct {
	Path string
	Body []byte
	Go   bool
	Test bool
}

type importEdge struct {
	File string
	Path string
}

type declaredType struct {
	File string
	Name string
	Kind string // "struct", "interface", "alias", "other"
}

type declaredParam struct {
	Name string
	Type string
}

// declaredCallable is one exported entry point: a package-level function, a
// method on an exported type, or an interface method.
type declaredCallable struct {
	File   string
	Owner  string // the interface or receiver type; empty for package functions
	Name   string
	Params []declaredParam
}

type surface struct {
	Files     []scannedFile
	Imports   []importEdge
	Types     []declaredType
	Callables []declaredCallable
}

func render(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, node); err != nil {
		return "<unrenderable>"
	}

	return strings.Join(strings.Fields(buf.String()), " ")
}

func params(fset *token.FileSet, list *ast.FieldList) []declaredParam {
	var out []declaredParam
	if list == nil {
		return out
	}
	for _, field := range list.List {
		typ := render(fset, field.Type)
		if len(field.Names) == 0 {
			out = append(out, declaredParam{Type: typ})
			continue
		}
		for _, name := range field.Names {
			out = append(out, declaredParam{Name: name.Name, Type: typ})
		}
	}

	return out
}

// inventory reads the packages this file guards.
func inventory(t *testing.T, dirs []string) surface {
	t.Helper()

	var found surface
	fset := token.NewFileSet()

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			ext := filepath.Ext(name)
			isGo := ext == ".go"
			if !isGo && ext != "" {
				continue
			}
			path := filepath.Join(dir, name)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			found.Files = append(found.Files, scannedFile{
				Path: path, Body: body, Go: isGo,
				Test: strings.HasSuffix(name, "_test.go"),
			})

			if !isGo {
				continue
			}

			file, err := parser.ParseFile(fset, path, body, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			// Import edges come from every Go file, test files included: a test
			// dependency couples the packages just as firmly, and it is how the
			// production import arrives a week later. Declarations come from
			// production files only, because the rules below are about what
			// these packages offer their callers.
			for _, spec := range file.Imports {
				found.Imports = append(found.Imports, importEdge{
					File: path,
					Path: strings.Trim(spec.Path.Value, `"`),
				})
			}
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if !d.Name.IsExported() {
						continue
					}
					owner := ""
					if d.Recv != nil && len(d.Recv.List) > 0 {
						base := d.Recv.List[0].Type
						if star, ok := base.(*ast.StarExpr); ok {
							base = star.X
						}
						if ident, ok := base.(*ast.Ident); ok {
							owner = ident.Name
						}
					}
					found.Callables = append(found.Callables, declaredCallable{
						File: path, Owner: owner, Name: d.Name.Name,
						Params: params(fset, d.Type.Params),
					})
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						typeSpec, ok := spec.(*ast.TypeSpec)
						if !ok || !typeSpec.Name.IsExported() {
							continue
						}
						kind := "other"
						switch underlying := typeSpec.Type.(type) {
						case *ast.StructType:
							kind = "struct"
						case *ast.InterfaceType:
							kind = "interface"
							for _, method := range underlying.Methods.List {
								fn, ok := method.Type.(*ast.FuncType)
								if !ok || len(method.Names) == 0 {
									continue
								}
								found.Callables = append(found.Callables, declaredCallable{
									File: path, Owner: typeSpec.Name.Name, Name: method.Names[0].Name,
									Params: params(fset, fn.Params),
								})
							}
						}
						if typeSpec.Assign.IsValid() {
							kind = "alias"
						}
						found.Types = append(found.Types, declaredType{
							File: path, Name: typeSpec.Name.Name, Kind: kind,
						})
					}
				}
			}
		}
	}

	sort.Slice(found.Files, func(i, j int) bool { return found.Files[i].Path < found.Files[j].Path })

	return found
}

// ---------------------------------------------------------------------------
// The rules, as pure functions over an inventory.
// ---------------------------------------------------------------------------

// productDomainTerms are the vocabularies Hangar must not learn. They are
// matched token by token against import paths, in singular and plural form.
var productDomainTerms = []string{
	"run", "workflow", "ticket", "agent", "anvil", "playbook",
}

// checkNoProductDomainImports matches import paths token by token rather than
// by substring, so `atc/runs` is caught while `atc/runtime`, `runner` and this
// repository's own `agentic` are not. A rule that cries wolf is deleted the
// first time it does.
func checkNoProductDomainImports(found surface) []string {
	var problems []string

	if len(found.Imports) == 0 {
		return []string{"the inventory found no import at all; this rule would pass vacuously"}
	}

	for _, edge := range found.Imports {
		for _, term := range productDomainTerms {
			if !namesTerm(edge.Path, term) {
				continue
			}
			problems = append(problems, edge.File+" imports "+edge.Path+
				": Hangar is the product-neutral durable result plane. It must not learn what a "+
				term+" is; the consumer composes with Hangar through a caller-owned transaction "+
				"and an opaque identity instead.")
		}
	}

	return problems
}

// namesTerm reports whether any token of s is term, or term pluralised.
func namesTerm(s, term string) bool {
	for _, token := range tokenize(s) {
		if token == term || token == term+"s" {
			return true
		}
	}

	return false
}

// tokenize splits an import path or identifier into lowercase words on
// punctuation and camelCase boundaries.
func tokenize(s string) []string {
	var (
		tokens  []string
		current []rune
	)
	flush := func() {
		if len(current) > 0 {
			tokens = append(tokens, strings.ToLower(string(current)))
			current = nil
		}
	}
	runes := []rune(s)
	for index, r := range runes {
		switch {
		case r >= 'A' && r <= 'Z':
			if index > 0 && runes[index-1] >= 'a' && runes[index-1] <= 'z' {
				flush()
			}
			current = append(current, r)
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			current = append(current, r)
		default:
			flush()
		}
	}
	flush()

	return tokens
}

// treeRefNames are the names a second exact-reference type would plausibly take.
var treeRefNames = []string{"TreeRef", "TreeReference", "ExactRef", "OutputRef", "ObjectRef"}

func checkNoSecondTreeRef(found surface) []string {
	var problems []string

	if len(found.Types) == 0 {
		return []string{"the inventory found no declared type; this rule would pass vacuously"}
	}

	for _, declared := range found.Types {
		for _, name := range treeRefNames {
			if declared.Name == name {
				problems = append(problems, declared.File+" declares type "+declared.Name+
					": there is exactly one exact reference in this system and it is "+
					"hangar.TreeRef. A second one is where two object models start diverging.")
			}
		}
	}

	// Non-vacuity in the other direction: if nothing referenced hangar.TreeRef
	// at all, the rule above would be trivially satisfied by a package that
	// simply has no exact references — which is not the shape being defended.
	referenced := false
	for _, file := range found.Files {
		if bytes.Contains(file.Body, []byte("hangar.TreeRef")) {
			referenced = true
			break
		}
	}
	if !referenced {
		problems = append(problems, "no scanned file references hangar.TreeRef. This rule exists "+
			"because the output plane binds the foundation's exact reference; a package that "+
			"references none satisfies it for the wrong reason.")
	}

	return problems
}

const durableCachePackage = "cmd/artifact-daemon/durable"

func checkNotRoutedThroughTheDurableCache(found surface) []string {
	var problems []string

	if len(found.Files) == 0 {
		return []string{"the inventory found no file; this rule would pass vacuously"}
	}

	for _, edge := range found.Imports {
		if strings.HasSuffix(edge.Path, durableCachePackage) {
			problems = append(problems, edge.File+" imports "+edge.Path+
				": the durable tier is a fail-open, name-keyed cache whose every method swallows "+
				"its errors. Durable output retention cannot be merged into it.")
		}
	}
	for _, file := range found.Files {
		// Production files only. The import clause above already counts a test
		// edge; this identifier scan is the belt-and-braces half, and running it
		// over test sources would make this very file -- which names
		// durable.Store in the fixture that proves the rule bites -- its own
		// first violation.
		if file.Test {
			continue
		}
		if bytes.Contains(file.Body, []byte("durable.Store")) {
			problems = append(problems, file.Path+" names durable.Store"+
				": output publication uses its own role-specific interfaces against the dedicated "+
				"output bucket, never the cache store.")
		}
	}

	return problems
}

// deleteOwner is the one interface allowed to offer a delete.
const deleteOwner = "Reclaimer"

// deleteVocabularyExemptions are callables whose name contains "delete" but
// which cannot delete anything: the closed vocabulary of outcomes a conditional
// delete may report, and its parser.
//
// Every entry must match something, so an exemption cannot outlive the thing it
// exempts and quietly widen the rule.
var deleteVocabularyExemptions = map[string]string{
	"DeleteOutcomes":     "the closed vocabulary of conditional-delete results; it performs none",
	"ParseDeleteOutcome": "the parser for that vocabulary",
}

func checkDeleteIsIsolatedToTheReclaimer(found surface) []string {
	var problems []string

	if len(found.Callables) == 0 {
		return []string{"the inventory found no exported callable; this rule would pass vacuously"}
	}

	deletes := 0
	sawOwner := false
	for _, declared := range found.Types {
		if declared.Name == deleteOwner && declared.Kind == "interface" {
			sawOwner = true
		}
	}

	exempted := map[string]bool{}
	for _, callable := range found.Callables {
		if !strings.Contains(strings.ToLower(callable.Name), "delete") {
			continue
		}
		if _, ok := deleteVocabularyExemptions[callable.Name]; ok && callable.Owner == "" {
			exempted[callable.Name] = true

			continue
		}
		deletes++
		if callable.Owner != deleteOwner {
			owner := callable.Owner
			if owner == "" {
				owner = "the package"
			}
			problems = append(problems, callable.File+": "+owner+"."+callable.Name+
				" offers a delete. Only "+deleteOwner+" may, because GCS IAM cannot require a "+
				"caller to send a generation precondition once delete permission exists — the "+
				"boundary has to be the code.")

			continue
		}

		// The one allowed delete must take an exact ref and a precondition.
		hasRef, hasPrecondition := false, false
		for _, param := range callable.Params {
			if strings.Contains(param.Type, "hangar.TreeRef") {
				hasRef = true
			}
			if strings.Contains(param.Type, "DeletePrecondition") {
				hasPrecondition = true
			}
			if param.Type == "string" {
				problems = append(problems, callable.File+": "+callable.Owner+"."+callable.Name+
					" takes a bare string parameter "+param.Name+". A key-only or unconditional "+
					"delete route is exactly what Req 55 forbids.")
			}
		}
		if !hasRef {
			problems = append(problems, callable.File+": "+callable.Owner+"."+callable.Name+
				" does not take a hangar.TreeRef; an exact registered ref is required.")
		}
		if !hasPrecondition {
			problems = append(problems, callable.File+": "+callable.Owner+"."+callable.Name+
				" does not take a DeletePrecondition; an unconditional delete may never broaden "+
				"from a conditional one.")
		}
	}

	if !sawOwner {
		problems = append(problems, "no interface named "+deleteOwner+" was found. The delete "+
			"isolation rule has nothing to bind to and would pass vacuously.")
	}
	if deletes == 0 {
		problems = append(problems, "no exported callable mentions a delete at all. The "+
			"isolation rule matched nothing and would pass vacuously.")
	}
	for name, reason := range deleteVocabularyExemptions {
		if !exempted[name] {
			problems = append(problems, "deleteVocabularyExemptions exempts "+name+" ("+reason+
				"), but nothing by that name exists any more. Remove the entry so the exemption "+
				"list keeps describing what is actually true.")
		}
	}

	return problems
}

// storageRoleInterfaces are the interfaces that reach the object store or the
// node's source ledger. They are the surface Req 7 and Req 24 are about, and
// the bare-string rule below applies to them by name-independent type.
//
// Each must exist, or the rule is guarding an interface somebody renamed.
var storageRoleInterfaces = []string{
	"Publisher", "Inventory", "Reclaimer", "SourceControl", "ReceiptVerifier",
}

// locationParamNames are parameter names that would let a caller choose where
// the output plane writes or reads.
//
// They are the *diagnostic*, not the rule: a name list is defeated by a rename,
// and `b`, `where` and `location` are ordinary names for a location. What the
// rule turns on is the type — see checkNoAPIAcceptsAStorageLocation. Keeping
// the names is still worth it, because they produce the message that says which
// authority was being handed over.
var locationParamNames = []string{
	"bucket", "prefix", "key", "objectkey", "path", "hostpath", "root", "dir", "directory", "url", "endpoint",
}

// locationParamTypes are types that carry the same authority.
var locationParamTypes = []string{"hangar.Scope"}

func checkNoAPIAcceptsAStorageLocation(found surface) []string {
	var problems []string

	if len(found.Callables) == 0 {
		return []string{"the inventory found no exported callable; this rule would pass vacuously"}
	}

	roles := map[string]bool{}
	for _, name := range storageRoleInterfaces {
		roles[name] = true
	}
	declared := map[string]bool{}
	for _, found := range found.Types {
		if found.Kind == "interface" {
			declared[found.Name] = true
		}
	}
	for _, name := range storageRoleInterfaces {
		if !declared[name] {
			problems = append(problems, "no interface named "+name+" was found. The "+
				"storage-location rule is stated over the roles that reach the object store and "+
				"the source ledger; a renamed or removed one leaves it guarding nothing.")
		}
	}

	for _, callable := range found.Callables {
		for _, param := range callable.Params {
			// A bare string on a role interface is forbidden outright. Every
			// identity these roles take is a distinct type -- a TreeRef, a
			// typed UUID, a resolved reservation -- so the only thing a plain
			// string can be is a name somebody chose, and Req 7 says a handle
			// string alone is never an identity. The delete rule already says
			// this for Reclaimer; there was no reason it stopped there.
			if roles[callable.Owner] && param.Type == "string" {
				name := param.Name
				if name == "" {
					name = "(unnamed)"
				}
				problems = append(problems, callable.File+": "+describe(callable)+
					" takes a bare string parameter "+name+". The control plane derives every "+
					"bucket, scope, key and path from authenticated deployment context; a role "+
					"that accepts a string accepts one a caller chose.")
			}
			lower := strings.ToLower(param.Name)
			for _, forbidden := range locationParamNames {
				if lower != forbidden {
					continue
				}
				problems = append(problems, callable.File+": "+describe(callable)+
					" accepts a caller-chosen "+param.Name+". The control plane derives the "+
					"bucket, scope and key prefix from authenticated deployment context alone; "+
					"no task, consumer or receipt may select or broaden them.")
			}
			for _, forbidden := range locationParamTypes {
				if param.Type != forbidden {
					continue
				}
				problems = append(problems, callable.File+": "+describe(callable)+
					" accepts a "+param.Type+" parameter. A scope is server-derived; accepting "+
					"one as an argument is how a caller chooses its own namespace.")
			}
		}
	}

	return problems
}

func describe(callable declaredCallable) string {
	if callable.Owner == "" {
		return callable.Name
	}

	return callable.Owner + "." + callable.Name
}

// allowedFirstPartyImports is the leaf rule for the two new packages, stated
// the way hangar/architecture_test.go states it for package hangar: not "as few
// as possible", but exactly these, each with a reason — and per package, so
// that the direction between them is part of the rule rather than an accident.
//
// hangar/executioncontrol is the stricter of the two. It is the product-neutral
// base protocol a sibling track and later consumers link, so it imports nothing
// first-party at all, not even hangar: an exact TreeRef is an output concept.
var allowedFirstPartyImports = map[string]map[string]string{
	".": {
		"github.com/concourse/concourse/hangar": "the foundation's exact TreeRef, Scope, Digest " +
			"and TreeAttributes; the whole point is that there is only one of each",
		"github.com/concourse/concourse/hangar/executioncontrol": "the base exact-execution " +
			"identity that DurableOutputCapture extends rather than forks",
	},
	"../executioncontrol": {},
}

func checkPackagesAreLeaves(found surface) []string {
	var problems []string

	if len(found.Imports) == 0 {
		return []string{"the inventory found no import at all; this rule would pass vacuously"}
	}

	const modulePrefix = "github.com/concourse/concourse"
	firstParty := 0
	for _, edge := range found.Imports {
		if strings.HasPrefix(edge.Path, "cloud.google.com/") || strings.HasPrefix(edge.Path, "google.golang.org/api") {
			problems = append(problems, edge.File+" imports "+edge.Path+
				": a cloud client belongs in a leaf package the daemon imports, not in a package "+
				"the web binary links.")
		}
		if !strings.HasPrefix(edge.Path, modulePrefix) {
			continue
		}

		dir := filepath.Dir(edge.File)
		allowed, known := allowedFirstPartyImports[dir]
		if !known {
			problems = append(problems, edge.File+" is in "+dir+", which allowedFirstPartyImports "+
				"does not describe. Every scanned package states its own allowed first-party "+
				"imports, so a new one cannot inherit a looser rule by default.")

			continue
		}
		if _, ok := allowed[edge.Path]; ok {
			firstParty++

			continue
		}
		problems = append(problems, edge.File+" imports first-party package "+edge.Path+
			": hangar/output and hangar/executioncontrol must stay importable from anywhere. "+
			"Add it to allowedFirstPartyImports["+dir+"] with the reason, or invert the dependency.")
	}

	if firstParty == 0 {
		problems = append(problems, "no scanned file imports any allowed first-party package. "+
			"The allowlist describes nothing and the rule would pass vacuously.")
	}

	return problems
}

// ---------------------------------------------------------------------------
// The rules, applied to the real packages.
// ---------------------------------------------------------------------------

func report(t *testing.T, name string, problems []string) {
	t.Helper()

	for _, problem := range problems {
		t.Errorf("%s: %s", name, problem)
	}
}

func TestArchitecture(t *testing.T) {
	found := inventory(t, scannedPackages)

	if len(found.Files) == 0 {
		t.Fatal("the architecture inventory scanned no file at all")
	}
	goFiles := 0
	for _, file := range found.Files {
		if file.Go {
			goFiles++
		}
	}
	if goFiles == 0 {
		t.Fatal("the architecture inventory scanned no Go source; every rule below would pass vacuously")
	}
	t.Logf("scanned %d files (%d Go) across %v", len(found.Files), goFiles, scannedPackages)

	report(t, "product-domain vocabulary", checkNoProductDomainImports(found))
	report(t, "one TreeRef", checkNoSecondTreeRef(found))
	report(t, "not the durable cache tier", checkNotRoutedThroughTheDurableCache(found))
	report(t, "delete isolation", checkDeleteIsIsolatedToTheReclaimer(found))
	report(t, "no caller-chosen storage location", checkNoAPIAcceptsAStorageLocation(found))
	report(t, "leaf packages", checkPackagesAreLeaves(found))
}

// TestArchitectureGuardsAreNotVacuous drives every rule above with a fixture
// that violates it, and with an empty inventory.
//
// Without this, each rule is an assertion that nothing was found — which is
// also what a rule that stopped working reports.
func TestArchitectureGuardsAreNotVacuous(t *testing.T) {
	empty := surface{}
	rules := map[string]func(surface) []string{
		"product-domain vocabulary":         checkNoProductDomainImports,
		"one TreeRef":                       checkNoSecondTreeRef,
		"not the durable cache tier":        checkNotRoutedThroughTheDurableCache,
		"delete isolation":                  checkDeleteIsIsolatedToTheReclaimer,
		"no caller-chosen storage location": checkNoAPIAcceptsAStorageLocation,
		"leaf packages":                     checkPackagesAreLeaves,
	}

	if len(rules) == 0 {
		t.Fatal("no rule is declared")
	}

	for name, rule := range rules {
		t.Run("empty inventory: "+name, func(t *testing.T) {
			if problems := rule(empty); len(problems) == 0 {
				t.Fatalf("%s passed over an empty inventory. That is the vacuous green this "+
					"assertion exists to prevent.", name)
			}
		})
	}

	violating := surface{
		Files: []scannedFile{{
			Path: "output.go",
			Go:   true,
			Body: []byte("package output\n\nvar _ = durable.Store(nil)\n"),
		}},
		Imports: []importEdge{
			{File: "output.go", Path: "github.com/concourse/concourse/atc/runs"},
			{File: "output.go", Path: "github.com/concourse/concourse/cmd/artifact-daemon/durable"},
			{File: "output.go", Path: "github.com/concourse/concourse/atc/db"},
			{File: "output.go", Path: "cloud.google.com/go/storage"},
		},
		Types: []declaredType{
			{File: "output.go", Name: "TreeRef", Kind: "struct"},
			{File: "output.go", Name: "Reclaimer", Kind: "interface"},
		},
		Callables: []declaredCallable{
			{File: "output.go", Owner: "Publisher", Name: "DeleteObject",
				Params: []declaredParam{{Name: "key", Type: "string"}}},
			{File: "output.go", Owner: "Publisher", Name: "Create",
				Params: []declaredParam{{Name: "bucket", Type: "string"}, {Name: "scope", Type: "hangar.Scope"}}},
		},
	}

	expectations := map[string]string{
		"product-domain vocabulary":         "atc/runs",
		"one TreeRef":                       "declares type TreeRef",
		"not the durable cache tier":        "artifact-daemon/durable",
		"delete isolation":                  "Publisher.DeleteObject",
		"no caller-chosen storage location": "caller-chosen bucket",
		"leaf packages":                     "atc/db",
	}

	for name, rule := range rules {
		t.Run("violating inventory: "+name, func(t *testing.T) {
			problems := rule(violating)
			if len(problems) == 0 {
				t.Fatalf("%s found nothing wrong with an inventory built to violate it", name)
			}
			want := expectations[name]
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, want) {
				t.Errorf("%s did not name %q. It reported:\n%s", name, want, joined)
			}
		})
	}

	// The storage-location rule must catch a location by *type*, not by the
	// name somebody gave the parameter. A rule that matches names is a rule a
	// rename defeats, and `b`, `where` and `location` are all perfectly
	// ordinary parameter names.
	t.Run("a bare string on a role interface is caught whatever it is called", func(t *testing.T) {
		roles := surface{
			Types: []declaredType{
				{File: "output.go", Name: "Publisher", Kind: "interface"},
				{File: "output.go", Name: "Inventory", Kind: "interface"},
				{File: "output.go", Name: "Reclaimer", Kind: "interface"},
				{File: "output.go", Name: "SourceControl", Kind: "interface"},
				{File: "output.go", Name: "ReceiptVerifier", Kind: "interface"},
			},
			Callables: []declaredCallable{
				{File: "output.go", Owner: "SourceControl", Name: "BeginSeal",
					Params: []declaredParam{{Name: "b", Type: "string"}}},
			},
		}
		problems := checkNoAPIAcceptsAStorageLocation(roles)
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "SourceControl.BeginSeal takes a bare string parameter b") {
			t.Errorf("the rule did not object to a bare string parameter named b. It reported:\n%s", joined)
		}
	})

	// And it must know the roles exist. A renamed interface would otherwise
	// leave the rule guarding nothing.
	t.Run("a missing role interface is reported", func(t *testing.T) {
		problems := checkNoAPIAcceptsAStorageLocation(surface{
			Types:     []declaredType{{File: "output.go", Name: "Publisher", Kind: "interface"}},
			Callables: []declaredCallable{{File: "output.go", Owner: "Publisher", Name: "StatExactObject"}},
		})
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "no interface named SourceControl") {
			t.Errorf("the rule did not notice a missing role interface. It reported:\n%s", joined)
		}
	})

	// The vocabulary rule must not fire on the words that legitimately contain
	// a forbidden term, or the allowlist above is doing nothing and the rule
	// will be deleted the first time it cries wolf.
	t.Run("vocabulary rule tolerates legitimate containing words", func(t *testing.T) {
		benign := surface{Imports: []importEdge{
			{File: "output.go", Path: "github.com/concourse/concourse/atc/runtime"},
			{File: "output.go", Path: "time"},
			{File: "output.go", Path: "context"},
		}}
		if problems := checkNoProductDomainImports(benign); len(problems) != 0 {
			t.Errorf("the vocabulary rule objected to benign imports: %v", problems)
		}
	})
}
