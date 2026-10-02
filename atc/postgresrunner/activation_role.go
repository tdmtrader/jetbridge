package postgresrunner

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/gomega"
)

// deferred: test helper, no production caller. The activation database role
// exists in production only through the bootstrap's database step
// (hangar/bootstrap.EnsureActivationRole); these give tests the same role.

// RequireSCRAM makes logins as role authenticate with a SCRAM password, ahead
// of the runner's trust lines, so a wrong password is refused rather than
// waved through.
func (runner *Runner) RequireSCRAM(role string) {
	conn := runner.adminDB()
	defer conn.Close()

	var hbaFile string
	Expect(conn.QueryRow("SHOW hba_file").Scan(&hbaFile)).To(Succeed())
	body, err := os.ReadFile(hbaFile)
	Expect(err).NotTo(HaveOccurred())
	lines := fmt.Sprintf("local all %[1]s scram-sha-256\nhost all %[1]s 127.0.0.1/32 scram-sha-256\n", role)
	if strings.Contains(string(body), lines) {
		return
	}
	Expect(os.WriteFile(hbaFile, append([]byte(lines), body...), 0600)).To(Succeed())
	_, err = conn.Exec("SELECT pg_reload_conf()")
	Expect(err).NotTo(HaveOccurred())
}

// ActivationRoleName is the activation database role of the test database.
func (runner *Runner) ActivationRoleName() string {
	conn := runner.adminDB()
	defer conn.Close()

	var name string
	Expect(conn.QueryRow("SELECT hangar_activation_role_name()").Scan(&name)).To(Succeed())
	return name
}

// ActivationRoleDB ensures the activation database role can log in with a
// fresh password and holds its grants in the test database, and returns a
// connection pool authenticated as it over SCRAM.
func (runner *Runner) ActivationRoleDB() *sql.DB {
	role := runner.ActivationRoleName()
	raw := make([]byte, 24)
	_, err := rand.Read(raw)
	Expect(err).NotTo(HaveOccurred())
	password := hex.EncodeToString(raw)

	admin := runner.adminDB()
	defer admin.Close()
	var exists bool
	Expect(admin.QueryRow("SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists)).To(Succeed())
	statement := "CREATE ROLE %s LOGIN PASSWORD %s"
	if exists {
		statement = "ALTER ROLE %s LOGIN PASSWORD %s"
	}
	_, err = admin.Exec(fmt.Sprintf(statement, quoteIdentifier(role), quoteLiteral(password)))
	Expect(err).NotTo(HaveOccurred())
	_, err = admin.Exec("SELECT hangar_activation_grants()")
	Expect(err).NotTo(HaveOccurred())
	runner.RequireSCRAM(role)

	conn, err := sql.Open("pgx", runner.ActivationRoleDataSourceName(role, password))
	Expect(err).NotTo(HaveOccurred())
	Expect(conn.Ping()).To(Succeed())
	return conn
}

// ActivationRoleDataSourceName is a keyword-form DSN logging in to the test
// database as role over TCP.
func (runner *Runner) ActivationRoleDataSourceName(role, password string) string {
	return fmt.Sprintf("host=127.0.0.1 port=%d dbname=testdb user=%s password=%s sslmode=disable", runner.Port, role, password)
}

// EnsureActivationRole creates the activation database role without a login
// if it is absent, and gives it its grants in the test database, for fixtures
// that write as it through AsActivationRole.
func (runner *Runner) EnsureActivationRole() {
	role := runner.ActivationRoleName()
	admin := runner.adminDB()
	defer admin.Close()
	_, err := admin.Exec(fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %[1]s) THEN CREATE ROLE %[2]s NOLOGIN; END IF;
	END $$`, quoteLiteral(role), quoteIdentifier(role)))
	Expect(err).NotTo(HaveOccurred())
	_, err = admin.Exec("SELECT hangar_activation_grants()")
	Expect(err).NotTo(HaveOccurred())
}

// AsActivationRole runs fn inside tx as the activation database role, for a
// fixture that writes the activation epochs the way the activation commands
// do. The role must exist (EnsureActivationRole); the caller's session must be
// able to SET ROLE to it, as the runner's superuser can.
func AsActivationRole(tx *sql.Tx, fn func() error) error {
	var role string
	if err := tx.QueryRow("SELECT hangar_activation_role_name()").Scan(&role); err != nil {
		return err
	}
	if _, err := tx.Exec("SET LOCAL ROLE " + quoteIdentifier(role)); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	_, err := tx.Exec("RESET ROLE")
	return err
}

// adminDB is a plain superuser connection to the test database; unlike OpenDB
// it runs no migration.
func (runner *Runner) adminDB() *sql.DB {
	conn, err := sql.Open("pgx", runner.DataSourceName())
	Expect(err).NotTo(HaveOccurred())
	return conn
}

func quoteIdentifier(name string) string { return pgx.Identifier{name}.Sanitize() }

func quoteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
