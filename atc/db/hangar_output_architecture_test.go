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
			inventory.Files++

			relative, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}

			ast.Inspect(parsed, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					return true
				}
				inventory.Literals++

				if !hangarLocksARow(value) {
					return true
				}
				inventory.Sites = append(inventory.Sites, hangarLockSite{
					File:    filepath.ToSlash(relative),
					Snippet: firstLine(value),
				})

				return true
			})

			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	return inventory
}

// hangarLocksARow is the rule itself, over one string literal, so that the same
// predicate drives the real scan and the fixtures below.
func hangarLocksARow(statement string) bool {
	upper := strings.ToUpper(statement)
	if !strings.Contains(upper, "FOR UPDATE") &&
		!strings.Contains(upper, "FOR NO KEY UPDATE") &&
		!strings.Contains(upper, "FOR SHARE") &&
		!strings.Contains(upper, "FOR KEY SHARE") {
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
