package db_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The lock order is an API, not a convention, and this is what makes that
// sentence enforceable.
//
// atc/db/hangar_output_locks.go is the only production file allowed to lock a
// Hangar row. A second place that took FOR UPDATE or FOR NO KEY UPDATE on a
// hangar_ table would be a second lock order -- and a second lock order is a
// deadlock nobody wrote down. The rule is derived from the source rather than
// from a list, so a new file inherits it without anyone remembering to add it.
const hangarLockHelper = "atc/db/hangar_output_locks.go"

// hangarLockSite is one string literal in production code that locks a Hangar
// row.
type hangarLockSite struct {
	File    string
	Snippet string
}

// hangarSourceInventory is what the scan found, including the counts that make
// a passing result mean something.
type hangarSourceInventory struct {
	Files    int
	Literals int
	Sites    []hangarLockSite
}

func scanHangarLockSites(t *testing.T, roots ...string) hangarSourceInventory {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	inventory := hangarSourceInventory{}
	fileSet := token.NewFileSet()

	type parsedFile struct {
		Relative  string
		Directory string
		File      *ast.File
	}
	var files []parsedFile

	for _, root := range roots {
		err := filepath.Walk(filepath.Join(repoRoot, root), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if name := info.Name(); name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}

				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			parsed, err := parser.ParseFile(fileSet, path, nil, 0)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			files = append(files, parsedFile{
				Relative:  filepath.ToSlash(relative),
				Directory: filepath.Dir(path),
				File:      parsed,
			})

			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	// String constants, per package directory, so that a lock clause assembled
	// out of a name declared elsewhere in the package can still be read.
	constants := map[string]map[string]string{}
	for _, file := range files {
		if constants[file.Directory] == nil {
			constants[file.Directory] = map[string]string{}
		}
		for name, value := range hangarStringDeclarations(file.File) {
			constants[file.Directory][name] = value
		}
	}

	seen := map[hangarLockSite]bool{}
	record := func(file parsedFile, rendered string) {
		site := hangarLockSite{File: file.Relative, Snippet: firstLine(rendered)}
		if seen[site] {
			return
		}
		seen[site] = true
		inventory.Sites = append(inventory.Sites, site)
	}

	for _, file := range files {
		inventory.Files++
		known := constants[file.Directory]

		ast.Inspect(file.File, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.BasicLit:
				if expression.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(expression.Value)
				if err != nil {
					return true
				}
				inventory.Literals++
				if hangarLocksARow(value) {
					record(file, value)
				}
			case *ast.CallExpr:
				// A format string and its arguments are one statement, whatever
				// the source does with the pieces.
				selector, ok := expression.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Sprintf" {
					return true
				}
				if rendered := hangarRenderString(expression, known); hangarLocksARow(rendered) {
					record(file, rendered)
				}
			case *ast.BinaryExpr:
				if expression.Op != token.ADD {
					return true
				}
				if rendered := hangarRenderString(expression, known); hangarLocksARow(rendered) {
					record(file, rendered)
				}
			}

			return true
		})
	}

	return inventory
}

// hangarStringDeclarations reads the file's string constants and vars.
func hangarStringDeclarations(file *ast.File) map[string]string {
	declared := map[string]string{}

	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for index, name := range spec.Names {
			if index >= len(spec.Values) {
				continue
			}
			literal, ok := spec.Values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			declared[name.Name] = value
		}

		return true
	})

	return declared
}

// hangarRenderString flattens an expression into everything it can say.
//
// It is deliberately not an evaluator: it collects every string literal and
// every name it can resolve anywhere inside the expression and joins them. That
// over-approximates what the statement will be, which is the right direction --
// the rule is asking whether a lock clause and a Hangar table meet in one
// statement, and a rendering that saw only half of it would answer no for the
// exact shape this exists to catch.
func hangarRenderString(expression ast.Node, known map[string]string) string {
	var parts []string

	ast.Inspect(expression, func(node ast.Node) bool {
		switch inner := node.(type) {
		case *ast.BasicLit:
			if inner.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(inner.Value); err == nil {
				parts = append(parts, value)
			}
		case *ast.Ident:
			if value, ok := known[inner.Name]; ok {
				parts = append(parts, value)
			}
		}

		return true
	})

	return strings.Join(parts, " ")
}

// hangarRenderStatement renders an assembled statement in its own order.
//
// hangarRenderString above answers "do a lock clause and a Hangar table appear
// together", for which an unordered join is enough. The table rule asks a
// harder question -- *which* relation does this statement name -- and that one
// needs the pieces in the places they will actually occupy. `fmt.Sprintf("…
// FROM %s … FOR UPDATE", consumerTable)` joined out of order reads as a
// statement whose only relation is `%s`, which is exactly the shape review
// finding R2-1 found evading all three structural rules.
//
// So this substitutes rather than concatenates: the format verbs of a Sprintf
// are filled, in order, from the arguments this file can resolve, and a `+`
// chain is joined left to right. An argument it cannot resolve stays as its
// verb, which keeps the answer honest -- an unreadable table is reported by
// TestARuntimeTableIsAlwaysNamedByALiteral, not silently rendered into
// something harmless.
func hangarRenderStatement(expression ast.Node, known map[string]string) string {
	switch node := expression.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return ""
		}
		value, err := strconv.Unquote(node.Value)
		if err != nil {
			return ""
		}

		return value

	case *ast.Ident:
		return known[node.Name]

	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return ""
		}

		return hangarRenderStatement(node.X, known) + hangarRenderStatement(node.Y, known)

	case *ast.CallExpr:
		selector, ok := node.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Sprintf" || len(node.Args) == 0 {
			return ""
		}
		format := hangarRenderStatement(node.Args[0], known)
		if format == "" {
			return ""
		}

		return hangarFillVerbs(format, node.Args[1:], known)
	}

	return ""
}

// hangarFillVerbs replaces each %-verb in order with the argument's value.
func hangarFillVerbs(format string, args []ast.Expr, known map[string]string) string {
	var (
		rendered strings.Builder
		next     int
	)
	for index := 0; index < len(format); index++ {
		if format[index] != '%' || index+1 >= len(format) {
			rendered.WriteByte(format[index])

			continue
		}
		verb := format[index+1]
		if verb == '%' {
			rendered.WriteString("%%")
			index++

			continue
		}
		substituted := ""
		if next < len(args) {
			substituted = hangarRenderStatement(args[next], known)
		}
		next++
		if substituted == "" {
			// Unresolvable: leave the verb standing so the statement still
			// says "a table is substituted here".
			rendered.WriteByte('%')
			rendered.WriteByte(verb)
			index++

			continue
		}
		rendered.WriteString(substituted)
		index++
	}

	return rendered.String()
}

