package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ticket queue-shell migrations", func() {
	const (
		beforeVersion = 1773106131
		statesVersion = 1773106133
		budgetVersion = 1773106134
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

	insertTicket := func(title, state string) int {
		var id int
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, body, repo, state, completed_at)
			VALUES ($1, 'body', 'repo', $2, now())
			RETURNING id
		`, title, state).Scan(&id)).To(Succeed())
		return id
	}

	readState := func(id int) (string, sql.NullTime) {
		var state string
		var completedAt sql.NullTime
		Expect(database.QueryRow(
			`SELECT state, completed_at FROM agent_tickets WHERE id = $1`, id,
		).Scan(&state, &completedAt)).To(Succeed())
		return state, completedAt
	}

	It("folds the disposition verbs onto the queue lifecycle", func() {
		merged := insertTicket("merged ticket", "merged")
		mergedWithFixes := insertTicket("merged with fixes ticket", "merged_with_fixes")
		concluded := insertTicket("concluded ticket", "concluded")
		abandoned := insertTicket("abandoned ticket", "abandoned")
		sentBack := insertTicket("sent back ticket", "sent_back")
		failed := insertTicket("failed ticket", "failed")
		errored := insertTicket("errored ticket", "errored")
		queued := insertTicket("queued ticket", "queued")

		Expect(migrator.Migrate(nil, nil, statesVersion)).To(Succeed())

		for _, id := range []int{merged, mergedWithFixes, concluded, abandoned} {
			state, _ := readState(id)
			Expect(state).To(Equal("closed"))
		}
		// A human still owns these: the run ended, nobody decided. They land
		// in the review queue with their completion cleared, not in a
		// terminal state nothing can leave.
		for _, id := range []int{sentBack, failed, errored} {
			state, completedAt := readState(id)
			Expect(state).To(Equal("needs_review"))
			Expect(completedAt.Valid).To(BeFalse())
		}
		state, _ := readState(queued)
		Expect(state).To(Equal("queued"))
	})

	It("rejects the retired disposition verbs at the CHECK", func() {
		Expect(migrator.Migrate(nil, nil, statesVersion)).To(Succeed())

		for _, retired := range []string{
			"merged", "merged_with_fixes", "sent_back", "concluded",
			"abandoned", "failed", "errored",
		} {
			_, err := database.Exec(`
				INSERT INTO agent_tickets (title, body, repo, state)
				VALUES ('retired', 'body', 'repo', $1)
			`, retired)
			Expect(err).To(HaveOccurred(), "state %q must be rejected", retired)
		}

		_, err := database.Exec(`
			INSERT INTO agent_tickets (title, body, repo, state)
			VALUES ('closed', 'body', 'repo', 'closed')
		`)
		Expect(err).NotTo(HaveOccurred())
	})

	It("folds the latest spec body into the ticket body and drops the content tables", func() {
		withBody := insertTicket("has a body", "draft")
		empty := insertTicket("no body", "draft")
		_, err := database.Exec(`UPDATE agent_tickets SET body = '' WHERE id = $1`, empty)
		Expect(err).NotTo(HaveOccurred())

		for _, ticketID := range []int{withBody, empty} {
			_, err := database.Exec(`
				INSERT INTO agent_ticket_specs (ticket_id, version, title, body)
				VALUES ($1, 1, 'old spec', 'old spec prose'),
				       ($1, 2, 'new spec', 'newest spec prose')
			`, ticketID)
			Expect(err).NotTo(HaveOccurred())
			_, err = database.Exec(`
				INSERT INTO agent_ticket_tasks (ticket_id, plan_version, ordering, title)
				VALUES ($1, 1, 1, 'do the thing')
			`, ticketID)
			Expect(err).NotTo(HaveOccurred())
		}

		Expect(migrator.Migrate(nil, nil, statesVersion)).To(Succeed())

		var body string
		Expect(database.QueryRow(`SELECT body FROM agent_tickets WHERE id = $1`, withBody).Scan(&body)).To(Succeed())
		Expect(body).To(ContainSubstring("body"))
		Expect(body).To(ContainSubstring("newest spec prose"))
		Expect(body).NotTo(ContainSubstring("old spec prose"))

		Expect(database.QueryRow(`SELECT body FROM agent_tickets WHERE id = $1`, empty).Scan(&body)).To(Succeed())
		Expect(body).To(Equal("newest spec prose"))

		for _, table := range []string{"agent_ticket_specs", "agent_ticket_tasks"} {
			var exists bool
			Expect(database.QueryRow(`
				SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)
			`, table).Scan(&exists)).To(Succeed())
			Expect(exists).To(BeFalse(), "%s must be gone", table)
		}
	})

	It("drops error_detail with the errored state and budget_usd with per-ticket budgets", func() {
		Expect(migrator.Migrate(nil, nil, budgetVersion)).To(Succeed())

		for _, column := range []string{"error_detail", "budget_usd"} {
			var exists bool
			Expect(database.QueryRow(`
				SELECT EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_name = 'agent_tickets' AND column_name = $1
				)
			`, column).Scan(&exists)).To(Succeed())
			Expect(exists).To(BeFalse(), "agent_tickets.%s must be gone", column)
		}
	})

	It("migrates down and back up without losing tickets", func() {
		id := insertTicket("round trip", "merged")
		Expect(migrator.Migrate(nil, nil, budgetVersion)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		var state string
		Expect(database.QueryRow(`SELECT state FROM agent_tickets WHERE id = $1`, id).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("closed"))

		var budgetColumn bool
		Expect(database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'agent_tickets' AND column_name = 'budget_usd'
			)
		`).Scan(&budgetColumn)).To(Succeed())
		Expect(budgetColumn).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, budgetVersion)).To(Succeed())
		Expect(database.QueryRow(`SELECT state FROM agent_tickets WHERE id = $1`, id).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("closed"))
	})
})
