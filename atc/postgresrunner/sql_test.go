package postgresrunner

import (
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
)

var _ = Describe("fixture SQL execution", func() {
	var runner Runner
	var process ifrit.Process
	var observer *sql.DB

	BeforeEach(func() {
		InitializeRunnerForGinkgo(&runner, &process)
		DeferCleanup(func() { FinalizeRunnerForGinkgo(&runner, &process) })
		runner.CreateTestDBFromTemplate()
		DeferCleanup(runner.DropTestDB)
		observer = runner.OpenSingleton()
		DeferCleanup(func() { Expect(observer.Close()).To(Succeed()) })
	})

	otherConnections := func() int {
		GinkgoHelper()
		var count int
		Expect(observer.QueryRow(`SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND pid <> pg_backend_pid()`).Scan(&count)).To(Succeed())
		return count
	}

	It("executes a multi-statement command and closes its connection", func() {
		Expect(runner.execSQL("testdb", `
			CREATE TABLE fixture_sql_probe (value integer);
			INSERT INTO fixture_sql_probe VALUES (42);
			SELECT * FROM fixture_sql_probe;
		`)).To(Succeed())
		var value int
		Expect(observer.QueryRow("SELECT value FROM fixture_sql_probe").Scan(&value)).To(Succeed())
		Expect(value).To(Equal(42))
		Eventually(otherConnections, time.Second).Should(BeZero())
	})

	It("returns SQL errors, rolls back the command, and closes its connection", func() {
		err := runner.execSQL("testdb", `
			CREATE TABLE fixture_sql_probe (value integer);
			INSERT INTO fixture_sql_probe VALUES ('not an integer');
		`)
		var pgErr *pgconn.PgError
		Expect(errors.As(err, &pgErr)).To(BeTrue(), "expected a PostgreSQL error: %v", err)
		Expect(pgErr.Code).To(Equal("22P02"))
		var absent bool
		Expect(observer.QueryRow("SELECT to_regclass('fixture_sql_probe') IS NULL").Scan(&absent)).To(Succeed())
		Expect(absent).To(BeTrue())
		Eventually(otherConnections, time.Second).Should(BeZero())
	})

	It("returns connection errors without touching another database", func() {
		err := runner.execSQL("missing_fixture_database", "CREATE TABLE fixture_sql_probe (value integer)")
		var pgErr *pgconn.PgError
		Expect(errors.As(err, &pgErr)).To(BeTrue(), "expected a PostgreSQL error: %v", err)
		Expect(pgErr.Code).To(Equal("3D000"))
		var absent bool
		Expect(observer.QueryRow("SELECT to_regclass('fixture_sql_probe') IS NULL").Scan(&absent)).To(Succeed())
		Expect(absent).To(BeTrue())
	})
	It("creates a pristine clone after the previous clone changes data and schema", func() {
		Expect(runner.execSQL("testdb", "CREATE TABLE fixture_sql_probe (value integer)")).To(Succeed())
		var firstID int
		Expect(observer.QueryRow("INSERT INTO teams (name) VALUES ('fixture-isolation') RETURNING id").Scan(&firstID)).To(Succeed())
		Expect(observer.Close()).To(Succeed())
		runner.DropTestDB()
		runner.CreateTestDBFromTemplate()
		observer = runner.OpenSingleton()

		var absent bool
		Expect(observer.QueryRow("SELECT to_regclass('fixture_sql_probe') IS NULL").Scan(&absent)).To(Succeed())
		Expect(absent).To(BeTrue())
		var count int
		Expect(observer.QueryRow("SELECT count(*) FROM teams WHERE name = 'fixture-isolation'").Scan(&count)).To(Succeed())
		Expect(count).To(BeZero())
		var nextID int
		Expect(observer.QueryRow("INSERT INTO teams (name) VALUES ('fixture-isolation') RETURNING id").Scan(&nextID)).To(Succeed())
		Expect(nextID).To(Equal(firstID), "clones must start with independent, identically seeded sequences")
	})

})