// hangarLocksARow is the rule itself, over one rendered statement, so that the
// same predicate drives the real scan and the fixtures below.
func hangarLocksARow(statement string) bool {
	upper := strings.ToUpper(statement)
	locking := false
	for _, clause := range []string{
		"FOR UPDATE", "FOR NO KEY UPDATE", "FOR SHARE", "FOR KEY SHARE", "LOCK TABLE",
	} {
		if strings.Contains(upper, clause) {
			locking = true

			break
		}
	}
	if !locking {
		return false
	}

	return strings.Contains(strings.ToLower(statement), "hangar_")
}

func firstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}

	return strings.TrimSpace(value)
}

func TestHangarRowLocksAreConfinedToTheOneHelper(t *testing.T) {
	inventory := scanHangarLockSites(t, "atc", "cmd", "hangar", "topgun", "skymarshal", "go-concourse")

	// A rule that scanned nothing would pass forever. These three numbers are
	// what make the pass mean "the source was read and this is what it says".
	if inventory.Files < 300 {
		t.Fatalf("the scan parsed %d production Go files, which is too few to have covered "+
			"atc/; the rule would be passing vacuously", inventory.Files)
	}
	if inventory.Literals < 1000 {
		t.Fatalf("the scan inspected %d string literals, which is too few to have covered the "+
			"SQL in atc/db", inventory.Literals)
	}
	if len(inventory.Sites) == 0 {
		t.Fatal("the scan found no Hangar row lock anywhere, including in " + hangarLockHelper +
			". Either the helper stopped locking rows or the rule stopped recognising them; " +
			"in both cases this test is guarding nothing.")
	}

	for _, site := range inventory.Sites {
		if site.File == hangarLockHelper {
			continue
		}
		t.Errorf("%s locks a Hangar row: %q.\n\nOnly %s may. Every Hangar transaction takes one "+
			"complete suffix after the caller's own domain prefix -- logical reservations, then "+
			"exact lifecycles, then capture rows, then subordinate rows -- and a second place "+
			"that locks these tables is a second order. Two orders is a deadlock nobody wrote "+
			"down. Call LockHangarSuffix with the identities you need.",
			site.File, site.Snippet, hangarLockHelper)
	}
}

// TestTheHangarLockRuleIsNotVacuous drives the predicate with the shapes it has
// to catch and the shapes it must not.
func TestTheHangarLockRuleIsNotVacuous(t *testing.T) {
	t.Run("it catches every lock strength", func(t *testing.T) {
		for _, statement := range []string{
			"SELECT 1 FROM hangar_claims WHERE claim_id = $1 FOR UPDATE",
			"SELECT 1 FROM hangar_exact_lifecycles WHERE id = $1 FOR NO KEY UPDATE",
			"select id from hangar_logical_reservations for share",
			"SELECT 1 FROM hangar_read_leases FOR KEY SHARE",
			// Case and line breaks are how a second lock site would actually
			// be written, not how a rule author imagines it.
			"\n\t\tSELECT 1\n\t\tFROM hangar_capture_reservations\n\t\tWHERE reservation_id = $1\n\t\tfor update\n",
		} {
			if !hangarLocksARow(statement) {
				t.Errorf("the rule did not recognise %q as locking a Hangar row", statement)
			}
		}
	})

	t.Run("it leaves other tables and other statements alone", func(t *testing.T) {
		for _, statement := range []string{
			"SELECT 1 FROM builds WHERE id = $1 FOR UPDATE",
			"SELECT state FROM hangar_exact_lifecycles WHERE id = $1",
			"UPDATE hangar_claims SET released_at = now() WHERE claim_id = $1",
			"INSERT INTO hangar_output_receipts (reservation_id) VALUES ($1)",
		} {
			if hangarLocksARow(statement) {
				t.Errorf("the rule objected to %q, which locks no Hangar row", statement)
			}
		}
	})

	// The shape that evaded the per-literal rule: the lock clause and the
	// table are both there, in one statement, written as two pieces.
	t.Run("it catches a lock assembled out of pieces", func(t *testing.T) {
		source := `package db

import "fmt"

const probeTable = "hangar_claims"

func probe(tx Tx) {
	_, _ = tx.Exec(fmt.Sprintf("SELECT 1 FROM %s WHERE claim_id = $1 FOR UPDATE", probeTable))
	_, _ = tx.Exec("SELECT 1 FROM hangar_read_leases WHERE read_lease_id = $1 " + "FOR UPDATE")
}
`
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, "probe.go", source, 0)
		if err != nil {
			t.Fatalf("parsing the fixture: %v", err)
		}
		known := hangarStringDeclarations(parsed)

		found := 0
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.CallExpr:
				selector, ok := expression.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Sprintf" {
					return true
				}
			case *ast.BinaryExpr:
				if expression.Op != token.ADD {
					return true
				}
			default:
				return true
			}
			if hangarLocksARow(hangarRenderString(node, known)) {
				found++
			}

			return true
		})

		if found != 2 {
			t.Errorf("the rule recognised %d of the 2 split lock sites in the fixture. A "+
				"`FROM %%s ... FOR UPDATE` with the table in a constant, and a clause "+
				"concatenated onto its own statement, are both a second lock order -- and a "+
				"second lock order is a deadlock nobody wrote down.", found)
		}
	})

	t.Run("it leaves a lock clause that names no Hangar table alone", func(t *testing.T) {
		// `Suffix("FOR SHARE")` on a builder over `pipelines` is how the rest
		// of atc/db locks its own rows. Reading the pieces together must not
		// turn those into Hangar lock sites.
		source := `package db

func other(builder Builder) {
	_ = builder.From("pipelines").Suffix("FOR UPDATE")
	_ = "SELECT 1 FROM builds WHERE id = $1 " + "FOR UPDATE"
}
`
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, "other.go", source, 0)
		if err != nil {
			t.Fatalf("parsing the fixture: %v", err)
		}
		known := hangarStringDeclarations(parsed)

		ast.Inspect(parsed, func(node ast.Node) bool {
			expression, ok := node.(*ast.BinaryExpr)
			if !ok || expression.Op != token.ADD {
				return true
			}
			if hangarLocksARow(hangarRenderString(node, known)) {
				t.Error("the rule objected to a lock on a table that is not Hangar's")
			}

			return true
		})
	})

	// And the whole scan, driven over a source tree that violates it.
	t.Run("it objects to a second lock site", func(t *testing.T) {
		inventory := hangarSourceInventory{
			Files:    400,
			Literals: 5000,
			Sites: []hangarLockSite{
				{File: hangarLockHelper, Snippet: "SELECT 1 FROM hangar_claims ... FOR UPDATE"},
				{File: "atc/db/somewhere_else.go", Snippet: "SELECT 1 FROM hangar_claims ... FOR UPDATE"},
			},
		}

		var offenders []string
		for _, site := range inventory.Sites {
			if site.File != hangarLockHelper {
				offenders = append(offenders, site.File)
			}
		}
		if len(offenders) != 1 || offenders[0] != "atc/db/somewhere_else.go" {
			t.Errorf("the rule did not name the second lock site; it reported %v", offenders)
		}
	})
}

