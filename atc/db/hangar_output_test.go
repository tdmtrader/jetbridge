package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// These are the races the lock suffix exists for, run against real PostgreSQL
// with real concurrent transactions and real arrival-order inversion.
//
// A test that only proves "no deadlock" would pass against a schema that let
// both sides win, so every case here also names the typed loser and asserts
// that nothing the loser was going to hand a consumer -- a claim, a binding --
// is visible afterwards.
var _ = Describe("the Hangar output lock suffix", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		consumer   db.HangarConsumerPrefix
	)

	BeforeEach(func() {
		ctx = context.Background()

		// The suite runs on one pooled connection so that code needing a second
		// one deadlocks visibly. These specs need three on purpose: two racing
		// transactions and a third to read pg_locks while both are open. It
		// goes back to one afterwards.
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		var err error
		consumer, err = db.HangarConsumerPrefixHeld("test-consumer")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)
	})
	// The fixture is package-level (hangar_output_fixture_test.go) because a
	// second file's specs need the same capture; these bindings keep every call
	// site below reading the way it did when it was a closure.
	activate := func() { hangarActivateEpoch(ctx, repository) }
	// publish, with its publication grace already elapsed on the database
	// clock. Every spec in this file that reclaims a generation needs that --
	// elapsed grace is one of the pass's preconditions -- and a spec that did
	// not arrange it would be asserting the grace refusal under the name of
	// whatever else it was about.
	publish := func(digest hangar.Digest, generation int64) (output.CaptureKey, hangar.TreeRef) {
		GinkgoHelper()
		capture, ref := hangarPublish(ctx, repository, digest, generation)
		hangarAgePublication(ref, hangarGraceElapsed)

		return capture, ref
	}

	acquire := func(tx db.Tx, id output.ClaimID, ref hangar.TreeRef, binding string) error {
		_, err := repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           id,
			Ref:               ref,
			ConsumerBindingID: output.OpaqueID(binding),
			RequestedAt:       output.NewTimestamp(time.Now()),
		})
		return err
	}

	countActiveClaims := func(ref hangar.TreeRef) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM hangar_claims c
			JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
			WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3 AND c.released_at IS NULL`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&count)).To(Succeed())

		return count
	}

	reclaimed := func(ref hangar.TreeRef) bool {
		GinkgoHelper()
		var stamped bool
		Expect(dbConn.QueryRow(`
			SELECT reclaimed_at IS NOT NULL FROM hangar_exact_lifecycles
			WHERE scope = $1 AND digest = $2 AND generation = $3`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&stamped)).To(Succeed())

		return stamped
	}

	// reclaim is the pass's transaction with the store call elided: hold the
	// generation under the tree and lifecycle locks and stamp it, in one
	// transaction the caller commits.
	reclaim := func(tx db.Tx, ref hangar.TreeRef) error {
		if err := repository.HoldForReclaim(ctx, tx, ref, output.DefaultPublicationGrace); err != nil {
			return err
		}

		return repository.StampReclaimed(ctx, tx, ref)
	}

	Describe("claimant versus reclaimer", func() {
		var ref hangar.TreeRef

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(1), 1725830823000001)
		})

		// Claimant first: the reclaimer must recheck under the lock and skip.
		It("lets a claimant that arrives first make the reclaimer skip", func() {
			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)

			claimID := output.ClaimID(uuid.NewString())
			Expect(acquire(claimant, claimID, ref, "binding-1")).To(Succeed())
			Expect(claimant.Commit()).To(Succeed())

			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)

			err = reclaim(reclaimer, ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 live claim(s)"))
			Expect(reclaimer.Rollback()).To(Succeed())

			Expect(countActiveClaims(ref)).To(Equal(1))
			Expect(reclaimed(ref)).To(BeFalse())
		})

		// Reclaimer first: the claimant finds no registered generation and
		// its whole transaction rolls back, so no binding and no claim
		// survive.
		It("makes a claimant that arrives second roll back with no claim", func() {
			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)

			Expect(reclaim(reclaimer, ref)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)

			claimID := output.ClaimID(uuid.NewString())
			err = acquire(claimant, claimID, ref, "binding-1")
			Expect(err).To(MatchError(output.ErrNotFound))
			Expect(err.Error()).To(ContainSubstring("reclaimed"))
			Expect(claimant.Rollback()).To(Succeed())

			Expect(countActiveClaims(ref)).To(BeZero())
			Expect(reclaimed(ref)).To(BeTrue())
		})

		// The genuinely concurrent case: both transactions open, both past
		// their consumer prefix, and the second one blocks on the exact
		// lifecycle row until the first commits. Exactly one wins, and which
		// one is decided by arrival rather than by luck.
		It("serializes two open transactions on the exact lifecycle row", func() {
			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)

			claimID := output.ClaimID(uuid.NewString())
			Expect(acquire(claimant, claimID, ref, "binding-1")).To(Succeed())

			passed := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				reclaimer, err := dbConn.Begin()
				if err != nil {
					passed <- err

					return
				}
				defer db.Rollback(reclaimer)
				err = reclaim(reclaimer, ref)
				if err == nil {
					err = reclaimer.Commit()
				}
				passed <- err
			}()

			// The reclaimer is blocked on the lifecycle row this transaction
			// holds. Nothing it could do would let it through, which is the
			// property: this is a lock, not a retry window.
			Consistently(passed, 500*time.Millisecond).ShouldNot(Receive())

			Expect(claimant.Commit()).To(Succeed())

			var reclaimErr error
			Eventually(passed, 10*time.Second).Should(Receive(&reclaimErr))
			Expect(reclaimErr).To(MatchError(output.ErrConflict))

			Expect(countActiveClaims(ref)).To(Equal(1))
			Expect(reclaimed(ref)).To(BeFalse())
		})
	})

	// A claim is the one hold, and the record it returns is what a read
	// warrant is minted from: a consumer's has no expiry, a reader's expires
	// after its term on the database clock, and asking again with the same
	// identity returns the committed row rather than a second hold.
	Describe("the claim record", func() {
		var ref hangar.TreeRef

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(2), 1725830823000002)
		})

		acquireRecord := func(acquisition output.ClaimAcquisition) output.ClaimRecord {
			GinkgoHelper()
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			record, err := repository.AcquireClaim(ctx, tx, acquisition)
			Expect(err).NotTo(HaveOccurred())
			Expect(db.HangarOutputTx{Tx: tx}.Commit()).To(Succeed())

			return record
		}

		It("records a consumer's hold with no expiry", func() {
			id := output.ClaimID(uuid.NewString())
			record := acquireRecord(output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           id,
				Ref:               ref,
				ConsumerBindingID: "binding-consumer",
				RequestedAt:       output.NewTimestamp(time.Now()),
			})
			Expect(record.Validate()).To(Succeed())
			Expect(record.ClaimID).To(Equal(id))
			Expect(record.Ref).To(Equal(ref))
			Expect(record.ConsumerBindingID).To(BeEquivalentTo("binding-consumer"))
			Expect(record.ActivationEpoch).To(BeEquivalentTo(1))
			Expect(record.ExpiresAt).To(BeNil(), "a consumer's hold was given a term")
			Expect(record.ReleasedAt).To(BeNil())
			Expect(record.Active()).To(BeTrue())
		})

		It("records a reader's hold expiring one term after it was acquired, on the database clock", func() {
			const term = 25 * time.Minute
			id := output.ClaimID(uuid.NewString())
			record := acquireRecord(output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           id,
				Ref:               ref,
				ConsumerBindingID: "input-read:task-handle/input-0",
				RequestedAt:       output.NewTimestamp(time.Now()),
				Term:              term,
			})
			Expect(record.Validate()).To(Succeed())
			Expect(record.ExpiresAt).NotTo(BeNil(), "a reader's hold was recorded without its term")
			Expect(record.ExpiresAt.Time).To(BeTemporally("~", record.AcquiredAt.Time.Add(term), time.Second))

			// The row is the record: what the warrant will carry is what the
			// reclaim pass will compare with now().
			var acquired, expires time.Time
			Expect(dbConn.QueryRow(`SELECT acquired_at, expires_at FROM hangar_claims WHERE claim_id = $1`,
				string(id)).Scan(&acquired, &expires)).To(Succeed())
			Expect(expires).To(BeTemporally("~", acquired.Add(term), time.Second))
			Expect(record.ExpiresAt.Time).To(BeTemporally("~", expires, time.Second))
		})

		It("answers a repeat of the same acquisition with the committed row", func() {
			acquisition := output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           output.ClaimID(uuid.NewString()),
				Ref:               ref,
				ConsumerBindingID: "input-read:task-handle/input-1",
				RequestedAt:       output.NewTimestamp(time.Now()),
				Term:              20 * time.Minute,
			}
			first := acquireRecord(acquisition)

			// The repeat names a later request and a longer term: an
			// acquisition whose commit answer was lost is asked again with
			// the same identity, and what comes back is what committed.
			acquisition.RequestedAt = output.NewTimestamp(time.Now().Add(time.Minute))
			acquisition.Term = 2 * time.Hour
			again := acquireRecord(acquisition)
			Expect(again).To(Equal(first))
			Expect(countActiveClaims(ref)).To(Equal(1))
		})

		It("refuses the same identity on another ref, and a released identity on its own", func() {
			_, other := publish(hangarDigest(22), 1725830823000022)
			id := output.ClaimID(uuid.NewString())
			acquireRecord(output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           id,
				Ref:               ref,
				ConsumerBindingID: "binding-1",
				RequestedAt:       output.NewTimestamp(time.Now()),
			})

			moving, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(moving)
			err = acquire(moving, id, other, "binding-1")
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("another tree ref"))
			Expect(moving.Rollback()).To(Succeed())

			releasing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(releasing)
			Expect(repository.ReleaseClaim(ctx, releasing, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         id,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(releasing.Commit()).To(Succeed())

			// The identity is never reused: the row is the tombstone.
			reviving, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reviving)
			err = acquire(reviving, id, ref, "binding-1")
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("tombstoned"))
			Expect(reviving.Rollback()).To(Succeed())
			Expect(countActiveClaims(ref)).To(BeZero())
		})
	})

	Describe("publication versus claim acquire", func() {
		It("refuses a claim on a ref no publication has registered, and admits it once one has", func() {
			activate()
			ref := hangar.TreeRef{
				Scope:      "team-a",
				Digest:     hangarDigest(3),
				Generation: 1725830823000003,
			}

			early, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(early)
			err = acquire(early, output.ClaimID(uuid.NewString()), ref, "binding-1")
			Expect(err).To(MatchError(output.ErrNotFound))
			Expect(early.Rollback()).To(Succeed())

			_, published := publish(hangarDigest(3), 1725830823000003)
			Expect(published).To(Equal(ref))

			late, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(late)
			Expect(acquire(late, output.ClaimID(uuid.NewString()), ref, "binding-1")).To(Succeed())
			Expect(late.Commit()).To(Succeed())
			Expect(countActiveClaims(ref)).To(Equal(1))
		})
	})

	Describe("a correlation an unresolved capture still protects", func() {
		It("shows no consumer result in the gap before a generation is published", func() {
			activate()

			// A capture that has written its digest but not yet published a
			// generation.
			hangarReserve(ctx, repository, hangarDigest(4), output.DefaultCaptureDeadline)

			orphan := hangar.TreeRef{
				Scope:      "team-a",
				Digest:     hangarDigest(4),
				Generation: 1725830823000004,
			}

			claimant, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)
			Expect(acquire(claimant, output.ClaimID(uuid.NewString()), orphan, "binding-1")).
				To(MatchError(output.ErrNotFound))
			Expect(claimant.Rollback()).To(Succeed())
		})
	})

	Describe("exact-generation replacement", func() {
		// Two captures of identical content deduplicate to one generation or
		// produce two; either way a claim names one exact generation and never
		// floats to the other.
		It("keeps a claim on the generation it named when another is published", func() {
			activate()
			_, first := publish(hangarDigest(5), 1725830823000005)
			_, second := publish(hangarDigest(5), 1725830823000006)
			Expect(first).NotTo(Equal(second))

			claimID := output.ClaimID(uuid.NewString())
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(acquire(tx, claimID, first, "binding-1")).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			// The same identity on the replacement generation is a conflict,
			// not a move.
			moving, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(moving)
			Expect(acquire(moving, claimID, second, "binding-1")).To(MatchError(output.ErrConflict))
			Expect(moving.Rollback()).To(Succeed())

			Expect(countActiveClaims(first)).To(Equal(1))
			Expect(countActiveClaims(second)).To(BeZero())

			// And the replacement can be reclaimed while the first is claimed,
			// which is the point of keying protection on the exact generation.
			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			Expect(reclaim(reclaimer, second)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			Expect(reclaimed(first)).To(BeFalse())
			Expect(reclaimed(second)).To(BeTrue())
		})
	})

	// The two lock-order rules that inverting which actor arrives first does
	// not cover: keys are taken in sorted order, and Hangar never acquires a
	// consumer-domain row.
	Describe("lock order and non-interference", func() {
		// The property is the *input* order, and it is proved by making the
		// helper block: a holder takes the key that sorts second, the helper is
		// handed the batch reversed, and a third connection asks with NOWAIT
		// whether the key that sorts first is held. A helper that locked in the
		// order it was given would have blocked on the second key immediately
		// and never reached the first, so the NOWAIT probe would succeed.
		//
		// The earlier version of this ran two concurrent transactions and
		// asserted neither deadlocked. It could not fail: two goroutines do not
		// interleave at statement granularity often enough to make an unsorted
		// helper deadlock, so the spec passed with sorting removed.
		blockedProbe := func(query string, args ...any) func() error {
			return func() error {
				probe, err := dbConn.Begin()
				if err != nil {
					return err
				}
				defer db.Rollback(probe)
				_, err = probe.Exec(query, args...)

				return err
			}
		}

		It("locks logical rows in sorted order when the batch arrives reversed", func() {
			activate()
			dbConn.SetMaxOpenConns(4)

			_, a := publish(hangarDigest(9), 1725830823000009)
			_, b := publish(hangarDigest(10), 1725830823000010)
			low, high := a, b
			if string(b.Digest) < string(a.Digest) {
				low, high = b, a
			}

			const lockLogical = `
				SELECT 1 FROM hangar_captures
				WHERE scope = $1 AND digest = $2
				FOR NO KEY UPDATE`
			probeFirst := blockedProbe(lockLogical+" NOWAIT", string(low.Scope), string(low.Digest))

			// Nobody holds the key that sorts first, yet. Without this the probe
			// below could be failing for a reason that has nothing to do with
			// the helper.
			Expect(probeFirst()).To(Succeed())

			holder, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(holder)
			_, err = holder.Exec(lockLogical, string(high.Scope), string(high.Digest))
			Expect(err).NotTo(HaveOccurred())

			done := make(chan error, 1)
			locked := make(chan db.HangarLocks, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					done <- err

					return
				}
				defer db.Rollback(tx)
				locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
					Logical: []db.HangarLogicalKey{
						{Scope: high.Scope, Digest: high.Digest},
						// The duplicate is deliberate: a batch that names one
						// correlation twice must lock it once.
						{Scope: high.Scope, Digest: high.Digest},
						{Scope: low.Scope, Digest: low.Digest},
					},
				})
				if err != nil {
					done <- err

					return
				}
				locked <- locks
				done <- nil
			}()

			Eventually(probeFirst, 10*time.Second, 50*time.Millisecond).Should(
				MatchError(ContainSubstring("55P03")),
				"the key that sorts first was never locked while the helper blocked on the key "+
					"that sorts second, so the helper took the batch in the order it was handed")

			Expect(holder.Rollback()).To(Succeed())

			var completed error
			Eventually(done, 10*time.Second).Should(Receive(&completed))
			Expect(completed).NotTo(HaveOccurred())

			var locks db.HangarLocks
			Expect(locked).To(Receive(&locks))
			Expect(locks.Logical).To(HaveLen(2), "the duplicated correlation was locked twice")
		})

		It("locks exact rows in sorted order when the batch arrives reversed", func() {
			activate()
			dbConn.SetMaxOpenConns(4)

			// One correlation, two generations, so the class-2 order is decided
			// by generation and compared numerically: "9" sorts after "10" as
			// bytes, and a lock order that depends on how a number was spelled is
			// not an order.
			digest := hangarDigest(14)
			// Two captures of one tree that published two generations.
			a := hangarPublishAt(ctx, repository, digest, 9, output.DefaultCaptureDeadline).Ref
			second := hangarPublishAt(ctx, repository, digest, 10, output.DefaultCaptureDeadline).Ref

			low, high := a, second

			const lockExact = `
				SELECT id FROM hangar_exact_lifecycles
				WHERE scope = $1 AND digest = $2 AND generation = $3
				FOR NO KEY UPDATE`
			probeFirst := blockedProbe(lockExact+" NOWAIT",
				string(low.Scope), string(low.Digest), low.Generation)

			Expect(probeFirst()).To(Succeed())

			holder, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(holder)
			_, err = holder.Exec(lockExact,
				string(high.Scope), string(high.Digest), high.Generation)
			Expect(err).NotTo(HaveOccurred())

			done := make(chan error, 1)
			locked := make(chan db.HangarLocks, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					done <- err

					return
				}
				defer db.Rollback(tx)
				locks, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
					Exact: []hangar.TreeRef{high, low, high},
				})
				if err != nil {
					done <- err

					return
				}
				locked <- locks
				done <- nil
			}()

			Eventually(probeFirst, 10*time.Second, 50*time.Millisecond).Should(
				MatchError(ContainSubstring("55P03")),
				"generation 9 was never locked while the helper blocked on generation 10, so the "+
					"helper took the batch in the order it was handed")

			Expect(holder.Rollback()).To(Succeed())

			var completed error
			Eventually(done, 10*time.Second).Should(Receive(&completed))
			Expect(completed).NotTo(HaveOccurred())

			var locks db.HangarLocks
			Expect(locked).To(Receive(&locks))
			Expect(locks.Exact).To(HaveLen(2), "the duplicated tree ref was locked twice")
			Expect(locks.Lifecycles).To(HaveLen(2))
		})

		It("acquires no lock on a consumer's own tables and inverts none it holds", func() {
			activate()
			_, ref := publish(hangarDigest(11), 1725830823000011)

			_, err := dbConn.Exec(`
				CREATE TABLE opaque_consumer_bindings (
					binding_id text PRIMARY KEY,
					visibility text NOT NULL,
					claim_id   uuid
				)`)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, err := dbConn.Exec(`DROP TABLE IF EXISTS opaque_consumer_bindings`)
				Expect(err).NotTo(HaveOccurred())
			})
			// Two rows: the one the consumer locks, and one it does not. The
			// second is what makes this spec able to fail -- pg_locks holds
			// one row per (relation, mode, pid) and row locks live in tuple
			// headers, so Hangar taking the same mode on the same row the
			// consumer already holds is invisible from outside. Reaching any
			// row the consumer left alone is not.
			_, err = dbConn.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility)
				VALUES ('binding-1', 'hidden'), ('binding-2', 'hidden')`)
			Expect(err).NotTo(HaveOccurred())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)

			// The consumer's own prefix, taken by the consumer, in its own
			// tables, before it enters the suffix.
			_, err = tx.Exec(`SELECT 1 FROM opaque_consumer_bindings WHERE binding_id = $1 FOR UPDATE`,
				"binding-1")
			Expect(err).NotTo(HaveOccurred())

			before := hangarLockedRelations(tx)
			Expect(before).To(ContainElement("opaque_consumer_bindings"))

			Expect(acquire(tx, output.ClaimID(uuid.NewString()), ref, "binding-1")).To(Succeed())

			after := hangarLockedRelations(tx)

			// Hangar took locks -- on Hangar tables.
			Expect(after).To(ContainElement("hangar_exact_lifecycles"))

			// And exactly one consumer relation is locked, the one the consumer
			// locked itself: Hangar acquired none and inverted none.
			consumerLocks := 0
			for _, relation := range after {
				if relation == "opaque_consumer_bindings" {
					consumerLocks++
				}
			}
			Expect(consumerLocks).To(Equal(1),
				"Hangar acquired a lock on a consumer table; it never acquires a consumer-domain row")

			// And the consumer row nobody locked is still free while this
			// transaction holds every lock the claim needed.
			probe, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(probe)
			_, err = probe.Exec(
				`SELECT 1 FROM opaque_consumer_bindings WHERE binding_id = $1 FOR UPDATE NOWAIT`,
				"binding-2")
			Expect(err).NotTo(HaveOccurred(),
				"Hangar reached a consumer row the consumer never locked")
			Expect(probe.Rollback()).To(Succeed())

			Expect(tx.Rollback()).To(Succeed())
		})
	})

	// Green: the product-neutral consumer that proves the seam composes.
	Describe("an opaque consumer", func() {
		var ref hangar.TreeRef

		BeforeEach(func() {
			activate()
			_, ref = publish(hangarDigest(12), 1725830823000012)

			_, err := dbConn.Exec(`
				CREATE TABLE opaque_consumer_bindings (
					binding_id text PRIMARY KEY,
					visibility text NOT NULL,
					claim_id   uuid
				)`)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				_, err := dbConn.Exec(`DROP TABLE IF EXISTS opaque_consumer_bindings`)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		binding := func(id string) (string, string) {
			GinkgoHelper()
			var visibility, claim string
			Expect(dbConn.QueryRow(`
				SELECT visibility, coalesce(claim_id::text, '')
				FROM opaque_consumer_bindings WHERE binding_id = $1`, id).
				Scan(&visibility, &claim)).To(Succeed())

			return visibility, claim
		}

		It("binds and acquires in one transaction, republishes without reacquiring, and unbinds beside the release", func() {
			claimID := output.ClaimID(uuid.NewString())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-1', 'hidden', $1)`, string(claimID))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquire(tx, claimID, ref, "binding-1")).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			visibility, claim := binding("binding-1")
			Expect(visibility).To(Equal("hidden"))
			Expect(claim).To(Equal(string(claimID)))
			Expect(countActiveClaims(ref)).To(Equal(1))

			// Hidden to published, with no second acquire: the consumer keeps
			// the id, and Hangar neither replaces nor reacquires it.
			tx, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				UPDATE opaque_consumer_bindings SET visibility = 'published' WHERE binding_id = 'binding-1'`)
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			visibility, claim = binding("binding-1")
			Expect(visibility).To(Equal("published"))
			Expect(claim).To(Equal(string(claimID)))
			Expect(countActiveClaims(ref)).To(Equal(1))

			// Unbind and release together: neither a dangling visible binding
			// nor an indefinitely leaked claim is a valid crash outcome.
			tx, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				UPDATE opaque_consumer_bindings SET visibility = 'unusable' WHERE binding_id = 'binding-1'`)
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			visibility, _ = binding("binding-1")
			Expect(visibility).To(Equal("unusable"))
			Expect(countActiveClaims(ref)).To(BeZero())
		})

		It("leaves neither half visible when the transaction is forced to roll back", func() {
			claimID := output.ClaimID(uuid.NewString())

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = tx.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-2', 'hidden', $1)`, string(claimID))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquire(tx, claimID, ref, "binding-2")).To(Succeed())

			// The injected failure: the consumer's own write fails after both
			// halves are in the transaction.
			_, err = tx.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility) VALUES ('binding-2', 'hidden')`)
			Expect(err).To(HaveOccurred())
			Expect(tx.Rollback()).To(Succeed())

			var bindings int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM opaque_consumer_bindings`).
				Scan(&bindings)).To(Succeed())
			Expect(bindings).To(BeZero(), "the consumer's binding survived a rolled-back transaction")
			Expect(countActiveClaims(ref)).To(BeZero(),
				"the Hangar claim survived a rolled-back transaction")

			var tombstones int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE claim_id = $1`,
				string(claimID)).Scan(&tombstones)).To(Succeed())
			Expect(tombstones).To(BeZero())
		})

		// ROLLBACK AT EVERY STATEMENT, not at the one an author happened to
		// think of.
		//
		// The spec above injects its fault after both halves are already in the
		// transaction, which is the easy half: a composition that only ever
		// failed there could still leave a claim behind when it broke in the
		// middle of the acquire. So this counts the statements the WHOLE
		// composition runs -- the consumer's own writes and every statement
		// inside AcquireClaim -- and then runs it once per statement, aborting
		// AT that statement. Neither half may be visible afterwards, at any
		// index.
		//
		// The count is discovered rather than written down: a number in the
		// spec would go stale the first time the repository grew a statement,
		// and the spec would keep passing over the shorter prefix it knew.
		It("leaves neither half visible when it is rolled back at any statement", func() {
			claimID := output.ClaimID(uuid.NewString())

			compose := func(counter *countingTx, id output.ClaimID, binding string) error {
				if _, err := counter.ExecContext(ctx, `
					INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
					VALUES ($1, 'hidden', $2)`, binding, string(id)); err != nil {
					return err
				}
				if _, err := repository.AcquireClaim(ctx, counter, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           id,
					Ref:               ref,
					ConsumerBindingID: output.OpaqueID(binding),
					RequestedAt:       output.NewTimestamp(time.Now()),
				}); err != nil {
					return err
				}
				_, err := counter.ExecContext(ctx, `
					UPDATE opaque_consumer_bindings SET visibility = 'published' WHERE binding_id = $1`,
					binding)

				return err
			}

			// One clean run, to learn how many statements there are.
			var total int
			func() {
				tx, err := dbConn.Begin()
				Expect(err).NotTo(HaveOccurred())
				defer db.Rollback(tx)

				counter := &countingTx{inner: tx}
				Expect(compose(counter, claimID, "binding-count")).To(Succeed())
				total = counter.count
			}()
			Expect(total).To(BeNumerically(">=", 4),
				"the composition runs too few statements for this spec to be saying anything")

			for at := 1; at <= total; at++ {
				id := output.ClaimID(uuid.NewString())
				binding := fmt.Sprintf("binding-at-%d", at)

				// The transaction is closed by a defer inside its own scope.
				// A failed assertion aborts the spec, and an aborted spec that
				// left this transaction open would hold a lock on the
				// consumer's own table until the cleanup DROP blocked on it --
				// a spec that reported a hang rather than a failure.
				func() {
					tx, err := dbConn.Begin()
					Expect(err).NotTo(HaveOccurred())
					defer db.Rollback(tx)

					counter := &countingTx{inner: tx, failAt: at}
					err = compose(counter, id, binding)
					Expect(err).To(HaveOccurred(),
						"statement %d was injected with a fault and the composition still succeeded", at)
					Expect(err.Error()).To(ContainSubstring("injected"),
						"statement %d failed for a reason this spec did not cause: %v", at, err)
				}()

				var bindings int
				Expect(dbConn.QueryRow(
					`SELECT count(*) FROM opaque_consumer_bindings WHERE binding_id = $1`, binding).
					Scan(&bindings)).To(Succeed())
				Expect(bindings).To(BeZero(),
					"the consumer's binding survived a rollback at statement %d", at)

				var claims int
				Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_claims WHERE claim_id = $1`,
					string(id)).Scan(&claims)).To(Succeed())
				Expect(claims).To(BeZero(),
					"a Hangar claim -- active or tombstoned -- survived a rollback at statement %d", at)
			}

			Expect(countActiveClaims(ref)).To(BeZero())
		})

		// The arrival inversion, with a consumer's own binding in it.
		//
		// The claimant-versus-reclaimer specs above prove which side wins. This
		// proves the thing they cannot say: that
		// the loser leaves no DANGLING BINDING. A consumer whose Hangar half
		// failed and whose own half committed would have published a reference
		// to content nothing protects, which is exactly the outcome the shared
		// transaction exists to make impossible.
		It("leaves no dangling binding whichever of the claimant and the reclaimer arrives first", func() {
			// Reclaimer first: the consumer loses, and takes its own binding
			// down with it.
			reclaimer, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(reclaimer)
			Expect(reclaim(reclaimer, ref)).To(Succeed())
			Expect(reclaimer.Commit()).To(Succeed())

			loser, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(loser)
			lateID := output.ClaimID(uuid.NewString())
			_, err = loser.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-late', 'hidden', $1)`, string(lateID))
			Expect(err).NotTo(HaveOccurred())
			err = acquire(loser, lateID, ref, "binding-late")
			Expect(err).To(MatchError(output.ErrNotFound))
			Expect(err.Error()).To(ContainSubstring("reclaimed"))
			Expect(loser.Rollback()).To(Succeed())

			var dangling int
			Expect(dbConn.QueryRow(
				`SELECT count(*) FROM opaque_consumer_bindings WHERE binding_id = 'binding-late'`).
				Scan(&dangling)).To(Succeed())
			Expect(dangling).To(BeZero(),
				"the consumer's binding survived a claim the reclaimer had already won")
			Expect(reclaimed(ref)).To(BeTrue())

			// Claimant first, on a second generation: the consumer wins, its
			// binding is there, and the reclaimer rechecks under the lock and
			// skips.
			_, second := publish(hangarDigest(21), 1725830823000021)

			winner, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(winner)
			earlyID := output.ClaimID(uuid.NewString())
			_, err = winner.Exec(`
				INSERT INTO opaque_consumer_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-early', 'hidden', $1)`, string(earlyID))
			Expect(err).NotTo(HaveOccurred())
			Expect(acquire(winner, earlyID, second, "binding-early")).To(Succeed())
			Expect(winner.Commit()).To(Succeed())

			late, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(late)
			err = reclaim(late, second)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 live claim(s)"))
			Expect(late.Rollback()).To(Succeed())

			visibility, claim := binding("binding-early")
			Expect(visibility).To(Equal("hidden"))
			Expect(claim).To(Equal(string(earlyID)))
			Expect(countActiveClaims(second)).To(Equal(1))
			Expect(reclaimed(second)).To(BeFalse())
		})

		It("refuses the consumer's own prefix being skipped", func() {
			_, err := db.HangarConsumerPrefixHeld("   ")
			Expect(err).To(MatchError(output.ErrIncomplete))

			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			_, err = db.LockHangarSuffix(ctx, tx, db.HangarConsumerPrefix{}, db.HangarLockRequest{
				Exact: []hangar.TreeRef{ref},
			})
			Expect(err).To(MatchError(output.ErrIncomplete))
			Expect(err.Error()).To(ContainSubstring("no consumer prefix token"))
		})
	})
})

