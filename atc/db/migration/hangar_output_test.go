package migration_test

import (
	"database/sql"
	"fmt"

	"github.com/concourse/concourse/atc/db/migration"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The migration that creates the Hangar output plane, and the head it follows.
//
// The number comes from atc/scripts/create-migration rather than from a person:
// there is one schema_migrations table and one linear version history, and a
// hand-picked number is how two branches end up claiming the same one.
const (
	hangarOutputVersion      = 1788936403
	hangarOutputPriorVersion = 1773105511
)

// The nineteen tables of the output plane, in the order the down migration
// removes them. Named here so that a table added later without a test is a
// visible omission rather than an invisible one.
var hangarOutputTables = []string{
	"hangar_output_activation_epochs",
	"hangar_handoff_predeclarations",
	"hangar_handoff_dispositions",
	"hangar_capture_reservations",
	"hangar_capture_attempt_leases",
	"hangar_no_capture_dispositions",
	"hangar_pre_reservation_cancel_dispositions",
	"hangar_logical_reservations",
	"hangar_exact_lifecycles",
	"hangar_receipt_stat_challenges",
	"hangar_output_receipts",
	"hangar_claims",
	"hangar_read_leases",
	"hangar_inventory_cursors",
	"hangar_inventory_debt",
	"hangar_reclaim_jobs",
	"hangar_reclaim_attempts",
	"hangar_policy_snapshots",
	"hangar_operation_leases",
}

const (
	sampleDigest      = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	otherDigest       = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	sampleGeneration  = 1725830823000001
	handoffID         = "11111111-1111-4111-8111-111111111111"
	sourceLeaseID     = "22222222-2222-4222-8222-222222222222"
	executionID       = "33333333-3333-4333-8333-333333333333"
	reservationID     = "44444444-4444-4444-8444-444444444444"
	captureOwnerID    = "55555555-5555-4555-8555-555555555555"
	claimID           = "66666666-6666-4666-8666-666666666666"
	readLeaseID       = "77777777-7777-4777-8777-777777777777"
	workerID          = "88888888-8888-4888-8888-888888888888"
	releaseIntentID   = "99999999-9999-4999-8999-999999999999"
	secondHandoffID   = "aaaaaaaa-1111-4111-8111-111111111111"
	secondLeaseID     = "aaaaaaaa-2222-4222-8222-222222222222"
	secondExecutionID = "aaaaaaaa-3333-4333-8333-333333333333"
	secondReservation = "aaaaaaaa-4444-4444-8444-444444444444"
	challengeNonce    = "nonce-0123456789abcdef"
)

// attempt runs statements in one transaction, forces every deferred constraint
// to fire, and rolls the whole thing back.
//
// SET CONSTRAINTS ALL IMMEDIATE is what makes the deferred triggers testable
// without committing: a deferred trigger that only fires at COMMIT would
// otherwise force every negative vector to leave rows behind, and every
// mutation to leave a dropped trigger behind. Here the DDL, the data and the
// verdict all live inside one transaction that never lands.
func attempt(database *sql.DB, statements ...string) error {
	GinkgoHelper()

	tx, err := database.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = tx.Rollback() }()

	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return err
	}

	return nil
}

func expectRefusal(database *sql.DB, because string, statements ...string) string {
	GinkgoHelper()

	err := attempt(database, statements...)
	ExpectWithOffset(1, err).To(HaveOccurred(), "the schema accepted %s", because)

	return err.Error()
}

func expectAccepted(database *sql.DB, twin string, statements ...string) {
	GinkgoHelper()

	Expect(attempt(database, statements...)).To(Succeed(),
		"the schema refused the valid twin: %s", twin)
}

