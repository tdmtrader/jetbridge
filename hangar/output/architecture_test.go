package output

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
//  4. The privilege split between the three roles. Each role is one binary and
//     one cloud service account, and the split only means something while
//     each role's exported method set stays its own: the publisher never
//     lists or deletes, the inventory never creates or deletes, and a delete
//     exists only on the
//     reclaimer's role type, takes a tree ref and a generation
//     precondition, and has no key-only route beside it. GCS IAM cannot
//     require a caller to send a precondition once delete permission exists,
//     so the seam has to be the code -- and the code that matters is the
//     concrete role type each binary holds, not an interface nobody dials.
//     (Reqs 47, 55; AC 20)
//  5. No production API accepts a bucket, object key, absolute path, hostPath
//     or caller-chosen scope. A handle string alone is never an identity.
//     (Reqs 7, 20, 24; AC 7)
//  6. Both packages stay leaves, for the reason hangar/architecture_test.go
//     already states about package hangar.
//  7. Each principal binary under cmd/ links exactly the role its name says
//     and none of the other three. A Pod's identity is Pod-wide, so a binary
//     that linked two roles would hold two roles' permissions.
//
// Every guard below is a pure function over an injected inventory, and every
// one of them is driven twice: once over the real packages, and once — in
// TestArchitectureGuardsAreNotVacuous — over a fixture that violates it. A
// structural test that silently matches zero files passes forever.

// scannedPackages are the leaf directories this file inventories.
var scannedPackages = []string{".", "../executioncontrol"}

// role is a package under this rule: one of the three storage-permission
// personas, or the leaf they all depend on.
type role string

const (
	leafRole      role = "leaf"
	publisherRole role = "publisher"
	inventoryRole role = "inventory"
	reclaimerRole role = "reclaimer"
)

// roles are the three role packages: one binary and one service account each.
// They are inventoried separately from the leaf because the rules over them
// are about what a process holds, not about what the leaf declares.
var roles = []role{publisherRole, inventoryRole, reclaimerRole}

// roleDirs are the directories the role packages live in, relative to this
// file.
func roleDirs() []string {
	dirs := make([]string, 0, len(roles))
	for _, each := range roles {
		dirs = append(dirs, string(each))
	}

	return dirs
}

// roleOf names the role a declaration belongs to, by the directory it is in.
func roleOf(file string) role {
	dir := filepath.Base(filepath.Dir(file))
	for _, each := range roles {
		if dir == string(each) {
			return each
		}
	}

	return leafRole
}

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
	File    string
	Owner   string // the interface or receiver type; empty for package functions
	Name    string
	Params  []declaredParam
	Results []declaredParam
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
							// An exported method on an unexported type is the
							// adapter behind a seam, not a callable a caller
							// can name; the seam it satisfies is inventoried
							// where it is declared.
							if !ident.IsExported() {
								continue
							}
							owner = ident.Name
						}
					}
					found.Callables = append(found.Callables, declaredCallable{
						File: path, Owner: owner, Name: d.Name.Name,
						Params:  params(fset, d.Type.Params),
						Results: params(fset, d.Type.Results),
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
									Params:  params(fset, fn.Params),
									Results: params(fset, fn.Results),
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

// union joins inventories, for the rules stated over the leaf and the roles at
// once.
func union(parts ...surface) surface {
	var joined surface
	for _, part := range parts {
		joined.Files = append(joined.Files, part.Files...)
		joined.Imports = append(joined.Imports, part.Imports...)
		joined.Types = append(joined.Types, part.Types...)
		joined.Callables = append(joined.Callables, part.Callables...)
	}

	return joined
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

// treeRefNames are the names a second tree-ref type would plausibly take.
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
					": there is exactly one tree ref in this system and it is "+
					"hangar.TreeRef. A second one is where two object models start diverging.")
			}
		}
	}

	// Non-vacuity in the other direction: if nothing referenced hangar.TreeRef
	// at all, the rule above would be trivially satisfied by a package that
	// simply has no tree refs — which is not the shape being defended.
	referenced := false
	for _, file := range found.Files {
		if bytes.Contains(file.Body, []byte("hangar.TreeRef")) {
			referenced = true
			break
		}
	}
	if !referenced {
		problems = append(problems, "no scanned file references hangar.TreeRef. This rule exists "+
			"because the output plane binds the foundation's tree ref; a package that "+
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
				": output publication uses its own role packages against the dedicated "+
				"output bucket, never the cache store.")
		}
	}

	return problems
}

