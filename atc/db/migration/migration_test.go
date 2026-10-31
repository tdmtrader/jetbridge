package migration_test

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing/fstest"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Migration", func() {
	var (
		err         error
		db          *sql.DB
		lockDB      [lock.FactoryCount]*sql.DB
		lockFactory lock.LockFactory
		fakeLogFunc = func(logger lager.Logger, id lock.LockID) {}
	)

	BeforeEach(func() {
		db, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())

		for i := range lock.FactoryCount {
			lockDB[i], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		lockFactory = lock.NewLockFactory(lockDB, fakeLogFunc, fakeLogFunc)
	})

	AfterEach(func() {
		_ = db.Close()
		for _, closer := range lockDB {
			closer.Close()
		}
	})

	Context("Migration test run", func() {
		It("Runs all the migrations", func() {
			migrator := migration.NewMigrator(db, lockFactory)

			err := migrator.Up(nil, nil)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Version Check", func() {
		It("CurrentVersion reports the current version stored in the database", func() {
			myDatabaseVersion := 1234567890

			SetupMigrationsHistoryTableToExistAtVersion(db, myDatabaseVersion)

			migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
				"1000_some_migration.up.sql":                                     &fstest.MapFile{},
				"1510262030_initial_schema.up.sql":                               &fstest.MapFile{},
				"1510670987_update_unique_constraint_for_resource_caches.up.sql": &fstest.MapFile{},
				"2000000000_latest_migration_does_not_matter.up.sql":             &fstest.MapFile{},
			})

			version, err := migrator.CurrentVersion()
			Expect(err).NotTo(HaveOccurred())
			Expect(version).To(Equal(myDatabaseVersion))
		})

		It("SupportedVersion reports the highest supported migration version", func() {
			SetupMigrationsHistoryTableToExistAtVersion(db, 1000)

			migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
				"1000_some_migration.up.sql":                                     &fstest.MapFile{},
				"1510262030_initial_schema.up.sql":                               &fstest.MapFile{},
				"1510670987_update_unique_constraint_for_resource_caches.up.sql": &fstest.MapFile{},
				"300000_this_is_to_prove_we_dont_use_string_sort.up.sql":         &fstest.MapFile{},
				"2000000000_latest_migration.up.sql":                             &fstest.MapFile{},
			})

			version, err := migrator.SupportedVersion()
			Expect(err).NotTo(HaveOccurred())
			Expect(version).To(Equal(2000000000))
		})

		It("Ignores files it can't parse", func() {
			SetupMigrationsHistoryTableToExistAtVersion(db, 1000)

			migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
				"1000_some_migration.up.sql":                                     &fstest.MapFile{},
				"1510262030_initial_schema.up.sql":                               &fstest.MapFile{},
				"1510670987_update_unique_constraint_for_resource_caches.up.sql": &fstest.MapFile{},
				"300000_this_is_to_prove_we_dont_use_string_sort.up.sql":         &fstest.MapFile{},
				"2000000000_latest_migration.up.sql":                             &fstest.MapFile{},
				"migrations.go":                                                  &fstest.MapFile{},
			})

			version, err := migrator.SupportedVersion()
			Expect(err).NotTo(HaveOccurred())
			Expect(version).To(Equal(2000000000))
		})
	})

	Context("Upgrade", func() {
		Context("sql migrations", func() {
			It("runs a migration", func() {
				migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
					"1000_test_table_created.up.sql": &fstest.MapFile{
						Data: []byte(`
							CREATE TABLE some_table (id integer);
						`),
					},
				})

				migrations, err := migrator.Migrations()
				Expect(err).NotTo(HaveOccurred())
				Expect(len(migrations)).To(Equal(1))

				err = migrator.Up(nil, nil)
				Expect(err).NotTo(HaveOccurred())

				By("Creating the table in the database")
				var exists string
				err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables where table_name = 'some_table')").Scan(&exists)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(Equal("true"))

				By("Updating the migrations_history table")
				ExpectDatabaseMigrationVersionToEqual(migrator, 1000)
			})

			It("ignores migrations before the current version", func() {
				SetupMigrationsHistoryTableToExistAtVersion(db, 1000)

				migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
					"1000_test_table_created.up.sql": &fstest.MapFile{
						Data: []byte(`
							CREATE TABLE some_table (id integer);
						`),
					},
				})

				err := migrator.Up(nil, nil)
				Expect(err).NotTo(HaveOccurred())

				By("Not creating the database referenced in the migration")
				var exists string
				err = db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.tables where table_name = 'some_table')").Scan(&exists)
				Expect(err).NotTo(HaveOccurred())
				Expect(exists).To(Equal("false"))
			})

			It("runs the up migrations in ascending order", func() {
				migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
					"1000_test_table_created.up.sql": &fstest.MapFile{
						Data: []byte(`
							CREATE TABLE some_table (id integer);
						`),
					},
					"1001_test_table_created.up.sql": &fstest.MapFile{
						Data: []byte(`
							DROP TABLE some_table;
						`),
					},
				})

				err := migrator.Up(nil, nil)
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when sql migrations fail", func() {
				It("rolls back and leaves the database clean", func() {
					migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
						"1000_test_table_created.up.sql": &fstest.MapFile{
							Data: []byte(`
								CREATE TABLE some_table (id integer);
							`),
						},
						"1000_test_table_created.down.sql": &fstest.MapFile{
							Data: []byte(`
								DROP TABLE some_table;
							`),
						},
						"1001_drop_bogus_table.up.sql": &fstest.MapFile{
							Data: []byte(`
								DROP TABLE some_bogus_table;
							`),
						},
					})

					err := migrator.Up(nil, nil)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed and was rolled back"))
					ExpectDatabaseMigrationVersionToEqual(migrator, 1000)
					ExpectMigrationToHaveFailed(db, 1001, false)
				})
			})

			It("Doesn't fail if there are no migrations to run", func() {
				migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
					"1000_test_table_created.up.sql": &fstest.MapFile{
						Data: []byte(`
							CREATE TABLE some_table (id integer);
						`),
					},
				})

				err := migrator.Up(nil, nil)
				Expect(err).NotTo(HaveOccurred())

				err = migrator.Up(nil, nil)
				Expect(err).NotTo(HaveOccurred())

				ExpectDatabaseMigrationVersionToEqual(migrator, 1000)
				ExpectMigrationVersionTableNotToExist(db)
			})

			It("Locks the database so multiple ATCs don't all run migrations at the same time", func() {
				migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
					"1000_test_table_created.up.sql": &fstest.MapFile{
						Data: []byte(`
							CREATE TABLE some_table (id integer);
						`),
					},
				})

				var wg sync.WaitGroup
				wg.Add(3)

				go TryRunUpAndVerifyResult(db, migrator, 1000, &wg)
				go TryRunUpAndVerifyResult(db, migrator, 1000, &wg)
				go TryRunUpAndVerifyResult(db, migrator, 1000, &wg)

				wg.Wait()
			})
		})
	})

	Context("Baseline", func() {
		baselineFS := func() fstest.MapFS {
			return fstest.MapFS{
				"1000_baseline.up.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE baseline_table (id integer);`)},
				"1001_next.up.sql":     &fstest.MapFile{Data: []byte(`CREATE TABLE next_table (id integer);`)},
			}
		}

		tableExists := func(name string) bool {
			GinkgoHelper()
			var exists bool
			Expect(db.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists)).To(Succeed())
			return exists
		}

		It("builds a fresh database from the earliest migration", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, baselineFS())
			Expect(migrator.Up(nil, nil)).To(Succeed())
			Expect(tableExists("baseline_table")).To(BeTrue())
			Expect(tableExists("next_table")).To(BeTrue())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1001)
		})

		It("treats a database at or past the baseline as having it", func() {
			SetupMigrationsHistoryTableToExistAtVersion(db, 1000)
			migrator := migration.NewMigratorForMigrations(db, lockFactory, baselineFS())
			Expect(migrator.Up(nil, nil)).To(Succeed())
			Expect(tableExists("baseline_table")).To(BeFalse())
			Expect(tableExists("next_table")).To(BeTrue())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1001)
		})

		It("refuses a database older than the baseline, naming the release that can upgrade it", func() {
			SetupMigrationsHistoryTableToExistAtVersion(db, 999)
			migrator := migration.NewMigratorForMigrations(db, lockFactory, baselineFS())
			err := migrator.Up(nil, nil)
			Expect(err).To(MatchError("database is at migration 999, older than this release's baseline migration 1000: upgrade it with " + migration.LastReleaseBeforeBaseline + " first"))
			Expect(tableExists("baseline_table")).To(BeFalse())
			ExpectDatabaseMigrationVersionToEqual(migrator, 999)
		})

		DescribeTable("refuses a database tracked by a table older than migrations_history, changing nothing",
			func(setup func(), table string, version int) {
				setup()
				migrator := migration.NewMigratorForMigrations(db, lockFactory, baselineFS())
				err := migrator.Up(nil, nil)
				Expect(err).To(MatchError(fmt.Sprintf("database records migration %d in %s, which predates this release's baseline migration 1000: upgrade it with %s first", version, table, migration.LastReleaseBeforeBaseline)))
				Expect(tableExists(table)).To(BeTrue())
				Expect(tableExists("migrations_history")).To(BeFalse())
				Expect(tableExists("baseline_table")).To(BeFalse())
			},
			Entry("schema_migrations", func() { SetupSchemaMigrationsTable(db, 1532706544, false) }, "schema_migrations", 1532706544),
			Entry("migration_version", func() { SetupMigrationVersionTableToExistAtVersion(db, 189) }, "migration_version", 189),
		)

		It("ignores a schema_migrations table left beside migrations_history", func() {
			SetupMigrationsHistoryTableToExistAtVersion(db, 1000)
			SetupSchemaMigrationsTable(db, 999, false)
			migrator := migration.NewMigratorForMigrations(db, lockFactory, baselineFS())
			Expect(migrator.Up(nil, nil)).To(Succeed())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1001)
		})

		It("refuses to migrate to a version older than the baseline", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, baselineFS())
			err := migrator.Migrate(nil, nil, 999)
			Expect(err).To(MatchError("cannot migrate to 999: it predates this release's baseline migration 1000"))
			Expect(tableExists("migrations_history")).To(BeFalse())
		})
	})

	Context("Forward-only migrations", func() {
		forwardOnlyFS := func() fstest.MapFS {
			return fstest.MapFS{
				"1000_first.up.sql":    &fstest.MapFile{Data: []byte(`CREATE TABLE first_table (id integer);`)},
				"1000_first.down.sql":  &fstest.MapFile{Data: []byte(`DROP TABLE first_table;`)},
				"1001_one_way.up.sql":  &fstest.MapFile{Data: []byte(`CREATE TABLE one_way_table (id integer);`)},
				"1002_second.up.sql":   &fstest.MapFile{Data: []byte(`CREATE TABLE second_table (id integer);`)},
				"1002_second.down.sql": &fstest.MapFile{Data: []byte(`DROP TABLE second_table;`)},
			}
		}

		tableExists := func(name string) bool {
			GinkgoHelper()
			var exists bool
			Expect(db.QueryRow("SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists)).To(Succeed())
			return exists
		}

		It("reverses migrations that have a down", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, forwardOnlyFS())
			Expect(migrator.Up(nil, nil)).To(Succeed())
			Expect(migrator.Migrate(nil, nil, 1001)).To(Succeed())
			Expect(tableExists("second_table")).To(BeFalse())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1001)
		})

		It("refuses to reverse a migration without a down before reversing anything", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, forwardOnlyFS())
			Expect(migrator.Up(nil, nil)).To(Succeed())
			var historyBefore int
			Expect(db.QueryRow("SELECT count(*) FROM migrations_history").Scan(&historyBefore)).To(Succeed())

			err := migrator.Migrate(nil, nil, 1000)
			Expect(err).To(MatchError("migration 1001 is forward-only: restore the pre-upgrade database backup instead"))
			Expect(tableExists("second_table")).To(BeTrue(), "the reversible migration above it was not reversed either")
			ExpectDatabaseMigrationVersionToEqual(migrator, 1002)
			var historyAfter int
			Expect(db.QueryRow("SELECT count(*) FROM migrations_history").Scan(&historyAfter)).To(Succeed())
			Expect(historyAfter).To(Equal(historyBefore))
		})

		It("reports no version once the earliest migration is reversed", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, fstest.MapFS{
				"1000_first.up.sql":   &fstest.MapFile{Data: []byte(`CREATE TABLE first_table (id integer);`)},
				"1000_first.down.sql": &fstest.MapFile{Data: []byte(`DROP TABLE first_table;`)},
			})
			Expect(migrator.Up(nil, nil)).To(Succeed())
			Expect(migrator.Migrate(nil, nil, 0)).To(Succeed())
			Expect(tableExists("first_table")).To(BeFalse())
			ExpectDatabaseMigrationVersionToEqual(migrator, 0)
		})
	})

	Context("Downgrade", func() {
		reversibleFS := func() fstest.MapFS {
			return fstest.MapFS{
				"1000_test_table_created.up.sql": &fstest.MapFile{Data: []byte(`CREATE TABLE some_table (id integer);`)},
				"1001_add_column.up.sql":         &fstest.MapFile{Data: []byte(`ALTER TABLE some_table ADD COLUMN name text;`)},
				"1001_add_column.down.sql":       &fstest.MapFile{Data: []byte(`ALTER TABLE some_table DROP COLUMN name;`)},
			}
		}

		It("Downgrades to a given version", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, reversibleFS())

			Expect(migrator.Up(nil, nil)).To(Succeed())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1001)

			Expect(migrator.Migrate(nil, nil, 1000)).To(Succeed())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1000)

			var columnExists bool
			Expect(db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'some_table' AND column_name = 'name')").Scan(&columnExists)).To(Succeed())
			Expect(columnExists).To(BeFalse())
		})

		It("Doesn't fail if already at the requested version", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, reversibleFS())

			Expect(migrator.Migrate(nil, nil, 1001)).To(Succeed())
			Expect(migrator.Migrate(nil, nil, 1001)).To(Succeed())
			ExpectDatabaseMigrationVersionToEqual(migrator, 1001)
		})

		It("Locks the database so multiple consumers don't run downgrade at the same time", func() {
			migrator := migration.NewMigratorForMigrations(db, lockFactory, reversibleFS())

			Expect(migrator.Up(nil, nil)).To(Succeed())

			var wg sync.WaitGroup
			wg.Add(3)

			go TryRunMigrateAndVerifyResult(db, migrator, 1000, &wg)
			go TryRunMigrateAndVerifyResult(db, migrator, 1000, &wg)
			go TryRunMigrateAndVerifyResult(db, migrator, 1000, &wg)

			wg.Wait()
		})
	})

})

