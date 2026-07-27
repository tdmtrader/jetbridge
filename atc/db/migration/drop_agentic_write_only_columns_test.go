package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("drop agentic write-only columns", func() {
	const (
		beforeVersion = 1773106134
		dropVersion   = 1773106136
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

	columnExists := func(table, column string) bool {
		var exists bool
		Expect(database.QueryRow(`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = $1 AND column_name = $2
			)
		`, table, column).Scan(&exists)).To(Succeed())
		return exists
	}

	It("rewrites surviving parked metrics to error and narrows the status CHECK", func() {
		_, err := database.Exec(`
			INSERT INTO agent_run_metrics (build_id, plan_id, step_name, status, summary, session_id)
			VALUES (1, 'p', 'implement', 'parked', 'awaiting human', 'sess-42'),
			       (1, 'q', 'implement', 'ok', 'delivered', '')
		`)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		// A parked row was an ingestion whose event stream ended without
		// step.end. That is exactly what the unconditional rule now records.
		var status string
		Expect(database.QueryRow(
			`SELECT status FROM agent_run_metrics WHERE plan_id = 'p'`).Scan(&status)).To(Succeed())
		Expect(status).To(Equal("error"))
		Expect(database.QueryRow(
			`SELECT status FROM agent_run_metrics WHERE plan_id = 'q'`).Scan(&status)).To(Succeed())
		Expect(status).To(Equal("ok"))

		_, err = database.Exec(`
			INSERT INTO agent_run_metrics (build_id, plan_id, step_name, status)
			VALUES (2, 'r', 'implement', 'parked')`)
		Expect(err).To(HaveOccurred(), "the CHECK must reject a status nothing can write")

		for _, kept := range []string{"ok", "failed", "error", "incomplete"} {
			_, err = database.Exec(`
				INSERT INTO agent_run_metrics (build_id, plan_id, step_name, status)
				VALUES (3, $1, 'implement', $1)`, kept)
			Expect(err).NotTo(HaveOccurred(), "status %q must still be accepted", kept)
		}

		Expect(columnExists("agent_run_metrics", "session_id")).To(BeFalse())
	})

	It("drops the unread cost rollup view but keeps the platform service user", func() {
		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		var viewExists bool
		Expect(database.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM information_schema.views WHERE table_name = 'agent_cost_daily_rollup')
		`).Scan(&viewExists)).To(Succeed())
		Expect(viewExists).To(BeFalse())

		var username string
		Expect(database.QueryRow(
			`SELECT username FROM users WHERE sub = 'agent-platform'`).Scan(&username)).To(Succeed())
		Expect(username).To(Equal("platform"))
	})

	It("narrows publication_state to the states the evidence-shape constraint admits", func() {
		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		var constraintSource string
		Expect(database.QueryRow(`
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conname = 'agent_workflow_outcomes_publication_state_check'
		`).Scan(&constraintSource)).To(Succeed())
		Expect(constraintSource).To(ContainSubstring("not_requested"))
		Expect(constraintSource).To(ContainSubstring("published"))
		Expect(constraintSource).NotTo(ContainSubstring("pending"))
		Expect(constraintSource).NotTo(ContainSubstring("failed"))
	})

	It("drops the jira seams and the read-never-written last_verified_at", func() {
		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		Expect(columnExists("agent_user_credentials", "jira_account_id")).To(BeFalse())
		Expect(columnExists("agent_user_credentials", "last_verified_at")).To(BeFalse())

		_, err := database.Exec(`
			INSERT INTO agent_tickets (title, body, repo, origin)
			VALUES ('jira ticket', 'body', 'repo', 'jira')`)
		Expect(err).To(HaveOccurred(), "'jira' must no longer be an origin")

		for _, origin := range []string{"web", "fly", "retrospective"} {
			_, err = database.Exec(`
				INSERT INTO agent_tickets (title, body, repo, origin)
				VALUES ($1, 'body', 'repo', $1)`, origin)
			Expect(err).NotTo(HaveOccurred(), "origin %q must still be accepted", origin)
		}
	})

	It("folds an existing jira-origin ticket onto web rather than failing the new CHECK", func() {
		var id int
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, body, repo, origin, external_ref)
			VALUES ('legacy', 'body', 'repo', 'jira', 'ENG-42')
			RETURNING id
		`).Scan(&id)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		var origin, externalRef string
		Expect(database.QueryRow(
			`SELECT origin, external_ref FROM agent_tickets WHERE id = $1`, id,
		).Scan(&origin, &externalRef)).To(Succeed())
		Expect(origin).To(Equal("web"))
		// external_ref stays: it is an origin-agnostic external identifier.
		Expect(externalRef).To(Equal("ENG-42"))
	})

	It("drops agent_tickets.branch but keeps the ticket's own repository selection", func() {
		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		Expect(columnExists("agent_tickets", "branch")).To(BeFalse())
		Expect(columnExists("agent_tickets", "repo")).To(BeTrue())
		Expect(columnExists("agent_tickets", "target_branch")).To(BeTrue())
	})

	It("drops the agent_reviews occurrence copies and their index", func() {
		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		for _, column := range []string{"build_name", "team_name", "pipeline_name", "job_name", "submitted_by"} {
			Expect(columnExists("agent_reviews", column)).To(BeFalse(), "agent_reviews.%s must be gone", column)
		}

		var indexExists bool
		Expect(database.QueryRow(`
			SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_agent_reviews_team_created')
		`).Scan(&indexExists)).To(Succeed())
		Expect(indexExists).To(BeFalse())
	})

	It("migrates down and back up", func() {
		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(columnExists("agent_run_metrics", "session_id")).To(BeTrue())
		Expect(columnExists("agent_tickets", "branch")).To(BeTrue())
		Expect(columnExists("agent_user_credentials", "jira_account_id")).To(BeTrue())
		Expect(columnExists("agent_reviews", "team_name")).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, dropVersion)).To(Succeed())
		Expect(columnExists("agent_run_metrics", "session_id")).To(BeFalse())
		Expect(columnExists("agent_tickets", "branch")).To(BeFalse())
	})
})