// deleteVocabularyExemptions are leaf callables whose name contains "delete"
// but which cannot delete anything: the closed vocabulary of outcomes a
// conditional delete may report, and its parser.
//
// Every entry must match something, so an exemption cannot outlive the thing it
// exempts and quietly widen the rule.
var deleteVocabularyExemptions = map[string]string{
	"DeleteOutcomes":     "the closed vocabulary of conditional-delete results; it performs none",
	"ParseDeleteOutcome": "the parser for that vocabulary",
}

// deleteRole is the one role package that may delete, and deleteRoleType the
// one exported type in it that carries the delete a binary calls.
const (
	deleteRole     = reclaimerRole
	deleteRoleType = "Reclaimer"
	deleteSeamType = "Store"
)

// checkOnlyTheReclaimerDeletes is stated over the leaf and the three roles at
// once: every callable whose name says delete is in package reclaimer, and in
// there it is either the role's conditional delete over a tree ref and
// precondition, or Store.DeleteExact with a generation argument. A different
// store method, package function or role must not offer another delete route.
func checkOnlyTheReclaimerDeletes(found surface) []string {
	var problems []string

	if len(found.Callables) == 0 {
		return []string{"the inventory found no exported callable; this rule would pass vacuously"}
	}

	roleDeletes, seamDeletes := 0, 0
	exempted := map[string]bool{}
	for _, callable := range found.Callables {
		if !namesTerm(callable.Name, "delete") {
			continue
		}
		owner := roleOf(callable.File)
		if owner == leafRole {
			if _, ok := deleteVocabularyExemptions[callable.Name]; ok && callable.Owner == "" {
				exempted[callable.Name] = true

				continue
			}
			problems = append(problems, callable.File+": "+describe(callable)+
				" offers a delete in the leaf. The leaf declares outcomes and identities; the "+
				"one delete in this plane is on the reclaimer role type.")

			continue
		}
		if owner != deleteRole {
			problems = append(problems, callable.File+": "+string(owner)+"."+describe(callable)+
				" offers a delete. Only the "+string(deleteRole)+" may, because GCS IAM cannot require "+
				"a caller to send a generation precondition once delete permission exists — the "+
				"seam has to be the code.")

			continue
		}

		switch callable.Owner {
		case deleteSeamType:
			hasGeneration := false
			for _, param := range callable.Params {
				if param.Type == "int64" {
					hasGeneration = true
				}
			}
			if callable.Name != "DeleteExact" || !hasGeneration {
				problems = append(problems, callable.File+": "+describe(callable)+" is not an exact-generation delete on Store")
			}
			seamDeletes++

		case deleteRoleType:
			roleDeletes++
			hasRef, hasPrecondition := false, false
			for _, param := range callable.Params {
				if strings.Contains(param.Type, "hangar.TreeRef") {
					hasRef = true
				}
				if strings.Contains(param.Type, "DeletePrecondition") {
					hasPrecondition = true
				}
				if isCallerChosenString(param.Type) {
					problems = append(problems, callable.File+": "+describe(callable)+
						" takes a bare string parameter "+param.Name+" ("+param.Type+"). A key-only or "+
						"unconditional delete route is exactly what Req 55 forbids.")
				}
			}
			if !hasRef {
				problems = append(problems, callable.File+": "+describe(callable)+
					" does not take a hangar.TreeRef; an exact registered ref is required.")
			}
			if !hasPrecondition {
				problems = append(problems, callable.File+": "+describe(callable)+
					" does not take a DeletePrecondition; an unconditional delete may never broaden "+
					"from a conditional one.")
			}
		default:
			holder := callable.Owner
			if holder == "" {
				holder = "a package function"
			}
			problems = append(problems, callable.File+": "+describe(callable)+" is a delete on "+
				holder+". In package "+string(deleteRole)+" a delete lives on "+deleteRoleType+" (the role "+
				"type, with its precondition) or on "+deleteSeamType+" (the pinned store seam) and "+
				"nowhere else; a third route is a route without the precondition.")
		}
	}

	if roleDeletes == 0 {
		problems = append(problems, "no delete on "+string(deleteRole)+"."+deleteRoleType+" was found. "+
			"The isolation rule has nothing to bind to and would pass vacuously.")
	}
	if roleDeletes > 1 {
		problems = append(problems, string(deleteRole)+"."+deleteRoleType+" offers more than one delete; "+
			"one conditional route is the whole point.")
	}
	if seamDeletes == 0 {
		problems = append(problems, "no Delete on "+string(deleteRole)+"."+deleteSeamType+" was found; "+
			"the role type's delete has no store seam to reach, or the seam was renamed and "+
			"this rule no longer describes it.")
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

// roleVerbs is the privilege split, as the verbs each role's exported surface
// must not carry. It is matched token by token against callable names, so
// `NewWriter` is a writer and `Restrict` is not a stat.
//
// The delete verb is deliberately absent from every row: the delete rule above
// states where the one delete lives, and a second statement of it here would be
// a second rule to keep in step.
var roleVerbs = map[role]struct {
	forbidden []string
	because   string
}{
	publisherRole: {
		forbidden: []string{"list", "update"},
		because: "the publisher creates and reads. objects.list is bucket-wide and cannot be " +
			"narrowed by IAM, and the marker's immutability rests on there being no way to " +
			"rewrite metadata after creation",
	},
	inventoryRole: {
		forbidden: []string{"create", "ensure", "write", "writer", "update"},
		because:   "inventory lists and stats. It cannot create",
	},
	reclaimerRole: {
		forbidden: []string{"create", "ensure", "write", "writer", "read", "reader", "list", "update"},
		because: "a reclaimer that could read could exfiltrate and one that could write could " +
			"resurrect; it deletes what it was told to delete and does not go looking",
	},
}

func checkEachRoleOffersOnlyItsOwnVerbs(found surface) []string {
	var problems []string

	if len(found.Callables) == 0 {
		return []string{"the inventory found no exported callable; this rule would pass vacuously"}
	}

	seen := map[role]int{}
	for _, callable := range found.Callables {
		owner := roleOf(callable.File)
		verbs, isRole := roleVerbs[owner]
		if !isRole {
			continue
		}
		seen[owner]++
		for _, verb := range verbs.forbidden {
			if !namesTerm(callable.Name, verb) {
				continue
			}
			problems = append(problems, callable.File+": "+string(owner)+"."+describe(callable)+
				" names "+verb+". "+verbs.because+".")
		}
	}
	for _, each := range roles {
		if seen[each] == 0 {
			problems = append(problems, "the inventory found no exported callable in package "+
				string(each)+"; the privilege split has nothing to bind to there.")
		}
	}

	return problems
}

// storeSeams are the exported interfaces through which an object role hands
// its derived location to explicit store operations. The role gives the store
// the key it derived itself. These interfaces are the adapter's
// narrowed view of hangar/objectstore, so the bare-string rule stops at them.
// Each must exist as an interface in its package, or the exemption is guarding
// a seam somebody renamed.
var storeSeams = map[string]string{
	"publisher/Store": "the publisher's create-and-read view of the store",
	"inventory/Store": "the inventory's list-and-stat view of the store",
	"reclaimer/Store": "the reclaimer's stat-and-conditional-delete view of the store",
}

// checkRoleTypesTakeNoCallerChosenLocation is the bare-string rule over the
// concrete object roles. Every identity a role type takes is a distinct type --
// a TreeRef, a resolved reservation, a cursor, a lease -- so the only thing a
// plain string could be is a name somebody chose, and Req 7 says a handle
// string alone is never an identity.
func checkRoleTypesTakeNoCallerChosenLocation(found surface) []string {
	var problems []string

	if len(found.Callables) == 0 {
		return []string{"the inventory found no exported callable; this rule would pass vacuously"}
	}

	declared := map[string]bool{}
	for _, typ := range found.Types {
		if typ.Kind == "interface" {
			declared[string(roleOf(typ.File))+"/"+typ.Name] = true
		}
	}
	for seam, reason := range storeSeams {
		if !declared[seam] {
			problems = append(problems, "storeSeams exempts "+seam+" ("+reason+"), and no "+
				"interface by that name exists in that package. Remove the entry so the exemption "+
				"list keeps describing what is actually true.")
		}
	}

	checked := 0
	for _, callable := range found.Callables {
		owner := roleOf(callable.File)
		if owner == leafRole {
			continue
		}
		if _, seam := storeSeams[string(owner)+"/"+callable.Owner]; seam {
			continue
		}
		checked++
		for _, param := range callable.Params {
			name := param.Name
			if name == "" {
				name = "(unnamed)"
			}
			if isCallerChosenString(param.Type) {
				problems = append(problems, callable.File+": "+string(owner)+"."+describe(callable)+
					" takes a bare string parameter "+name+" ("+param.Type+"). The control plane derives "+
					"every bucket, scope, key and path from authenticated deployment context; a role "+
					"that accepts a string accepts one a caller chose.")
			}
			for _, forbidden := range locationParamTypes {
				if param.Type == forbidden {
					problems = append(problems, callable.File+": "+string(owner)+"."+describe(callable)+
						" accepts a "+param.Type+" parameter. A scope is server-derived; accepting "+
						"one as an argument is how a caller chooses its own namespace.")
				}
			}
		}
	}
	if checked == 0 {
		problems = append(problems, "no role-type callable was checked; every callable in the "+
			"object roles was a store seam, which cannot be right.")
	}

	return problems
}

// principalBinaries are the three cmd/ roots and the one role each may link.
var principalBinaries = map[string]role{
	"cmd/hangar-output-daemon":    publisherRole,
	"cmd/hangar-output-inventory": inventoryRole,
	"cmd/hangar-output-reclaimer": reclaimerRole,
}

const rolePackagePrefix = "github.com/concourse/concourse/hangar/output/"

// checkEachPrincipalLinksExactlyItsRole is the cmd/ half of the privilege
// split, over the transitive build graph rather than over import lines: a role
// reached through a pass package is linked all the same.
func checkEachPrincipalLinksExactlyItsRole(linked map[string][]string) []string {
	var problems []string

	if len(linked) == 0 {
		return []string{"no principal binary was listed; this rule would pass vacuously"}
	}

	for binary, own := range principalBinaries {
		deps, listed := linked[binary]
		if !listed || len(deps) == 0 {
			problems = append(problems, binary+" was not listed, or links nothing; either it moved "+
				"or the listing failed.")

			continue
		}
		links := map[role]bool{}
		for _, dep := range deps {
			if strings.HasPrefix(dep, rolePackagePrefix) {
				links[role(strings.TrimPrefix(dep, rolePackagePrefix))] = true
			}
		}
		if !links[own] {
			problems = append(problems, binary+" does not link "+rolePackagePrefix+string(own)+
				". The binary is that role's principal; a principal that holds no role is a "+
				"process nothing can attest.")
		}
		for _, other := range roles {
			if other != own && links[other] {
				problems = append(problems, binary+" links "+rolePackagePrefix+string(other)+
					" as well as its own role. A Pod's identity is Pod-wide, so a binary that "+
					"links two roles holds two roles' permissions.")
			}
		}
	}

	return problems
}

// locationParamNames are parameter names that would let a caller choose where
// the output plane writes or reads.
//
// They are the *diagnostic*, not the rule: a name list is defeated by a rename,
// and `b`, `where` and `location` are ordinary names for a location. What the
// rule turns on is the type -- see checkNoAPIAcceptsAStorageLocation. Keeping
// the names is still worth it, because they produce the message that says which
// authority was being handed over.
var locationParamNames = []string{
	"bucket", "prefix", "key", "objectkey", "path", "hostpath", "root", "dir", "directory", "url", "endpoint",
}

// locationParamTypes are types that carry the same authority.
var locationParamTypes = []string{"hangar.Scope"}

// isCallerChosenString reports whether a rendered parameter type is a string a
// caller picked, in any of the shapes one can travel in.
//
// The rule used to be `param.Type == "string"`, which is the one spelling out
// of five that a reviewer thinks of first. `[]string`, `...string`, `*string`
// and `map[string]string` are each exactly "a string a caller chose" (Req 7,
// AC 7) and each walked straight past it. Tokenizing is what makes the rule
// about the type rather than about how it was written.
func isCallerChosenString(rendered string) bool {
	for _, token := range tokenize(rendered) {
		if token == "string" {
			return true
		}
	}

	return false
}

// sourceLedgerSeam is the one role interface still declared in the leaf: the
// daemon's seam to the node's source ledger. Its bare-string rule stays here
// because the seam does; the three object roles are concrete and are checked
// in checkRoleTypesTakeNoCallerChosenLocation.
const sourceLedgerSeam = "SourceControl"

func checkNoAPIAcceptsAStorageLocation(found surface) []string {
	var problems []string

	if len(found.Callables) == 0 {
		return []string{"the inventory found no exported callable; this rule would pass vacuously"}
	}

	declared := false
	for _, found := range found.Types {
		if found.Kind == "interface" && found.Name == sourceLedgerSeam {
			declared = true
		}
	}
	if !declared {
		problems = append(problems, "no interface named "+sourceLedgerSeam+" was found. The "+
			"bare-string rule over the source ledger seam is guarding nothing.")
	}

	for _, callable := range found.Callables {
		for _, param := range callable.Params {
			if callable.Owner == sourceLedgerSeam && isCallerChosenString(param.Type) {
				name := param.Name
				if name == "" {
					name = "(unnamed)"
				}
				problems = append(problems, callable.File+": "+describe(callable)+
					" takes a bare string parameter "+name+" ("+param.Type+"). The control plane derives every "+
					"bucket, scope, key and path from authenticated deployment context; a seam "+
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

// principalLinks asks the toolchain what each principal binary links, so the
// cmd/ half of the split reads the real build graph rather than import lines.
func principalLinks(t *testing.T) map[string][]string {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))

	args := []string{"list", "-f", "{{.ImportPath}} {{join .Deps \" \"}}"}
	for binary := range principalBinaries {
		args = append(args, "./"+binary)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list failed: %v\n%s", err, ee.Stderr)
		}
		t.Fatalf("go list failed: %v", err)
	}

	const modulePrefix = "github.com/concourse/concourse/"
	linked := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		linked[strings.TrimPrefix(fields[0], modulePrefix)] = fields[1:]
	}

	return linked
}

func TestArchitecture(t *testing.T) {
	leafSurface := inventory(t, scannedPackages)
	personas := inventory(t, roleDirs())

	for name, found := range map[string]surface{"leaf": leafSurface, "roles": personas} {
		if len(found.Files) == 0 {
			t.Fatalf("the %s inventory scanned no file at all", name)
		}
		goFiles := 0
		for _, file := range found.Files {
			if file.Go {
				goFiles++
			}
		}
		if goFiles == 0 {
			t.Fatalf("the %s inventory scanned no Go source; every rule over it would pass vacuously", name)
		}
		t.Logf("%s: scanned %d files (%d Go)", name, len(found.Files), goFiles)
	}

	report(t, "product-domain vocabulary", checkNoProductDomainImports(leafSurface))
	report(t, "one TreeRef", checkNoSecondTreeRef(leafSurface))
	report(t, "not the durable cache tier", checkNotRoutedThroughTheDurableCache(leafSurface))
	report(t, "no caller-chosen storage location", checkNoAPIAcceptsAStorageLocation(leafSurface))
	report(t, "leaf packages", checkPackagesAreLeaves(leafSurface))

	report(t, "delete isolation", checkOnlyTheReclaimerDeletes(union(leafSurface, personas)))
	report(t, "role verbs", checkEachRoleOffersOnlyItsOwnVerbs(personas))
	report(t, "role types take no location", checkRoleTypesTakeNoCallerChosenLocation(personas))
	report(t, "principal binaries", checkEachPrincipalLinksExactlyItsRole(principalLinks(t)))
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
		"no caller-chosen storage location": checkNoAPIAcceptsAStorageLocation,
		"leaf packages":                     checkPackagesAreLeaves,
		"delete isolation":                  checkOnlyTheReclaimerDeletes,
		"role verbs":                        checkEachRoleOffersOnlyItsOwnVerbs,
		"role types take no location":       checkRoleTypesTakeNoCallerChosenLocation,
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
			{File: "output.go", Name: "SourceControl", Kind: "interface"},
		},
		Callables: []declaredCallable{
			// A delete on the publisher's role type, keyed by a string.
			{File: "publisher/publisher.go", Owner: "Publisher", Name: "DeleteObject",
				Params: []declaredParam{{Name: "key", Type: "string"}}},
			// A list on the publisher, and a create on the inventory.
			{File: "publisher/publisher.go", Owner: "Publisher", Name: "ListPage"},
			{File: "inventory/inventory.go", Owner: "Inventory", Name: "EnsureObject"},
			// A bucket handed to the leaf's source ledger seam.
			{File: "output.go", Owner: "SourceControl", Name: "BeginSeal",
				Params: []declaredParam{{Name: "bucket", Type: "string"}, {Name: "scope", Type: "hangar.Scope"}}},
		},
	}

	expectations := map[string]string{
		"product-domain vocabulary":         "atc/runs",
		"one TreeRef":                       "declares type TreeRef",
		"not the durable cache tier":        "artifact-daemon/durable",
		"no caller-chosen storage location": "caller-chosen bucket",
		"leaf packages":                     "atc/db",
		"delete isolation":                  "publisher.Publisher.DeleteObject offers a delete",
		"role verbs":                        "publisher.Publisher.ListPage names list",
		"role types take no location":       "publisher.Publisher.DeleteObject takes a bare string parameter key",
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

	// The remaining verbs, one per role, so no row of the table is the next
	// thing to go unexercised.
	t.Run("every role's row of the verb table bites", func(t *testing.T) {
		problems := checkEachRoleOffersOnlyItsOwnVerbs(surface{Callables: []declaredCallable{
			{File: "inventory/inventory.go", Owner: "Inventory", Name: "NewWriter"},
			{File: "reclaimer/reclaimer.go", Owner: "Reclaimer", Name: "NewReader"},
		}})
		joined := strings.Join(problems, "\n")
		for _, expected := range []string{
			"inventory.Inventory.NewWriter names writer",
			"reclaimer.Reclaimer.NewReader names reader",
		} {
			if !strings.Contains(joined, expected) {
				t.Errorf("the rule did not object to %q. It reported:\n%s", expected, joined)
			}
		}
	})

	// The delete rule's own shape: the reclaimer's one conditional delete is
	// accepted, and every other route in that package is not.
	t.Run("a delete in the reclaimer must be the role type's conditional one or the pinned seam", func(t *testing.T) {
		accepted := surface{Callables: []declaredCallable{
			{File: "reclaimer/reclaimer.go", Owner: "Reclaimer", Name: "DeleteExactGeneration",
				Params: []declaredParam{
					{Name: "ctx", Type: "context.Context"},
					{Name: "ref", Type: "hangar.TreeRef"},
					{Name: "precondition", Type: "output.DeletePrecondition"},
				}},
			{File: "reclaimer/reclaimer.go", Owner: "Store", Name: "DeleteExact",
				Params: []declaredParam{{Name: "ctx", Type: "context.Context"}, {Name: "generation", Type: "int64"}}},
			{File: "outcomes.go", Name: "DeleteOutcomes"},
			{File: "outcomes.go", Name: "ParseDeleteOutcome",
				Params: []declaredParam{{Name: "s", Type: "string"}}},
		}}
		if problems := checkOnlyTheReclaimerDeletes(accepted); len(problems) != 0 {
			t.Errorf("the rule objected to the accepted shape: %v", problems)
		}

		routes := surface{Callables: append(accepted.Callables,
			declaredCallable{File: "reclaimer/reclaimer.go", Owner: "Store", Name: "DeleteObject",
				Params: []declaredParam{{Name: "key", Type: "string"}}},
			declaredCallable{File: "reclaimer/reclaimer.go", Name: "DeleteAll"},
			declaredCallable{File: "reclaimer/reclaimer.go", Owner: "Reclaimer", Name: "DeleteByKey",
				Params: []declaredParam{{Name: "key", Type: "string"}}},
		)}
		joined := strings.Join(checkOnlyTheReclaimerDeletes(routes), "\n")
		for _, expected := range []string{
			"Store.DeleteObject is not an exact-generation delete on Store",
			"DeleteAll is a delete on a package function",
			"offers more than one delete",
			"Reclaimer.DeleteByKey takes a bare string parameter key",
			"Reclaimer.DeleteByKey does not take a hangar.TreeRef",
		} {
			if !strings.Contains(joined, expected) {
				t.Errorf("the rule did not object to %q. It reported:\n%s", expected, joined)
			}
		}
	})

	// The bare-string rule over the role types must catch a location by
	// *type*, not by the name somebody gave the parameter, in every spelling a
	// string travels in -- and must stop at the store seams, which are exactly
	// where the derived key is handed over.
	t.Run("a bare string on a role type is caught whatever it is called", func(t *testing.T) {
		seams := surface{
			Types: []declaredType{
				{File: "publisher/publisher.go", Name: "Store", Kind: "interface"},
				{File: "publisher/publisher.go", Name: "Handle", Kind: "interface"},
				{File: "inventory/inventory.go", Name: "Store", Kind: "interface"},
				{File: "inventory/inventory.go", Name: "Handle", Kind: "interface"},
				{File: "reclaimer/reclaimer.go", Name: "Store", Kind: "interface"},
				{File: "reclaimer/reclaimer.go", Name: "Handle", Kind: "interface"},
			},
			Callables: []declaredCallable{
				{File: "publisher/publisher.go", Owner: "Publisher", Name: "StatExactObject",
					Params: []declaredParam{{Name: "keys", Type: "[]string"}}},
				{File: "inventory/inventory.go", Owner: "Inventory", Name: "ListPage",
					Params: []declaredParam{{Name: "only", Type: "...string"}}},
				{File: "reclaimer/reclaimer.go", Owner: "Reclaimer", Name: "ObserveExactAbsence",
					Params: []declaredParam{{Name: "at", Type: "*string"}}},
				{File: "reclaimer/reclaimer.go", Name: "New",
					Params: []declaredParam{{Name: "labels", Type: "map[string]string"}}},
				{File: "publisher/publisher.go", Owner: "Publisher", Name: "EnsureObject",
					Params: []declaredParam{{Name: "where", Type: "hangar.Scope"}}},
				// The seam, which hands the derived key over and is exempt.
				{File: "publisher/publisher.go", Owner: "Store", Name: "Object",
					Params: []declaredParam{{Name: "bucket", Type: "string"}, {Name: "key", Type: "string"}}},
			},
		}
		problems := checkRoleTypesTakeNoCallerChosenLocation(seams)
		joined := strings.Join(problems, "\n")
		for _, expected := range []string{
			"publisher.Publisher.StatExactObject takes a bare string parameter keys ([]string)",
			"inventory.Inventory.ListPage takes a bare string parameter only (...string)",
			"reclaimer.Reclaimer.ObserveExactAbsence takes a bare string parameter at (*string)",
			"reclaimer.New takes a bare string parameter labels (map[string]string)",
			"publisher.Publisher.EnsureObject accepts a hangar.Scope parameter",
		} {
			if !strings.Contains(joined, expected) {
				t.Errorf("the rule did not object to %q. It reported:\n%s", expected, joined)
			}
		}
		if strings.Contains(joined, "Store.Object") {
			t.Errorf("the rule objected to the store seam, which is where the derived key is "+
				"handed over. It reported:\n%s", joined)
		}
	})

	// And a store seam that stopped existing is reported, so the exemption
	// cannot outlive the interface it excuses.
	t.Run("a missing store seam is reported", func(t *testing.T) {
		problems := checkRoleTypesTakeNoCallerChosenLocation(surface{
			Callables: []declaredCallable{{File: "publisher/publisher.go", Owner: "Publisher", Name: "StatExactObject"}},
		})
		joined := strings.Join(problems, "\n")
		if !strings.Contains(joined, "storeSeams exempts publisher/Store") {
			t.Errorf("the rule did not notice a missing store seam. It reported:\n%s", joined)
		}
	})

	// The cmd/ half, over a fabricated build graph: a principal that links a
	// second role, and one that links none.
	t.Run("a principal binary linking the wrong role is caught", func(t *testing.T) {
		if problems := checkEachPrincipalLinksExactlyItsRole(nil); len(problems) == 0 {
			t.Fatal("the principal rule passed over an empty listing")
		}
		listing := map[string][]string{
			"cmd/hangar-output-daemon":    {rolePackagePrefix + "publisher", rolePackagePrefix + "reclaimer"},
			"cmd/hangar-output-inventory": {"fmt"},
			"cmd/hangar-output-reclaimer": {rolePackagePrefix + "reclaimer"},
		}
		joined := strings.Join(checkEachPrincipalLinksExactlyItsRole(listing), "\n")
		for _, expected := range []string{
			"cmd/hangar-output-daemon links " + rolePackagePrefix + "reclaimer as well as its own role",
			"cmd/hangar-output-inventory does not link " + rolePackagePrefix + "inventory",
		} {
			if !strings.Contains(joined, expected) {
				t.Errorf("the rule did not object to %q. It reported:\n%s", expected, joined)
			}
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
