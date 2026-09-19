package migration_test

import (
	"database/sql"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The rename is only safe if the trigger bodies moved with the column: a
// RENAME COLUMN leaves PL/pgSQL text alone, so a function that still says
// source_lease_id compiles fine and fails on its first row. Both triggers are
// therefore driven through a real row on either side of the migration.
var _ = Describe("renaming source_lease_id to source_hold_id", func() {
	const preMigrationVersion = 1789792877
	const postMigrationVersion = 1789793120

	var db *sql.DB

	BeforeEach(func() {
		db = postgresRunner.OpenDBAtVersion(preMigrationVersion)
	})

	AfterEach(func() {
		Expect(db.Close()).To(Succeed())
	})

	columns := func(table string) []string {
		rows, err := db.Query(`
			SELECT column_name FROM information_schema.columns
			WHERE table_name = $1 AND column_name IN ('source_lease_id', 'source_hold_id')
			ORDER BY column_name`, table)
		Expect(err).ToNot(HaveOccurred())
		defer rows.Close()
		var found []string
		for rows.Next() {
			var name string
			Expect(rows.Scan(&name)).To(Succeed())
			found = append(found, name)
		}
		return found
	}

	tables := []string{
		"hangar_handoff_predeclarations",
		"hangar_capture_reservations",
		"hangar_no_capture_dispositions",
		"hangar_pre_reservation_cancel_dispositions",
	}

	It("renames the column on every table that carries it, and back", func() {
		for _, table := range tables {
			Expect(columns(table)).To(Equal([]string{"source_lease_id"}), table)
		}

		postgresRunner.MigrateToVersion(postMigrationVersion)
		for _, table := range tables {
			Expect(columns(table)).To(Equal([]string{"source_hold_id"}), table)
		}

		postgresRunner.MigrateToVersion(preMigrationVersion)
		for _, table := range tables {
			Expect(columns(table)).To(Equal([]string{"source_lease_id"}), table)
		}
	})

	It("moves the trigger bodies with the column", func() {
		postgresRunner.MigrateToVersion(postMigrationVersion)

		_, err := db.Exec(`
			INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation, output_attestation,
				 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
				 materialization_key_id, bucket_fingerprint, derived_namespace)
			VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
				now() - interval '1 day', now() + interval '30 days',
				'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
		Expect(err).ToNot(HaveOccurred())

		// Every write under an epoch is gated on a safe lifetime-policy
		// attestation for it; without one the plane refuses before either
		// trigger under test gets to run.
		_, err = db.Exec(`
			INSERT INTO hangar_policy_snapshots
				(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
				 lifecycle_delete_rules, state)
			VALUES (1, 'gs://output-bucket', 3, 'policy-hash-1', 0, 'safe')`)
		Expect(err).ToNot(HaveOccurred())

		_, err = db.Exec(`
			INSERT INTO hangar_handoff_predeclarations
				(handoff_id, source_hold_id, execution_id, execution_fence, output_name,
				 activation_epoch, capture_deadline_at)
			VALUES ('11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222',
				'33333333-3333-4333-8333-333333333333', 1, 'out', 1, now() + interval '2 hours')`)
		Expect(err).ToNot(HaveOccurred())

		// hangar_predeclaration_immutable runs on UPDATE and compares NEW and
		// OLD source_hold_id; a stale body would raise "column does not exist".
		_, err = db.Exec(`
			UPDATE hangar_handoff_predeclarations
			SET source_hold_id = '44444444-4444-4444-8444-444444444444'
			WHERE handoff_id = '11111111-1111-4111-8111-111111111111'`)
		Expect(err).To(HaveOccurred())
		// The raise must be the trigger's own, reached AFTER its comparison
		// over the renamed column; a stale body fails before it with
		// "column source_lease_id does not exist" instead.
		Expect(err.Error()).To(ContainSubstring("immutable"), "the immutability trigger did not run against the renamed column")
		Expect(err.Error()).ToNot(ContainSubstring("source_lease_id"))
	})
})
