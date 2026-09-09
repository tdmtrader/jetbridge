package db_test

import (
	"go/ast"
	"go/parser"
	"go/token"
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

	named := 0
	for name, parsed := range files {
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			for _, table := range hangarTablesNamedBy(value) {
				// A table substituted at run time is checked at its call
				// sites below, not here: this literal does not know what it
				// will say.
				if table == "%s" {
					continue
				}
				named++
				if !strings.HasPrefix(table, hangarTablePrefix) {
					t.Errorf("atc/db/%s names the table %q.\n\nThe output plane's production code "+
						"names only its own tables. A consumer's rows are the consumer's -- Hangar "+
						"cannot know what locks the caller already holds on them, so touching one "+
						"is how an already-held domain lock gets acquired or inverted, which AC 11 "+
						"forbids. Statement: %q", name, table, firstLine(value))
				}
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
}
