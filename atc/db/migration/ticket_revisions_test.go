package migration_test

import (
	"database/sql"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ticket revision migration", func() {
	const (
		beforeVersion = 1773106103
		targetVersion = 1773106104
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

	It("backfills revision one and enforces consistent comment answers", func() {
		var ticketID int
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, body, repo)
			VALUES ('migration ticket', 'body', 'repo')
			RETURNING id
		`).Scan(&ticketID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var revision int64
		Expect(database.QueryRow(`SELECT revision FROM agent_tickets WHERE id = $1`, ticketID).Scan(&revision)).To(Succeed())
		Expect(revision).To(Equal(int64(1)))

		var commentID int
		Expect(database.QueryRow(`
			INSERT INTO agent_ticket_comments (ticket_id, body, created_by)
			VALUES ($1, 'Can this change the lockfile?', 'agent')
			RETURNING id
		`, ticketID).Scan(&commentID)).To(Succeed())
		Expect(commentID).To(BeNumerically(">", 0))

		_, err := database.Exec(`
			UPDATE agent_ticket_comments
			SET answer = 'yes', answered_by = '', answered_at = now()
			WHERE id = $1
		`, commentID)
		Expect(err).To(HaveOccurred())
		_, err = database.Exec(`UPDATE agent_tickets SET revision = 0 WHERE id = $1`, ticketID)
		Expect(err).To(HaveOccurred())
	})

	It("migrates down and back up without losing tickets", func() {
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var ticketID int
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, body, repo, revision)
			VALUES ($1, 'body', 'repo', 7)
			RETURNING id
		`, fmt.Sprintf("round-trip-%d", time.Now().UnixNano())).Scan(&ticketID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var revisionColumn bool
		Expect(database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'agent_tickets' AND column_name = 'revision'
			)
		`).Scan(&revisionColumn)).To(Succeed())
		Expect(revisionColumn).To(BeFalse())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var revision int64
		Expect(database.QueryRow(`SELECT revision FROM agent_tickets WHERE id = $1`, ticketID).Scan(&revision)).To(Succeed())
		Expect(revision).To(Equal(int64(1)))
	})
})