// The other half of AC 11's non-interference clause: "Hangar never acquires a
// consumer-domain row."
//
// The behavioural spec beside this one can only see what a lock looks like from
// outside the transaction, and PostgreSQL holds one pg_locks row per (relation,
// mode, pid) with row locks living in tuple headers -- so a consumer's own FOR
// UPDATE and a Hangar FOR UPDATE on the same row are the same row in that view.
// This half is therefore structural: the plane's production code may not name a
// table outside the plane at all, in any statement, locking or not.
const hangarTablePrefix = "hangar_"

// hangarTablesNamedBy returns every table an SQL statement names.
//
// It reads the keywords that introduce a relation and takes the token after
// each. `FOR UPDATE`, `FOR NO KEY UPDATE` and `ON CONFLICT ... DO UPDATE SET`
// contain the word UPDATE and introduce nothing, so they are excluded by what
// stands beside them rather than by a list of statements to skip.
func hangarTablesNamedBy(statement string) []string {
	if !hangarLooksLikeSQL(statement) {
		return nil
	}
	flattened := strings.NewReplacer("\n", " ", "\t", " ", ",", " ", ";", " ").Replace(statement)
	fields := strings.Fields(flattened)

	var tables []string
	for index, field := range fields {
		switch strings.ToUpper(field) {
		case "FROM", "JOIN", "INTO":
		case "UPDATE":
			if index == 0 {
				break
			}
			switch strings.ToUpper(fields[index-1]) {
			case "FOR", "KEY", "DO":
				continue
			}
		default:
			continue
		}
		if index+1 >= len(fields) {
			continue
		}
		name := strings.Trim(fields[index+1], "()")
		if name == "" || strings.HasPrefix(name, "(") || strings.EqualFold(name, "SET") {
			continue
		}
		tables = append(tables, name)
	}

	return tables
}

// hangarLooksLikeSQL keeps English prose out of the rule.
//
// These files carry long refusal messages, and a sentence like "protects its
// correlation from adoption" has the shape of a FROM clause without being one.
// A statement is SQL when it names an SQL verb in the case SQL is written in
// here, which is the same convention the lock-clause rule above relies on.
func hangarLooksLikeSQL(statement string) bool {
	for _, verb := range []string{"SELECT ", "INSERT INTO", "UPDATE ", "DELETE FROM"} {
		if strings.Contains(statement, verb) {
			return true
		}
	}

	return false
}

// hangarPlaneFiles is the production source of the output plane.
func hangarPlaneFiles(t *testing.T) (map[string]*ast.File, *token.FileSet, string) {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	directory := filepath.Dir(thisFile)
	entries, err := filepath.Glob(filepath.Join(directory, "hangar_output_*.go"))
	if err != nil {
		t.Fatalf("globbing the plane's files: %v", err)
	}

	fileSet := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, entry := range entries {
		if strings.HasSuffix(entry, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fileSet, entry, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", entry, err)
		}
		files[filepath.Base(entry)] = parsed
	}
	if len(files) < 4 {
		t.Fatalf("found %d production files matching hangar_output_*.go, which is too few to be "+
			"the plane; this rule would be passing vacuously", len(files))
	}

	return files, fileSet, directory
}

func TestTheOutputPlaneNamesNoTableOutsideItself(t *testing.T) {
	files, _, _ := hangarPlaneFiles(t)

	// The package's string declarations, so a statement assembled out of a
	// constant declared in another file of the same package can still be read.
	known := map[string]string{}
	for _, parsed := range files {
		for name, value := range hangarStringDeclarations(parsed) {
			known[name] = value
		}
	}

	named := 0
	for name, parsed := range files {
		check := func(statement string) {
			for _, table := range hangarTablesNamedBy(statement) {
				// A table substituted at run time and still unresolved is
				// checked at its call sites below, not here: this statement
				// does not know what it will say.
				if strings.HasPrefix(table, "%") {
					continue
				}
				named++
				if !strings.HasPrefix(table, hangarTablePrefix) {
					t.Errorf("atc/db/%s names the table %q.\n\nThe output plane's production code "+
						"names only its own tables. A consumer's rows are the consumer's -- Hangar "+
						"cannot know what locks the caller already holds on them, so touching one "+
						"is how an already-held domain lock gets acquired or inverted, which AC 11 "+
						"forbids. Statement: %q", name, table, firstLine(statement))
				}
			}
		}

		ast.Inspect(parsed, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.BasicLit:
				if expression.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(expression.Value)
				if err != nil {
					return true
				}
				check(value)
			case *ast.CallExpr:
				// R2-1: a statement assembled by Sprintf is one statement,
				// whatever the source does with the pieces. Reading only the
				// format literal reports its relation as `%s` and lets a
				// consumer table named by a constant through all three rules.
				selector, ok := expression.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Sprintf" {
					return true
				}
				check(hangarRenderStatement(expression, known))

				// Rendered whole; descending would read the format literal
				// again and, for a nested `+` chain, once per node.
				return false
			case *ast.BinaryExpr:
				if expression.Op != token.ADD {
					return true
				}
				check(hangarRenderStatement(expression, known))

				return false
			}

			return true
		})
	}

	if named < 15 {
		t.Fatalf("the rule found %d table names across the plane's production files, which is too "+
			"few to have read the SQL; either the extractor stopped recognising relations or the "+
			"files moved", named)
	}
}

