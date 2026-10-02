package migration_test

import (
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc/db/migration"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Web's privileges on the activation epochs, under a fresh migration run by a
// non-superuser owner -- the only kind of owner a REVOKE binds -- and the
// guard that holds even for a superuser (hangar_activation_db_role A3, A4).
var _ = Describe("The activation epochs under a non-superuser owner", func() {
	const (
		ownerRole = "activation_privileges_owner"
		database  = "activation_privileges"
		guardCode = "JB003"
	)
	activationRole := database + "_hangar_activation"

	var (
		admin *sql.DB // superuser, on the owner's database
		owner *sql.DB // web's role: owns every table, ran every migration
	)

	open := func(user string) *sql.DB {
		conn, err := sql.Open("pgx", fmt.Sprintf("host=/tmp user=%s dbname=%s sslmode=disable port=%d", user, database, postgresRunner.Port))
		Expect(err).NotTo(HaveOccurred())
		return conn
	}

	permissionDenied := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "42501"
	}
	guarded := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == guardCode
	}

	BeforeEach(func() {
		server := postgresRunner.OpenDB()
		DeferCleanup(server.Close)
		for _, statement := range []string{
			"DROP DATABASE IF EXISTS " + database,
			fmt.Sprintf(`DO $$ BEGIN
				IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN DROP ROLE %[1]s; END IF;
				IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[2]s') THEN DROP ROLE %[2]s; END IF;
			END $$`, activationRole, ownerRole),
			"CREATE ROLE " + ownerRole + " LOGIN",
			"CREATE DATABASE " + database + " OWNER " + ownerRole,
		} {
			_, err := server.Exec(statement)
			Expect(err).NotTo(HaveOccurred(), statement)
		}
		DeferCleanup(func() {
			_, _ = server.Exec(fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'`, database))
			_, _ = server.Exec("DROP DATABASE IF EXISTS " + database)
			_, _ = server.Exec("DROP ROLE IF EXISTS " + activationRole)
			_, _ = server.Exec("DROP ROLE IF EXISTS " + ownerRole)
		})

		migrated, err := migration.NewOpenHelper("pgx",
			fmt.Sprintf("host=/tmp user=%s dbname=%s sslmode=disable port=%d", ownerRole, database, postgresRunner.Port),
			nil, nil, nil).Open()
		Expect(err).NotTo(HaveOccurred(), "a fresh migration run under a non-superuser owner")
		Expect(migrated.Close()).To(Succeed())

		admin = open("postgres")
		DeferCleanup(admin.Close)
		owner = open(ownerRole)
		DeferCleanup(owner.Close)

		// One epoch, written as the activation database role writes it.
		_, err = admin.Exec("CREATE ROLE " + activationRole + " NOLOGIN")
		Expect(err).NotTo(HaveOccurred())
		_, err = admin.Exec("SELECT hangar_activation_grants()")
		Expect(err).NotTo(HaveOccurred())
		tx, err := admin.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec("SET LOCAL ROLE " + activationRole)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec("INSERT INTO hangar_output_activation_epochs (epoch_id) VALUES (1)")
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
	})

	It("refuses web's INSERT, DELETE, TRUNCATE and UPDATE of every column but epoch_id", func() {
		for _, statement := range []string{
			"INSERT INTO hangar_output_activation_epochs (epoch_id) VALUES (2)",
			"DELETE FROM hangar_output_activation_epochs WHERE epoch_id = 1",
			"TRUNCATE hangar_output_activation_epochs CASCADE",
		} {
			_, err := owner.Exec(statement)
			Expect(permissionDenied(err)).To(BeTrue(), "%s: %v", statement, err)
		}

		rows, err := admin.Query(`SELECT column_name FROM information_schema.columns
			WHERE table_name = 'hangar_output_activation_epochs' AND column_name <> 'epoch_id'`)
		Expect(err).NotTo(HaveOccurred())
		var columns []string
		for rows.Next() {
			var column string
			Expect(rows.Scan(&column)).To(Succeed())
			columns = append(columns, column)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(len(columns)).To(BeNumerically(">", 10))
		for _, column := range columns {
			_, err := owner.Exec(fmt.Sprintf("UPDATE hangar_output_activation_epochs SET %[1]s = %[1]s WHERE epoch_id = 1", column))
			Expect(permissionDenied(err)).To(BeTrue(), "UPDATE of %s: %v", column, err)
		}
	})

	It("refuses web's UPDATE of epoch_id with the guard's own diagnostic, and only the guard does", func() {
		// epoch_id is the one column web may still UPDATE (its row lock and the
		// foreign keys need it), so the privilege check passes and the guard
		// is what answers.
		statement := "UPDATE hangar_output_activation_epochs SET epoch_id = epoch_id WHERE epoch_id = 1"
		_, err := owner.Exec(statement)
		Expect(guarded(err)).To(BeTrue(), "got %v", err)
		Expect(err.Error()).To(ContainSubstring("written only by the activation database role"))

		// With the guard gone the same statement is not refused by it: the
		// assertion above is not something the older transition trigger also
		// satisfies.
		_, err = admin.Exec("DROP TRIGGER hangar_output_activation_role_guard ON hangar_output_activation_epochs")
		Expect(err).NotTo(HaveOccurred())
		_, err = owner.Exec(statement)
		Expect(guarded(err)).To(BeFalse(), "got %v", err)
	})

	It("refuses a superuser's write too: the guard, not a privilege, is what holds for one", func() {
		_, err := admin.Exec("UPDATE hangar_output_activation_epochs SET revision = revision + 1 WHERE epoch_id = 1")
		Expect(guarded(err)).To(BeTrue(), "got %v", err)
	})

	It("still lets web lock the row and reference it", func() {
		tx, err := owner.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer tx.Rollback()
		_, err = tx.Exec("SELECT 1 FROM hangar_output_activation_epochs WHERE epoch_id = 1 FOR SHARE")
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`INSERT INTO hangar_policy_violations (activation_epoch, violation, subject)
			VALUES (1, 'out_of_band_absence', 'objects/lost')`)
		Expect(err).NotTo(HaveOccurred())
	})

	// A4: the tables and columns the activation commands write, read from their
	// source, each have a matching grant.
	It("grants the role every column its commands write", func() {
		writes := activationWrites()
		Expect(writes).NotTo(BeEmpty())
		for _, write := range writes {
			var has bool
			Expect(admin.QueryRow("SELECT has_column_privilege($1, $2, $3, $4)",
				activationRole, write.table, write.column, write.privilege).Scan(&has)).To(Succeed())
			Expect(has).To(BeTrue(), "%s may not %s %s.%s", activationRole, write.privilege, write.table, write.column)
		}
	})
})