// hangarDigest is a distinct valid sha256 digest per test.
func hangarDigest(n int) hangar.Digest {
	return hangar.Digest(fmt.Sprintf("sha256:%064d", n))
}

// hangarLockedRelations names every relation this transaction currently holds a
// row-level or table-level lock on.
func hangarLockedRelations(tx db.Tx) []string {
	GinkgoHelper()

	rows, err := tx.Query(`
		SELECT c.relname
		FROM pg_locks l
		JOIN pg_class c ON c.oid = l.relation
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE l.pid = pg_backend_pid()
		  AND n.nspname = 'public'
		  AND l.granted`)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()

	var relations []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		relations = append(relations, name)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())

	return relations
}

// countingTx counts the statements a composition runs, and can fail at any one
// of them.
//
// It is the only honest way to say "rollback at EVERY statement": the
// composition's statements are not all the spec's -- most of them are inside
// AcquireClaim -- so a spec that injected its fault between the calls it can
// see would be asserting about three boundaries out of a dozen. Counting first
// and injecting by index makes the assertion cover whatever the repository
// currently does, and grow with it.
//
// The error is a plain one on purpose: what the spec checks is that the
// composition fails and leaves nothing, not that a particular sentinel came
// back out.
type countingTx struct {
	inner  db.Tx
	count  int
	failAt int
}

func (tx *countingTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.count++
	if tx.failAt == tx.count {
		return nil, fmt.Errorf("injected fault at statement %d", tx.count)
	}

	return tx.inner.ExecContext(ctx, query, args...)
}

func (tx *countingTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.count++
	if tx.failAt == tx.count {
		return nil, fmt.Errorf("injected fault at statement %d", tx.count)
	}

	return tx.inner.QueryContext(ctx, query, args...)
}

var _ output.Tx = (*countingTx)(nil)