// TestARuntimeTableIsAlwaysNamedByALiteral closes the one gap the rule above
// leaves: `UPDATE %s` says nothing about what it will update, so the value has
// to be a literal the guard can read at the call site.
func TestARuntimeTableIsAlwaysNamedByALiteral(t *testing.T) {
	files, _, _ := hangarPlaneFiles(t)

	// Functions that take a table by name, and the argument position it is in.
	positions := map[string]int{}
	for _, parsed := range files {
		ast.Inspect(parsed, func(node ast.Node) bool {
			function, ok := node.(*ast.FuncDecl)
			if !ok || function.Type.Params == nil {
				return true
			}
			index := 0
			for _, field := range function.Type.Params.List {
				for _, name := range field.Names {
					if name.Name == "table" {
						positions[function.Name.Name] = index
					}
					index++
				}
			}

			return true
		})
	}
	if len(positions) == 0 {
		// Nothing takes a table by name, so nothing can substitute one. The
		// rule above is then the whole rule.
		return
	}

	checked := 0
	for name, parsed := range files {
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			var called string
			switch function := call.Fun.(type) {
			case *ast.Ident:
				called = function.Name
			case *ast.SelectorExpr:
				called = function.Sel.Name
			default:
				return true
			}
			index, takesTable := positions[called]
			if !takesTable || index >= len(call.Args) {
				return true
			}
			checked++

			literal, ok := call.Args[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Errorf("atc/db/%s calls %s with a table this rule cannot read. A table name "+
					"substituted into SQL has to be a string literal here, or nothing can say "+
					"which tables the plane touches.", name, called)

				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			if !strings.HasPrefix(value, hangarTablePrefix) {
				t.Errorf("atc/db/%s calls %s with the table %q, which is not the output plane's",
					name, called, value)
			}

			return true
		})
	}
	if checked == 0 {
		t.Errorf("%d function(s) take a table by name and no call site was found; either the "+
			"callers moved or this rule stopped recognising them", len(positions))
	}
}

// TestTheTableRuleIsNotVacuous drives the extractor with the shapes it has to
// read and the shapes it must not mistake for a relation.
func TestTheTableRuleIsNotVacuous(t *testing.T) {
	for statement, expected := range map[string][]string{
		"SELECT 1 FROM hangar_claims WHERE claim_id = $1 FOR UPDATE":              {"hangar_claims"},
		"SELECT 1 FROM opaque_consumer_bindings WHERE binding_id = $1 FOR UPDATE": {"opaque_consumer_bindings"},
		"UPDATE hangar_claims SET released_at = now()":                            {"hangar_claims"},
		"INSERT INTO hangar_read_leases (read_lease_id) VALUES ($1)":              {"hangar_read_leases"},
		"UPDATE %s SET release_acknowledged_at = now()":                           {"%s"},
		"SELECT now()": nil,
		"INSERT INTO hangar_claims (claim_id) VALUES ($1) ON CONFLICT (claim_id) DO UPDATE SET x = 1": {"hangar_claims"},
		"SELECT 1 FROM hangar_capture_reservations r LEFT JOIN hangar_capture_attempt_leases l ON l.id = r.id FOR NO KEY UPDATE": {
			"hangar_capture_reservations", "hangar_capture_attempt_leases",
		},
	} {
		found := hangarTablesNamedBy(statement)
		if len(found) != len(expected) {
			t.Errorf("read %v from %q, expected %v", found, statement, expected)

			continue
		}
		for index := range found {
			if found[index] != expected[index] {
				t.Errorf("read %v from %q, expected %v", found, statement, expected)

				break
			}
		}
	}

	// M-evade-consumer, the shape review finding R2-1 found evading all three
	// structural rules: the lock clause and the consumer table are both in one
	// statement, written as two pieces, and the piece naming the relation is a
	// constant. The lock rule does not object -- correctly, it is not a Hangar
	// table -- and the runtime-table rule does not follow it, because the
	// function it is written in takes no parameter named `table`. This rule is
	// the one that has to see it.
	t.Run("it reads a consumer table assembled out of pieces", func(t *testing.T) {
		source := `package db

import "fmt"

const hangarConsumerBindingsTable = "opaque_consumer_bindings"
const hangarClaimsTable = "hangar_claims"

func probe(tx Tx) {
	_, _ = tx.Exec(fmt.Sprintf("SELECT 1 FROM %s WHERE binding_id = $1 FOR UPDATE", hangarConsumerBindingsTable))
	_, _ = tx.Exec("SELECT 1 FROM " + hangarConsumerBindingsTable + " WHERE binding_id = $1")
	_, _ = tx.Exec(fmt.Sprintf("SELECT 1 FROM %s WHERE claim_id = $1 FOR UPDATE", hangarClaimsTable))
}
`
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, "probe.go", source, 0)
		if err != nil {
			t.Fatalf("parsing the fixture: %v", err)
		}
		known := hangarStringDeclarations(parsed)

		var read []string
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.CallExpr:
				selector, ok := expression.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Sprintf" {
					return true
				}
			case *ast.BinaryExpr:
				if expression.Op != token.ADD {
					return true
				}
			default:
				return true
			}
			read = append(read, hangarTablesNamedBy(hangarRenderStatement(node, known))...)

			return false
		})

		outside := 0
		for _, table := range read {
			if !strings.HasPrefix(table, hangarTablePrefix) && !strings.HasPrefix(table, "%") {
				outside++
			}
		}
		if outside != 2 {
			t.Errorf("the rule read %v from the fixture and found %d table(s) outside the plane, "+
				"expected 2. A `FROM %%s` whose relation is a constant, and a relation "+
				"concatenated onto its own statement, both name a consumer table in one "+
				"statement -- and the behavioural spec cannot see either, because a row lock in "+
				"the consumer's own mode is invisible in pg_locks.", read, outside)
		}
		if len(read) != 3 {
			t.Errorf("the rule read %v; it must still read the plane's own table out of the "+
				"third statement, or a green here would mean it stopped reading rather than "+
				"that the statements are clean", read)
		}
	})
}

