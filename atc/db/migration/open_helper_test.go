package migration_test

import (
	"database/sql"
	"fmt"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenHelper", func() {
	var (
		err         error
		db          *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		openHelper  *migration.OpenHelper
		fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}
	)

	JustBeforeEach(func() {
		db, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())

		for i := range lock.FactoryCount {
			lockDB[i], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		lockFactory = lock.NewLockFactory(lockDB, fakeLogFunc, fakeLogFunc)
		openHelper = migration.NewOpenHelper(testLogger, "pgx", postgresRunner.DataSourceName(), lockFactory, nil, nil)
	})

	AfterEach(func() {
		_ = db.Close()
		for _, closer := range lockDB {
			closer.Close()
		}
	})

	It("refuses a database tracked by migration_version, leaving it as it was", func() {
		SetupMigrationVersionTableToExistAtVersion(db, 189)

		err = openHelper.MigrateToVersion(v801LastMigration)
		Expect(err).To(MatchError(fmt.Sprintf("database records migration 189 in migration_version, which predates this release's baseline migration %d: upgrade it with JetBridge 0.3.1 first", v801LastMigration)))

		ExpectDatabaseVersionToEqual(db, 189, "migration_version")
		var historyExists bool
		Expect(db.QueryRow("SELECT to_regclass('migrations_history') IS NOT NULL").Scan(&historyExists)).To(Succeed())
		Expect(historyExists).To(BeFalse())
	})

	It("migrates a fresh database to the requested version", func() {
		err = openHelper.MigrateToVersion(v801LastMigration)
		Expect(err).NotTo(HaveOccurred())

		ExpectDatabaseVersionToEqual(db, v801LastMigration, "migrations_history")
		ExpectMigrationVersionTableNotToExist(db)
		ExpectToBeAbleToInsertData(db)
	})

	It("refuses a version older than the baseline", func() {
		err = openHelper.MigrateToVersion(v713LastMigration)
		Expect(err).To(MatchError(fmt.Sprintf("cannot migrate to %d: it predates this release's baseline migration %d", v713LastMigration, v801LastMigration)))
	})
})

func SetupMigrationVersionTableToExistAtVersion(db *sql.DB, version int) {
	GinkgoHelper()
	_, err := db.Exec(`CREATE TABLE migration_version(version int)`)
	Expect(err).NotTo(HaveOccurred())

	_, err = db.Exec(`INSERT INTO migration_version(version) VALUES($1)`, version)
	Expect(err).NotTo(HaveOccurred())
}

func ExpectMigrationVersionTableNotToExist(dbConn *sql.DB) {
	GinkgoHelper()
	var exists string
	err := dbConn.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables where table_name = 'migration_version')").Scan(&exists)
	Expect(err).NotTo(HaveOccurred())
	Expect(exists).To(Equal("false"))
}

func ExpectDatabaseVersionToEqual(db *sql.DB, version int, table string) {
	GinkgoHelper()
	var dbVersion int
	query := "SELECT version from " + table + " LIMIT 1"
	err := db.QueryRow(query).Scan(&dbVersion)
	Expect(err).NotTo(HaveOccurred())
	Expect(dbVersion).To(Equal(version))
}