type columnWrite struct{ table, column, privilege string }

var (
	insertInto  = regexp.MustCompile(`(?is)INSERT\s+INTO\s+(\w+)\s*\(([^)]*)\)`)
	updateSet   = regexp.MustCompile(`(?is)UPDATE\s+(\w+)\s+SET\s+(.*?)\s+WHERE`)
	assignment  = regexp.MustCompile(`(?s)^\s*(%s|\w+)\s*=`)
	facetColumn = []string{"base_state", "output_state"} // what %s expands to (activation.Facet.Column)
)

// activationWrites parses the string literals of the activation package and
// of ReconcilePolicyViolation for the columns they INSERT or UPDATE.
func activationWrites() []columnWrite {
	var literals []string
	files, err := filepath.Glob("../../hangaroutput/activation/*.go")
	Expect(err).NotTo(HaveOccurred())
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		literals = append(literals, stringLiterals(path, "")...)
	}
	literals = append(literals, stringLiterals("../hangar_output_operations.go", "ReconcilePolicyViolation")...)

	var writes []columnWrite
	for _, literal := range literals {
		for _, match := range insertInto.FindAllStringSubmatch(literal, -1) {
			for _, column := range strings.Split(match[2], ",") {
				writes = append(writes, columnWrite{match[1], strings.TrimSpace(column), "INSERT"})
			}
		}
		for _, match := range updateSet.FindAllStringSubmatch(literal, -1) {
			for _, part := range strings.Split(match[2], ",") {
				found := assignment.FindStringSubmatch(part)
				if found == nil {
					continue
				}
				columns := []string{found[1]}
				if found[1] == "%s" {
					columns = facetColumn
				}
				for _, column := range columns {
					writes = append(writes, columnWrite{match[1], column, "UPDATE"})
				}
			}
		}
	}
	return writes
}

// stringLiterals returns the string literals of a Go file, or of one function
// in it when function is set.
func stringLiterals(path, function string) []string {
	source, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	Expect(err).NotTo(HaveOccurred())
	var literals []string
	collect := func(node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if value, err := strconv.Unquote(lit.Value); err == nil {
					literals = append(literals, value)
				}
			}
			return true
		})
	}
	if function == "" {
		collect(file)
		return literals
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == function {
			collect(fn)
		}
	}
	Expect(literals).NotTo(BeEmpty(), "found no %s in %s", function, path)
	return literals
}
