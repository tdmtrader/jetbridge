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

var _ = Describe("associate runs with tickets migration", func() {
	const beforeVersion, targetVersion = 1773106157, 1773106158

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
		var count int
		Expect(database.QueryRow(`
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		`, table, column).Scan(&count)).To(Succeed())
		return count > 0
	}

	// seedRun inserts a workflow run through raw SQL so the spec exercises the
	// schema rather than the factory that reads it.
	seedRun := func(key string) int64 {
		var teamID int
		Expect(database.QueryRow(
			`INSERT INTO teams (name) VALUES ($1) RETURNING id`, "team-"+key).Scan(&teamID)).To(Succeed())
		var definitionID int
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'plan: []', 'alice', 3, 1)
			RETURNING id
		`, "wf-"+key, fmt.Sprintf("%064d", len(key))).Scan(&definitionID)).To(Succeed())
		var runID int64
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version, definition_content_hash,
				 idempotency_key, parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status)
			VALUES ('workflow', $1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $5,
			        'ticket', '1', 'alice', 'admitting')
			RETURNING id
		`, teamID, "team-"+key, definitionID, "wf-"+key,
			fmt.Sprintf("%064d", len(key)), "idem-"+key).Scan(&runID)).To(Succeed())
		return runID
	}

	seedTicket := func(externalRef string) int64 {
		var id int64
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, repo, external_ref)
			VALUES ('t', 'org/repo', $1) RETURNING id
		`, externalRef).Scan(&id)).To(Succeed())
		return id
	}

	It("moves the association onto the run and drops the inverse column", func() {
		Expect(columnExists("agent_workflow_runs", "ticket_id")).To(BeFalse())
		Expect(columnExists("agent_tickets", "workflow_run_id")).To(BeTrue())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(columnExists("agent_workflow_runs", "ticket_id")).To(BeTrue())
		Expect(columnExists("agent_workflow_runs", "ticket_reference")).To(BeTrue())
		Expect(columnExists("agent_tickets", "workflow_run_id")).To(BeFalse())
	})

	It("adopts the association the dropped column already expressed", func() {
		runID := seedRun("backfill")
		ticketID := seedTicket("PROJ-7")
		_, err := database.Exec(
			`UPDATE agent_tickets SET workflow_run_id = $2 WHERE id = $1`, ticketID, runID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var adoptedTicket int64
		var reference string
		Expect(database.QueryRow(
			`SELECT ticket_id, ticket_reference FROM agent_workflow_runs WHERE id = $1`, runID,
		).Scan(&adoptedTicket, &reference)).To(Succeed())
		Expect(adoptedTicket).To(Equal(ticketID))
		Expect(reference).To(Equal("PROJ-7"))
	})

	It("synthesises evidence for a native ticket with no external reference", func() {
		runID := seedRun("native")
		ticketID := seedTicket("")
		_, err := database.Exec(
			`UPDATE agent_tickets SET workflow_run_id = $2 WHERE id = $1`, ticketID, runID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var reference string
		Expect(database.QueryRow(
			`SELECT ticket_reference FROM agent_workflow_runs WHERE id = $1`, runID,
		).Scan(&reference)).To(Succeed())
		Expect(reference).To(Equal(fmt.Sprintf("ticket-%d", ticketID)))
	})

	It("leaves a run with no ticket unattached", func() {
		runID := seedRun("standalone")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		var ticketID sql.NullInt64
		var reference string
		Expect(database.QueryRow(
			`SELECT ticket_id, ticket_reference FROM agent_workflow_runs WHERE id = $1`, runID,
		).Scan(&ticketID, &reference)).To(Succeed())
		Expect(ticketID.Valid).To(BeFalse())
		Expect(reference).To(BeEmpty())
	})

	It("clears the live reference on ticket deletion while keeping the evidence", func() {
		runID := seedRun("cascade")
		ticketID := seedTicket("PROJ-9")
		_, err := database.Exec(
			`UPDATE agent_tickets SET workflow_run_id = $2 WHERE id = $1`, ticketID, runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		// The immutability trigger must not block the ON DELETE SET NULL
		// cascade; a trigger that did would break deletion entirely.
		_, err = database.Exec(`DELETE FROM agent_tickets WHERE id = $1`, ticketID)
		Expect(err).NotTo(HaveOccurred())

		var survivingTicket sql.NullInt64
		var reference string
		Expect(database.QueryRow(
			`SELECT ticket_id, ticket_reference FROM agent_workflow_runs WHERE id = $1`, runID,
		).Scan(&survivingTicket, &reference)).To(Succeed())
		Expect(survivingTicket.Valid).To(BeFalse())
		Expect(reference).To(Equal("PROJ-9"))
	})

	It("refuses admitting a live association with no durable evidence", func() {
		runID := seedRun("checked")
		ticketID := seedTicket("PROJ-11")
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		// The insert path is the one the CHECK constraint owns; the trigger
		// only guards updates, so an evidence-less admission must be rejected
		// by the constraint itself.
		insertWithReference := func(key, reference string) error {
			_, err := database.Exec(`
				INSERT INTO agent_workflow_runs
					(definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
					 workflow_version, schema_version, signature_version, definition_content_hash,
					 idempotency_key, parameterized_config, parameterized_config_hash,
					 origin_kind, origin_reference, created_by, status, ticket_id, ticket_reference)
				SELECT definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
				       workflow_version, schema_version, signature_version, definition_content_hash,
				       $2, parameterized_config, parameterized_config_hash,
				       origin_kind, origin_reference, created_by, status, $3, $4
				FROM agent_workflow_runs WHERE id = $1
			`, runID, key, ticketID, reference)
			return err
		}

		Expect(insertWithReference("blank-evidence", "")).To(HaveOccurred())
		Expect(insertWithReference("whitespace-evidence", "   ")).To(HaveOccurred())
		Expect(insertWithReference("real-evidence", "PROJ-11")).NotTo(HaveOccurred())
	})

	It("rolls back lossily but re-appliably", func() {
		runID := seedRun("rollback")
		ticketID := seedTicket("PROJ-13")
		_, err := database.Exec(
			`UPDATE agent_tickets SET workflow_run_id = $2 WHERE id = $1`, ticketID, runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		Expect(columnExists("agent_workflow_runs", "ticket_id")).To(BeFalse())
		Expect(columnExists("agent_workflow_runs", "ticket_reference")).To(BeFalse())
		Expect(columnExists("agent_tickets", "workflow_run_id")).To(BeTrue())

		var restored sql.NullInt64
		Expect(database.QueryRow(
			`SELECT workflow_run_id FROM agent_tickets WHERE id = $1`, ticketID).Scan(&restored)).To(Succeed())
		Expect(restored.Valid).To(BeTrue())
		Expect(restored.Int64).To(Equal(runID))

		var functions int
		Expect(database.QueryRow(`
			SELECT count(*) FROM pg_proc
			WHERE proname = 'enforce_agent_workflow_run_ticket_immutability'
		`).Scan(&functions)).To(Succeed())
		Expect(functions).To(BeZero())

		// A rollback that cannot be migrated forward again strands an operator
		// mid-upgrade.
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(columnExists("agent_workflow_runs", "ticket_id")).To(BeTrue())
	})

	It("keeps only the earliest of a ticket's several runs on rollback", func() {
		first := seedRun("lossy-first")
		ticketID := seedTicket("PROJ-17")
		_, err := database.Exec(
			`UPDATE agent_tickets SET workflow_run_id = $2 WHERE id = $1`, ticketID, first)
		Expect(err).NotTo(HaveOccurred())
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		// The second run joins the same ticket at admission — which is exactly
		// what the dropped column could never hold — and occurs later.
		_, err = database.Exec(`
			UPDATE agent_workflow_runs SET created_at = now() - interval '1 hour' WHERE id = $1
		`, first)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
				 workflow_version, schema_version, signature_version, definition_content_hash,
				 idempotency_key, parameterized_config, parameterized_config_hash,
				 origin_kind, origin_reference, created_by, status, ticket_id, ticket_reference)
			SELECT definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
			       workflow_version, schema_version, signature_version, definition_content_hash,
			       'second-attempt', parameterized_config, parameterized_config_hash,
			       origin_kind, origin_reference, created_by, status, $2, 'PROJ-17'
			FROM agent_workflow_runs WHERE id = $1
		`, first, ticketID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())

		var restored sql.NullInt64
		Expect(database.QueryRow(
			`SELECT workflow_run_id FROM agent_tickets WHERE id = $1`, ticketID).Scan(&restored)).To(Succeed())
		Expect(restored.Int64).To(Equal(first), "the earliest run survives; the rest are lost by necessity")
	})
})
