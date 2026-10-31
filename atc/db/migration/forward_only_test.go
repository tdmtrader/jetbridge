package migration_test

import (
	"database/sql"
	"fmt"
	"sort"

	"github.com/concourse/concourse/atc/db/migration"
	"github.com/concourse/concourse/atc/postgresrunner"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// schemaFingerprint captures columns, constraints, indexes, triggers and function bodies.
func schemaFingerprint(database *sql.DB) []string {
	rows, err := database.Query(`
		SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
		  FROM information_schema.columns WHERE table_schema = 'public'
		UNION ALL
		SELECT 'constraint ' || conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid)
		  FROM pg_constraint WHERE connamespace = 'public'::regnamespace
		UNION ALL
		SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public'
		UNION ALL
		SELECT 'trigger ' || tgrelid::regclass::text || ' ' || tgname || ' ' || pg_get_triggerdef(oid)
		  FROM pg_trigger WHERE NOT tgisinternal
		UNION ALL
		SELECT 'function ' || pg_get_functiondef(p.oid)
		  FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace`)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()
	var shape []string
	for rows.Next() {
		var line string
		Expect(rows.Scan(&line)).To(Succeed())
		shape = append(shape, line)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	sort.Strings(shape)
	return shape
}

var _ = Describe("Fork migrations are forward-only", func() {
	It("carries no down for any migration after the upstream boundary", func() {
		migrations, err := migration.NewMigrator(testLogger, nil, nil).Migrations()
		Expect(err).NotTo(HaveOccurred())
		ups := 0
		for _, m := range migrations {
			if m.Version <= v801LastMigration {
				continue
			}
			Expect(m.Direction).To(Equal("up"), "%s: a fork migration is forward-only, so it has no down", m.Name)
			ups++
		}
		Expect(ups).NotTo(BeZero())
	})

	DescribeTable("refuses HEAD to the upstream boundary without changing schema, data or effective version", func(populated bool) {
		database := postgresRunner.OpenDBAtVersion(v801LastMigration)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
		migrator := migration.NewMigrator(testLogger, database, nil)
		head, err := migrator.SupportedVersion()
		Expect(err).NotTo(HaveOccurred())
		if populated {
			insertV801FixtureData(database)
		}
		Expect(migrator.Up(postgresrunner.EncryptionKey())).To(Succeed())
		before := schemaFingerprint(database)
		dataSnapshot := func() map[string]string {
			GinkgoHelper()
			snapshot := map[string]string{}
			for _, table := range []string{"teams", "pipelines", "jobs", "builds", "components"} {
				var rows string
				Expect(database.QueryRow("SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY id)::text, '[]') FROM " + table + " t").Scan(&rows)).To(Succeed())
				snapshot[table] = rows
			}
			return snapshot
		}
		dataBefore := dataSnapshot()
		var historyBefore int
		Expect(database.QueryRow("SELECT count(*) FROM migrations_history").Scan(&historyBefore)).To(Succeed())

		err = migrator.Migrate(postgresrunner.EncryptionKey(), v801LastMigration)
		Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf(
			"migration %d is forward-only: restore the pre-upgrade database backup instead", head))))
		Expect(schemaFingerprint(database)).To(Equal(before))
		ExpectDatabaseMigrationVersionToEqual(migrator, head)
		Expect(dataSnapshot()).To(Equal(dataBefore))
		var historyAfter int
		Expect(database.QueryRow("SELECT count(*) FROM migrations_history").Scan(&historyAfter)).To(Succeed())
		Expect(historyAfter).To(Equal(historyBefore), "a refused downgrade starts nothing, so it records nothing")
		Expect(database.Close()).To(Succeed())
		database = postgresRunner.OpenDBAtVersion(head)
		Expect(dataSnapshot()).To(Equal(dataBefore))
		Expect(schemaFingerprint(database)).To(Equal(before))
		ExpectDatabaseMigrationVersionToEqual(migration.NewMigrator(testLogger, database, nil), head)
	}, Entry("empty database", false), Entry("populated database", true))
})
