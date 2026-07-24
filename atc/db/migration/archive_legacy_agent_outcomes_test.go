package migration_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Task 2: migrate exact legacy agent_outcomes rows into the durable generic
// outcome table and archive the compatibility table. The migration never
// guesses an owning run or output; anything it cannot resolve exactly is set
// aside as inert audit data in agent_legacy_outcomes_unresolved.
var _ = Describe("legacy agent outcomes migration", func() {
	const beforeVersion, targetVersion = 1773106124, 1773106125

	var database *sql.DB
	var lockDB [lock.FactoryCount]*sql.DB
	var migrator migration.Migrator
	var seq int

	next := func() int {
		seq++
		return seq
	}
	digest := func() string { return fmt.Sprintf("sha256:%064x", next()) }
	hash := func() string { return fmt.Sprintf("%064x", next()) }

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
		seq = 0
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	// --- fixture helpers -------------------------------------------------

	insertTeam := func() (int, string) {
		name := fmt.Sprintf("legacy-outcomes-%d", next())
		var id int
		Expect(database.QueryRow(`INSERT INTO teams (name) VALUES ($1) RETURNING id`, name).Scan(&id)).To(Succeed())
		return id, name
	}

	// schema1Definition is a real decodable schema-1 manifest with no
	// disposition_output, so the migration must resolve the owning output by
	// requiring exactly one promoted public output on the run.
	schema1Definition := func(name string) string {
		return fmt.Sprintf(`schema_version: 1
name: %s
prompts: {work: work}
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`, name)
	}

	insertDefinition := func(name, definition string, schemaVersion, signatureVersion int) int {
		var id int
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, $3, 'alice', $4, $5)
			RETURNING id
		`, name, hash(), definition, schemaVersion, signatureVersion).Scan(&id)).To(Succeed())
		return id
	}

	insertRun := func(teamID int, teamName string, definitionID int, originKind, originReference string) int64 {
		var id int64
		Expect(database.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status)
			VALUES ($1, $2, $3, $2, 1, 3, 1, $4, $5, '{}', $6, $7, $8, 'alice', 'succeeded')
			RETURNING id
		`, teamID, teamName, definitionID, hash(), fmt.Sprintf("key-%d", next()),
			hash(), originKind, originReference).Scan(&id)).To(Succeed())
		return id
	}

	insertSnapshot := func() int64 {
		var id int64
		Expect(database.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ('review', 1, $1, 0, 0, 'inline')
			RETURNING id
		`, digest()).Scan(&id)).To(Succeed())
		return id
	}

	bindOutput := func(runID, snapshotID int64, port string) {
		_, err := database.Exec(`
			INSERT INTO agent_workflow_run_snapshots (workflow_run_id, direction, port_name, snapshot_id)
			VALUES ($1, 'output', $2, $3)
		`, runID, port, snapshotID)
		Expect(err).NotTo(HaveOccurred())
	}

	insertTicket := func(teamName string, definitionID int, runID int64) int {
		var id int
		Expect(database.QueryRow(`
			INSERT INTO agent_tickets (title, repo, workflow_name, workflow_definition_id, workflow_run_id)
			VALUES ('legacy ticket', 'acme/widgets', $1, $2, $3)
			RETURNING id
		`, teamName, definitionID, runID).Scan(&id)).To(Succeed())
		return id
	}

	// legacyColumns translates a single effective disposition into the two
	// legacy columns (merge_state, disposition) that encode it.
	legacyColumns := func(legacy string) (string, string) {
		switch legacy {
		case "merged":
			return "merged", ""
		case "merged_with_fixes":
			return "merged_with_fixes", ""
		case "sent_back":
			return "closed_unmerged", "sent_back"
		case "abandoned":
			return "closed_unmerged", "abandoned"
		case "concluded":
			return "concluded", "concluded"
		default:
			return "open", ""
		}
	}

	insertLegacyOutcome := func(ticketID int, mergeState, disposition string) {
		_, err := database.Exec(`
			INSERT INTO agent_outcomes (ticket_id, repo, branch, merge_state, disposition, disposed_by)
			VALUES ($1, 'acme/widgets', $2, $3, $4, 'reviewer')
		`, ticketID, fmt.Sprintf("agent/ticket-%d", ticketID), mergeState, disposition)
		Expect(err).NotTo(HaveOccurred())
	}

	type fixture struct {
		TicketID int
		RunID    int64
		OutputID int64
		TeamID   int
	}

	// insertExactLegacyOutcome builds a legacy outcome whose ticket links to
	// exactly one run through the durable agent_tickets.workflow_run_id adapter,
	// where the run has exactly one promoted public output.
	insertExactLegacyOutcome := func(legacy string) fixture {
		teamID, teamName := insertTeam()
		definitionID := insertDefinition(teamName, schema1Definition(teamName), 1, 0)
		// origin_kind is deliberately not 'ticket' so only the exact
		// agent_tickets.workflow_run_id link can resolve this run.
		runID := insertRun(teamID, teamName, definitionID, "manual", "")
		outputID := insertSnapshot()
		bindOutput(runID, outputID, "workspace")
		ticketID := insertTicket(teamName, definitionID, runID)
		mergeState, disposition := legacyColumns(legacy)
		insertLegacyOutcome(ticketID, mergeState, disposition)
		return fixture{TicketID: ticketID, RunID: runID, OutputID: outputID, TeamID: teamID}
	}

	// --- read helpers ----------------------------------------------------

	type workflowOutcome struct {
		Disposition       string
		HumanModified     bool
		InterventionCount int
		PublicationState  string
		Actor             string
		Labels            []string
	}

	readWorkflowOutcome := func(runID, outputID int64) workflowOutcome {
		var outcome workflowOutcome
		var labels []byte
		Expect(database.QueryRow(`
			SELECT disposition, human_modified, intervention_count, publication_state, actor, labels
			FROM agent_workflow_outcomes
			WHERE workflow_run_id = $1 AND output_snapshot_id = $2
		`, runID, outputID).Scan(
			&outcome.Disposition, &outcome.HumanModified, &outcome.InterventionCount,
			&outcome.PublicationState, &outcome.Actor, &labels,
		)).To(Succeed())
		Expect(json.Unmarshal(labels, &outcome.Labels)).To(Succeed())
		return outcome
	}

	unresolvedReason := func(ticketID int) (string, bool) {
		var reason string
		err := database.QueryRow(`SELECT reason FROM agent_legacy_outcomes_unresolved WHERE ticket_id = $1`, ticketID).Scan(&reason)
		if err == sql.ErrNoRows {
			return "", false
		}
		Expect(err).NotTo(HaveOccurred())
		return reason, true
	}

	archiveHas := func(ticketID int) bool {
		var exists bool
		Expect(database.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_legacy_outcomes_archive WHERE ticket_id = $1)`, ticketID).Scan(&exists)).To(Succeed())
		return exists
	}

	tableExists := func(name string) bool {
		var exists bool
		Expect(database.QueryRow(`
			SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = $1)
		`, name).Scan(&exists)).To(Succeed())
		return exists
	}

	// --- specs -----------------------------------------------------------

	DescribeTable("migrates exact legacy dispositions",
		func(legacy, generic string, modified bool, interventions int) {
			f := insertExactLegacyOutcome(legacy)

			Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

			outcome := readWorkflowOutcome(f.RunID, f.OutputID)
			Expect(outcome.Disposition).To(Equal(generic))
			Expect(outcome.HumanModified).To(Equal(modified))
			Expect(outcome.InterventionCount).To(Equal(interventions))
			Expect(outcome.PublicationState).To(Equal("not_requested"))
			Expect(outcome.Labels).To(ContainElement("legacy-ticket-migrated"))

			By("keeping the original row in the inert archive")
			Expect(archiveHas(f.TicketID)).To(BeTrue())
			_, unresolved := unresolvedReason(f.TicketID)
			Expect(unresolved).To(BeFalse())
		},
		Entry("merged", "merged", "merged", false, 0),
		Entry("merged with fixes", "merged_with_fixes", "merged", true, 1),
		Entry("sent back", "sent_back", "rejected", false, 0),
		Entry("abandoned", "abandoned", "abandoned", false, 0),
		Entry("concluded", "concluded", "accepted", false, 0),
	)

	It("resolves a run through the unique historical ticket-origin fallback", func() {
		teamID, teamName := insertTeam()
		definitionID := insertDefinition(teamName, schema1Definition(teamName), 1, 0)
		ticketID := next()
		// No agent_tickets adapter row: only the origin_kind/origin_reference
		// fallback can resolve this run.
		runID := insertRun(teamID, teamName, definitionID, "ticket", strconv.Itoa(ticketID))
		outputID := insertSnapshot()
		bindOutput(runID, outputID, "workspace")
		insertLegacyOutcome(ticketID, "merged", "")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		Expect(readWorkflowOutcome(runID, outputID).Disposition).To(Equal("merged"))
		_, unresolved := unresolvedReason(ticketID)
		Expect(unresolved).To(BeFalse())
	})

	It("refuses to guess when multiple ticket-origin runs exist", func() {
		teamID, teamName := insertTeam()
		definitionID := insertDefinition(teamName, schema1Definition(teamName), 1, 0)
		ticketID := next()
		firstRun := insertRun(teamID, teamName, definitionID, "ticket", strconv.Itoa(ticketID))
		bindOutput(firstRun, insertSnapshot(), "workspace")
		secondRun := insertRun(teamID, teamName, definitionID, "ticket", strconv.Itoa(ticketID))
		bindOutput(secondRun, insertSnapshot(), "workspace")
		insertLegacyOutcome(ticketID, "merged", "")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		reason, unresolved := unresolvedReason(ticketID)
		Expect(unresolved).To(BeTrue())
		Expect(reason).To(Equal("ambiguous-run"))
		Expect(archiveHas(ticketID)).To(BeTrue())
	})

	It("refuses to guess when the run has an ambiguous output", func() {
		teamID, teamName := insertTeam()
		definitionID := insertDefinition(teamName, schema1Definition(teamName), 1, 0)
		runID := insertRun(teamID, teamName, definitionID, "manual", "")
		bindOutput(runID, insertSnapshot(), "one")
		bindOutput(runID, insertSnapshot(), "two")
		ticketID := insertTicket(teamName, definitionID, runID)
		insertLegacyOutcome(ticketID, "merged", "")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		reason, unresolved := unresolvedReason(ticketID)
		Expect(unresolved).To(BeTrue())
		Expect(reason).To(Equal("ambiguous-output"))
		Expect(archiveHas(ticketID)).To(BeTrue())
	})

	It("refuses to guess when no run links to the ticket", func() {
		ticketID := next()
		insertLegacyOutcome(ticketID, "merged", "")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		reason, unresolved := unresolvedReason(ticketID)
		Expect(unresolved).To(BeTrue())
		Expect(reason).To(Equal("missing-run"))
		Expect(archiveHas(ticketID)).To(BeTrue())
	})

	It("never overwrites a native workflow outcome", func() {
		f := insertExactLegacyOutcome("merged")
		// A native row already owns this (run, output). The legacy value would
		// map to 'merged'; the migration must leave the native row untouched.
		_, err := database.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition, publication_state,
				 human_modified, intervention_count, actor, labels)
			VALUES ($1, $2, $3, 'accepted', 'not_requested', true, 3, 'human-reviewer', '["native"]'::jsonb)
		`, f.TeamID, f.RunID, f.OutputID)
		Expect(err).NotTo(HaveOccurred())

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())

		outcome := readWorkflowOutcome(f.RunID, f.OutputID)
		Expect(outcome.Disposition).To(Equal("accepted"))
		Expect(outcome.Actor).To(Equal("human-reviewer"))
		Expect(outcome.InterventionCount).To(Equal(3))
		Expect(outcome.HumanModified).To(BeTrue())
		Expect(outcome.Labels).To(ContainElement("native"))
		Expect(outcome.Labels).NotTo(ContainElement("legacy-ticket-migrated"))
	})

	It("round-trips down and up, preserving the archived rows", func() {
		f := insertExactLegacyOutcome("merged")

		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(readWorkflowOutcome(f.RunID, f.OutputID).Disposition).To(Equal("merged"))
		Expect(tableExists("agent_outcomes")).To(BeFalse())
		Expect(tableExists("agent_legacy_outcomes_archive")).To(BeTrue())
		Expect(tableExists("agent_legacy_outcomes_unresolved")).To(BeTrue())

		By("down restoring agent_outcomes and dropping the report")
		Expect(migrator.Migrate(nil, nil, beforeVersion)).To(Succeed())
		Expect(tableExists("agent_outcomes")).To(BeTrue())
		Expect(tableExists("agent_legacy_outcomes_archive")).To(BeFalse())
		Expect(tableExists("agent_legacy_outcomes_unresolved")).To(BeFalse())
		var count int
		Expect(database.QueryRow(`SELECT count(*) FROM agent_outcomes WHERE ticket_id = $1`, f.TicketID).Scan(&count)).To(Succeed())
		Expect(count).To(Equal(1))

		By("re-applying the migration cleanly")
		Expect(migrator.Migrate(nil, nil, targetVersion)).To(Succeed())
		Expect(readWorkflowOutcome(f.RunID, f.OutputID).Disposition).To(Equal("merged"))
		Expect(tableExists("agent_legacy_outcomes_archive")).To(BeTrue())
	})
})