// Every write to hangar_read_leases takes the read-lease suffix.
//
// The rule above says only the helper may LOCK a Hangar row. This is the other
// half, and the one that was silently unmet: a statement that WRITES a Hangar
// row without having entered the suffix is not a second lock order, it is no
// lock order -- the row is taken at the write's own moment, in whatever order
// the writes happen to arrive.
//
// Renew, release and abandoned-lease recovery all issued bare UPDATEs. I traced
// renew-versus-reclaim in both arrival orders and there is no correctness hole
// today: hangar_reclaim_exclusion is a DEFERRED trigger that fires on the
// read-lease UPDATE and on the reclaim-job INSERT, each commit's trigger sees
// the other's committed row, and the second to commit rolls back. But "the
// schema happens to catch it" is not the rule requirement 33 states, and a rule
// that holds by accident is one the next statement breaks. The suffix is an API.
//
// The check is per FUNCTION rather than per file, because the suffix has to be
// entered by the transaction that writes, not somewhere in the same package.
func TestEveryReadLeaseWriteTakesTheReadLeaseSuffix(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	directory := filepath.Dir(thisFile)

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("reading atc/db: %v", err)
	}

	writes := func(statement string) bool {
		lowered := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		for _, verb := range []string{"update hangar_read_leases", "insert into hangar_read_leases",
			"delete from hangar_read_leases"} {
			if strings.Contains(lowered, verb) {
				return true
			}
		}

		return false
	}

	scanned, writers := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "hangar_output_") ||
			!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// The helper is where the row lock lives; it locks rather than writes.
		if filepath.ToSlash(filepath.Join("atc/db", name)) == hangarLockHelper {
			continue
		}

		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, filepath.Join(directory, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++

		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}

			wrote, locked := false, false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch expression := node.(type) {
				case *ast.BasicLit:
					if expression.Kind == token.STRING {
						if value, err := strconv.Unquote(expression.Value); err == nil &&
							writes(value) {
							wrote = true
						}
					}
				case *ast.CallExpr:
					identifier, ok := expression.Fun.(*ast.Ident)
					if !ok || identifier.Name != "LockHangarSuffix" {
						return true
					}
					for _, argument := range expression.Args {
						composite, ok := argument.(*ast.CompositeLit)
						if !ok {
							continue
						}
						for _, element := range composite.Elts {
							pair, ok := element.(*ast.KeyValueExpr)
							if !ok {
								continue
							}
							if key, ok := pair.Key.(*ast.Ident); ok &&
								key.Name == "ReadLeases" {
								locked = true
							}
						}
					}
				}

				return true
			})

			if !wrote {
				continue
			}
			writers++
			if !locked {
				t.Errorf("atc/db/%s: %s writes hangar_read_leases without entering the suffix "+
					"for the lease it writes.\n\nRequirement 33 and \"lock order is an API, not a "+
					"convention\" put warrant and read-lease work inside one complete suffix. A bare "+
					"UPDATE takes the row at the write's own moment, in whatever order the writes "+
					"arrive; that the deferred hangar_reclaim_exclusion trigger happens to catch "+
					"the race today is the schema's doing, not this transaction's. Call "+
					"LockHangarSuffix with ReadLeases: the identities this function writes.",
					name, function.Name.Name)
			}
		}
	}

	if scanned == 0 {
		t.Fatal("this guard read no atc/db/hangar_output_*.go file, so it is passing vacuously")
	}
	if writers < 4 {
		t.Errorf("this guard found %d function(s) writing hangar_read_leases; the plane has at "+
			"least four (acquire, renew, release, close-abandoned), so either they moved or the "+
			"predicate stopped recognising them", writers)
	}
}

// Every production Hangar output transaction is the TYPED one.
//
// Two of this plane's constraint triggers are DEFERRED, so their refusals
// arrive at COMMIT and nowhere earlier. db.HangarOutputTx is what maps that
// commit's SQLSTATE onto the output leaf's vocabulary, and a coordinator handed
// an unmapped commit failure reads a refusal ("stop, or change something
// first") as a lost answer ("ask again with the same identity") -- against an
// at-risk lifetime policy only an attestor can change, that is a retry loop
// with no exit.
//
// The mapping moved into one adapter so that the three places which hand the
// coordinator a transaction stopped carrying three copies of it. What they
// still carry is three copies of the DECISION TO APPLY IT, and nothing saw
// that: the specs commit through the harness's own transactor and brine through
// its own, both of which wrap independently, so the ATC could ship
// `return tx, nil` with every tier green. That is the mutation this guard is
// written against, and it is the one production wiring site R1-F1 was about.
//
// It is derived from the source rather than from a list of files, so a SECOND
// wiring site -- Phase 8 serves LeaseControl from the ATC -- inherits the rule
// without anyone remembering it, and the floor below fails if it is added
// without the wrapper.
func TestEveryProductionHangarOutputTransactionIsTyped(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))

	// Whether a result type is the coordinator's Transaction port. Inside
	// package hangaroutput it is spelled bare; everywhere else it is qualified.
	isTransactionPort := func(expression ast.Expr) bool {
		switch typed := expression.(type) {
		case *ast.SelectorExpr:
			identifier, ok := typed.X.(*ast.Ident)

			return ok && identifier.Name == "hangaroutput" && typed.Sel.Name == "Transaction"
		case *ast.Ident:
			return typed.Name == "Transaction"
		}

		return false
	}

	// Whether an expression constructs db.HangarOutputTx. A composite literal
	// is what the three adapters write; the pointer and conversion forms are
	// admitted so that the rule is about the TYPE rather than about a spelling.
	var typedTransaction func(expression ast.Expr) bool
	typedTransaction = func(expression ast.Expr) bool {
		switch typed := expression.(type) {
		case *ast.CompositeLit:
			return typedTransaction(typed.Type)
		case *ast.UnaryExpr:
			return typed.Op == token.AND && typedTransaction(typed.X)
		case *ast.SelectorExpr:
			return typed.Sel.Name == "HangarOutputTx"
		case *ast.Ident:
			return typed.Name == "HangarOutputTx"
		}

		return false
	}

	isNil := func(expression ast.Expr) bool {
		identifier, ok := expression.(*ast.Ident)

		return ok && identifier.Name == "nil"
	}

	var sites []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch name := entry.Name(); {
			case name == "vendor", name == "node_modules", name == "testdata",
				strings.HasPrefix(name, "."):
				return fs.SkipDir
			}

			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		// The cheap filter first: a function returning the port has to name the
		// type, and reading 1,800 files is cheaper than parsing them. Package
		// hangaroutput's own files are read whole, because there the type is
		// spelled without its package.
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(source, []byte("hangaroutput.Transaction")) &&
			filepath.Base(filepath.Dir(path)) != "hangaroutput" {
			return nil
		}
		scanned++

		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, source, 0)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)

		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || function.Type.Results == nil ||
				len(function.Type.Results.List) == 0 ||
				!isTransactionPort(function.Type.Results.List[0].Type) {
				continue
			}
			sites = append(sites, relative+":"+function.Name.Name)

			ast.Inspect(function.Body, func(node ast.Node) bool {
				// A nested function literal answers for itself; the results
				// above are this function's.
				if _, ok := node.(*ast.FuncLit); ok {
					return false
				}
				statement, ok := node.(*ast.ReturnStmt)
				if !ok || len(statement.Results) == 0 {
					return true
				}
				returned := statement.Results[0]
				if isNil(returned) || typedTransaction(returned) {
					return true
				}
				t.Errorf("%s: %s hands the output coordinator a transaction that is not "+
					"db.HangarOutputTx at %s.\n\nTwo of this plane's constraint triggers are "+
					"DEFERRED: their refusals arrive at COMMIT, and a raw transaction's Commit "+
					"answers an unclassified driver error. The coordinator reads that as a LOST "+
					"answer and retries the same identity against a refusal only an attestor can "+
					"lift. Wrap it: db.HangarOutputTx{Tx: tx}.", relative, function.Name.Name,
					fileSet.Position(statement.Pos()))

				return true
			})
		}

		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if scanned == 0 {
		t.Fatal("this guard read no file naming hangaroutput.Transaction, so it is passing " +
			"vacuously -- the port was renamed, or the walk no longer reaches the tree")
	}
	// The floor. Today: the ATC's own wiring and the brine harness's. Phase 8
	// adds a second ATC site (serving LeaseControl), which raises it.
	if len(sites) < 2 {
		t.Errorf("this guard found %d production implementation(s) of the transaction port %v; "+
			"there are at least two (the ATC's wiring and brine's), so either they moved or the "+
			"result-type predicate stopped recognising them", len(sites), sites)
	}
	atc := false
	for _, site := range sites {
		if strings.HasPrefix(site, "atc/atccmd/") {
			atc = true
		}
	}
	if !atc {
		t.Errorf("no production implementation of the transaction port lives in atc/atccmd; "+
			"the ATC's own wiring is the site a green test suite cannot see, because every "+
			"tier commits through its own adapter. Found: %v", sites)
	}
}

