package db_test

// Phase 9's integrated acceptance suite, and what it deliberately does NOT
// contain.
//
// The plan's rule for this phase is that no acceptance task restates a contract
// a Phase 2-8 scenario already pins; where it would, it cites instead. Applied
// honestly that removes most of what this box originally named, because those
// contracts are pinned and the citations are exact:
//
//   - Stage 2 atomicity, the no_capture halves and both pre_reservation_cancel
//     paths, and the three-way disposition exclusion:
//     `atc/worker/jetbridge/brine/features/hangar-disposition.feature`, and
//     hangar_output_test.go's own disposition Describes.
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
//   - Cursor and debt recovery: hangar_output_inventory_test.go:206-310 and
//     hangar_output_controller_pass_test.go:470.
//   - Policy at-risk stopping each of the five admissions:
//     hangar_output_policy_test.go:118-190 and hangar_output_test.go:544.
//   - Unknown-ref read-then-lock-then-revalidate:
//     hangar_output_test.go:1298-1400.
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
//  1. One capture from predeclaration to a confirmed reclamation, with nothing
//     staged between legs: whatever `RegisterReceipt` committed is what
//     `AcquireClaim` is given, whatever that committed is what `AcquireReadLease`
//     is given, and so on to `FinalizeReclaim`.
//  2. The terminal downgrade refusal over state the PRODUCTION capture path
//     produced. `atc/hangaroutput/downgrade_test.go` proves the predicate, and
//     says plainly why it seeds `hangar_exact_lifecycles` directly rather than
//     reconstructing the chain. That is the right call there and it leaves
//     exactly one thing unasserted: that the rows a real capture writes are the
//     rows the drain predicate counts. A predicate that missed a class would
//     look identical in that suite and would silently permit a downgrade that
//     strands live objects.

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/activation"
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

		// Two open transactions at once in the first spec (the reader holding
		// while the reclaimer is refused), so the suite's deliberate
		// one-connection default has to lift and be put back.
		dbConn.SetMaxOpenConns(3)
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

	It("carries one capture from predeclaration to a confirmed reclamation, each leg reading what the last one committed", func() {
		digest := hangarDigest(91)
		generation := int64(1725830823000091)

		// --- capture -------------------------------------------------------
		//
		// hangarPublishAt drives predeclaration, reservation, hold, Stage 2,
		// the capture lease, the logical resolution, the first object create
		// and the receipt registration through the repository. Nothing here
		// writes a row itself: a fixture that did would be proving the schema
		// twice and the composition not at all.
		capture := hangarPublishAt(ctx, repository, digest, generation,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

		Expect(lifecycleStateOf(capture.Ref)).To(Equal("registered"))

		// The receipt's registration is what makes the exact generation
		// readable. Before the source is released the capture is not settled,
		// and that is a state the plane sits in on purpose.
		var settled bool
		Expect(dbConn.QueryRow(`
			SELECT state = 'registered' AND release_acknowledged_at IS NOT NULL
			  FROM hangar_capture_reservations WHERE reservation_id = $1`,
			string(capture.ReservationID)).Scan(&settled)).To(Succeed())
		Expect(settled).To(BeFalse(),
			"the capture reported itself settled before its source was released; "+
				"Req 40 wants the source released and not only the decision taken")

		hangarReleaseSource(ctx, repository, capture)

		// --- the consumer binds and claims, in ONE transaction --------------
		//
		// The consumer is product-neutral and opaque: a binding id and a
		// visibility, and Hangar learns nothing else about it. Req 30's shape
		// is that both halves commit together, so the claim is acquired inside
		// the consumer's own transaction rather than beside it.
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

		// AC 13's last clause: releasing the LAST claim during a transfer
		// cannot delete until the read lease closes. The claim is gone and the
		// reader is still holding, so the refusal must now be the lease.
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
		// The released claim stays tombstoned for the lifetime of the exact-ref
		// record (Req 32), and a caller cannot re-acquire on a reclaimed ref
		// (Req 38). Both are read from the state the legs above committed, not
		// from a row this spec wrote.
		var tombstoned int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM hangar_claims claim
			JOIN hangar_exact_lifecycles lifecycle ON lifecycle.id = claim.lifecycle_id
			WHERE lifecycle.scope = $1 AND lifecycle.digest = $2 AND lifecycle.generation = $3
			  AND claim.released_at IS NOT NULL`,
			string(capture.Ref.Scope), string(capture.Ref.Digest),
			capture.Ref.Generation).Scan(&tombstoned)).To(Succeed())
		Expect(tombstoned).To(Equal(1),
			"the released claim identity must remain tombstoned; a purged tombstone is a "+
				"claim id that can silently reactivate")

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

	It("refuses a terminal downgrade on state the production capture path produced, and accepts it once nothing is left", func() {
		// A second handle on the same test database, because `activation.Epochs`
		// takes a *sql.DB and this suite's `dbConn` is a db.DbConn. It is the
		// same database and the same rows; what differs is the API.
		conn := postgresRunner.OpenDB()
		DeferCleanup(func() { Expect(conn.Close()).To(Succeed()) })
		epochs := activation.Epochs{DB: conn}

		digest := hangarDigest(92)
		capture := hangarPublishAt(ctx, repository, digest, 1725830823000092,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

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

		// The whole step, not its pieces: emission stops first and the
		// predicate then counts what is left. `--finalize` is what turns a
		// report into a refusal, and it is asked for here because a report is
		// not an assertion about whether a downgrade would be allowed.
		outcome, err := epochs.DrainStep(ctx, acceptanceEpoch, activation.FacetOutput, true)
		Expect(err).To(MatchError(activation.ErrDrainRefused))
		Expect(errors.Is(err, output.ErrConflict)).To(BeTrue(),
			"a drain refusal is a lifecycle conflict, not an infrastructure failure")
		Expect(outcome.Drained).To(BeTrue(),
			"emission must stop before the predicate counts, so the set it counts cannot grow")
		Expect(outcome.Disabled).To(BeFalse())

		residue := outcome.Residue

		// The classes a real capture-and-claim produces. Named exactly rather
		// than "at least one", because the whole point of this spec is that the
		// predicate sees what the production path writes: a class the predicate
		// forgot is invisible to a "len(residue) > 0" assertion.
		classes := map[string]int{}
		for _, one := range residue {
			classes[one.Class] = one.Count
		}
		Expect(classes).To(HaveKeyWithValue("live exact generations", 1))
		Expect(classes).To(HaveKeyWithValue("active claims", 1))

		// The refusal names the classes rather than a count: an operator who
		// cannot act on a refusal will work around it.
		Expect(err.Error()).To(ContainSubstring("live exact generations"))
		Expect(err.Error()).To(ContainSubstring("active claims"))

		// And the facet is left DRAINING, not rolled back: stopping emission is
		// unconditionally right, and it is what keeps the counted set stable.
		state, err := epochs.Read(ctx, acceptanceEpoch)
		Expect(err).NotTo(HaveOccurred())
		Expect(state.Output).To(Equal("draining"))

		// Releases and settlement continue while draining -- that is the state
		// a deployment may legitimately sit in -- so the plane can be brought
		// down the supported way.
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

		// Already-admitted delete work may FINISH while draining. It is the one
		// mutation a draining facet still performs, and a downgrade that could
		// not finish it would strand the object it had already decided to
		// delete.
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

		// Mid-reclaim the facet is MORE blocked, not less: the job is admitted
		// and unfinalized, which is an object whose disposition is unknown.
		outcome, err = epochs.DrainStep(ctx, acceptanceEpoch, activation.FacetOutput, false)
		Expect(err).NotTo(HaveOccurred(),
			"without --finalize a facet that still holds state is a report, not a failure")
		classes = map[string]int{}
		for _, one := range outcome.Residue {
			classes[one.Class] = one.Count
		}
		Expect(classes).To(HaveKeyWithValue("unfinalized reclaim jobs", 1))

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

		outcome, err = epochs.DrainStep(ctx, acceptanceEpoch, activation.FacetOutput, true)
		Expect(err).NotTo(HaveOccurred(),
			"after the last generation reclaimed and the last claim released, the output "+
				"facet still refused: %v", outcome.Residue)
		Expect(outcome.Residue).To(BeEmpty())
		Expect(outcome.Disabled).To(BeTrue(),
			"the refusal above must be a refusal and not an inability")

		// And the base facet follows only afterwards, never before: output can
		// never be in service under a disabled base.
		state, err = epochs.Read(ctx, acceptanceEpoch)
		Expect(err).NotTo(HaveOccurred())
		Expect(state.Output).To(Equal("disabled"))
		Expect(state.Base).NotTo(Equal("disabled"))
	})
})