func TryRunUpAndVerifyResult(db *sql.DB, migrator migration.Migrator, version int, wg *sync.WaitGroup) {
	defer GinkgoRecover()
	defer wg.Done()

	err := migrator.Up(nil, nil)
	Expect(err).NotTo(HaveOccurred())

	ExpectDatabaseMigrationVersionToEqual(migrator, version)
}

func TryRunMigrateAndVerifyResult(db *sql.DB, migrator migration.Migrator, version int, wg *sync.WaitGroup) {
	defer GinkgoRecover()
	defer wg.Done()

	err := migrator.Migrate(nil, nil, version)
	Expect(err).NotTo(HaveOccurred())

	ExpectDatabaseMigrationVersionToEqual(migrator, version)
}

func SetupMigrationsHistoryTableToExistAtVersion(db *sql.DB, version int) {
	GinkgoHelper()
	_, err := db.Exec(`CREATE TABLE migrations_history(version bigint, tstamp timestamp with time zone, direction varchar, status varchar, dirty boolean)`)
	Expect(err).NotTo(HaveOccurred())

	_, err = db.Exec(`INSERT INTO migrations_history(version, tstamp, direction, status, dirty) VALUES($1, current_timestamp, 'up', 'passed', false)`, version)
	Expect(err).NotTo(HaveOccurred())
}