// THE SAME RULE, FOR THE LOCKS NOBODY WROTE DOWN.
//
// hangarLocksARow above recognises a statement that ASKS for a row lock. That
// is not the same set as the statements that TAKE one: PostgreSQL acquires FOR
// NO KEY UPDATE on every row an UPDATE touches and FOR UPDATE on every row a
// DELETE removes, so a bare `UPDATE hangar_logical_reservations` is a class-1
// acquisition with no lock clause anywhere in it. That is why two writers took
// the capture class before the logical class for ten phases with the guard
// above passing: the guard measured the route into the lock rather than the
// lock.
//
// So this rule reads the ORDER instead of the route. For every production
// function it replays, in source order, every class this transaction acquires
// -- explicitly through a HangarLockRequest, implicitly through a write against
// a table in a class -- following calls into the package's own helpers, because
// hangarTerminalizeLogical is a different function from the one that locks. A
// write against class C while a class above C is already held, and class C is
// not, is the inversion. That is exactly the shape of the blocker this rule was
// written for, and it reddens against it.

// hangarTableClass is the suffix class each locked Hangar table belongs to.
//
// It is checked against LockHangarSuffix's own statements below rather than
// trusted, so a fifth class, or a table moving between classes, cannot leave
// this list quietly stale.
var hangarTableClass = map[string]int{
	"hangar_logical_reservations": 1,
	"hangar_exact_lifecycles":     2,
	"hangar_capture_reservations": 3,
	"hangar_output_receipts":      4,
	"hangar_claims":               4,
	"hangar_read_leases":          4,
}

// hangarRequestFieldClass maps a HangarLockRequest field to the class it names.
var hangarRequestFieldClass = map[string]int{
	"Logical":    1,
	"Exact":      2,
	"Captures":   3,
	"Receipts":   4,
	"Claims":     4,
	"ReadLeases": 4,
}

// hangarAcquisition is one lock class a function takes, in source order.
type hangarAcquisition struct {
	Class    int
	Implicit bool
	Snippet  string
	File     string
	Line     int
	// Callee, when set, is a call into another function in the same package
	// whose acquisitions happen here.
	Callee string
}

// hangarWriteTable reads the Hangar table a statement WRITES, or "".
//
// An INSERT is not a row lock -- it creates a row nothing else can be holding
// -- unless it can fall through to an UPDATE, which ON CONFLICT ... DO UPDATE
// can. A SELECT is not a write. Everything else that names a table after UPDATE
// or DELETE FROM takes that table's row lock.
func hangarWriteTable(statement string) string {
	fields := strings.Fields(statement)
	for index, field := range fields {
		word := strings.ToUpper(field)
		var candidate string
		switch {
		case word == "UPDATE" && index+1 < len(fields):
			candidate = fields[index+1]
		case word == "DELETE" && index+2 < len(fields) &&
			strings.ToUpper(fields[index+1]) == "FROM":
			candidate = fields[index+2]
		default:
			continue
		}
		candidate = strings.Trim(strings.ToLower(candidate), "(),;`\"'")
		if _, classed := hangarTableClass[candidate]; classed {
			return candidate
		}
	}

	return ""
}

