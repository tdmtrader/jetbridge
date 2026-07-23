package migration_test

import (
	"database/sql"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ticket workflow-run link migration", func() {
	const beforeVersion, targetVersion = 1773106107, 1773106108
	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator

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

	It("round-trips durable adapter links while retaining tickets, runs, and snapshots", func() {
		var teamID, definitionID, ticketID, pipelineID, pipelineRunID int
		var workflowRunID, workItemID, repositoryID int64
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ('ticket-link-migration') RETURNING id`).Scan(&teamID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ('ticket-link-migration', 1, $1, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, 'ticket-link-migration', $2, 'ticket-link-migration', 1,
			        3, 1, $3, 'ticket-link-run', '{}', $4, 'ticket', '1', 'alice', 'admitting')
			RETURNING id
		`, teamID, definitionID, strings.Repeat("a", 64), strings.Repeat("b", 64)).Scan(&workflowRunID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots (type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('work-item', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("c", 64)).Scan(&workItemID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots (type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('repository', 1, $1, 1, 1, 'filesystem-tree-v1') RETURNING id
		`, "sha256:"+strings.Repeat("d", 64)).Scan(&repositoryID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, repo, workflow_name, workflow_version, workflow_definition_id)
			VALUES ('migration ticket', 'example/repo', 'ticket-link-migration', 1, $1)
			RETURNING id
		`, definitionID).Scan(&ticketID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO pipelines (name, team_id, secondary_ordering)
			VALUES ('ticket-link-template', $1, 1) RETURNING id
		`, teamID).Scan(&pipelineID)).To(Succeed())
		Expect(database.QueryRow(`
			INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
			VALUES ($1, $1, 1) RETURNING id
		`, pipelineID).Scan(&pipelineRunID)).To(Succeed())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		_, err := database.Exec(`
			UPDATE agent_tickets
			SET workflow_run_id = $2, work_item_snapshot_id = $3,
			    repository_snapshot_id = $4, dispatch_reservation_key = 'ticket-link-reservation',
			    pipeline_run_id = $5
			WHERE id = $1
		`, ticketID, workflowRunID, workItemID, repositoryID, pipelineRunID)
		Expect(err).NotTo(HaveOccurred())
		_, err = database.Exec(`
			INSERT INTO agent_tickets (title, repo, dispatch_reservation_key)
			VALUES ('duplicate', 'example/repo', 'ticket-link-reservation')
		`)
		Expect(err).To(HaveOccurred(), "reservation keys must be globally unique")

		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		for table, id := range map[string]int64{
			"agent_tickets": int64(ticketID), "agent_workflow_runs": workflowRunID,
			"agent_snapshots": workItemID,
		} {
			var count int
			Expect(database.QueryRow(`SELECT count(*) FROM `+table+` WHERE id = $1`, id).Scan(&count)).To(Succeed())
			Expect(count).To(Equal(1), table)
		}

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		var workflowLink, workItemLink, repositoryLink sql.NullInt64
		var reservation string
		Expect(database.QueryRow(`
			SELECT workflow_run_id, work_item_snapshot_id, repository_snapshot_id, dispatch_reservation_key
			FROM agent_tickets WHERE id = $1
		`, ticketID).Scan(&workflowLink, &workItemLink, &repositoryLink, &reservation)).To(Succeed())
		Expect(workflowLink.Valid || workItemLink.Valid || repositoryLink.Valid).To(BeFalse())
		Expect(reservation).To(BeEmpty())
	})
})
