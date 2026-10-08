package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The lock order, taken by two connections that really meet.
//
// Every other suite in this package asserts the order by reading a single
// transaction's statements in sequence. That cannot see an ABBA: an inversion
// is only an inversion relative to somebody else, and the somebody else has to
// be open at the time. Each spec here opens a second connection, holds one
// class on it, and asserts that the writer under test WAITS rather than
// deadlocking or sailing past -- so an implementation that reached the same
// committed state by taking the classes the other way round is red here and
// green everywhere else.
//
// The counterparty is always spelled with LockHangarSuffix itself, and always
// with the same request field a production writer uses, so that the holder is
// the real order and not this file's idea of it.
var _ = Describe("the Hangar lock order under two connections", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		consumer   db.HangarConsumerPrefix
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Two racing transactions plus a third for the NOWAIT probes.
		dbConn.SetMaxOpenConns(4)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		var err error
		consumer, err = db.HangarConsumerPrefixHeld("lock-order-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	// second is a connection of its own. The suite runs on one pooled
	// connection precisely so that code needing a second one deadlocks
	// visibly; these specs need the second one on purpose.
	second := func() db.DbConn {
		GinkgoHelper()
		conn := postgresRunner.OpenConn()
		DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })

		return conn
	}

	deadline := func() time.Duration { return output.DefaultCaptureDeadline }

	// holdLogical takes class 1 for one correlation on its own connection and
	// leaves it held, which is exactly where CASPublishingToPublished is
	// between its first suffix statement and its capture-row statement.
	holdLogical := func(key db.HangarLogicalKey) db.Tx {
		GinkgoHelper()

		conn := second()
		tx, err := conn.Begin()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { db.Rollback(tx) })

		_, err = db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
			Logical: []db.HangarLogicalKey{key},
		})
		Expect(err).NotTo(HaveOccurred())

		return tx
	}

	// takeCaptureClass is the publisher's capture-row statement: the row of
	// the same capture, reached while it still holds class 1. A writer that
	// took the row first and is now reaching back for class 1 turns this into
	// a cycle, and PostgreSQL says so.
	takeCaptureClass := func(tx db.Tx, key output.CaptureKey) error {
		_, err := db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
			CaptureRows: []output.CaptureKey{key},
		})

		return err
	}

	Describe("a terminal capture failure against a publisher holding the logical row", func() {
		// F1. A failure that took the capture row and then reached back for
		// the logical class would be an ABBA against a publisher holding class
		// 1 and reaching for the row. MarkFailed takes the row alone, and the
		// row is one of the rows class 1 holds, so it waits.
		It("waits for the logical row instead of deadlocking against the publisher", func() {
			digest := hangarDigest(61)
			capture := hangarReserve(ctx, repository, digest, deadline())
			key := db.HangarLogicalKey{Scope: "team-a", Digest: digest}

			publisher := holdLogical(key)

			done := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					done <- err

					return
				}
				defer db.Rollback(tx)
				if _, err := repository.MarkFailed(ctx, tx, capture.Key, "seal_unconfirmed"); err != nil {
					done <- err

					return
				}
				done <- tx.Commit()
			}()

			Consistently(done, time.Second, 50*time.Millisecond).ShouldNot(Receive(),
				"the terminal failure finished without ever meeting the capture row the "+
					"publisher's logical class holds")

			// The publisher now reaches for the capture row, which is what it
			// does inside its own suffix.
			Expect(takeCaptureClass(publisher, capture.Key)).To(Succeed(),
				"the publisher's capture-row acquisition deadlocked; the terminal failure holds "+
					"the row and is reaching back for class 1, which is the ABBA the lock "+
					"order exists to forbid")
			Expect(publisher.Commit()).To(Succeed())

			var failure error
			Eventually(done, 30*time.Second).Should(Receive(&failure))
			Expect(failure).NotTo(HaveOccurred())

			var state string
			Expect(dbConn.QueryRow(`
				SELECT state FROM hangar_captures WHERE execution_id = $1 AND output_name = $2`,
				string(capture.Key.ExecutionID), string(capture.Key.Output)).Scan(&state)).To(Succeed())
			Expect(state).To(Equal("failed"))
		})
	})

	Describe("a claim acquisition against the reclaim pass holding the generation", func() {
		// F2. The pass holds the tree and the lifecycle row across its store
		// delete and stamps the row in the same transaction. A claimant that
		// arrives meanwhile takes the same exact-lifecycle lock, so it WAITS
		// on the pass and then finds the generation reclaimed -- never a
		// claim on an object the pass is deleting. The reverse order is the
		// claimant holding the row and the pass waiting, then rechecking and
		// deferring.
		//
		// What is asserted is the blocking, not only the outcome: a spec that
		// merely committed one then the other would pass against code that
		// took disjoint lock sets, which is how a deferred-trigger version of
		// this survived ten phases.
		acquire := func(tx db.Tx, ref hangar.TreeRef) error {
			return hangarAcquireClaim(ctx, repository, tx, output.ClaimID(uuid.NewString()), ref,
				"binding-lock-order")
		}

		It("blocks the claimant on the lifecycle row until the pass commits, and then refuses it", func() {
			digest := hangarDigest(63)
			_, ref := hangarPublish(ctx, repository, digest, 1725830823000063)
			hangarAgePublication(ref, hangarGraceElapsed)

			// The pass: holding the generation, between its hold and its
			// stamp, and not committed.
			passing := second()
			pass, err := passing.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(pass)
			Expect(repository.HoldForReclaim(ctx, pass, ref, output.DefaultPublicationGrace)).To(Succeed())

			claimed := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					claimed <- err

					return
				}
				defer db.Rollback(tx)
				if err := acquire(tx, ref); err != nil {
					claimed <- err

					return
				}
				claimed <- db.HangarOutputTx{Tx: tx}.Commit()
			}()

			Consistently(claimed, time.Second, 50*time.Millisecond).ShouldNot(Receive(),
				"the claimant answered without ever meeting a row the open pass holds, so "+
					"a claim can be taken on a generation whose delete is in flight")

			Expect(repository.StampReclaimed(ctx, pass, ref)).To(Succeed())
			Expect(pass.Commit()).To(Succeed())

			var refusal error
			Eventually(claimed, 30*time.Second).Should(Receive(&refusal))
			Expect(refusal).To(MatchError(output.ErrNotFound),
				"a claim was taken on a generation the pass had already reclaimed")
			Expect(refusal.Error()).To(ContainSubstring("reclaimed"))

			var claims int
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM hangar_claims c
				JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
				WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3 AND c.released_at IS NULL`,
				string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&claims)).To(Succeed())
			Expect(claims).To(BeZero())
		})

		It("blocks the pass on the lifecycle row until the claimant commits, and then defers it", func() {
			digest := hangarDigest(67)
			_, ref := hangarPublish(ctx, repository, digest, 1725830823000067)
			hangarAgePublication(ref, hangarGraceElapsed)

			claiming := second()
			claimant, err := claiming.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claimant)
			Expect(acquire(claimant, ref)).To(Succeed())

			held := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					held <- err

					return
				}
				defer db.Rollback(tx)
				held <- repository.HoldForReclaim(ctx, tx, ref, output.DefaultPublicationGrace)
			}()

			Consistently(held, time.Second, 50*time.Millisecond).ShouldNot(Receive(),
				"the pass held the generation without ever meeting the row the open claimant "+
					"holds, so its recheck read a snapshot and not the claim")

			Expect(db.HangarOutputTx{Tx: claimant}.Commit()).To(Succeed())

			var refusal error
			Eventually(held, 30*time.Second).Should(Receive(&refusal))
			Expect(refusal).To(MatchError(output.ErrConflict),
				"the pass held a generation beside a live claim")
			Expect(refusal.Error()).To(ContainSubstring("1 live claim(s)"))

			var reclaimed bool
			Expect(dbConn.QueryRow(`
				SELECT reclaimed_at IS NOT NULL FROM hangar_exact_lifecycles
				WHERE scope = $1 AND digest = $2 AND generation = $3`,
				string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&reclaimed)).To(Succeed())
			Expect(reclaimed).To(BeFalse())
		})
	})

	Describe("the capture class of the suffix", func() {
		// F3. The capture-row statement (class 1, after the correlations) was
		// once pinned by nothing: deleting `FOR NO KEY UPDATE` from it left 138
		// specs green. This is the NOWAIT arm the lock-order specs already use
		// for the correlation and exact classes, applied to a capture row named
		// by key: a holder takes the row, and a third connection asks whether
		// it is really held.
		It("really holds the capture row it says it locked", func() {
			digest := hangarDigest(64)
			capture := hangarReserve(ctx, repository, digest, deadline())

			const probeCapture = `
				SELECT 1 FROM hangar_captures
				WHERE execution_id = $1 AND output_name = $2
				FOR NO KEY UPDATE NOWAIT`
			probe := func() error {
				tx, err := dbConn.Begin()
				if err != nil {
					return err
				}
				defer db.Rollback(tx)
				_, err = tx.Exec(probeCapture, string(capture.Key.ExecutionID), string(capture.Key.Output))

				return err
			}

			// Nobody holds it yet, so a failure below is the holder's doing.
			Expect(probe()).To(Succeed())

			holding := second()
			holder, err := holding.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(holder)
			_, err = db.LockHangarSuffix(ctx, holder, consumer, db.HangarLockRequest{
				CaptureRows: []output.CaptureKey{capture.Key},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(probe()).To(MatchError(ContainSubstring("55P03")),
				"the capture-row statement returned without taking the row lock it names, so the "+
					"capture rows of the suffix are a comment")

			Expect(holder.Rollback()).To(Succeed())
			Expect(probe()).To(Succeed())
		})

		// And the ordering the class buys, stated as an input-order property
		// the way classes 1 and 2 are: the helper sorts, so two callers handed
		// overlapping batches the other way round take them the same way round.
		It("locks capture rows in sorted order when the batch arrives reversed", func() {
			first := hangarReserve(ctx, repository, hangarDigest(65), deadline())
			other := hangarReserve(ctx, repository, hangarDigest(66), deadline())
			low, high := first.Key, other.Key
			if high.String() < low.String() {
				low, high = high, low
			}

			const lockCapture = `
				SELECT 1 FROM hangar_captures
				WHERE execution_id = $1 AND output_name = $2
				FOR NO KEY UPDATE`
			probeFirst := func() error {
				tx, err := dbConn.Begin()
				if err != nil {
					return err
				}
				defer db.Rollback(tx)
				_, err = tx.Exec(lockCapture+" NOWAIT", string(low.ExecutionID), string(low.Output))

				return err
			}
			Expect(probeFirst()).To(Succeed())

			holding := second()
			holder, err := holding.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(holder)
			_, err = holder.Exec(lockCapture, string(high.ExecutionID), string(high.Output))
			Expect(err).NotTo(HaveOccurred())

			done := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					done <- err

					return
				}
				defer db.Rollback(tx)
				_, err = db.LockHangarSuffix(ctx, tx, consumer, db.HangarLockRequest{
					CaptureRows: []output.CaptureKey{high, high, low},
				})
				done <- err
			}()

			Eventually(probeFirst, 10*time.Second, 50*time.Millisecond).Should(
				MatchError(ContainSubstring("55P03")),
				"the capture that sorts first was never locked while the helper blocked on "+
					"the one that sorts second, so the helper took the batch as handed")

			Expect(holder.Rollback()).To(Succeed())
			var completed error
			Eventually(done, 10*time.Second).Should(Receive(&completed))
			Expect(completed).NotTo(HaveOccurred())
		})
	})
})