// hangarAcquisitionsByFunction replays every function in the scanned tree.
func hangarAcquisitionsByFunction(t *testing.T, roots ...string) map[string][]hangarAcquisition {
	t.Helper()

	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	fileSet := token.NewFileSet()

	type parsedFile struct {
		Relative  string
		Directory string
		File      *ast.File
	}
	var files []parsedFile

	for _, root := range roots {
		err := filepath.Walk(filepath.Join(repoRoot, root), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if name := info.Name(); name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}

				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			files = append(files, parsedFile{
				Relative:  filepath.ToSlash(relative),
				Directory: filepath.Dir(path),
				File:      parsed,
			})

			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	constants := map[string]map[string]string{}
	for _, file := range files {
		if constants[file.Directory] == nil {
			constants[file.Directory] = map[string]string{}
		}
		for name, value := range hangarStringDeclarations(file.File) {
			constants[file.Directory][name] = value
		}
	}

	acquisitions := map[string][]hangarAcquisition{}
	hangarDeclaredIn = map[string]string{}
	hangarExported = map[string]bool{}
	for _, file := range files {
		known := constants[file.Directory]
		for _, declaration := range file.File.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			key := file.Directory + "." + function.Name.Name
			hangarDeclaredIn[key] = file.Relative
			hangarExported[key] = function.Name.IsExported()
			var taken []hangarAcquisition

			record := func(acquisition hangarAcquisition, position token.Pos) {
				acquisition.File = file.Relative
				acquisition.Line = fileSet.Position(position).Line
				taken = append(taken, acquisition)
			}

			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch expression := node.(type) {
				case *ast.CompositeLit:
					name := ""
					switch typed := expression.Type.(type) {
					case *ast.Ident:
						name = typed.Name
					case *ast.SelectorExpr:
						name = typed.Sel.Name
					}
					if name != "HangarLockRequest" {
						return true
					}
					for _, element := range expression.Elts {
						pair, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						field, ok := pair.Key.(*ast.Ident)
						if !ok {
							continue
						}
						if class, named := hangarRequestFieldClass[field.Name]; named {
							record(hangarAcquisition{
								Class:   class,
								Snippet: "HangarLockRequest{" + field.Name + "}",
							}, expression.Pos())
						}
					}

					return true

				case *ast.BasicLit:
					if expression.Kind != token.STRING {
						return true
					}
					value, err := strconv.Unquote(expression.Value)
					if err != nil {
						return true
					}
					if table := hangarWriteTable(value); table != "" {
						record(hangarAcquisition{
							Class:    hangarTableClass[table],
							Implicit: true,
							Snippet:  firstLine(value),
						}, expression.Pos())
					}

					return true

				case *ast.BinaryExpr:
					if expression.Op != token.ADD {
						return true
					}
					rendered := hangarRenderStatement(expression, known)
					if table := hangarWriteTable(rendered); table != "" {
						record(hangarAcquisition{
							Class:    hangarTableClass[table],
							Implicit: true,
							Snippet:  firstLine(rendered),
						}, expression.Pos())
					}

					return false

				case *ast.CallExpr:
					callee := ""
					switch fun := expression.Fun.(type) {
					case *ast.Ident:
						callee = fun.Name
					case *ast.SelectorExpr:
						callee = fun.Sel.Name
					}
					if callee == "" || callee == "LockHangarSuffix" {
						return true
					}
					if callee == "Sprintf" {
						rendered := hangarRenderStatement(expression, known)
						if table := hangarWriteTable(rendered); table != "" {
							record(hangarAcquisition{
								Class:    hangarTableClass[table],
								Implicit: true,
								Snippet:  firstLine(rendered),
							}, expression.Pos())
						}

						return true
					}
					record(hangarAcquisition{Callee: file.Directory + "." + callee},
						expression.Pos())

					return true
				}

				return true
			})

			if len(taken) > 0 {
				acquisitions[key] = taken
			}
		}
	}

	return acquisitions
}

// hangarDeclaredIn and hangarExported are filled by the replay, so that the
// rules below can tell a helper inside the one lock file from a caller of it,
// and an entry point from an internal step.
var (
	hangarDeclaredIn = map[string]string{}
	hangarExported   = map[string]bool{}
)

// hangarExpand inlines intra-package calls so that a helper's writes are
// attributed to the transaction that entered it -- hangarTerminalizeLogical is
// a different function from the one that locks, and the transaction is the same
// one.
//
// A function declared inside the lock helper file is inlined as its SORTED set
// of classes instead of its statement sequence. That file is where the order is
// defined, so checking its own statements against the order would be circular;
// what a caller is entitled to assume is that entering it takes the classes it
// names, in order. Black-boxing it also keeps the rule sharp: a class the
// helper stops naming disappears from every caller's held set at once.
func hangarExpand(key string, byFunction map[string][]hangarAcquisition, seen map[string]bool, depth int) []hangarAcquisition {
	if depth > 6 || seen[key] {
		return nil
	}
	seen[key] = true
	defer delete(seen, key)

	var flattened []hangarAcquisition
	for _, acquisition := range byFunction[key] {
		if acquisition.Callee == "" {
			flattened = append(flattened, acquisition)

			continue
		}
		inner := hangarExpand(acquisition.Callee, byFunction, seen, depth+1)
		if hangarDeclaredIn[acquisition.Callee] == hangarLockHelper {
			inner = hangarSortedClasses(inner)
		}
		flattened = append(flattened, inner...)
	}

	return flattened
}

// hangarSortedClasses reduces a helper's acquisitions to the classes it takes,
// in class order.
func hangarSortedClasses(acquisitions []hangarAcquisition) []hangarAcquisition {
	seen := map[int]hangarAcquisition{}
	for _, acquisition := range acquisitions {
		if _, known := seen[acquisition.Class]; !known {
			acquisition.Implicit = false
			seen[acquisition.Class] = acquisition
		}
	}

	var ordered []hangarAcquisition
	for class := 1; class <= 4; class++ {
		if acquisition, taken := seen[class]; taken {
			ordered = append(ordered, acquisition)
		}
	}

	return ordered
}

