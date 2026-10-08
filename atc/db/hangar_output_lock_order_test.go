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

	Describe("a read-lease renewal against a reclaim admission", func() {
		// F2. The renewal took class 3 alone and AdmitReclaim takes classes 1
		// and 2, so the two took DISJOINT lock sets and nothing serialized
		// them. The deferred hangar_reclaim_exclusion trigger is a snapshot
		// read, not a mutex: with the renewal still uncommitted, the
		// admission's constraint phase saw no live lease and both committed --
		// a generation admitted to reclamation with a renewed read lease over
		// it, which is forbidden.
		//
		// What is asserted is the blocking, not only the outcome: a spec that
		// merely committed one then the other would pass against the broken
		// code, which is how this survived ten phases.
		It("blocks the admission on the renewal's own lock rather than on a deferred trigger", func() {
			digest := hangarDigest(63)
			capture, ref := hangarPublish(ctx, repository, digest, 1725830823000063)
			Expect(capture.Validate()).To(Succeed())
			hangarAgePublication(ref, hangarGraceElapsed)

			claimID := output.ClaimID(uuid.NewString())
			claiming, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(claiming)
			Expect(repository.AcquireClaim(ctx, claiming, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           claimID,
				Ref:               ref,
				ConsumerBindingID: output.OpaqueID("binding-lock-order"),
				RequestedAt:       output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(db.HangarOutputTx{Tx: claiming}.Commit()).To(Succeed())

			leaseID := output.ReadLeaseID(uuid.NewString())
			granting, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(granting)
			lease, err := repository.AcquireReadLease(ctx, granting,
				hangarReadLeaseRequest(leaseID, claimID, ref))
			Expect(err).NotTo(HaveOccurred())
			Expect(db.HangarOutputTx{Tx: granting}.Commit()).To(Succeed())

			// The consumer released its last claim during the transfer, so
			// the read lease is the only protection left.
			releasing, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(releasing)
			Expect(repository.ReleaseClaim(ctx, releasing, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
			Expect(db.HangarOutputTx{Tx: releasing}.Commit()).To(Succeed())

			// The renewal: open, admitted, holding whatever it holds, and not
			// committed.
			renewing := second()
			renewal, err := renewing.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(renewal)
			renewed, err := repository.RenewReadLease(ctx, renewal, lease)
			Expect(err).NotTo(HaveOccurred())
			Expect(renewed.ExpiresAt.After(lease.ExpiresAt.Time) ||
				renewed.ExpiresAt.Equal(lease.ExpiresAt.Time)).To(BeTrue())

			admitted := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					admitted <- err

					return
				}
				defer db.Rollback(tx)
				if err := repository.AdmitReclaim(ctx, tx, ref, uuid.NewString(), 1,
					output.MinLeaseTerm, output.DefaultPublicationGrace); err != nil {
					admitted <- err

					return
				}
				admitted <- db.HangarOutputTx{Tx: tx}.Commit()
			}()

			Consistently(admitted, time.Second, 50*time.Millisecond).ShouldNot(Receive(),
				"the admission answered without ever meeting a row the open renewal holds, so "+
					"the only thing between a live reader and a reclaimer is a deferred "+
					"trigger -- and a deferred trigger is a snapshot read, not a mutex")

			Expect(renewal.Commit()).To(Succeed())

			var refusal error
			Eventually(admitted, 30*time.Second).Should(Receive(&refusal))
			Expect(refusal).To(MatchError(output.ErrConflict),
				"a generation was admitted to reclamation with a renewed read lease over it")
			Expect(refusal.Error()).To(ContainSubstring("read lease"))

			var state string
			Expect(dbConn.QueryRow(`
				SELECT state FROM hangar_exact_lifecycles
				WHERE scope = $1 AND digest = $2 AND generation = $3`,
				string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&state)).To(Succeed())
			Expect(state).To(Equal("registered"))

			var live int
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM hangar_read_leases
				WHERE read_lease_id = $1 AND released_at IS NULL AND expires_at > now()`,
				string(leaseID)).Scan(&live)).To(Succeed())
			Expect(live).To(Equal(1))
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
