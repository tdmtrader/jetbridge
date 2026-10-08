package db_test

// Phase 9's integrated acceptance suite, and what it deliberately does NOT
// contain.
//
// The plan's rule for this phase is that no acceptance task restates a contract
// a Phase 2-8 scenario already pins; where it would, it cites instead. Applied
// honestly that removes most of what this box originally named, because those
// contracts are pinned and the citations are exact:
//
//   - The capture row's own transitions -- pending, publishing, published,
//     discarded, failed, released: hangar_capture_test.go.
//   - Claim acquire/release in a caller transaction, forced rollback leaving
//     neither half visible, and the same claim ID surviving a hidden-to-
//     published transition: `features/hangar-binding.feature`, plus
//     hangar_output_test.go:2758-3040 over the `opaque_consumer_bindings`
//     product-neutral consumer.
//   - Claimant/reader/reclaimer inversions: hangar_output_test.go:129-310.
//   - The opposite-input-order batch: hangar_output_test.go:2167-2420, which
//     uses a blocking holder and a NOWAIT probe because the obvious form --
//     two goroutines and "neither deadlocked" -- passed with the sorting
//     removed.
//   - Policy at-risk stopping each of the five admissions:
//     hangar_output_policy_test.go:118-190 and hangar_output_test.go:544.
//
// What is left is the composition, and the survey of this package found it
// genuinely unwritten: every leg of the plane is proved, and nothing proves that
// the state one leg COMMITS is the state the next leg's production code reads.
// That is the failure mode an integrated suite exists for -- each unit spec
// arranges its own starting state, so a leg can be individually correct and
// collectively unreachable.
//
// Two specs, therefore, and both of them span seams no other spec spans:
//
//  1. One capture from its pending row to a confirmed reclamation, with nothing
//     staged between legs: whatever `CASPublishingToPublished` committed is what
//     `AcquireClaim` is given, whatever that committed is what `AcquireReadLease`
//     is given, and so on to `FinalizeReclaim`.
//  2. The drain's residue count over state the PRODUCTION capture path
//     produced: that the rows a real capture writes are the rows the count
//     sees. A count that missed a class would silently permit removing a
//     daemon that strands live objects.

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

