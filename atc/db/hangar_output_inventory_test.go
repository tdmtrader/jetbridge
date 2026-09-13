package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/output"
)

// The durable half of the sweep: one cursor per bucket and epoch, its fence,
// and the debt that has to be committed before it moves.
//
// Real PostgreSQL, because every assertion here is about what survives a
// rollback, a second owner, or a fence that moved under a reader. The page and
// classification internals are stdlib `testing` next to the role that owns them
// (hangar/output/conformance/inventory_test.go); this file is the part that has
// a transaction in it.
var _ = Describe("the Hangar inventory cursor", func() {
	const (
		bucket = "gs://output-bucket"
		epoch  = int64(1)
	)

	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	begin := func() db.HangarOutputTx {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())

		return db.HangarOutputTx{Tx: tx}
	}

	// in runs one closure in its own committed transaction, which is how a
	// spec says "this much is durable" without repeating six lines.
	in := func(work func(tx db.HangarOutputTx)) {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)
		work(tx)
		Expect(tx.Commit()).To(Succeed())
	}

	activateEpoch := func() {
		GinkgoHelper()
		_, err := dbConn.Exec(`
			INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation, output_attestation,
				 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
				 materialization_key_id, bucket_fingerprint, derived_namespace)
			VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
				now() - interval '1 day', now() + interval '30 days',
				'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
		Expect(err).NotTo(HaveOccurred())
	}

	debtFor := func(key string, generation int64, reason output.DebtReason) output.InventoryDebt {
		return output.InventoryDebt{
			ProtocolVersion: output.ProtocolVersion,
			ActivationEpoch: 1,
			ObjectKey:       key,
			Generation:      generation,
			Reason:          reason,
			Attempts:        1,
			ObservedAt:      output.NewTimestamp(time.Now()),
			Detail:          "recorded by a spec",
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("inventory-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		activateEpoch()
	})

	It("has exactly one cursor per bucket and activation epoch", func() {
		var first, second output.InventoryCursor
		in(func(tx db.HangarOutputTx) {
			var err error
			first, err = repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
			Expect(err).NotTo(HaveOccurred())
		})
		in(func(tx db.HangarOutputTx) {
			var err error
			second, err = repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
			Expect(err).NotTo(HaveOccurred())
		})

		Expect(first.CursorFence).To(Equal(second.CursorFence))
		Expect(first.AtCycleStart()).To(BeTrue())

		var rows int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM hangar_inventory_cursors
			WHERE bucket_fingerprint = $1 AND activation_epoch = $2`,
			bucket, epoch).Scan(&rows)).To(Succeed())
		Expect(rows).To(Equal(1),
			"two owners over one bucket is two partitions of one sweep, and an object in "+
				"neither partition is never adopted and never collected")
	})

	Describe("the fence", func() {
		BeforeEach(func() {
			in(func(tx db.HangarOutputTx) {
				_, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 4)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("lets the current owner reserve a page and refuses a stale one", func() {
			// The control first: the current fence is admitted, so the refusal
			// below cannot pass for a method that refuses everything.
			in(func(tx db.HangarOutputTx) {
				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 4)
				Expect(err).NotTo(HaveOccurred())
				Expect(cursor.CursorFence).To(BeEquivalentTo(4))
			})

			tx := begin()
			defer db.Rollback(tx)
			_, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 3)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("stale owner"))
		})

		It("advances under the current fence and refuses an overtaken one", func() {
			var next output.InventoryCursor
			in(func(tx db.HangarOutputTx) {
				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 4)
				Expect(err).NotTo(HaveOccurred())
				next = cursor
				next.AfterKey = "deployments/blue/hangar/v1/a"
				next.AfterGeneration = 17
				Expect(repository.AdvanceInventoryCursor(ctx, tx, bucket, next, nil)).To(Succeed())
			})

			in(func(tx db.HangarOutputTx) {
				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 4)
				Expect(err).NotTo(HaveOccurred())
				Expect(cursor.AfterKey).To(Equal("deployments/blue/hangar/v1/a"))
				Expect(cursor.AfterGeneration).To(BeEquivalentTo(17))
			})

			// A takeover moves the fence, and the previous owner's advance --
			// prepared before the takeover and arriving after it -- writes
			// nothing.
			in(func(tx db.HangarOutputTx) {
				_, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 5)
				Expect(err).NotTo(HaveOccurred())
			})

			stale := next
			stale.AfterKey = "deployments/blue/hangar/v1/z"
			stale.AfterGeneration = 99

			tx := begin()
			defer db.Rollback(tx)
			err := repository.AdvanceInventoryCursor(ctx, tx, bucket, stale, nil)
			Expect(err).To(MatchError(output.ErrConflict))

			var key string
			Expect(dbConn.QueryRow(`
				SELECT after_key FROM hangar_inventory_cursors
				WHERE bucket_fingerprint = $1 AND activation_epoch = $2`,
				bucket, epoch).Scan(&key)).To(Succeed())
			Expect(key).To(Equal("deployments/blue/hangar/v1/a"),
				"an overtaken owner moved the cursor past objects its successor has not read")
		})

		It("refuses a fence that moves backwards", func() {
			_, err := dbConn.Exec(`
				UPDATE hangar_inventory_cursors SET cursor_fence = 2
				WHERE bucket_fingerprint = $1 AND activation_epoch = $2`, bucket, epoch)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("moved its fence backwards"))
		})

		It("refuses a cycle that goes backwards", func() {
			_, err := dbConn.Exec(`
				UPDATE hangar_inventory_cursors SET cycle = 3
				WHERE bucket_fingerprint = $1 AND activation_epoch = $2`, bucket, epoch)
			Expect(err).NotTo(HaveOccurred())

			_, err = dbConn.Exec(`
				UPDATE hangar_inventory_cursors SET cycle = 2
				WHERE bucket_fingerprint = $1 AND activation_epoch = $2`, bucket, epoch)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("went back a cycle"))
		})
	})

	Describe("debt and the advance", func() {
		BeforeEach(func() {
			in(func(tx db.HangarOutputTx) {
				_, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("commits per-object debt in the same transaction as the advance", func() {
			var next output.InventoryCursor
			in(func(tx db.HangarOutputTx) {
				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
				Expect(err).NotTo(HaveOccurred())
				next = cursor
				next.AfterKey = "deployments/blue/hangar/v1/poison"
				next.AfterGeneration = 5
				Expect(repository.AdvanceInventoryCursor(ctx, tx, bucket, next, []output.InventoryDebt{
					debtFor("deployments/blue/hangar/v1/poison", 5, output.DebtPoisonMetadata),
				})).To(Succeed())
			})

			in(func(tx db.HangarOutputTx) {
				owed, err := repository.ReadInventoryDebt(ctx, tx, bucket, epoch, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(owed).To(HaveLen(1))
				Expect(owed[0].Reason).To(Equal(output.DebtPoisonMetadata))
				Expect(owed[0].Attempts).To(Equal(1))

				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(cursor.AfterGeneration).To(BeEquivalentTo(5))
			})
		})

		It("replays the page when the pass crashes before its commit", func() {
			// A pass that writes debt and advances, and then dies. Neither half
			// is durable, so the next owner reads the same cursor and reads the
			// same objects -- which is the crash replay Req 42 requires, and
			// the reason the two halves are one transaction.
			tx := begin()
			cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
			Expect(err).NotTo(HaveOccurred())
			next := cursor
			next.AfterKey = "deployments/blue/hangar/v1/never-committed"
			next.AfterGeneration = 11
			Expect(repository.AdvanceInventoryCursor(ctx, tx, bucket, next, []output.InventoryDebt{
				debtFor("deployments/blue/hangar/v1/never-committed", 11, output.DebtStatFailure),
			})).To(Succeed())
			db.Rollback(tx)

			in(func(tx db.HangarOutputTx) {
				replayed, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(replayed.AfterKey).To(BeEmpty(),
					"the cursor moved past a page whose dispositions were never committed")

				owed, err := repository.ReadInventoryDebt(ctx, tx, bucket, epoch, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(owed).To(BeEmpty(),
					"debt outlived the transaction that was going to justify it")
			})
		})

		It("counts a repeated object's attempts instead of growing a row per sighting", func() {
			for attempt := 0; attempt < 3; attempt++ {
				in(func(tx db.HangarOutputTx) {
					Expect(repository.RecordInventoryDebt(ctx, tx, bucket,
						debtFor("deployments/blue/hangar/v1/stubborn", 2,
							output.DebtMarkerMismatch))).To(Succeed())
				})
			}

			in(func(tx db.HangarOutputTx) {
				owed, err := repository.ReadInventoryDebt(ctx, tx, bucket, epoch, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(owed).To(HaveLen(1),
					"one poisoned object produced one row per sighting; the table is unbounded")
				Expect(owed[0].Attempts).To(Equal(3))
			})
		})

		It("refuses debt for a bucket and epoch it holds no cursor for", func() {
			tx := begin()
			defer db.Rollback(tx)
			err := repository.RecordInventoryDebt(ctx, tx, "gs://somebody-elses-bucket",
				debtFor("deployments/blue/hangar/v1/a", 1, output.DebtPoisonMetadata))
			Expect(err).To(HaveOccurred())
		})

		It("does not let one poisoned object stop the keys behind it", func() {
			// Two objects: the first is debt, the second classifies. The cursor
			// ends past BOTH, which is the whole of "poison cannot starve later
			// keys" expressed durably.
			in(func(tx db.HangarOutputTx) {
				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
				Expect(err).NotTo(HaveOccurred())
				next := cursor
				next.AfterKey = "deployments/blue/hangar/v1/second"
				next.AfterGeneration = 8
				Expect(repository.AdvanceInventoryCursor(ctx, tx, bucket, next, []output.InventoryDebt{
					debtFor("deployments/blue/hangar/v1/first", 7, output.DebtPoisonMetadata),
				})).To(Succeed())
			})

			in(func(tx db.HangarOutputTx) {
				cursor, err := repository.LoadInventoryCursor(ctx, tx, bucket, epoch, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(cursor.AfterKey).To(Equal("deployments/blue/hangar/v1/second"))
			})
		})
	})
})

// One durable lease per operation kind, on the database clock.
var _ = Describe("the Hangar operation leases", func() {
	const epoch = int64(1)

	var (
		ctx            context.Context
		repository     *db.HangarOutputRepository
		ownerA, ownerB string
	)

	begin := func() db.HangarOutputTx {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())

		return db.HangarOutputTx{Tx: tx}
	}

	in := func(work func(tx db.HangarOutputTx)) {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)
		work(tx)
		Expect(tx.Commit()).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		ownerA, ownerB = uuid.NewString(), uuid.NewString()
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("lease-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		_, err = dbConn.Exec(`
			INSERT INTO hangar_output_activation_epochs
				(epoch_id, base_state, output_state, base_attestation, output_attestation,
				 receipt_public_key_id, receipt_key_valid_from, receipt_key_valid_until,
				 materialization_key_id, bucket_fingerprint, derived_namespace)
			VALUES (1, 'enabled', 'enabled', '{}', '{}', 'receipt-key-1',
				now() - interval '1 day', now() + interval '30 days',
				'materialize-key-1', 'gs://output-bucket', 'deployment/ns')`)
		Expect(err).NotTo(HaveOccurred())
	})

	It("gives one kind to one owner and refuses a second while it is live", func() {
		var held output.OperationLease
		in(func(tx db.HangarOutputTx) {
			var err error
			held, err = repository.ClaimOperationLease(ctx, tx,
				output.OperationInventory, epoch, ownerA, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
			Expect(held.LeaseFence).To(BeEquivalentTo(1))
		})

		tx := begin()
		defer db.Rollback(tx)
		_, err := repository.ClaimOperationLease(ctx, tx,
			output.OperationInventory, epoch, ownerB, output.MinLeaseTerm)
		Expect(err).To(MatchError(output.ErrConflict))
	})

	It("is idempotent for the same owner and does not fence it out of its own lease", func() {
		var first, again output.OperationLease
		in(func(tx db.HangarOutputTx) {
			var err error
			first, err = repository.ClaimOperationLease(ctx, tx,
				output.OperationReclaimDelete, epoch, ownerA, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
		})
		in(func(tx db.HangarOutputTx) {
			var err error
			again, err = repository.ClaimOperationLease(ctx, tx,
				output.OperationReclaimDelete, epoch, ownerA, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
		})
		Expect(again.LeaseFence).To(Equal(first.LeaseFence),
			"a controller that lost its answer and asked again fenced itself out")
	})

	It("lets a second owner take over only after expiry, and advances the fence", func() {
		var held output.OperationLease
		in(func(tx db.HangarOutputTx) {
			var err error
			held, err = repository.ClaimOperationLease(ctx, tx,
				output.OperationAdoption, epoch, ownerA, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
		})

		// Expire it on the DATABASE clock. The schema's own term floor is what
		// stops a controller writing a short lease, so the expiry is moved
		// rather than the term shortened.
		_, err := dbConn.Exec(`
			UPDATE hangar_operation_leases
			   SET renewed_at = now() - interval '1 hour', expires_at = now() - interval '45 minutes'
			 WHERE kind = 'adoption' AND activation_epoch = $1`, epoch)
		Expect(err).NotTo(HaveOccurred())

		var taken output.OperationLease
		in(func(tx db.HangarOutputTx) {
			var err error
			taken, err = repository.ClaimOperationLease(ctx, tx,
				output.OperationAdoption, epoch, ownerB, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
		})
		Expect(taken.LeaseFence).To(BeNumerically(">", held.LeaseFence))

		// And the fenced-out owner cannot renew its way back in.
		tx := begin()
		defer db.Rollback(tx)
		_, err = repository.RenewOperationLease(ctx, tx, held, output.MinLeaseTerm)
		Expect(err).To(MatchError(output.ErrConflict))
	})

	It("renews a lease the caller still holds", func() {
		var held, renewed output.OperationLease
		in(func(tx db.HangarOutputTx) {
			var err error
			held, err = repository.ClaimOperationLease(ctx, tx,
				output.OperationPolicyAttestation, epoch, ownerA, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
		})

		_, err := dbConn.Exec(`
			UPDATE hangar_operation_leases
			   SET renewed_at = now() - interval '10 minutes',
			       expires_at = now() + interval '5 minutes'
			 WHERE kind = 'policy_attestation' AND activation_epoch = $1`, epoch)
		Expect(err).NotTo(HaveOccurred())

		in(func(tx db.HangarOutputTx) {
			var err error
			renewed, err = repository.RenewOperationLease(ctx, tx, held, output.MinLeaseTerm)
			Expect(err).NotTo(HaveOccurred())
		})
		Expect(renewed.ExpiresAt.UTC()).To(BeTemporally(">", held.ExpiresAt.UTC().Add(-time.Second)))
		Expect(renewed.LeaseFence).To(Equal(held.LeaseFence))
	})

	It("keeps every kind independent, so one busy operation cannot starve another", func() {
		// Every kind held by a DIFFERENT owner at once. A lease keyed on
		// anything less than (kind, epoch) would refuse the second of these,
		// and one operation's cursor would be another's.
		in(func(tx db.HangarOutputTx) {
			for _, kind := range output.OperationKinds() {
				lease, err := repository.ClaimOperationLease(ctx, tx, kind, epoch,
					uuid.NewString(), output.MinLeaseTerm)
				Expect(err).NotTo(HaveOccurred(), "claiming %s", kind)
				Expect(lease.Kind).To(Equal(kind))
			}
		})

		var kinds int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM hangar_operation_leases WHERE activation_epoch = $1`,
			epoch).Scan(&kinds)).To(Succeed())
		Expect(kinds).To(Equal(len(output.OperationKinds())))
	})

	It("refuses a lease term below the schema's floor", func() {
		tx := begin()
		defer db.Rollback(tx)
		_, err := repository.ClaimOperationLease(ctx, tx,
			output.OperationInventory, epoch, ownerA, time.Minute)
		Expect(err).To(HaveOccurred())
	})
})