func TestEveryHangarRowLockIsTakenInClassOrder(t *testing.T) {
	byFunction := hangarAcquisitionsByFunction(t, "atc", "cmd", "hangar")

	if len(byFunction) < 20 {
		t.Fatalf("the replay found lock acquisitions in only %d functions, which is too few to "+
			"have covered atc/db; the rule would be passing vacuously", len(byFunction))
	}

	var implicit int
	for key := range byFunction {
		// The lock file itself is not checked against the order it defines.
		// That would be circular, and it is also where the one acquisition this
		// system cannot make in class order lives: a logical row that did not
		// exist when the lock set was chosen cannot have been locked then, and
		// hangarLockTerminalCapture takes it afterwards with the reason written
		// at the site. Callers see that helper as its sorted class set, which
		// is what hangarExpand does, so nothing here is hidden from them.
		if hangarDeclaredIn[key] == hangarLockHelper {
			continue
		}
		flattened := hangarExpand(key, byFunction, map[string]bool{}, 0)
		held := map[int]bool{}
		highest := 0
		for _, acquisition := range flattened {
			if acquisition.Implicit {
				implicit++
			}
			if !acquisition.Implicit && !held[acquisition.Class] && acquisition.Class < highest {
				t.Errorf("%s:%d enters the Hangar suffix out of order: %s names class %d after "+
					"this transaction has already taken class %d. One transaction takes ONE "+
					"complete suffix; a second entry that reaches back to an earlier class is "+
					"the second order the rule exists to forbid.",
					acquisition.File, acquisition.Line, acquisition.Snippet, acquisition.Class,
					highest)
			}
			if acquisition.Implicit && !held[acquisition.Class] && acquisition.Class < highest {
				t.Errorf("%s:%d takes the Hangar suffix out of order: %q writes a class %d row "+
					"while this transaction already holds class %d and not class %d.\n\n"+
					"A bare UPDATE or DELETE takes the row's lock just as FOR NO KEY UPDATE "+
					"does, so this is a lock acquisition whether or not it says so. Two orders "+
					"is a deadlock nobody wrote down -- and this exact shape "+
					"(capture row, then logical row, against a publisher holding the logical "+
					"row) was reproduced live as SQLSTATE 40P01. Name class %d in the "+
					"HangarLockRequest this transaction already passes to LockHangarSuffix.",
					acquisition.File, acquisition.Line, acquisition.Snippet, acquisition.Class,
					highest, acquisition.Class, acquisition.Class)
			}
			held[acquisition.Class] = true
			if acquisition.Class > highest {
				highest = acquisition.Class
			}
		}
	}

	// EVERY ROW A WRITER LOCKS IS NAMED, not only every inversion.
	//
	// A write against a classed table that no HangarLockRequest named is a row
	// lock taken with nothing on the way in to say so. On its own it cannot
	// invert -- one class has nothing to invert with -- but it is invisible to
	// the order, and invisibility is the whole reason the blocker survived. The
	// rule is asked of ENTRY POINTS: an unexported step like
	// hangarTerminalizeLogical is covered by the exported method that entered
	// it, which the expansion above already attributes.
	for key := range byFunction {
		if !hangarExported[key] || !strings.HasPrefix(hangarDeclaredIn[key], "atc/db/") {
			continue
		}
		flattened := hangarExpand(key, byFunction, map[string]bool{}, 0)
		named := map[int]bool{}
		for _, acquisition := range flattened {
			if !acquisition.Implicit {
				named[acquisition.Class] = true
			}
		}
		for _, acquisition := range flattened {
			if acquisition.Implicit && !named[acquisition.Class] {
				t.Errorf("%s:%d writes a class %d Hangar row that no lock request names: %q.\n\n"+
					"A bare UPDATE or DELETE takes that row's lock. Name the class in a "+
					"HangarLockRequest so the order can see it.",
					acquisition.File, acquisition.Line, acquisition.Class, acquisition.Snippet)
			}
		}
	}

	if implicit < 10 {
		t.Errorf("the replay saw %d implicit row locks across the whole tree, and atc/db has "+
			"more UPDATEs against classed Hangar tables than that; the statement reader has "+
			"stopped reading them", implicit)
	}
}

// TestTheHangarClassMapMatchesTheHelper keeps the table-to-class map honest
// against the only statements that define the classes.
func TestTheHangarClassMapMatchesTheHelper(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	source, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(hangarLockHelper)))
	if err != nil {
		t.Fatalf("reading %s: %v", hangarLockHelper, err)
	}

	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, hangarLockHelper, source, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", hangarLockHelper, err)
	}

	locked := map[string]bool{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || !hangarLocksARow(value) {
			return true
		}
		for _, field := range strings.Fields(value) {
			name := strings.Trim(strings.ToLower(field), "(),;")
			if strings.HasPrefix(name, "hangar_") {
				locked[name] = true

				break
			}
		}

		return true
	})

	if len(locked) == 0 {
		t.Fatal(hangarLockHelper + " locks no Hangar table, so this comparison is vacuous")
	}
	for table := range locked {
		if _, classed := hangarTableClass[table]; !classed {
			t.Errorf("%s locks %s and hangarTableClass does not give it a class, so every bare "+
				"UPDATE against it is invisible to the order rule", hangarLockHelper, table)
		}
	}
	for table := range hangarTableClass {
		if !locked[table] {
			t.Errorf("hangarTableClass gives %s a class and %s never locks it; the map is "+
				"describing an order that is not the one the helper takes", table, hangarLockHelper)
		}
	}
}

// TestTheHangarWriteRuleIsNotVacuous drives the statement reader with the
// shapes it has to catch and the shapes it must not, the same way
// TestTheHangarLockRuleIsNotVacuous drives the lock-clause rule.
func TestTheHangarWriteRuleIsNotVacuous(t *testing.T) {
	for statement, expected := range map[string]string{
		"UPDATE hangar_logical_reservations SET state = 'terminal' WHERE reservation_id = $1": "hangar_logical_reservations",
		"\n\t\tUPDATE hangar_capture_reservations r\n\t\tSET state = 'failed'\n":              "hangar_capture_reservations",
		"update hangar_read_leases set released_at = now()":                                   "hangar_read_leases",
		"DELETE FROM hangar_claims WHERE claim_id = $1":                                       "hangar_claims",
		"delete from hangar_exact_lifecycles where id = $1":                                   "hangar_exact_lifecycles",
		// A read is not a lock, an insert creates a row nobody can hold, and a
		// table outside the four classes is outside this order.
		"SELECT state FROM hangar_logical_reservations WHERE reservation_id = $1": "",
		"INSERT INTO hangar_claims (claim_id) VALUES ($1)":                        "",
		"UPDATE hangar_reclaim_jobs SET finalized_at = now() WHERE id = $1":       "",
		"UPDATE builds SET status = 'succeeded' WHERE id = $1":                    "",
		// The shape that matters most: the table is named several words in,
		// and a read of an unrelated Hangar table comes first.
		"UPDATE hangar_exact_lifecycles SET state = 'reclaiming' FROM hangar_reclaim_jobs j WHERE j.id = $1": "hangar_exact_lifecycles",
	} {
		if found := hangarWriteTable(statement); found != expected {
			t.Errorf("the write rule read %q as writing %q, not %q", statement, found, expected)
		}
	}
}