func SetupSchemaMigrationsTable(db *sql.DB, version int, dirty bool) {
	GinkgoHelper()
	_, err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version bigint, dirty boolean)")
	Expect(err).NotTo(HaveOccurred())
	_, err = db.Exec("INSERT INTO schema_migrations (version, dirty) VALUES ($1, $2)", version, dirty)
	Expect(err).NotTo(HaveOccurred())
}

func ExpectDatabaseMigrationVersionToEqual(migrator migration.Migrator, expectedVersion int) {
	dbVersion, err := migrator.CurrentVersion()
	Expect(err).NotTo(HaveOccurred())
	Expect(dbVersion).To(Equal(expectedVersion))
}

func ExpectToBeAbleToInsertData(dbConn *sql.DB) {
	teamID := rand.IntN(10000)
	_, err := dbConn.Exec("INSERT INTO teams(id, name) VALUES ($1, $2)", teamID, strconv.Itoa(teamID))
	Expect(err).NotTo(HaveOccurred())

	pipelineID := rand.IntN(10000)
	_, err = dbConn.Exec("INSERT INTO pipelines(id, team_id, name, secondary_ordering) VALUES ($1, $2, $3, 1)", pipelineID, teamID, strconv.Itoa(pipelineID))
	Expect(err).NotTo(HaveOccurred())

	jobID := rand.IntN(10000)
	_, err = dbConn.Exec("INSERT INTO jobs(id, pipeline_id, name, config) VALUES ($1, $2, $3, '{}')", jobID, pipelineID, strconv.Itoa(jobID))
	Expect(err).NotTo(HaveOccurred())
}

func ExpectMigrationToHaveFailed(dbConn *sql.DB, failedVersion int, expectDirty bool) {
	GinkgoHelper()
	var status string
	var dirty bool
	err := dbConn.QueryRow("SELECT status, dirty FROM migrations_history WHERE version=$1 ORDER BY tstamp desc LIMIT 1", failedVersion).Scan(&status, &dirty)
	Expect(err).NotTo(HaveOccurred())
	Expect(status).To(Equal("failed"))
	Expect(dirty).To(Equal(expectDirty))
}
