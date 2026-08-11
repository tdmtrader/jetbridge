package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("retire retrospective ticket origin", func() {
	const (
		beforeVersion = 1773106159
		afterVersion  = 1773106160
	)

	var (
		database *sql.DB
		lockDB   [lock.FactoryCount]*sql.DB
		migrator migration.Migrator
	)

	BeforeEach(func() {
		var err error
		database, err = sql.Open("pgx", postgresRunner.DataSourceName())
		Expect(err).NotTo(HaveOccurred())
		for index := range lock.FactoryCount {
			lockDB[index], err = sql.Open("pgx", postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
		}
		noop := func(lager.Logger, lock.LockID) {}
		migrator = migration.NewMigrator(database, lock.NewLockFactory(lockDB, noop, noop))
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("narrows the origin CHECK to web and fly", func() {
		Expect(migrator.Migrate(nil, nil, afterVersion)).To(Succeed())

		_, err := database.Exec(`
			INSERT INTO agent_tickets (title, body, repo, origin)
			VALUES ('follow-up', 'body', 'repo', 'retrospective')`)
		Expect(err).To(HaveOccurred(), "'retrospective' must no longer be an origin")

		for _, origin := range []string{"web", "fly"} {
			_, err = database.Exec(`
				INSERT INTO agent_tickets (title, body, repo, origin)
				VALUES ($1, 'body', 'repo', $1)`, origin)
			Expect(err).NotTo(HaveOccurred(), "origin %q must still be accepted", origin)
		}
	})

	It("refuses to migrate up while a retrospective-origin ticket still exists", func() {
		_, err := database.Exec(`
			INSERT INTO agent_tickets (title, body, repo, origin)
			VALUES ('legacy follow-up', 'body', 'repo', 'retrospective')`)
		Expect(err).NotTo(HaveOccurred(), "'retrospective' is still valid at beforeVersion")

		Expect(migrator.Migrate(nil, nil, afterVersion)).To(
			MatchError(ContainSubstring("retrospective-origin ticket still exists")))

		var constraintSource string
		Expect(database.QueryRow(`
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conname = 'agent_tickets_origin_check'
		`).Scan(&constraintSource)).To(Succeed())
		Expect(constraintSource).To(ContainSubstring("retrospective"), "the CHECK must be left untouched when the guard refuses")
	})

	It("migrates down and back up", func() {
		Expect(migrator.Migrate(nil, nil, afterVersion)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		_, err := database.Exec(`
			INSERT INTO agent_tickets (title, body, repo, origin)
			VALUES ('restored', 'body', 'repo', 'retrospective')`)
		Expect(err).NotTo(HaveOccurred(), "'retrospective' must be accepted again after migrating down")

		Expect(migrator.Migrate(nil, nil, afterVersion)).To(
			MatchError(ContainSubstring("retrospective-origin ticket still exists")),
			"the row inserted while down must block re-migrating up")
	})
})