var _ = Describe("the Hangar output plane schema", func() {
	var database *sql.DB

	BeforeEach(func() {
		database = postgresRunner.OpenDBAtVersion(hangarOutputVersion)
		DeferCleanup(func() { Expect(database.Close()).To(Succeed()) })
	})

	// Every fixture below is written as SQL rather than through a repository,
	// because what is under test is the schema. A helper that went through the
	// Go code would be asserting that the Go code agrees with itself.
	seedEpoch := func() {
		GinkgoHelper()
		mustExec(database, `
			INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation, output_attestation,
				 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
				 materialization_key_id, bucket_fingerprint, derived_namespace)
			VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
				now() - interval '1 day', now() + interval '30 days',
				'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
	}
	seedSafePolicy := func() {
		GinkgoHelper()
		mustExec(database, `
			INSERT INTO hangar_policy_snapshots
				(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
				 lifecycle_delete_rules, state)
			VALUES (1, 'gs://output-bucket', 3, 'policy-hash-1', 0, 'safe')`)
	}
	seedPredeclaration := func(handoff, lease, execution string, holdAcknowledged bool) {
		GinkgoHelper()
		hold := "NULL"
		if holdAcknowledged {
			hold = "now()"
		}
		mustExec(database, fmt.Sprintf(`
			INSERT INTO hangar_handoff_predeclarations
				(handoff_id, source_lease_id, execution_id, execution_fence, output_name,
				 activation_epoch, capture_deadline_at, hold_acknowledged_at)
			VALUES ('%s', '%s', '%s', 1, 'result', 1, now() + interval '24 hours', %s)`,
			handoff, lease, execution, hold))
	}
	// The Stage 2 commit as one transaction: the arbiter row and its single
	// branch child, exactly as CommitCaptureReservation will write them.
	stage2 := func(handoff, reservation string) []string {
		return []string{
			fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
				VALUES ('%s', 'capture')`, handoff),
			fmt.Sprintf(`INSERT INTO hangar_capture_reservations
				(reservation_id, handoff_id, execution_id, activation_epoch, source_lease_id,
				 producer_checkpoint_id, finish_acknowledgement, finish_successful,
				 capture_fence, capture_deadline_at)
				SELECT '%s', handoff_id, execution_id, activation_epoch, source_lease_id,
					'opaque-checkpoint', '{"kind":"finish"}', true, 1, capture_deadline_at
				FROM hangar_handoff_predeclarations WHERE handoff_id = '%s'`,
				reservation, handoff),
		}
	}
	commitStage2 := func(handoff, reservation string) {
		GinkgoHelper()
		tx, err := database.Begin()
		Expect(err).NotTo(HaveOccurred())
		// Without this, a fixture that fails mid-transaction leaves the suite's
		// one connection idle in transaction, and the next spec's DROP DATABASE
		// reports a session it cannot terminate rather than the real failure.
		defer func() { _ = tx.Rollback() }()
		for _, statement := range stage2(handoff, reservation) {
			_, err := tx.Exec(statement)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(tx.Commit()).To(Succeed())
	}
	seedCaptureLease := func(reservation string, fence int) {
		GinkgoHelper()
		mustExec(database, fmt.Sprintf(`
			INSERT INTO hangar_capture_attempt_leases
				(reservation_id, owner_id, capture_fence, expires_at)
			VALUES ('%s', '%s', %d, now() + interval '15 minutes')`,
			reservation, captureOwnerID, fence))
	}
	seedLogicalReservation := func(reservation, digest string, fence int) {
		GinkgoHelper()
		mustExec(database, fmt.Sprintf(`
			INSERT INTO hangar_logical_reservations
				(reservation_id, scope, digest, logical_bytes, capture_fence)
			VALUES ('%s', 'team-a', '%s', 4096, %d)`, reservation, digest, fence))
	}
	seedLifecycle := func(digest string, generation int64) int64 {
		GinkgoHelper()
		var id int64
		Expect(database.QueryRow(fmt.Sprintf(`
			INSERT INTO hangar_exact_lifecycles
				(scope, digest, generation, metageneration, activation_epoch,
				 marker_version, origin, state)
			VALUES ('team-a', '%s', %d, 1, 1, 'hangar-output-v1', 'registered', 'registered')
			RETURNING id`, digest, generation)).Scan(&id)).To(Succeed())

		return id
	}
	seedClaim := func(id string, lifecycle int64) {
		GinkgoHelper()
		mustExec(database, fmt.Sprintf(`
			INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
			VALUES ('%s', %d, 1, 'opaque-binding')`, id, lifecycle))
	}

	// The whole valid chain, from epoch to claim. Every negative vector below
	// is one deviation from it, so that "the schema refused this" always means
	// "the schema refused this one thing".
	// The whole valid chain past the epoch, policy and predeclaration its
	// callers already seeded.
	seedFullChain := func() int64 {
		GinkgoHelper()
		commitStage2(handoffID, reservationID)
		seedCaptureLease(reservationID, 1)
		seedLogicalReservation(reservationID, sampleDigest, 1)
		mustExec(database, fmt.Sprintf(`
			UPDATE hangar_capture_reservations
			SET first_create_attempted_at = now(), past_irreversible_publish_point = true
			WHERE reservation_id = '%s'`, reservationID))
		lifecycle := seedLifecycle(sampleDigest, sampleGeneration)
		mustExec(database, fmt.Sprintf(`
			INSERT INTO hangar_receipt_stat_challenges
				(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
				 scope, digest, generation, capture_fence, not_after, consumed_at)
			VALUES ('%s', '%s', '%s', 1, 'receipt-key-1', 'team-a', '%s', %d, 1,
				now() + interval '5 minutes', now())`,
			challengeNonce, handoffID, reservationID, sampleDigest, sampleGeneration))
		mustExec(database, fmt.Sprintf(`
			INSERT INTO hangar_output_receipts
				(reservation_id, lifecycle_id, handoff_id, activation_epoch, receipt_key_id,
				 challenge_nonce, marker_version, algorithm, claims, signature)
			VALUES ('%s', %d, '%s', 1, 'receipt-key-1', '%s', 'hangar-output-v1', 'ed25519',
				'{}', 'signature')`,
			reservationID, lifecycle, handoffID, challengeNonce))
		mustExec(database, fmt.Sprintf(`
			UPDATE hangar_logical_reservations SET state = 'registered'
			WHERE reservation_id = '%s'`, reservationID))

		return lifecycle
	}

	Describe("the upgrade", func() {
		It("creates every table of the output plane", func() {
			for _, table := range hangarOutputTables {
				var exists bool
				Expect(database.QueryRow(
					`SELECT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname = 'public' AND tablename = $1)`,
					table,
				).Scan(&exists)).To(Succeed())
				Expect(exists).To(BeTrue(), "table %s was not created", table)
			}
		})

		// The migration must not turn the feature on. Req 57 says the epoch
		// attests migrations, keys, workers, bucket, policy, namespaces and
		// principals, and a DDL script can observe none of them.
		It("creates held state only, with no default-ready epoch", func() {
			var epochs int
			Expect(database.QueryRow(`SELECT count(*) FROM hangar_output_activation_epochs`).
				Scan(&epochs)).To(Succeed())
			Expect(epochs).To(BeZero(), "the upgrade seeded an activation epoch; a migration "+
				"that inserts one is a migration that enables the feature")

			for _, table := range hangarOutputTables {
				expectRowCount(database, table, 0)
			}
		})

		It("leaves nothing that can be admitted without an epoch", func() {
			// Every table either references the epoch or hangs off something
			// that does, so on a freshly migrated database the first insert
			// anywhere fails for want of an activation epoch.
			err := attempt(database, fmt.Sprintf(`
				INSERT INTO hangar_handoff_predeclarations
					(handoff_id, source_lease_id, execution_id, execution_fence, output_name,
					 activation_epoch, capture_deadline_at)
				VALUES ('%s', '%s', '%s', 1, 'result', 1, now() + interval '24 hours')`,
				handoffID, sourceLeaseID, executionID))
			Expect(err).To(MatchError(ContainSubstring("hangar_handoff_predeclarations_activation_epoch_fkey")))
		})
	})

	Describe("activation epochs", func() {
		BeforeEach(seedEpoch)

		// Decision F1. The index is on `enabled` alone, per facet, and it says
		// nothing about any other state.
		It("refuses a second enabled row for the same facet", func() {
			message := expectRefusal(database, "a second enabled base epoch", `
				INSERT INTO hangar_output_activation_epochs
					(epoch_id, base_state, output_state, base_attestation)
				VALUES (2, 'enabled', 'initial', '{}')`)
			Expect(message).To(ContainSubstring("hangar_output_one_enabled_base_epoch"))

			message = expectRefusal(database, "a second enabled output epoch", `
				INSERT INTO hangar_output_activation_epochs
					(epoch_id, base_state, output_state, base_attestation, output_attestation,
					 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
					 materialization_key_id, bucket_fingerprint, derived_namespace)
				VALUES (2, 'attested', 'enabled', '{}', '{}', 'receipt-key-2',
					now(), now() + interval '30 days', 'materialize-key-2', 'gs://b', 'ns')`)
			Expect(message).To(ContainSubstring("hangar_output_one_enabled_output_epoch"))
		})

		// The overlap is the point: it is what gives rotation no emission gap.
		// An index phrased "at most one non-terminal row" would forbid this and
		// force a gap on every rotation.
		//
		// The swap is one transaction, not two steps. Taken literally, "CAS the
		// next epoch to enabled and only then move the outgoing row to
		// draining" would need two `enabled` rows for an instant, which this
		// index forbids; committing both moves together is what delivers the
		// property the rule is actually about -- no instant with two, and no
		// instant with none.
		It("accepts a draining row and an enabled row for the same facet", func() {
			mustExec(database, `
				INSERT INTO hangar_output_activation_epochs
					(epoch_id, base_state, output_state, base_attestation, output_attestation,
					 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
					 materialization_key_id, bucket_fingerprint, derived_namespace)
				VALUES (2, 'attested', 'attested', '{}', '{}', 'receipt-key-2',
					now(), now() + interval '30 days',
					'materialize-key-2', 'gs://output-bucket', 'deployment/ns')`)

			rotate := func(facet string) {
				GinkgoHelper()
				tx, err := database.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer func() { _ = tx.Rollback() }()
				_, err = tx.Exec(fmt.Sprintf(`UPDATE hangar_output_activation_epochs
					SET %s = 'draining', revision = revision + 1 WHERE epoch_id = 1`, facet))
				Expect(err).NotTo(HaveOccurred())
				_, err = tx.Exec(fmt.Sprintf(`UPDATE hangar_output_activation_epochs
					SET %s = 'enabled', revision = revision + 1 WHERE epoch_id = 2`, facet))
				Expect(err).NotTo(HaveOccurred())
				Expect(tx.Commit()).To(Succeed())
			}
			states := func(facet string) map[int]string {
				GinkgoHelper()
				rows, err := database.Query(fmt.Sprintf(
					`SELECT epoch_id, %s FROM hangar_output_activation_epochs`, facet))
				Expect(err).NotTo(HaveOccurred())
				defer rows.Close()
				found := map[int]string{}
				for rows.Next() {
					var id int
					var state string
					Expect(rows.Scan(&id, &state)).To(Succeed())
					found[id] = state
				}
				Expect(rows.Err()).NotTo(HaveOccurred())

				return found
			}

			rotate("output_state")
			Expect(states("output_state")).To(Equal(map[int]string{1: "draining", 2: "enabled"}))

			// Output leaves service before base does, which is the order
			// `drain --facet=all` uses and the only one the "output can never
			// be ready without base control" check permits.
			mustExec(database, `UPDATE hangar_output_activation_epochs
				SET output_state = 'disabled', revision = revision + 1 WHERE epoch_id = 1`)
			rotate("base_state")
			Expect(states("base_state")).To(Equal(map[int]string{1: "draining", 2: "enabled"}))
		})

		It("has no retired state anywhere in the schema", func() {
			var mentions int
			Expect(database.QueryRow(`
				SELECT count(*) FROM pg_constraint
				WHERE conrelid = 'hangar_output_activation_epochs'::regclass
				  AND pg_get_constraintdef(oid) LIKE '%retired%'`).Scan(&mentions)).To(Succeed())
			Expect(mentions).To(BeZero())

			var ordinal sql.NullInt64
			Expect(database.QueryRow(`SELECT hangar_output_facet_ordinal('retired')`).
				Scan(&ordinal)).To(Succeed())
			Expect(ordinal.Valid).To(BeFalse(), "'retired' is a facet state; disabled is terminal "+
				"and rotation creates a new epoch row rather than reviving an old one")
		})

		It("refuses a facet moving backwards, and disabled is terminal", func() {
			mustExec(database, `UPDATE hangar_output_activation_epochs
				SET output_state = 'draining', revision = revision + 1 WHERE epoch_id = 1`)
			mustExec(database, `UPDATE hangar_output_activation_epochs
				SET output_state = 'disabled', revision = revision + 1 WHERE epoch_id = 1`)

			Expect(expectRefusal(database, "a disabled output facet returning to enabled", `
				UPDATE hangar_output_activation_epochs
				SET output_state = 'enabled', revision = revision + 1 WHERE epoch_id = 1`)).
				To(ContainSubstring("moved output_state backwards"))
		})

		It("refuses a write that does not advance the revision", func() {
			Expect(expectRefusal(database, "a write with an unchanged revision", `
				UPDATE hangar_output_activation_epochs SET cohort_digest = 'x' WHERE epoch_id = 1`)).
				To(ContainSubstring("without advancing its revision"))
		})

		It("refuses an attested facet with no evidence bundle", func() {
			expectRefusal(database, "an attested base facet with no attestation", `
				INSERT INTO hangar_output_activation_epochs (epoch_id, base_state, output_state)
				VALUES (2, 'attested', 'initial')`)

			expectAccepted(database, "an attested base facet carrying its bundle", `
				INSERT INTO hangar_output_activation_epochs
					(epoch_id, base_state, output_state, base_attestation)
				VALUES (2, 'attested', 'initial', '{"cohort":[]}')`)
		})

		It("refuses output readiness without base control", func() {
			expectRefusal(database, "an attesting output facet over an initial base", `
				INSERT INTO hangar_output_activation_epochs
					(epoch_id, base_state, output_state, output_attestation)
				VALUES (2, 'initial', 'attesting', '{}')`)
		})
	})

	Describe("the relational groups", func() {
		It("gives every table a primary key", func() {
			for _, table := range hangarOutputTables {
				var keys int
				Expect(database.QueryRow(`
					SELECT count(*) FROM pg_constraint
					WHERE conrelid = $1::regclass AND contype = 'p'`, table).Scan(&keys)).To(Succeed())
				Expect(keys).To(Equal(1), "table %s has no primary key", table)
			}
		})

		// The predeclaration is non-authorizing, and this is the assertion that
		// says so in a form a future column cannot quietly widen: the exact
		// column set, listed. There is no producer checkpoint, success fact,
		// capture owner or lease, scope, digest, seal, receipt, publication or
		// claim here, and no API can put one here because there is nowhere.
		It("gives the predeclaration exactly the pre-start facts and nothing else", func() {
			var columns []string
			rows, err := database.Query(`
				SELECT column_name FROM information_schema.columns
				WHERE table_name = 'hangar_handoff_predeclarations' ORDER BY column_name`)
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			for rows.Next() {
				var name string
				Expect(rows.Scan(&name)).To(Succeed())
				columns = append(columns, name)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())

			Expect(columns).To(ConsistOf(
				"activation_epoch",
				"capture_deadline_at",
				"created_at",
				"execution_fence",
				"execution_id",
				"handoff_id",
				"hold_acknowledged_at",
				"output_name",
				"source_lease_id",
			))
		})

		It("wires the foreign keys that order the plane", func() {
			type edge struct{ from, to string }
			var found []edge
			rows, err := database.Query(`
				SELECT c.conrelid::regclass::text, c.confrelid::regclass::text
				FROM pg_constraint c
				WHERE c.contype = 'f' AND c.conrelid::regclass::text LIKE 'hangar_%'`)
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			for rows.Next() {
				var e edge
				Expect(rows.Scan(&e.from, &e.to)).To(Succeed())
				found = append(found, e)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())

			Expect(found).To(ContainElements(
				edge{"hangar_handoff_dispositions", "hangar_handoff_predeclarations"},
				edge{"hangar_capture_reservations", "hangar_handoff_dispositions"},
				edge{"hangar_capture_attempt_leases", "hangar_capture_reservations"},
				edge{"hangar_no_capture_dispositions", "hangar_handoff_dispositions"},
				edge{"hangar_pre_reservation_cancel_dispositions", "hangar_handoff_dispositions"},
				edge{"hangar_logical_reservations", "hangar_capture_reservations"},
				edge{"hangar_output_receipts", "hangar_logical_reservations"},
				edge{"hangar_output_receipts", "hangar_exact_lifecycles"},
				edge{"hangar_output_receipts", "hangar_receipt_stat_challenges"},
				edge{"hangar_claims", "hangar_exact_lifecycles"},
				edge{"hangar_read_leases", "hangar_claims"},
				edge{"hangar_inventory_debt", "hangar_inventory_cursors"},
				edge{"hangar_reclaim_attempts", "hangar_reclaim_jobs"},
			))
		})

		It("stamps every creation time on the database clock", func() {
			rows, err := database.Query(`
				SELECT table_name, column_name, data_type, column_default
				FROM information_schema.columns
				WHERE table_name LIKE 'hangar_%' AND is_nullable = 'NO' AND column_name IN
					('created_at', 'acquired_at', 'granted_at', 'renewed_at', 'observed_at',
					 'issued_at', 'registered_at', 'updated_at', 'decided_at', 'resolved_at',
					 'intent_recorded_at', 'admitted_at', 'admitted_delete_at')`)
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()

			seen := 0
			for rows.Next() {
				var table, column, dataType string
				var def sql.NullString
				Expect(rows.Scan(&table, &column, &dataType, &def)).To(Succeed())
				seen++
				Expect(dataType).To(Equal("timestamp with time zone"),
					"%s.%s is %s", table, column, dataType)
				Expect(def.String).To(Equal("now()"),
					"%s.%s defaults to %q; a node whose clock drifts must not be able to "+
						"stamp its own lease", table, column, def.String)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())
			Expect(seen).To(BeNumerically(">=", 15),
				"the database-clock rule inspected %d columns, which is too few to mean anything", seen)
		})
	})

	Describe("the negative vectors", func() {
		BeforeEach(func() {
			seedEpoch()
			seedSafePolicy()
			seedPredeclaration(handoffID, sourceLeaseID, executionID, true)
		})

		Context("the three branches exclude one another permanently", func() {
			It("refuses capture beside no_capture", func() {
				Expect(expectRefusal(database, "a handoff in two branches at once",
					append(stage2(handoffID, reservationID), fmt.Sprintf(`
						INSERT INTO hangar_no_capture_dispositions
							(handoff_id, execution_id, activation_epoch, source_lease_id,
							 reason, release_intent_id)
						VALUES ('%s', '%s', 1, '%s', 'unresolved', '%s')`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))...)).
					To(ContainSubstring("exclude one another permanently"))

				expectAccepted(database, "capture alone", stage2(handoffID, reservationID)...)
			})

			It("refuses cancellation after Stage 2", func() {
				commitStage2(handoffID, reservationID)

				Expect(expectRefusal(database, "a cancellation branch beside a committed Stage 2",
					fmt.Sprintf(`
						INSERT INTO hangar_pre_reservation_cancel_dispositions
							(handoff_id, execution_id, activation_epoch, source_lease_id,
							 hold_acknowledged, release_intent_id)
						VALUES ('%s', '%s', 1, '%s', true, '%s')`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))).
					To(ContainSubstring("exclude one another permanently"))
			})

			It("refuses an ordinary no_capture recorded under the cancellation branch", func() {
				Expect(expectRefusal(database, "a cancel child under a no_capture arbiter",
					fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
						VALUES ('%s', 'no_capture')`, handoffID),
					fmt.Sprintf(`INSERT INTO hangar_pre_reservation_cancel_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 hold_acknowledged, release_intent_id)
						VALUES ('%s', '%s', 1, '%s', true, '%s')`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))).
					To(ContainSubstring("carries the wrong branch record"))

				expectAccepted(database, "a no_capture child under a no_capture arbiter",
					fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
						VALUES ('%s', 'no_capture')`, handoffID),
					fmt.Sprintf(`INSERT INTO hangar_no_capture_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 reason, release_intent_id, finish_acknowledgement)
						VALUES ('%s', '%s', 1, '%s', 'authoritative_non_success', '%s', '{}')`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))
			})

			It("refuses a decided arbiter with no branch record at all", func() {
				Expect(expectRefusal(database, "an arbiter with no branch child",
					fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
						VALUES ('%s', 'capture')`, handoffID))).
					To(ContainSubstring("carries no branch record"))
			})

			It("refuses a branch record with no arbiter", func() {
				Expect(expectRefusal(database, "a no_capture child with no arbiter row",
					fmt.Sprintf(`INSERT INTO hangar_no_capture_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 reason, release_intent_id)
						VALUES ('%s', '%s', 1, '%s', 'unresolved', '%s')`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))).
					To(SatisfyAny(
						ContainSubstring("no disposition arbiter row"),
						ContainSubstring("violates foreign key constraint"),
					))
			})

			It("refuses re-deciding a handoff", func() {
				commitStage2(handoffID, reservationID)

				Expect(expectRefusal(database, "a re-decided arbiter", fmt.Sprintf(`
					UPDATE hangar_handoff_dispositions SET disposition = 'no_capture'
					WHERE handoff_id = '%s'`, handoffID))).
					To(ContainSubstring("exclude one another permanently"))
			})
		})

		Context("the cancellation branch", func() {
			decide := func() string {
				return fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
					VALUES ('%s', 'pre_reservation_cancel')`, handoffID)
			}

			It("refuses an unheld cancellation carrying release fields", func() {
				Expect(expectRefusal(database, "an unheld cancellation with a release intent",
					decide(),
					fmt.Sprintf(`INSERT INTO hangar_pre_reservation_cancel_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 hold_acknowledged, release_intent_id)
						VALUES ('%s', '%s', 1, '%s', false, '%s')`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))).
					To(ContainSubstring("hangar_cancel_unheld_carries_no_release"))

				expectAccepted(database, "an unheld cancellation closing with no daemon call",
					decide(),
					fmt.Sprintf(`INSERT INTO hangar_pre_reservation_cancel_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 hold_acknowledged, finalized_at)
						VALUES ('%s', '%s', 1, '%s', false, now())`,
						handoffID, executionID, sourceLeaseID))
			})

			It("refuses a held cancellation finalized without an exact release acknowledgement", func() {
				Expect(expectRefusal(database, "a held cancellation finalized with no acknowledgement",
					decide(),
					fmt.Sprintf(`INSERT INTO hangar_pre_reservation_cancel_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 hold_acknowledged, release_intent_id, finalized_at)
						VALUES ('%s', '%s', 1, '%s', true, '%s', now())`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))).
					To(ContainSubstring("hangar_cancel_held_finalizes_on_acknowledgement"))

				expectAccepted(database, "a held cancellation finalized on its acknowledgement",
					decide(),
					fmt.Sprintf(`INSERT INTO hangar_pre_reservation_cancel_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id,
						 hold_acknowledged, release_intent_id, release_acknowledged_at,
						 release_acknowledgement, finalized_at)
						VALUES ('%s', '%s', 1, '%s', true, '%s', now(), '{}', now())`,
						handoffID, executionID, sourceLeaseID, releaseIntentID))
			})

			It("refuses a held cancellation with no release intent", func() {
				Expect(expectRefusal(database, "a held cancellation with no release intent",
					decide(),
					fmt.Sprintf(`INSERT INTO hangar_pre_reservation_cancel_dispositions
						(handoff_id, execution_id, activation_epoch, source_lease_id, hold_acknowledged)
						VALUES ('%s', '%s', 1, '%s', true)`,
						handoffID, executionID, sourceLeaseID))).
					To(ContainSubstring("hangar_cancel_held_records_intent"))
			})
		})

		Context("the predeclaration is non-authorizing and immutable", func() {
			for _, column := range []string{
				"producer_checkpoint_id", "finish_successful", "capture_fence", "scope",
				"digest", "seal_confirmed_at", "receipt_id", "generation", "claim_id",
			} {
				It("has nowhere to record a "+column, func() {
					err := attempt(database, fmt.Sprintf(
						`INSERT INTO hangar_handoff_predeclarations (handoff_id, %s) VALUES ('%s', '1')`,
						column, secondHandoffID))
					Expect(err).To(MatchError(ContainSubstring(
						fmt.Sprintf(`column "%s" of relation "hangar_handoff_predeclarations" does not exist`, column))))
				})
			}

			It("refuses a rewrite of a predeclared identity", func() {
				Expect(expectRefusal(database, "a mutated predeclaration", fmt.Sprintf(`
					UPDATE hangar_handoff_predeclarations SET output_name = 'other'
					WHERE handoff_id = '%s'`, handoffID))).
					To(ContainSubstring("is immutable"))
			})

			It("refuses a second acknowledgement of the same hold", func() {
				Expect(expectRefusal(database, "a re-acknowledged hold", fmt.Sprintf(`
					UPDATE hangar_handoff_predeclarations SET hold_acknowledged_at = now()
					WHERE handoff_id = '%s'`, handoffID))).
					To(ContainSubstring("already acknowledged"))
			})

			It("bounds the capture deadline", func() {
				for _, deadline := range []string{"30 minutes", "8 days"} {
					Expect(expectRefusal(database, "a capture deadline of "+deadline, fmt.Sprintf(`
						INSERT INTO hangar_handoff_predeclarations
							(handoff_id, source_lease_id, execution_id, execution_fence,
							 output_name, activation_epoch, capture_deadline_at)
						VALUES ('%s', '%s', '%s', 1, 'result', 1, now() + interval '%s')`,
						secondHandoffID, secondLeaseID, secondExecutionID, deadline))).
						To(ContainSubstring("hangar_predeclaration_deadline_bounded"))
				}

				expectAccepted(database, "a 24-hour capture deadline", fmt.Sprintf(`
					INSERT INTO hangar_handoff_predeclarations
						(handoff_id, source_lease_id, execution_id, execution_fence,
						 output_name, activation_epoch, capture_deadline_at)
					VALUES ('%s', '%s', '%s', 1, 'result', 1, now() + interval '24 hours')`,
					secondHandoffID, secondLeaseID, secondExecutionID))
			})

			It("refuses an output name that is a path", func() {
				Expect(expectRefusal(database, "an output name that is a path", fmt.Sprintf(`
					INSERT INTO hangar_handoff_predeclarations
						(handoff_id, source_lease_id, execution_id, execution_fence,
						 output_name, activation_epoch, capture_deadline_at)
					VALUES ('%s', '%s', '%s', 1, '/srv/steps/result', 1, now() + interval '24 hours')`,
					secondHandoffID, secondLeaseID, secondExecutionID))).
					To(ContainSubstring("output_name"))
			})
		})

		Context("Stage 2", func() {
			It("refuses a non-successful finish", func() {
				Expect(expectRefusal(database, "a Stage 2 reservation for a non-successful finish",
					fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
						VALUES ('%s', 'capture')`, handoffID),
					fmt.Sprintf(`INSERT INTO hangar_capture_reservations
						(reservation_id, handoff_id, execution_id, activation_epoch, source_lease_id,
						 producer_checkpoint_id, finish_acknowledgement, finish_successful,
						 capture_fence, capture_deadline_at)
						SELECT '%s', handoff_id, execution_id, activation_epoch, source_lease_id,
							'cp', '{}', false, 1, capture_deadline_at
						FROM hangar_handoff_predeclarations WHERE handoff_id = '%s'`,
						reservationID, handoffID))).
					To(ContainSubstring("finish_successful"))
			})

			It("refuses a reservation with no acknowledged source hold", func() {
				seedPredeclaration(secondHandoffID, secondLeaseID, secondExecutionID, false)

				Expect(expectRefusal(database, "Stage 2 over an unacknowledged hold",
					stage2(secondHandoffID, secondReservation)...)).
					To(ContainSubstring("no acknowledged source hold"))
			})

			It("refuses a reservation that contradicts its predeclaration", func() {
				Expect(expectRefusal(database, "a reservation naming another source lease",
					fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
						VALUES ('%s', 'capture')`, handoffID),
					fmt.Sprintf(`INSERT INTO hangar_capture_reservations
						(reservation_id, handoff_id, execution_id, activation_epoch, source_lease_id,
						 producer_checkpoint_id, finish_acknowledgement, finish_successful,
						 capture_fence, capture_deadline_at)
						SELECT '%s', handoff_id, execution_id, activation_epoch, '%s',
							'cp', '{}', true, 1, capture_deadline_at
						FROM hangar_handoff_predeclarations WHERE handoff_id = '%s'`,
						reservationID, secondLeaseID, handoffID))).
					To(ContainSubstring("does not match the predeclared"))
			})

			It("refuses a zero capture fence", func() {
				Expect(expectRefusal(database, "a Stage 2 reservation at fence zero",
					fmt.Sprintf(`INSERT INTO hangar_handoff_dispositions (handoff_id, disposition)
						VALUES ('%s', 'capture')`, handoffID),
					fmt.Sprintf(`INSERT INTO hangar_capture_reservations
						(reservation_id, handoff_id, execution_id, activation_epoch, source_lease_id,
						 producer_checkpoint_id, finish_acknowledgement, finish_successful,
						 capture_fence, capture_deadline_at)
						SELECT '%s', handoff_id, execution_id, activation_epoch, source_lease_id,
							'cp', '{}', true, 0, capture_deadline_at
						FROM hangar_handoff_predeclarations WHERE handoff_id = '%s'`,
						reservationID, handoffID))).
					To(ContainSubstring("capture_fence"))
			})

			It("refuses a Stage 2 reservation that reuses the predeclaration row", func() {
				commitStage2(handoffID, reservationID)

				Expect(expectRefusal(database, "a second reservation on one handoff", fmt.Sprintf(`
					INSERT INTO hangar_capture_reservations
						(reservation_id, handoff_id, execution_id, activation_epoch, source_lease_id,
						 producer_checkpoint_id, finish_acknowledgement, finish_successful,
						 capture_fence, capture_deadline_at)
					SELECT '%s', handoff_id, execution_id, activation_epoch, source_lease_id,
						'cp', '{}', true, 1, capture_deadline_at
					FROM hangar_handoff_predeclarations WHERE handoff_id = '%s'`,
					secondReservation, handoffID))).
					To(ContainSubstring("hangar_capture_reservations_handoff_id_key"))
			})
		})

		Context("settling a capture", func() {
			BeforeEach(func() { commitStage2(handoffID, reservationID) })

			It("refuses calling a cancelled capture settled with the source still held", func() {
				Expect(expectRefusal(database, "a cancelled capture settled with no release", fmt.Sprintf(`
					UPDATE hangar_capture_reservations
					SET state = 'cancelled', settled_at = now()
					WHERE reservation_id = '%s'`, reservationID))).
					To(ContainSubstring("hangar_reservation_settlement_is_earned"))

				expectAccepted(database, "a cancelled capture whose release was acknowledged",
					fmt.Sprintf(`
						UPDATE hangar_capture_reservations
						SET state = 'cancelled', settled_at = now(), release_acknowledged_at = now()
						WHERE reservation_id = '%s'`, reservationID))
			})

			It("accepts a cancelled capture that is decided and not yet settled", func() {
				expectAccepted(database, "a cancellation waiting on its release", fmt.Sprintf(`
					UPDATE hangar_capture_reservations SET state = 'cancelled'
					WHERE reservation_id = '%s'`, reservationID))
			})

			It("refuses a settlement time on a capture that is not terminal", func() {
				Expect(expectRefusal(database, "an unresolved capture with a settlement time",
					fmt.Sprintf(`
						UPDATE hangar_capture_reservations
						SET settled_at = now(), release_acknowledged_at = now()
						WHERE reservation_id = '%s'`, reservationID))).
					To(ContainSubstring("hangar_reservation_settlement_is_terminal"))
			})

			It("refuses withdrawing or restamping an acknowledged release", func() {
				mustExec(database, fmt.Sprintf(`
					UPDATE hangar_capture_reservations SET release_acknowledged_at = now()
					WHERE reservation_id = '%s'`, reservationID))

				Expect(expectRefusal(database, "a withdrawn source release", fmt.Sprintf(`
					UPDATE hangar_capture_reservations SET release_acknowledged_at = NULL
					WHERE reservation_id = '%s'`, reservationID))).
					To(ContainSubstring("acknowledged once"))

				Expect(expectRefusal(database, "a restamped source release", fmt.Sprintf(`
					UPDATE hangar_capture_reservations
					SET release_acknowledged_at = now() + interval '1 hour'
					WHERE reservation_id = '%s'`, reservationID))).
					To(ContainSubstring("acknowledged once"))
			})
		})

		Context("logical resolution and the first object create", func() {
			BeforeEach(func() {
				commitStage2(handoffID, reservationID)
				seedCaptureLease(reservationID, 4)
			})

			It("refuses resolution by a stale owner", func() {
				Expect(expectRefusal(database, "a logical resolution at a superseded fence",
					fmt.Sprintf(`INSERT INTO hangar_logical_reservations
						(reservation_id, scope, digest, logical_bytes, capture_fence)
						VALUES ('%s', 'team-a', '%s', 4096, 3)`, reservationID, sampleDigest))).
					To(ContainSubstring("a stale owner may not seal, publish, sign, register or finalize"))

				expectAccepted(database, "a logical resolution at the current fence",
					fmt.Sprintf(`INSERT INTO hangar_logical_reservations
						(reservation_id, scope, digest, logical_bytes, capture_fence)
						VALUES ('%s', 'team-a', '%s', 4096, 4)`, reservationID, sampleDigest))
			})

			It("refuses resolution for a reservation owned by nobody", func() {
				mustExec(database, fmt.Sprintf(
					`DELETE FROM hangar_capture_attempt_leases WHERE reservation_id = '%s'`, reservationID))

				Expect(expectRefusal(database, "a logical resolution with no capture owner",
					fmt.Sprintf(`INSERT INTO hangar_logical_reservations
						(reservation_id, scope, digest, logical_bytes, capture_fence)
						VALUES ('%s', 'team-a', '%s', 4096, 4)`, reservationID, sampleDigest))).
					To(ContainSubstring("owned by nobody"))
			})

			It("refuses a first object create before the logical reservation exists", func() {
				Expect(expectRefusal(database, "an object create with no logical reservation",
					fmt.Sprintf(`UPDATE hangar_capture_reservations
						SET first_create_attempted_at = now() WHERE reservation_id = '%s'`,
						reservationID))).
					To(ContainSubstring("no resolved logical reservation"))

				expectAccepted(database, "an object create after resolution",
					fmt.Sprintf(`INSERT INTO hangar_logical_reservations
						(reservation_id, scope, digest, logical_bytes, capture_fence)
						VALUES ('%s', 'team-a', '%s', 4096, 4)`, reservationID, sampleDigest),
					fmt.Sprintf(`UPDATE hangar_capture_reservations
						SET first_create_attempted_at = now() WHERE reservation_id = '%s'`,
						reservationID))
			})

			It("refuses a rewrite of a resolved logical identity", func() {
				seedLogicalReservation(reservationID, sampleDigest, 4)

				Expect(expectRefusal(database, "a mutated logical identity", fmt.Sprintf(`
					UPDATE hangar_logical_reservations SET digest = '%s'
					WHERE reservation_id = '%s'`, otherDigest, reservationID))).
					To(ContainSubstring("cannot float to replacement content"))
			})
		})

		Context("receipts", func() {
			var lifecycle int64

			BeforeEach(func() {
				commitStage2(handoffID, reservationID)
				seedCaptureLease(reservationID, 1)
				seedLogicalReservation(reservationID, sampleDigest, 1)
				lifecycle = seedLifecycle(sampleDigest, sampleGeneration)
				mustExec(database, fmt.Sprintf(`
					INSERT INTO hangar_receipt_stat_challenges
						(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
						 scope, digest, generation, capture_fence, not_after, consumed_at)
					VALUES ('%s', '%s', '%s', 1, 'receipt-key-1', 'team-a', '%s', %d, 1,
						now() + interval '5 minutes', now())`,
					challengeNonce, handoffID, reservationID, sampleDigest, sampleGeneration))
			})

			registerWith := func(markerVersion, keyID string, lifecycleID int64) string {
				return fmt.Sprintf(`INSERT INTO hangar_output_receipts
					(reservation_id, lifecycle_id, handoff_id, activation_epoch, receipt_key_id,
					 challenge_nonce, marker_version, algorithm, claims, signature)
					VALUES ('%s', %d, '%s', 1, '%s', '%s', '%s', 'ed25519', '{}', 'signature')`,
					reservationID, lifecycleID, handoffID, keyID, challengeNonce, markerVersion)
			}

			It("refuses a receipt without the accepted marker version", func() {
				Expect(expectRefusal(database, "a receipt with a foreign marker version",
					registerWith("hangar-output-v0", "receipt-key-1", lifecycle))).
					To(ContainSubstring("marker_version"))

				expectAccepted(database, "a receipt with the accepted marker version",
					registerWith("hangar-output-v1", "receipt-key-1", lifecycle))
			})

			It("refuses a receipt whose key the activation epoch does not attest", func() {
				Expect(expectRefusal(database, "a receipt signed by an unattested key",
					registerWith("hangar-output-v1", "receipt-key-9", lifecycle))).
					To(ContainSubstring("activation epoch 1 attests key receipt-key-1"))
			})

			It("refuses a ref registered ahead of its logical reservation", func() {
				other := seedLifecycle(otherDigest, sampleGeneration+1)

				Expect(expectRefusal(database, "a receipt registering a foreign ref",
					registerWith("hangar-output-v1", "receipt-key-1", other))).
					To(ContainSubstring("may not be registered ahead of the logical reservation"))
			})

			It("refuses a receipt that did not consume its challenge", func() {
				// A fresh, unconsumed challenge for the same capture and ref.
				// Un-consuming the seeded one is a different rule's business,
				// and the one-use trigger refuses it.
				mustExec(database, fmt.Sprintf(`
					INSERT INTO hangar_receipt_stat_challenges
						(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
						 scope, digest, generation, capture_fence, not_after)
					VALUES ('nonce-fedcba9876543210', '%s', '%s', 1, 'receipt-key-1', 'team-a',
						'%s', %d, 1, now() + interval '5 minutes')`,
					handoffID, reservationID, sampleDigest, sampleGeneration))

				Expect(expectRefusal(database, "a receipt registered without consuming its challenge",
					fmt.Sprintf(`INSERT INTO hangar_output_receipts
						(reservation_id, lifecycle_id, handoff_id, activation_epoch, receipt_key_id,
						 challenge_nonce, marker_version, algorithm, claims, signature)
						VALUES ('%s', %d, '%s', 1, 'receipt-key-1', 'nonce-fedcba9876543210',
							'hangar-output-v1', 'ed25519', '{}', 'signature')`,
						reservationID, lifecycle, handoffID))).
					To(ContainSubstring("without consuming its one-use stat challenge"))
			})

			It("refuses a second use of a consumed challenge", func() {
				Expect(expectRefusal(database, "a second use of one challenge", fmt.Sprintf(`
					UPDATE hangar_receipt_stat_challenges SET consumed_at = now() WHERE nonce = '%s'`,
					challengeNonce))).
					To(ContainSubstring("it is one-use"))
			})

			It("bounds the challenge window to five minutes", func() {
				Expect(expectRefusal(database, "a challenge valid for an hour", fmt.Sprintf(`
					INSERT INTO hangar_receipt_stat_challenges
						(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
						 scope, digest, generation, capture_fence, not_after)
					VALUES ('nonce-0f1e2d3c4b5a6978', '%s', '%s', 1, 'receipt-key-1', 'team-a',
						'%s', %d, 1, now() + interval '1 hour')`,
					handoffID, reservationID, sampleDigest, sampleGeneration))).
					To(ContainSubstring("hangar_challenge_window"))
			})
		})

		Context("claims, read leases and reclamation", func() {
			var lifecycle int64

			BeforeEach(func() { lifecycle = seedFullChain() })

			It("refuses a claim on an unregistered exact ref", func() {
				Expect(expectRefusal(database, "a claim on a ref with no lifecycle record", fmt.Sprintf(`
					INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
					VALUES ('%s', %d, 1, 'opaque-binding')`, claimID, lifecycle+9999))).
					To(ContainSubstring("violates foreign key constraint"))

				expectAccepted(database, "a claim on a registered exact ref", fmt.Sprintf(`
					INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
					VALUES ('%s', %d, 1, 'opaque-binding')`, claimID, lifecycle))
			})

			It("refuses reactivating a released claim, and refuses deleting the tombstone", func() {
				seedClaim(claimID, lifecycle)
				mustExec(database, fmt.Sprintf(
					`UPDATE hangar_claims SET released_at = now() WHERE claim_id = '%s'`, claimID))

				Expect(expectRefusal(database, "a reactivated claim", fmt.Sprintf(`
					UPDATE hangar_claims SET released_at = NULL WHERE claim_id = '%s'`, claimID))).
					To(ContainSubstring("cannot silently reactivate"))

				Expect(expectRefusal(database, "a deleted claim tombstone", fmt.Sprintf(`
					DELETE FROM hangar_claims WHERE claim_id = '%s'`, claimID))).
					To(ContainSubstring("stays tombstoned"))
			})

			It("refuses moving a claim to another exact ref", func() {
				seedClaim(claimID, lifecycle)
				other := seedLifecycle(otherDigest, sampleGeneration+1)

				Expect(expectRefusal(database, "a claim moved to another ref", fmt.Sprintf(`
					UPDATE hangar_claims SET lifecycle_id = %d WHERE claim_id = '%s'`, other, claimID))).
					To(ContainSubstring("protects one immutable generation"))
			})

			It("refuses a read lease shorter than the fifteen-minute floor", func() {
				seedClaim(claimID, lifecycle)

				Expect(expectRefusal(database, "a five-minute read lease", fmt.Sprintf(`
					INSERT INTO hangar_read_leases
						(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at)
					VALUES ('%s', '%s', %d, 1, 1, now() + interval '5 minutes')`,
					readLeaseID, claimID, lifecycle))).
					To(ContainSubstring("hangar_read_lease_term"))
			})

			It("refuses a read lease under a claim on another ref", func() {
				seedClaim(claimID, lifecycle)
				other := seedLifecycle(otherDigest, sampleGeneration+1)

				Expect(expectRefusal(database, "a read lease that reads past its claim", fmt.Sprintf(`
					INSERT INTO hangar_read_leases
						(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at)
					VALUES ('%s', '%s', %d, 1, 1, now() + interval '20 minutes')`,
					readLeaseID, claimID, other))).
					To(ContainSubstring("under a claim on lifecycle"))
			})

			admitReclaim := func(lifecycleID int64) string {
				return fmt.Sprintf(`INSERT INTO hangar_reclaim_jobs
					(lifecycle_id, activation_epoch, owner_id, lease_fence, generation,
					 metageneration, expires_at)
					VALUES (%d, 1, '%s', 1, %d, 1, now() + interval '20 minutes')`,
					lifecycleID, workerID, sampleGeneration)
			}

			It("refuses reclaim admission beside an active claim", func() {
				seedClaim(claimID, lifecycle)

				Expect(expectRefusal(database, "reclaim admitted beside an active claim",
					admitReclaim(lifecycle))).
					To(ContainSubstring("has an admitted reclaim beside 1 active claim(s)"))

				mustExec(database, fmt.Sprintf(
					`UPDATE hangar_claims SET released_at = now() WHERE claim_id = '%s'`, claimID))
				expectAccepted(database, "reclaim admitted with no active claim", admitReclaim(lifecycle))
			})

			It("refuses reclaim admission beside an active read lease", func() {
				seedClaim(claimID, lifecycle)
				mustExec(database, fmt.Sprintf(`
					INSERT INTO hangar_read_leases
						(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at)
					VALUES ('%s', '%s', %d, 1, 1, now() + interval '20 minutes')`,
					readLeaseID, claimID, lifecycle))
				mustExec(database, fmt.Sprintf(
					`UPDATE hangar_claims SET released_at = now() WHERE claim_id = '%s'`, claimID))

				Expect(expectRefusal(database, "reclaim admitted beside an active read lease",
					admitReclaim(lifecycle))).
					To(ContainSubstring("1 active read lease(s)"))
			})

			It("refuses reclaim admission beside an unresolved reservation for the same content", func() {
				seedPredeclaration(secondHandoffID, secondLeaseID, secondExecutionID, true)
				commitStage2(secondHandoffID, secondReservation)
				seedCaptureLease(secondReservation, 1)
				seedLogicalReservation(secondReservation, sampleDigest, 1)

				Expect(expectRefusal(database, "reclaim admitted beside an unresolved reservation",
					admitReclaim(lifecycle))).
					To(ContainSubstring("1 unresolved reservation(s)"))
			})

			It("refuses a second unfinalized reclaim job for one generation", func() {
				expectRefusal(database, "two concurrent reclaim jobs",
					admitReclaim(lifecycle), admitReclaim(lifecycle))
			})

			It("refuses calling an ambiguous external deletion confirmed", func() {
				mustExec(database, admitReclaim(lifecycle))
				var job int64
				Expect(database.QueryRow(`SELECT id FROM hangar_reclaim_jobs LIMIT 1`).
					Scan(&job)).To(Succeed())
				mustExec(database, fmt.Sprintf(`
					INSERT INTO hangar_reclaim_attempts (job_id, lease_fence, outcome, observed_at)
					VALUES (%d, 1, 'timeout', now())`, job))

				Expect(expectRefusal(database, "an inferred reclaim called confirmed", fmt.Sprintf(`
					UPDATE hangar_reclaim_jobs SET outcome = 'reclaimed_confirmed', finalized_at = now()
					WHERE id = %d`, job))).
					To(ContainSubstring("no acknowledged conditional delete"))

				Expect(expectRefusal(database, "an inferred reclaim with no observed absence", fmt.Sprintf(`
					UPDATE hangar_reclaim_jobs SET outcome = 'reclaimed_inferred', finalized_at = now()
					WHERE id = %d`, job))).
					To(ContainSubstring("without observing exact absence"))

				expectAccepted(database, "an inferred reclaim with an admitted delete and observed absence",
					fmt.Sprintf(`UPDATE hangar_reclaim_jobs
						SET outcome = 'reclaimed_inferred', finalized_at = now(), absence_observed_at = now()
						WHERE id = %d`, job))
			})

			It("refuses finalizing a lifecycle that was never admitted to reclaim", func() {
				Expect(expectRefusal(database, "a lifecycle reclaimed without admission", fmt.Sprintf(`
					UPDATE hangar_exact_lifecycles SET state = 'reclaimed_confirmed' WHERE id = %d`,
					lifecycle))).
					To(ContainSubstring("admission marks a generation reclaiming durably before any external delete"))
			})
		})

		Context("policy health", func() {
			var lifecycle int64

			BeforeEach(func() { lifecycle = seedFullChain() })

			It("refuses a claim while the policy trust state is at risk", func() {
				mustExec(database, `
					INSERT INTO hangar_policy_snapshots
						(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
						 lifecycle_delete_rules, state)
					VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)

				Expect(expectRefusal(database, "a claim acquired while at risk", fmt.Sprintf(`
					INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
					VALUES ('%s', %d, 1, 'opaque-binding')`, claimID, lifecycle))).
					To(ContainSubstring("new captures, claim acquires, grants, adoption and reclaim admission stop"))
			})

			It("refuses a claim on a stale attestation", func() {
				mustExec(database, `
					UPDATE hangar_policy_snapshots SET observed_at = now() - interval '20 minutes'`)

				Expect(expectRefusal(database, "a claim acquired on stale evidence", fmt.Sprintf(`
					INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
					VALUES ('%s', %d, 1, 'opaque-binding')`, claimID, lifecycle))).
					To(ContainSubstring("past the 15-minute detection bound"))
			})

			It("still permits a release while at risk", func() {
				seedClaim(claimID, lifecycle)
				mustExec(database, `
					INSERT INTO hangar_policy_snapshots
						(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
						 lifecycle_delete_rules, state)
					VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)

				expectAccepted(database, "a release while at risk", fmt.Sprintf(`
					UPDATE hangar_claims SET released_at = now() WHERE claim_id = '%s'`, claimID))
			})

			It("refuses calling a policy with Delete rules safe", func() {
				Expect(expectRefusal(database, "a safe snapshot with a Delete rule", `
					INSERT INTO hangar_policy_snapshots
						(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
						 lifecycle_delete_rules, state)
					VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'safe')`)).
					To(ContainSubstring("hangar_policy_safe_has_no_delete_rules"))
			})

			// Req 52 names five admissions that stop from detection onward:
			// new captures, claim acquires, managed-output grants, orphan
			// adoption and reclaim admission. Each vector below is one of
			// them, and each runs twice against the same statement -- once
			// while the freshest attestation is safe, once after an at-risk
			// one lands. The safe run is the valid twin: a gate that refused
			// the shape rather than the state would fail it.
			goAtRisk := func() {
				GinkgoHelper()
				mustExec(database, `
					INSERT INTO hangar_policy_snapshots
						(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
						 lifecycle_delete_rules, state)
					VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)
			}

			It("refuses a managed-output grant while at risk", func() {
				seedClaim(claimID, lifecycle)
				grant := fmt.Sprintf(`
					INSERT INTO hangar_read_leases
						(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at)
					VALUES ('%s', '%s', %d, 1, 1, now() + interval '20 minutes')`,
					readLeaseID, claimID, lifecycle)

				expectAccepted(database, "a read lease granted on a safe policy", grant)

				goAtRisk()

				Expect(expectRefusal(database, "a read lease granted while at risk", grant)).
					To(ContainSubstring("new captures, claim acquires, grants, adoption and reclaim admission stop"))
			})

			It("refuses orphan adoption while at risk, and still lets a registration finish", func() {
				adopt := fmt.Sprintf(`
					INSERT INTO hangar_exact_lifecycles
						(scope, digest, generation, metageneration, activation_epoch,
						 marker_version, origin, state)
					VALUES ('team-a', '%s', %d, 1, 1, 'hangar-output-v1', 'adopted', 'adopted')`,
					otherDigest, sampleGeneration+1)

				expectAccepted(database, "an adoption on a safe policy", adopt)

				goAtRisk()

				Expect(expectRefusal(database, "an orphan adopted while at risk", adopt)).
					To(ContainSubstring("new captures, claim acquires, grants, adoption and reclaim admission stop"))

				// Registration is the other origin on this table and it is
				// deliberately not gated. A capture that has already passed
				// its first object create cannot be un-made by a policy
				// observation; refusing its receipt would leave the
				// generation in the bucket as an unregistered orphan with
				// nothing correlating it -- the exact state adoption exists to
				// clean up. Req 52 blocks admission and lets already-admitted
				// work finish, and this is that work finishing.
				expectAccepted(database, "a registration completing while at risk", fmt.Sprintf(`
					INSERT INTO hangar_exact_lifecycles
						(scope, digest, generation, metageneration, activation_epoch,
						 marker_version, origin, state)
					VALUES ('team-a', '%s', %d, 1, 1, 'hangar-output-v1', 'registered', 'registered')`,
					otherDigest, sampleGeneration+2))
			})

			It("refuses a new capture while at risk", func() {
				seedPredeclaration(secondHandoffID, secondLeaseID, secondExecutionID, true)
				capture := stage2(secondHandoffID, secondReservation)

				expectAccepted(database, "a Stage 2 reservation on a safe policy", capture...)

				goAtRisk()

				Expect(expectRefusal(database, "a Stage 2 reservation committed while at risk",
					capture...)).
					To(ContainSubstring("new captures, claim acquires, grants, adoption and reclaim admission stop"))
			})
		})

		// The class travels with the refusal, all the way to the client.
		//
		// atc/db maps a refusal onto a typed outcome by its SQLSTATE, and a
		// class that only existed in the migration source would be a map keyed
		// on something the wire never carries. One vector per class, so a
		// class that stopped arriving is named.
		Context("the refusal classes", func() {
			var lifecycle int64

			BeforeEach(func() { lifecycle = seedFullChain() })

			It("raises a conflict as JB001", func() {
				seedClaim(claimID, lifecycle)
				mustExec(database, fmt.Sprintf(
					`UPDATE hangar_claims SET released_at = now() WHERE claim_id = '%s'`, claimID))

				Expect(expectRefusal(database, "a reactivated claim", fmt.Sprintf(
					`UPDATE hangar_claims SET released_at = NULL WHERE claim_id = '%s'`, claimID))).
					To(ContainSubstring("SQLSTATE JB001"))
			})

			It("raises an at-risk admission as JB002", func() {
				mustExec(database, `
					INSERT INTO hangar_policy_snapshots
						(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
						 lifecycle_delete_rules, state)
					VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)

				Expect(expectRefusal(database, "a claim acquired while at risk", fmt.Sprintf(`
					INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
					VALUES ('%s', %d, 1, 'opaque-binding')`, claimID, lifecycle))).
					To(ContainSubstring("SQLSTATE JB002"))
			})

			It("raises a superseded fence as JB003", func() {
				// Forward first, so the vector is the trigger's own refusal
				// and not the CHECK that keeps the fence positive.
				mustExec(database, fmt.Sprintf(`
					UPDATE hangar_capture_attempt_leases SET capture_fence = 3
					WHERE reservation_id = '%s'`, reservationID))

				Expect(expectRefusal(database, "a capture fence moved backwards", fmt.Sprintf(`
					UPDATE hangar_capture_attempt_leases SET capture_fence = 2
					WHERE reservation_id = '%s'`, reservationID))).
					To(ContainSubstring("SQLSTATE JB003"))
			})

			It("raises an immutable rewrite as JB004", func() {
				Expect(expectRefusal(database, "a rewritten predeclaration", fmt.Sprintf(`
					UPDATE hangar_handoff_predeclarations SET output_name = 'other'
					WHERE handoff_id = '%s'`, handoffID))).
					To(ContainSubstring("SQLSTATE JB004"))
			})
		})

		Context("inventory and worker leases", func() {
			It("refuses debt for a bucket and epoch this deployment holds no cursor for", func() {
				Expect(expectRefusal(database, "debt from another deployment's sweep", `
					INSERT INTO hangar_inventory_debt
						(bucket_fingerprint, activation_epoch, object_key, reason)
					VALUES ('gs://someone-elses-bucket', 1, 'some/key', 'poison_metadata')`)).
					To(ContainSubstring("violates foreign key constraint"))

				expectAccepted(database, "debt under this deployment's cursor",
					`INSERT INTO hangar_inventory_cursors
						(bucket_fingerprint, activation_epoch, cursor_fence)
					 VALUES ('gs://output-bucket', 1, 1)`,
					`INSERT INTO hangar_inventory_debt
						(bucket_fingerprint, activation_epoch, object_key, reason)
					 VALUES ('gs://output-bucket', 1, 'some/key', 'poison_metadata')`)
			})

			It("refuses a cursor generation with no after-key", func() {
				Expect(expectRefusal(database, "a cursor generation ordering against nothing", `
					INSERT INTO hangar_inventory_cursors
						(bucket_fingerprint, activation_epoch, cursor_fence, after_generation)
					VALUES ('gs://output-bucket', 1, 1, 7)`)).
					To(ContainSubstring("hangar_cursor_generation_needs_key"))
			})

			It("refuses a cursor fence moving backwards", func() {
				mustExec(database, `INSERT INTO hangar_inventory_cursors
					(bucket_fingerprint, activation_epoch, cursor_fence) VALUES ('gs://output-bucket', 1, 5)`)

				Expect(expectRefusal(database, "a stale cursor owner advancing the cursor", `
					UPDATE hangar_inventory_cursors SET cursor_fence = 4`)).
					To(ContainSubstring("moved its fence backwards"))
			})

			It("gives each operation kind its own lease", func() {
				expectAccepted(database, "two kinds of worker leasing at once",
					fmt.Sprintf(`INSERT INTO hangar_operation_leases
						(kind, activation_epoch, owner_id, lease_fence, expires_at)
					 VALUES ('inventory', 1, '%s', 1, now() + interval '15 minutes')`, workerID),
					fmt.Sprintf(`INSERT INTO hangar_operation_leases
						(kind, activation_epoch, owner_id, lease_fence, expires_at)
					 VALUES ('reclaim_delete', 1, '%s', 1, now() + interval '15 minutes')`, workerID))

				expectRefusal(database, "two owners of one operation kind",
					fmt.Sprintf(`INSERT INTO hangar_operation_leases
						(kind, activation_epoch, owner_id, lease_fence, expires_at)
					 VALUES ('inventory', 1, '%s', 1, now() + interval '15 minutes')`, workerID),
					fmt.Sprintf(`INSERT INTO hangar_operation_leases
						(kind, activation_epoch, owner_id, lease_fence, expires_at)
					 VALUES ('inventory', 1, '%s', 2, now() + interval '15 minutes')`, claimID))
			})

			It("refuses a takeover that does not advance the fence", func() {
				mustExec(database, fmt.Sprintf(`INSERT INTO hangar_operation_leases
					(kind, activation_epoch, owner_id, lease_fence, expires_at)
					VALUES ('inventory', 1, '%s', 3, now() + interval '15 minutes')`, workerID))

				Expect(expectRefusal(database, "a takeover at the same fence", fmt.Sprintf(`
					UPDATE hangar_operation_leases SET owner_id = '%s' WHERE kind = 'inventory'`,
					claimID))).
					To(ContainSubstring("changed owner without advancing the fence"))
			})

			It("refuses a lease term under the fifteen-minute floor", func() {
				Expect(expectRefusal(database, "a five-minute worker lease", fmt.Sprintf(`
					INSERT INTO hangar_operation_leases
						(kind, activation_epoch, owner_id, lease_fence, expires_at)
					VALUES ('inventory', 1, '%s', 1, now() + interval '5 minutes')`, workerID))).
					To(ContainSubstring("hangar_operation_lease_term"))
			})
		})
	})

	// Every deferred constraint trigger and partial unique index above is only
	// worth having if removing it changes the answer. Each case here drops the
	// guard inside a transaction that is rolled back, re-runs the vector that
	// the guard refused, and requires it to be accepted.
	// The two statements every Hangar transaction runs, and what the planner
	// can do with them.
	//
	// `SET LOCAL enable_seqscan = off` is what makes this decidable on an empty
	// database: it is a cost penalty, not a prohibition, so a table with a
	// usable index switches to it and a table without one still sequentially
	// scans and says so. Without the penalty every plan here is a Seq Scan on
	// three rows and the assertion would mean nothing either way.
	Describe("the hot statements have an index", func() {
		BeforeEach(seedEpoch)

		planFor := func(statement string, arguments ...any) string {
			GinkgoHelper()

			tx, err := database.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = tx.Rollback() }()

			_, err = tx.Exec(`SET LOCAL enable_seqscan = off`)
			Expect(err).NotTo(HaveOccurred())

			rows, err := tx.Query("EXPLAIN "+statement, arguments...)
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()

			var plan string
			for rows.Next() {
				var line string
				Expect(rows.Scan(&line)).To(Succeed())
				plan += line + "\n"
			}
			Expect(rows.Err()).NotTo(HaveOccurred())

			return plan
		}

		It("looks up a logical reservation by correlation without a sequential scan", func() {
			// The lock helper's class-1 statement, verbatim. The only
			// (scope, digest) index was partial on state =
			// 'unresolved_generation', and this statement carries no such
			// predicate, so the planner could not use it: every Hangar
			// transaction that touches a correlation scanned the table.
			Expect(planFor(`
				SELECT 1 FROM hangar_logical_reservations
				WHERE scope = $1 AND digest = $2
				ORDER BY reservation_id
				FOR NO KEY UPDATE`, "team-a", sampleDigest)).
				To(ContainSubstring("hangar_logical_reservations_correlation_idx"),
					"the correlation was answered by a scan or by the primary key, not by an "+
						"index on (scope, digest)")
		})

		It("reads a receipt by handoff without a sequential scan", func() {
			Expect(planFor(
				`SELECT claims FROM hangar_output_receipts WHERE handoff_id = $1`, handoffID)).
				To(ContainSubstring("hangar_output_receipts_handoff_idx"))
		})
	})

	Describe("the guards are load-bearing", func() {
		BeforeEach(func() {
			seedEpoch()
			seedSafePolicy()
			seedPredeclaration(handoffID, sourceLeaseID, executionID, true)
		})

		dropTriggerOnEach := func(name string, tables ...string) []string {
			var statements []string
			for _, table := range tables {
				statements = append(statements,
					fmt.Sprintf(`DROP TRIGGER %s ON %s`, name, table))
			}

			return statements
		}

		It("without hangar_handoff_branch_cardinality, a handoff reaches two branches", func() {
			vector := append(stage2(handoffID, reservationID), fmt.Sprintf(`
				INSERT INTO hangar_no_capture_dispositions
					(handoff_id, execution_id, activation_epoch, source_lease_id, reason, release_intent_id)
				VALUES ('%s', '%s', 1, '%s', 'unresolved', '%s')`,
				handoffID, executionID, sourceLeaseID, releaseIntentID))

			Expect(attempt(database, vector...)).To(HaveOccurred())

			mutated := append(dropTriggerOnEach("hangar_handoff_branch_cardinality",
				"hangar_handoff_dispositions", "hangar_capture_reservations",
				"hangar_no_capture_dispositions", "hangar_pre_reservation_cancel_dispositions"),
				vector...)
			Expect(attempt(database, mutated...)).To(Succeed(),
				"dropping the trigger did not change the answer, so the vector was not testing it")
		})

		It("without hangar_stage_two_matches_predeclaration, Stage 2 needs no acknowledged hold", func() {
			seedPredeclaration(secondHandoffID, secondLeaseID, secondExecutionID, false)
			vector := stage2(secondHandoffID, secondReservation)

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append([]string{
				`DROP TRIGGER hangar_stage_two_matches_predeclaration ON hangar_capture_reservations`,
			}, vector...)...)).To(Succeed())
		})

		It("without hangar_logical_resolution_is_fenced, a stale owner resolves", func() {
			commitStage2(handoffID, reservationID)
			seedCaptureLease(reservationID, 4)
			vector := []string{fmt.Sprintf(`INSERT INTO hangar_logical_reservations
				(reservation_id, scope, digest, logical_bytes, capture_fence)
				VALUES ('%s', 'team-a', '%s', 4096, 3)`, reservationID, sampleDigest)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append([]string{
				`DROP TRIGGER hangar_logical_resolution_is_fenced ON hangar_logical_reservations`,
			}, vector...)...)).To(Succeed())
		})

		It("without hangar_create_follows_resolution, an object create precedes its reservation", func() {
			commitStage2(handoffID, reservationID)
			seedCaptureLease(reservationID, 1)
			vector := []string{fmt.Sprintf(`UPDATE hangar_capture_reservations
				SET first_create_attempted_at = now() WHERE reservation_id = '%s'`, reservationID)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append([]string{
				`DROP TRIGGER hangar_create_follows_resolution ON hangar_capture_reservations`,
			}, vector...)...)).To(Succeed())
		})

		It("without hangar_receipt_admission, a receipt registers under an unattested key", func() {
			commitStage2(handoffID, reservationID)
			seedCaptureLease(reservationID, 1)
			seedLogicalReservation(reservationID, sampleDigest, 1)
			lifecycle := seedLifecycle(sampleDigest, sampleGeneration)
			mustExec(database, fmt.Sprintf(`
				INSERT INTO hangar_receipt_stat_challenges
					(nonce, handoff_id, reservation_id, activation_epoch, receipt_public_key_id,
					 scope, digest, generation, capture_fence, not_after, consumed_at)
				VALUES ('%s', '%s', '%s', 1, 'receipt-key-1', 'team-a', '%s', %d, 1,
					now() + interval '5 minutes', now())`,
				challengeNonce, handoffID, reservationID, sampleDigest, sampleGeneration))
			vector := []string{fmt.Sprintf(`INSERT INTO hangar_output_receipts
				(reservation_id, lifecycle_id, handoff_id, activation_epoch, receipt_key_id,
				 challenge_nonce, marker_version, algorithm, claims, signature)
				VALUES ('%s', %d, '%s', 1, 'receipt-key-9', '%s', 'hangar-output-v1', 'ed25519',
					'{}', 'signature')`,
				reservationID, lifecycle, handoffID, challengeNonce)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append([]string{
				`DROP TRIGGER hangar_receipt_admission ON hangar_output_receipts`,
			}, vector...)...)).To(Succeed())
		})

		It("without hangar_reclaim_exclusion, a reclaim is admitted beside an active claim", func() {
			lifecycle := seedFullChain()
			seedClaim(claimID, lifecycle)
			vector := []string{fmt.Sprintf(`INSERT INTO hangar_reclaim_jobs
				(lifecycle_id, activation_epoch, owner_id, lease_fence, generation, metageneration, expires_at)
				VALUES (%d, 1, '%s', 1, %d, 1, now() + interval '20 minutes')`,
				lifecycle, workerID, sampleGeneration)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append(dropTriggerOnEach("hangar_reclaim_exclusion",
				"hangar_reclaim_jobs", "hangar_claims", "hangar_read_leases"), vector...)...)).
				To(Succeed())
		})

		It("without hangar_policy_admits_new_protection, an at-risk plane admits a claim", func() {
			lifecycle := seedFullChain()
			mustExec(database, `
				INSERT INTO hangar_policy_snapshots
					(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
					 lifecycle_delete_rules, state)
				VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)
			vector := []string{fmt.Sprintf(`
				INSERT INTO hangar_claims (claim_id, lifecycle_id, activation_epoch, consumer_binding_id)
				VALUES ('%s', %d, 1, 'opaque-binding')`, claimID, lifecycle)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append(dropTriggerOnEach("hangar_policy_admits_new_protection",
				"hangar_claims", "hangar_reclaim_jobs"), vector...)...)).To(Succeed())
		})

		// The other three admissions Req 52 names. Each drops only the copy on
		// its own table, so a passing row says which attachment carried the
		// refusal rather than that some copy somewhere did.
		It("without hangar_policy_admits_new_protection on hangar_read_leases, an at-risk plane grants", func() {
			lifecycle := seedFullChain()
			seedClaim(claimID, lifecycle)
			mustExec(database, `
				INSERT INTO hangar_policy_snapshots
					(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
					 lifecycle_delete_rules, state)
				VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)
			vector := []string{fmt.Sprintf(`
				INSERT INTO hangar_read_leases
					(read_lease_id, claim_id, lifecycle_id, activation_epoch, lease_fence, expires_at)
				VALUES ('%s', '%s', %d, 1, 1, now() + interval '20 minutes')`,
				readLeaseID, claimID, lifecycle)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append(dropTriggerOnEach("hangar_policy_admits_new_protection",
				"hangar_read_leases"), vector...)...)).To(Succeed())
		})

		It("without hangar_policy_admits_new_protection on hangar_exact_lifecycles, an at-risk plane adopts", func() {
			seedFullChain()
			mustExec(database, `
				INSERT INTO hangar_policy_snapshots
					(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
					 lifecycle_delete_rules, state)
				VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)
			vector := []string{fmt.Sprintf(`
				INSERT INTO hangar_exact_lifecycles
					(scope, digest, generation, metageneration, activation_epoch,
					 marker_version, origin, state)
				VALUES ('team-a', '%s', %d, 1, 1, 'hangar-output-v1', 'adopted', 'adopted')`,
				otherDigest, sampleGeneration+1)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append(dropTriggerOnEach("hangar_policy_admits_new_protection",
				"hangar_exact_lifecycles"), vector...)...)).To(Succeed())
		})

		It("without hangar_policy_admits_new_protection on hangar_capture_reservations, an at-risk plane captures", func() {
			seedPredeclaration(secondHandoffID, secondLeaseID, secondExecutionID, true)
			mustExec(database, `
				INSERT INTO hangar_policy_snapshots
					(activation_epoch, bucket_fingerprint, metageneration, policy_hash,
					 lifecycle_delete_rules, state)
				VALUES (1, 'gs://output-bucket', 4, 'policy-hash-2', 1, 'at_risk')`)
			vector := stage2(secondHandoffID, secondReservation)

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append(dropTriggerOnEach("hangar_policy_admits_new_protection",
				"hangar_capture_reservations"), vector...)...)).To(Succeed())
		})

		It("without hangar_reclaim_evidence, an ambiguous deletion is called confirmed", func() {
			lifecycle := seedFullChain()
			mustExec(database, fmt.Sprintf(`INSERT INTO hangar_reclaim_jobs
				(lifecycle_id, activation_epoch, owner_id, lease_fence, generation, metageneration, expires_at)
				VALUES (%d, 1, '%s', 1, %d, 1, now() + interval '20 minutes')`,
				lifecycle, workerID, sampleGeneration))
			var job int64
			Expect(database.QueryRow(`SELECT id FROM hangar_reclaim_jobs LIMIT 1`).Scan(&job)).To(Succeed())
			mustExec(database, fmt.Sprintf(`INSERT INTO hangar_reclaim_attempts
				(job_id, lease_fence, outcome, observed_at) VALUES (%d, 1, 'timeout', now())`, job))
			vector := []string{fmt.Sprintf(`UPDATE hangar_reclaim_jobs
				SET outcome = 'reclaimed_confirmed', finalized_at = now() WHERE id = %d`, job)}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append([]string{
				`DROP TRIGGER hangar_reclaim_evidence ON hangar_reclaim_jobs`,
			}, vector...)...)).To(Succeed())
		})

		It("without the per-facet enabled indexes, two epochs are enabled at once", func() {
			vector := []string{`INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation) VALUES (2, 'enabled', 'initial', '{}')`}

			Expect(attempt(database, vector...)).To(HaveOccurred())
			Expect(attempt(database, append([]string{
				`DROP INDEX hangar_output_one_enabled_base_epoch`,
			}, vector...)...)).To(Succeed())
		})

		It("without hangar_reclaim_jobs_one_active_idx, two reclaims run on one generation", func() {
			lifecycle := seedFullChain()
			admit := fmt.Sprintf(`INSERT INTO hangar_reclaim_jobs
				(lifecycle_id, activation_epoch, owner_id, lease_fence, generation, metageneration, expires_at)
				VALUES (%d, 1, '%s', 1, %d, 1, now() + interval '20 minutes')`,
				lifecycle, workerID, sampleGeneration)

			Expect(attempt(database, admit, admit)).To(HaveOccurred())
			Expect(attempt(database, `DROP INDEX hangar_reclaim_jobs_one_active_idx`, admit, admit)).
				To(Succeed())
		})
	})

	Describe("the down migration", func() {
		migrateDown := func() error {
			return migration.NewOpenHelper("pgx", postgresRunner.DataSourceName(), nil, nil, nil).
				MigrateToVersion(hangarOutputPriorVersion)
		}

		It("refuses before any DDL while the plane holds state", func() {
			seedEpoch()
			seedSafePolicy()
			seedPredeclaration(handoffID, sourceLeaseID, executionID, true)
			seedFullChain()
			Expect(database.Close()).To(Succeed())

			err := migrateDown()
			Expect(err).To(MatchError(ContainSubstring("refusing to remove the output plane")))
			Expect(err.Error()).To(ContainSubstring("1 registered receipt(s)"))

			// "Before any DDL" is the claim, so the schema must be untouched.
			database = postgresRunner.OpenDBAtVersion(hangarOutputVersion)
			for _, table := range hangarOutputTables {
				var exists bool
				Expect(database.QueryRow(
					`SELECT EXISTS(SELECT 1 FROM pg_tables WHERE tablename = $1)`, table,
				).Scan(&exists)).To(Succeed())
				Expect(exists).To(BeTrue(), "the refused down migration dropped %s", table)
			}
		})

		It("refuses while a worker still holds an operation lease", func() {
			// The one row the blocker list did not check. Every other table it
			// omits is reachable transitively through a RESTRICT foreign key to
			// one it does check, so DROP TABLE would have failed loudly; an
			// operation lease references only the epoch, and an `initial`
			// epoch is allowed to drop -- so this combination dropped the
			// plane out from under a running worker.
			mustExec(database, `
				INSERT INTO hangar_output_activation_epochs (epoch_id) VALUES (1)`)
			mustExec(database, fmt.Sprintf(`
				INSERT INTO hangar_operation_leases
					(kind, activation_epoch, owner_id, lease_fence, expires_at)
				VALUES ('inventory', 1, '%s', 1, now() + interval '15 minutes')`, workerID))
			Expect(database.Close()).To(Succeed())

			err := migrateDown()
			Expect(err).To(MatchError(ContainSubstring("refusing to remove the output plane")))
			Expect(err.Error()).To(ContainSubstring("1 held operation lease(s)"))

			database = postgresRunner.OpenDBAtVersion(hangarOutputVersion)
		})

		It("refuses while an epoch has left initial, even with no other rows", func() {
			seedEpoch()
			Expect(database.Close()).To(Succeed())

			Expect(migrateDown()).To(MatchError(ContainSubstring("activation epoch(s) past initial")))

			database = postgresRunner.OpenDBAtVersion(hangarOutputVersion)
		})

		It("removes the plane when it is held and empty", func() {
			Expect(database.Close()).To(Succeed())
			Expect(migrateDown()).To(Succeed())

			database = postgresRunner.OpenDBAtVersion(hangarOutputPriorVersion)
			for _, table := range hangarOutputTables {
				var exists bool
				Expect(database.QueryRow(
					`SELECT EXISTS(SELECT 1 FROM pg_tables WHERE tablename = $1)`, table,
				).Scan(&exists)).To(Succeed())
				Expect(exists).To(BeFalse(), "the down migration left %s behind", table)
			}
			var functions int
			Expect(database.QueryRow(`
				SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'public' AND p.proname LIKE 'hangar%'`).Scan(&functions)).To(Succeed())
			Expect(functions).To(BeZero())
		})
	})
})
