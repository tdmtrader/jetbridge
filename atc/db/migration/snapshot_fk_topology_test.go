package migration_test

import (
	"database/sql"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/db/migration"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The snapshot design never deletes an agent_snapshots row: bytes expire, rows
// and lineage stay forever. Three foreign keys are nonetheless ON DELETE
// CASCADE, so any future delete path would silently destroy review, feedback,
// and projection rows instead of failing. This test makes both the RESTRICT set
// and the CASCADE trio a deliberate, reviewed choice.
var _ = Describe("agent_snapshots foreign-key topology", func() {
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
		Expect(migrator.Migrate(nil, nil, jetbridgeHeadMigration)).To(Succeed())
	})

	AfterEach(func() {
		_ = database.Close()
		for _, connection := range lockDB {
			_ = connection.Close()
		}
	})

	It("keeps every snapshot reference RESTRICT except the three declared CASCADEs", func() {
		// key: "<child table>(<ordered child columns>)"; value: delete action.
		want := map[string]string{
			"agent_experiment_evaluations(measurement_snapshot_id)":        "RESTRICT",
			"agent_experiment_fixture_bindings(snapshot_id)":               "RESTRICT",
			"agent_publication_occurrences(approval_answer_snapshot_id)":   "RESTRICT",
			"agent_publication_occurrences(approval_question_snapshot_id)": "RESTRICT",
			"agent_publication_occurrences(input_snapshot_id)":             "RESTRICT",
			"agent_publications(approval_answer_snapshot_id)":              "RESTRICT",
			"agent_publications(approval_question_snapshot_id)":            "RESTRICT",
			"agent_publications(input_snapshot_id)":                        "RESTRICT",
			"agent_snapshot_exposures(input_snapshot_id,tree_digest)":      "RESTRICT",
			"agent_snapshot_grants(snapshot_id)":                           "RESTRICT",
			"agent_snapshot_lineage(input_snapshot_id)":                    "RESTRICT",
			"agent_snapshot_productions(snapshot_id)":                      "RESTRICT",
			"agent_snapshot_retention_claims(snapshot_id)":                 "RESTRICT",
			"agent_tickets(repository_snapshot_id)":                        "RESTRICT",
			"agent_tickets(work_item_snapshot_id)":                         "RESTRICT",
			"agent_workflow_outcomes(modification_snapshot_id)":            "RESTRICT",
			"agent_workflow_outcomes(output_snapshot_id)":                  "RESTRICT",
			"agent_workflow_run_snapshots(snapshot_id)":                    "RESTRICT",
			"agent_workflow_waits(answer_snapshot_id)":                     "RESTRICT",
			"agent_workflow_waits(default_snapshot_id)":                    "RESTRICT",
			"agent_workflow_waits(question_snapshot_id)":                   "RESTRICT",

			// The only three cascades. Each is a derived projection of the
			// snapshot it points at, so it may not outlive that row. Adding a
			// fourth, or reaching one of these from a new delete path, is a
			// data-loss decision and must edit this list on purpose.
			"agent_feedback(review_snapshot_id)":               "CASCADE",
			"agent_repository_change_projections(snapshot_id)": "CASCADE",
			"agent_reviews(snapshot_id)":                       "CASCADE",
		}

		rows, err := database.Query(`
			SELECT c.conrelid::regclass::text AS child_table,
			       (
			           SELECT string_agg(a.attname, ',' ORDER BY k.ordinality)
			           FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, ordinality)
			           JOIN pg_attribute a
			             ON a.attrelid = c.conrelid AND a.attnum = k.attnum
			       ) AS child_columns,
			       pg_get_constraintdef(c.oid) AS definition
			FROM pg_constraint c
			WHERE c.contype = 'f' AND c.confrelid = 'agent_snapshots'::regclass
			ORDER BY 1, 2
		`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()

		got := map[string]string{}
		for rows.Next() {
			var table, columns, definition string
			Expect(rows.Scan(&table, &columns, &definition)).To(Succeed())
			key := table + "(" + columns + ")"
			Expect(definition).To(ContainSubstring("REFERENCES agent_snapshots"), key)
			action, found := want[key]
			Expect(found).To(BeTrue(),
				"undeclared foreign key into agent_snapshots: %s -> %s", key, definition)
			Expect(definition).To(ContainSubstring("ON DELETE "+action),
				"%s must be ON DELETE %s, got %q", key, action, definition)
			got[key] = action
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(got).To(Equal(want), "the set of foreign keys into agent_snapshots changed")

		cascades := 0
		for _, action := range got {
			if action == "CASCADE" {
				cascades++
			}
		}
		Expect(got).To(HaveLen(24))
		Expect(cascades).To(Equal(3))
	})
})