var _ = Describe("the Hangar output plane, end to end", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	const (
		acceptanceEpoch = executioncontrol.ActivationEpoch(1)
		deleteTimeout   = 2 * time.Minute
	)

	BeforeEach(func() {
		ctx = context.Background()

		// The suite runs on one pooled connection so that code needing a second
		// deadlocks visibly. These specs need two, and the reason is
		// `hangarReadLeaseRequest` (hangar_output_fixture_test.go:350): it does
		// a bare `dbConn.QueryRow` for the marker's reservation WHILE the
		// caller's transaction is open, which is exactly the second connection
		// the default is there to surface. It goes back to one afterwards.
		dbConn.SetMaxOpenConns(2)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("acceptance-consumer")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

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

	lifecycleStateOf := func(ref hangar.TreeRef) string {
		GinkgoHelper()
		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM hangar_exact_lifecycles
			WHERE scope = $1 AND digest = $2 AND generation = $3`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&state)).To(Succeed())

		return state
	}

	It("carries one capture from its pending row to a confirmed reclamation, each leg reading what the last one committed", func() {
		digest := hangarDigest(91)
		generation := int64(1725830823000091)

		// --- capture -------------------------------------------------------
		//
		// hangarPublishAt drives the capture row from pending through
		// publishing to published through the repository. Nothing here writes
		// a row itself: a fixture that did would be proving the schema twice
		// and the composition not at all.
		capture := hangarPublishAt(ctx, repository, digest, generation,
			output.DefaultCaptureDeadline)

		Expect(lifecycleStateOf(capture.Ref)).To(Equal("registered"))

		// Publication is what makes the exact generation readable. Before the
		// source is released the capture is not settled, and that is a state
		// the plane sits in on purpose.
		var (
			captureState string
			releasedAt   sql.NullTime
		)
		// Two columns, scanned separately. Collapsing them into one boolean and
		// asserting it false would also pass if `state` had drifted to anything
		// other than `published` -- i.e. a regression in the publish path
		// would satisfy the assertion that is supposed to be about the release.
		Expect(dbConn.QueryRow(`
			SELECT state, released_at
			  FROM hangar_captures WHERE execution_id = $1 AND output_name = $2`,
			string(capture.Key.ExecutionID), string(capture.Key.Output)).
			Scan(&captureState, &releasedAt)).To(Succeed())
		Expect(captureState).To(Equal("published"))
		Expect(releasedAt.Valid).To(BeFalse(),
			"the capture reported its source released before it was; the source must be "+
				"released and not only the decision taken")

		hangarReleaseSource(ctx, repository, capture)

		// The publication took the capture's own claim; the consumer below
		// takes its own, and the capture's is given back the way a Run that
		// did not select this output gives it back.
		hangarReleaseCaptureClaim(ctx, repository, capture)

		// --- the consumer binds and claims, in ONE transaction --------------
		//
		// The consumer is product-neutral and opaque: a binding id and a
		// visibility, and Hangar learns nothing else about it. Both halves
		// commit together, so the claim is acquired inside the consumer's own
		// transaction rather than beside it.
		_, err := dbConn.Exec(`
			CREATE TABLE acceptance_bindings (
				binding_id text PRIMARY KEY,
				visibility text NOT NULL,
				claim_id   text)`)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, err := dbConn.Exec(`DROP TABLE IF EXISTS acceptance_bindings`)
			Expect(err).NotTo(HaveOccurred())
		})

		claimID := output.ClaimID(uuid.NewString())
		in(func(tx db.HangarOutputTx) {
			_, err := tx.Exec(`
				INSERT INTO acceptance_bindings (binding_id, visibility, claim_id)
				VALUES ('binding-1', 'hidden', $1)`, string(claimID))
			Expect(err).NotTo(HaveOccurred())

			Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           claimID,
				Ref:               capture.Ref,
				ConsumerBindingID: output.OpaqueID("binding-1"),
				RequestedAt:       output.NewTimestamp(time.Now()),
			})).To(Succeed())
		})

		// --- the reader takes a lease under the same claim ------------------
		//
		// The lease request carries a stat proof, and the fixture reads the
		// reservation id back out of the row the capture above wrote rather
		// than inventing one. That is the seam: a marker the publisher did not
		// write would not match, and only a real chain produces a matching one.
		var lease output.ReadLease
		in(func(tx db.HangarOutputTx) {
			var err error
			lease, err = repository.AcquireReadLease(ctx, tx,
				hangarReadLeaseRequest(output.ReadLeaseID(uuid.NewString()), claimID, capture.Ref))
			Expect(err).NotTo(HaveOccurred())
		})

		// --- the reclaimer is refused, twice, for two different reasons -----
		//
		// Grace has not elapsed yet, so the first refusal is grace. Age the
		// publication and the refusal becomes the claim; release the claim and
		// it becomes the lease. Three refusals over one ref, in order, is what
		// says the preconditions are independent rather than one check wearing
		// three messages.
		hangarAgeCapture(capture, 48*time.Hour)

		reclaimRefusal := func() error {
			tx := begin()
			defer db.Rollback(tx)

			return repository.AdmitReclaim(ctx, tx, capture.Ref, uuid.NewString(), 1,
				output.LeaseTermFor(deleteTimeout), output.DefaultPublicationGrace)
		}

		err = reclaimRefusal()
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(err.Error()).To(ContainSubstring("grace"))

		hangarAgePublication(capture.Ref, hangarGraceElapsed)

		err = reclaimRefusal()
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(err.Error()).To(ContainSubstring("claim"))

		// --- the consumer unbinds and releases, in ONE transaction ----------
		in(func(tx db.HangarOutputTx) {
			_, err := tx.Exec(`
				UPDATE acceptance_bindings SET visibility = 'unusable' WHERE binding_id = 'binding-1'`)
			Expect(err).NotTo(HaveOccurred())

			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             capture.Ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
		})

		// Releasing the LAST claim during a transfer cannot delete until the
		// read lease closes. The claim is gone and the reader is still
		// holding, so the refusal must now be the lease.
		err = reclaimRefusal()
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(err.Error()).To(ContainSubstring("read lease"))

		in(func(tx db.HangarOutputTx) {
			Expect(repository.ReleaseReadLease(ctx, tx, lease)).To(Succeed())
		})

		// --- reclaim, and only now -----------------------------------------
		var job db.HangarReclaimJob
		in(func(tx db.HangarOutputTx) {
			Expect(repository.AdmitReclaim(ctx, tx, capture.Ref, uuid.NewString(), 1,
				output.LeaseTermFor(deleteTimeout), output.DefaultPublicationGrace)).To(Succeed())
		})
		Expect(lifecycleStateOf(capture.Ref)).To(Equal("reclaiming"),
			"admission must mark the generation `reclaiming` durably BEFORE any external delete")

		in(func(tx db.HangarOutputTx) {
			var err error
			job, err = repository.LoadReclaimJob(ctx, tx, capture.Ref)
			Expect(err).NotTo(HaveOccurred())
		})

		var attempt int64
		in(func(tx db.HangarOutputTx) {
			var err error
			attempt, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
			Expect(err).NotTo(HaveOccurred())
		})
		in(func(tx db.HangarOutputTx) {
			Expect(repository.RecordDeleteOutcome(ctx, tx, job, attempt,
				output.DeleteConfirmed)).To(Succeed())
			Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)).
				To(Succeed())
		})

		Expect(lifecycleStateOf(capture.Ref)).To(Equal("reclaimed_confirmed"))

		// --- and the far end of the chain holds ----------------------------
		//
		// The released claim stays tombstoned for the lifetime of the tree-ref
		// record, and a caller cannot re-acquire on a reclaimed ref. Both are
		// read from the state the legs above committed, not from a row this
		// spec wrote.
		var claims []output.ClaimRecord
		in(func(tx db.HangarOutputTx) {
			var err error
			claims, err = repository.ReadClaims(ctx, tx, capture.Ref)
			Expect(err).NotTo(HaveOccurred())
		})
		Expect(claims).To(HaveLen(2),
			"the released claim identities -- the capture's and the consumer's -- must remain "+
				"tombstoned; a purged tombstone is a claim id that can silently reactivate")
		ids := []output.ClaimID{}
		for _, claim := range claims {
			ids = append(ids, claim.ClaimID)
			Expect(claim.Active()).To(BeFalse(),
				"claim %s is still active after it was released", claim.ClaimID)
		}
		Expect(ids).To(ConsistOf(claimID, capture.Key.ClaimID()))

		tx := begin()
		defer db.Rollback(tx)
		err = repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           output.ClaimID(uuid.NewString()),
			Ref:               capture.Ref,
			ConsumerBindingID: output.OpaqueID("binding-2"),
			RequestedAt:       output.NewTimestamp(time.Now()),
		})
		Expect(err).To(MatchError(output.ErrConflict))
		Expect(tx.Rollback()).To(Succeed())
	})

	It("counts the residue the production capture path leaves, and reaches zero once it is released and reclaimed", func() {
		// The drain is: take the plane out of service, then wait for the
		// residue count to reach zero. This asserts that the rows a real
		// capture-and-claim writes are the rows that count sees: a class the
		// count forgot would let a daemon be removed under live work.
		residue := func() output.PlaneCounts {
			GinkgoHelper()
			var counts output.PlaneCounts
			in(func(tx db.HangarOutputTx) {
				var err error
				counts, err = repository.CountOutputPlaneState(ctx, tx)
				Expect(err).NotTo(HaveOccurred())
			})
			return counts
		}

		digest := hangarDigest(92)
		capture := hangarPublishAt(ctx, repository, digest, 1725830823000092,
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)

		claimID := output.ClaimID(uuid.NewString())
		in(func(tx db.HangarOutputTx) {
			Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           claimID,
				Ref:               capture.Ref,
				ConsumerBindingID: output.OpaqueID("binding-1"),
				RequestedAt:       output.NewTimestamp(time.Now()),
			})).To(Succeed())
		})

		counts := residue()
		Expect(counts.LiveGenerations).To(Equal(1))
		Expect(counts.OpenClaims).To(Equal(1))
		Expect(counts.UnreleasedCaptures).To(Equal(1),
			"a published capture whose step marker the node has not released is residue")
		Expect(counts.Residue()).To(BeNumerically(">", 0))

		in(func(tx db.HangarOutputTx) {
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             capture.Ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
		})
		hangarReleaseSource(ctx, repository, capture)
		hangarAgeCapture(capture, 48*time.Hour)
		hangarAgePublication(capture.Ref, hangarGraceElapsed)

		var job db.HangarReclaimJob
		in(func(tx db.HangarOutputTx) {
			Expect(repository.AdmitReclaim(ctx, tx, capture.Ref, uuid.NewString(), 1,
				output.LeaseTermFor(deleteTimeout), output.DefaultPublicationGrace)).To(Succeed())
		})
		in(func(tx db.HangarOutputTx) {
			var err error
			job, err = repository.LoadReclaimJob(ctx, tx, capture.Ref)
			Expect(err).NotTo(HaveOccurred())
		})

		// Mid-reclaim the residue is an unfinalized job: an object whose
		// disposition is unknown.
		Expect(residue().UnfinalizedReclaimJobs).To(Equal(1))

		var attempt int64
		in(func(tx db.HangarOutputTx) {
			var err error
			attempt, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
			Expect(err).NotTo(HaveOccurred())
		})
		in(func(tx db.HangarOutputTx) {
			Expect(repository.RecordDeleteOutcome(ctx, tx, job, attempt,
				output.DeleteConfirmed)).To(Succeed())
			Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)).
				To(Succeed())
		})

		counts = residue()
		Expect(counts.Residue()).To(BeZero(),
			"after the last generation reclaimed and the last claim released, residue remains: %+v",
			counts)
		Expect(counts.LiveGenerations).To(BeZero())
	})
})
