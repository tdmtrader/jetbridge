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

// Reclamation's durable half: who may admit one, who may finish one, and which
// of four things a finished one is allowed to claim happened.
//
// Real PostgreSQL, because every case is either two transactions arriving in an
// order neither chose, or a crash between a record and its effect. The typed
// conditional delete itself -- the six answers a real store gives -- lives
// beside the reclaimer role in hangar/output/conformance.
var _ = Describe("reclaiming an exact generation", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		owner      string
	)

	const deleteTimeout = 2 * time.Minute

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

	// reclaimable publishes a capture, settles it completely, and returns an
	// exact ref nothing protects: no claim, no read lease, no unresolved
	// reservation, a terminal and settled capture past its deadline.
	// published is a settled capture whose generation is still inside its
	// publication grace. Elapsed grace is a precondition in its own right and
	// one spec below is about exactly that, so the two are separate helpers.
	published := func(digest hangar.Digest, generation int64) hangar.TreeRef {
		GinkgoHelper()
		capture := hangarPublishAt(ctx, repository, digest, generation,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		hangarReleaseSource(ctx, repository, capture)
		hangarAgeCapture(capture, 48*time.Hour)

		return capture.Ref
	}

	reclaimable := func(digest hangar.Digest, generation int64) hangar.TreeRef {
		GinkgoHelper()
		ref := published(digest, generation)
		hangarAgePublication(ref, hangarGraceElapsed)

		return ref
	}

	admit := func(ref hangar.TreeRef) db.HangarReclaimJob {
		GinkgoHelper()
		var job db.HangarReclaimJob
		in(func(tx db.HangarOutputTx) {
			Expect(repository.AdmitReclaim(ctx, tx, ref, owner, 1,
				output.LeaseTermFor(deleteTimeout), output.DefaultPublicationGrace)).To(Succeed())
		})
		in(func(tx db.HangarOutputTx) {
			var err error
			job, err = repository.LoadReclaimJob(ctx, tx, ref)
			Expect(err).NotTo(HaveOccurred())
		})

		return job
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

	expireTheLease := func(job db.HangarReclaimJob) {
		GinkgoHelper()
		// Moved wholesale into the past rather than shortened: the schema
		// refuses a term under fifteen minutes, and what is being arranged is
		// time passing, not an illegally short lease.
		_, err := dbConn.Exec(`
			UPDATE hangar_reclaim_jobs
			   SET renewed_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
			 WHERE id = $1`, job.ID)
		Expect(err).NotTo(HaveOccurred())
	}

	BeforeEach(func() {
		ctx = context.Background()
		owner = uuid.NewString()
		dbConn.SetMaxOpenConns(4)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("reclaim-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	Describe("admission", func() {
		It("refuses a generation whose publication grace has not elapsed", func() {
			// Req 46 lists seven preconditions and elapsed grace is one of
			// them. Without it a generation is admissible the instant its
			// receipt lands: the capture that made the object has settled, so
			// nothing else here objects, and the plane would delete a freshly
			// published tree because no claim had been taken yet.
			ref := published(hangarDigest(60), 1725830823000060)

			tx := begin()
			err := repository.AdmitReclaim(ctx, tx, ref, owner, 1,
				output.LeaseTermFor(deleteTimeout), output.DefaultPublicationGrace)
			db.Rollback(tx)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("publication grace"))
			Expect(lifecycleStateOf(ref)).To(Equal("registered"),
				"a generation inside its publication grace was marked reclaiming")

			// And the control: the same generation, once its grace has
			// elapsed on the database clock, is admitted.
			hangarAgePublication(ref, hangarGraceElapsed)
			in(func(tx db.HangarOutputTx) {
				Expect(repository.AdmitReclaim(ctx, tx, ref, owner, 1,
					output.LeaseTermFor(deleteTimeout), output.DefaultPublicationGrace)).
					To(Succeed())
			})
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"))
		})

		It("marks the generation reclaiming before any external delete", func() {
			ref := reclaimable(hangarDigest(50), 1725830823000050)
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))

			job := admit(ref)
			Expect(job.Ref).To(Equal(ref))
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"),
				"admission did not mark the generation reclaiming durably before the delete; a "+
					"crash here would leave an object deleted and a row saying it is registered")
			Expect(job.AdmittedDeletes).To(Equal(0),
				"admission recorded a delete nobody has attempted")
		})

		It("is refused while a claim is active, and admitted once it is released", func() {
			ref := reclaimable(hangarDigest(51), 1725830823000051)
			claimID := output.ClaimID(uuid.NewString())

			in(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           claimID,
					Ref:               ref,
					ConsumerBindingID: "binding-reclaim",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})

			tx := begin()
			err := repository.AdmitReclaim(ctx, tx, ref, owner, 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
			db.Rollback(tx)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("claim"),
				"the refusal is about something other than the active claim; a precondition "+
					"added later must not quietly become the reason this spec is green")
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))

			in(func(tx db.HangarOutputTx) {
				Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
					ProtocolVersion: output.ProtocolVersion,
					ClaimID:         claimID,
					Ref:             ref,
					RequestedAt:     output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})

			admit(ref)
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"))
		})

		It("loses to a claim that arrives first, and wins against one that arrives after", func() {
			// Both orders, over one generation, with two real transactions.
			// Either the claimant wins and reclamation is refused, or
			// reclamation wins and the claim is refused; never both.
			ref := reclaimable(hangarDigest(52), 1725830823000052)

			claiming := begin()
			defer db.Rollback(claiming)
			Expect(repository.AcquireClaim(ctx, claiming, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           output.ClaimID(uuid.NewString()),
				Ref:               ref,
				ConsumerBindingID: "binding-first",
				RequestedAt:       output.NewTimestamp(time.Now()),
			})).To(Succeed())

			admitted := make(chan error, 1)
			go func() {
				defer GinkgoRecover()
				tx, err := dbConn.Begin()
				if err != nil {
					admitted <- err

					return
				}
				if err := repository.AdmitReclaim(ctx, db.HangarOutputTx{Tx: tx}, ref, owner, 1,
					output.MinLeaseTerm, output.DefaultPublicationGrace); err != nil {
					_ = tx.Rollback()
					admitted <- err

					return
				}
				admitted <- tx.Commit()
			}()

			Consistently(admitted, 300*time.Millisecond).ShouldNot(Receive(),
				"reclaim admission did not serialize with the claim on the exact-lifecycle lock")
			Expect(claiming.Commit()).To(Succeed())

			var err error
			Eventually(admitted, 5*time.Second).Should(Receive(&err))
			Expect(err).To(MatchError(output.ErrConflict),
				"reclamation was admitted beside an active claim")
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))
		})

		It("is refused while an unresolved reservation still correlates the ref", func() {
			digest := hangarDigest(53)
			ref := reclaimable(digest, 1725830823000053)

			// A second capture of the same content opens and does not resolve.
			hangarReserve(ctx, repository, digest,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

			tx := begin()
			defer db.Rollback(tx)
			err := repository.AdmitReclaim(ctx, tx, ref, owner, 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("unresolved reservation"))
		})

		It("is refused while the policy is at risk, and already-admitted work still finishes", func() {
			ref := reclaimable(hangarDigest(54), 1725830823000054)
			other := reclaimable(hangarDigest(55), 1725830823000055)
			job := admit(ref)

			// The policy goes at-risk BETWEEN admission and delete. Req 52
			// stops new admission from detection onward and lets
			// already-admitted conditional delete work finish.
			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordPolicySnapshot(ctx, tx, output.PolicySnapshot{
					ProtocolVersion:      output.ProtocolVersion,
					ActivationEpoch:      1,
					BucketFingerprint:    "gs://output-bucket",
					Metageneration:       4,
					PolicyHash:           "policy-hash-changed",
					LifecycleDeleteRules: 1,
					State:                output.PolicyAtRisk,
					ObservedAt:           output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})

			// New admission stops. The generation it would be admitted for is
			// published BEFORE the policy turns, because a capture is one of
			// the five admissions Req 52 also stops -- a fixture built after
			// the turn would be refused for the wrong reason.
			// The refusal arrives at COMMIT: the policy gate is a deferred
			// constraint trigger, so an admission that looked fine statement by
			// statement is still refused before it is durable. That is the
			// arrangement the rest of this plane uses, and HangarOutputTx is
			// what turns the refusal into this leaf's vocabulary.
			tx := begin()
			Expect(repository.AdmitReclaim(ctx, tx, other, owner, 1,
				output.MinLeaseTerm, output.DefaultPublicationGrace)).To(Succeed())
			err := tx.Commit()
			db.Rollback(tx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at_risk"))

			// The admitted job finishes: the delete record, its outcome and the
			// finalization all go through.
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
			Expect(lifecycleStateOf(ref)).To(Equal("reclaimed_confirmed"))
		})
	})

	Describe("the lease", func() {
		It("refuses to begin work without the delete timeout plus two minutes left", func() {
			ref := reclaimable(hangarDigest(56), 1725830823000056)
			job := admit(ref)

			// The control: with a full term, work begins.
			in(func(tx db.HangarOutputTx) {
				_, err := repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})

			// One minute left, and a two-minute delete needs four. Both columns
			// move: the schema's term floor is fifteen minutes, and what is
			// being arranged is a lease nearly used up rather than a short one.
			_, err := dbConn.Exec(`
				UPDATE hangar_reclaim_jobs
				   SET renewed_at = now() - interval '20 minutes',
				       expires_at = now() + interval '1 minute'
				 WHERE id = $1`, job.ID)
			Expect(err).NotTo(HaveOccurred())

			tx := begin()
			defer db.Rollback(tx)
			_, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
			Expect(err).To(MatchError(output.ErrTimeout))
			Expect(err.Error()).To(ContainSubstring("work begins only with"))
		})

		It("refuses a renewal from an owner that was taken over", func() {
			ref := reclaimable(hangarDigest(57), 1725830823000057)
			job := admit(ref)

			// The control: a live owner renews.
			in(func(tx db.HangarOutputTx) {
				renewed, err := repository.RenewReclaimLease(ctx, tx, job, output.MinLeaseTerm)
				Expect(err).NotTo(HaveOccurred())
				Expect(renewed.LeaseFence).To(Equal(job.LeaseFence))
			})

			expireTheLease(job)

			var taken db.HangarReclaimJob
			in(func(tx db.HangarOutputTx) {
				var err error
				taken, err = repository.TakeOverReclaimJob(ctx, tx, job, uuid.NewString(),
					output.MinLeaseTerm)
				Expect(err).NotTo(HaveOccurred())
			})
			Expect(taken.LeaseFence).To(BeNumerically(">", job.LeaseFence))

			tx := begin()
			defer db.Rollback(tx)
			_, err := repository.RenewReclaimLease(ctx, tx, job, output.MinLeaseTerm)
			Expect(err).To(MatchError(output.ErrConflict))
		})

		It("refuses a stale fence even when the owner is the same process", func() {
			// The case the fence exists for, and the one the owner check cannot
			// cover: a controller whose lease lapsed takes its OWN job back,
			// under its own id, and is still holding the job value it had
			// before. That value's fence is behind the row's, and every write
			// it makes has to refuse -- otherwise a delete decided under the
			// old lease lands under the new one.
			ref := reclaimable(hangarDigest(71), 1725830823000071)
			stale := admit(ref)
			expireTheLease(stale)

			var retaken db.HangarReclaimJob
			in(func(tx db.HangarOutputTx) {
				var err error
				retaken, err = repository.TakeOverReclaimJob(ctx, tx, stale, owner,
					output.MinLeaseTerm)
				Expect(err).NotTo(HaveOccurred())
			})
			Expect(retaken.OwnerID).To(Equal(stale.OwnerID))
			Expect(retaken.LeaseFence).To(BeNumerically(">", stale.LeaseFence))

			// The control: the re-read job renews, so the refusal below is the
			// fence's and not a lease that is simply gone.
			in(func(tx db.HangarOutputTx) {
				_, err := repository.RenewReclaimLease(ctx, tx, retaken, output.MinLeaseTerm)
				Expect(err).NotTo(HaveOccurred())
			})

			tx := begin()
			_, err := repository.RenewReclaimLease(ctx, tx, stale, output.MinLeaseTerm)
			db.Rollback(tx)
			Expect(err).To(MatchError(output.ErrConflict),
				"the same process renewed under a fence it no longer holds")

			// And its delete admission refuses too, before any record is
			// written: a stale owner that could admit a delete would put a
			// durable "we asked" on a job somebody else is running.
			tx = begin()
			_, err = repository.AdmitDelete(ctx, tx, stale, deleteTimeout)
			db.Rollback(tx)
			Expect(err).To(MatchError(output.ErrConflict))

			// And its outcome and finalization refuse for the same reason.
			var attempt int64
			in(func(tx db.HangarOutputTx) {
				var err error
				attempt, err = repository.AdmitDelete(ctx, tx, retaken, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})
			tx = begin()
			defer db.Rollback(tx)
			Expect(repository.RecordDeleteOutcome(ctx, tx, stale, attempt, output.DeleteConfirmed)).
				To(MatchError(output.ErrConflict))
			Expect(repository.FinalizeReclaim(ctx, tx, stale, output.ReclaimAbandoned, false)).
				To(MatchError(output.ErrConflict))
		})

		It("refuses a taken-over owner's delete outcome and its finalization", func() {
			ref := reclaimable(hangarDigest(58), 1725830823000058)
			job := admit(ref)

			var attempt int64
			in(func(tx db.HangarOutputTx) {
				var err error
				attempt, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})

			expireTheLease(job)
			in(func(tx db.HangarOutputTx) {
				_, err := repository.TakeOverReclaimJob(ctx, tx, job, uuid.NewString(),
					output.MinLeaseTerm)
				Expect(err).NotTo(HaveOccurred())
			})

			// The old owner's delete comes back, after the takeover. It writes
			// nothing: two owners writing two answers about one attempt is how
			// an ambiguous delete becomes a confirmed one.
			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.RecordDeleteOutcome(ctx, tx, job, attempt, output.DeleteConfirmed)).
				To(MatchError(output.ErrConflict))
			Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)).
				To(MatchError(output.ErrConflict))
		})

		It("refuses a finalization from an expired owner that was not taken over", func() {
			ref := reclaimable(hangarDigest(59), 1725830823000059)
			job := admit(ref)

			in(func(tx db.HangarOutputTx) {
				_, err := repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})
			expireTheLease(job)

			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimAbandoned, false)).
				To(MatchError(output.ErrConflict))
		})
	})

	Describe("the evidence a finished job may claim", func() {
		It("confirms only with an acknowledged conditional delete", func() {
			ref := reclaimable(hangarDigest(60), 1725830823000060)
			job := admit(ref)

			// No attempt at all: `reclaimed_confirmed` has nothing behind it.
			tx := begin()
			err := repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)
			if err == nil {
				err = tx.Commit()
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("acknowledged conditional delete"))
			db.Rollback(tx)

			// An admitted delete whose answer never came is still not a
			// confirmation.
			var attempt int64
			in(func(tx db.HangarOutputTx) {
				var err error
				attempt, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})
			tx = begin()
			err = repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)
			if err == nil {
				err = tx.Commit()
			}
			Expect(err).To(HaveOccurred())
			db.Rollback(tx)

			// And with the acknowledgement it is one.
			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordDeleteOutcome(ctx, tx, job, attempt,
					output.DeleteConfirmed)).To(Succeed())
				Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConfirmed, false)).
					To(Succeed())
			})
			Expect(lifecycleStateOf(ref)).To(Equal("reclaimed_confirmed"))
		})

		It("infers only from an admitted delete plus observed exact absence", func() {
			ref := reclaimable(hangarDigest(61), 1725830823000061)
			job := admit(ref)

			var attempt int64
			in(func(tx db.HangarOutputTx) {
				var err error
				attempt, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})

			// The delete happened and the response was lost. That is an
			// infrastructure failure on the attempt, not a confirmation.
			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordDeleteOutcome(ctx, tx, job, attempt,
					output.DeleteInfrastructure)).To(Succeed())
			})

			// Without observing absence, inference is refused.
			tx := begin()
			err := repository.FinalizeReclaim(ctx, tx, job, output.ReclaimInferred, false)
			if err == nil {
				err = tx.Commit()
			}
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("absence"))
			db.Rollback(tx)

			in(func(tx db.HangarOutputTx) {
				Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimInferred, true)).
					To(Succeed())
			})
			Expect(lifecycleStateOf(ref)).To(Equal("reclaimed_inferred"))

			var confirmed bool
			Expect(dbConn.QueryRow(`
				SELECT EXISTS (SELECT 1 FROM hangar_reclaim_attempts
				                WHERE job_id = $1 AND outcome = 'deleted')`,
				job.ID).Scan(&confirmed)).To(Succeed())
			Expect(confirmed).To(BeFalse(),
				"an inferred reclamation recorded an acknowledged delete it never received")
		})

		It("makes a generation conflict debt and never an unconditional delete", func() {
			ref := reclaimable(hangarDigest(62), 1725830823000062)
			job := admit(ref)

			var attempt int64
			in(func(tx db.HangarOutputTx) {
				var err error
				attempt, err = repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})
			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordDeleteOutcome(ctx, tx, job, attempt,
					output.DeleteGenerationConflict)).To(Succeed())
				Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimConflicted, false)).
					To(Succeed())
			})

			Expect(lifecycleStateOf(ref)).To(Equal("conflicted"))

			var attempts int
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM hangar_reclaim_attempts WHERE job_id = $1`,
				job.ID).Scan(&attempts)).To(Succeed())
			Expect(attempts).To(Equal(1),
				"a refused conditional delete was retried, and the only retry available to it "+
					"would be a broader one")
		})

		It("gives an abandoned job's generation back its protection", func() {
			ref := reclaimable(hangarDigest(63), 1725830823000063)
			job := admit(ref)
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"))

			in(func(tx db.HangarOutputTx) {
				Expect(repository.FinalizeReclaim(ctx, tx, job, output.ReclaimAbandoned, false)).
					To(Succeed())
			})
			Expect(lifecycleStateOf(ref)).To(Equal("registered"),
				"an abandoned reclaim left its generation reclaiming, which no claim may "+
					"protect and no reader may be granted")

			// And it can be admitted again, which is what "abandoned" has to
			// mean for a job that was given up on rather than finished.
			admit(ref)
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"))
		})

		It("never resurrects a reclaimed generation", func() {
			ref := reclaimable(hangarDigest(64), 1725830823000064)
			job := admit(ref)
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

			tx := begin()
			defer db.Rollback(tx)
			err := repository.AdmitReclaim(ctx, tx, ref, owner, 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
			Expect(err).To(MatchError(output.ErrConflict))

			// And a claim on it is refused: the tombstone is what stops a
			// stale resurrection.
			Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           output.ClaimID(uuid.NewString()),
				Ref:               ref,
				ConsumerBindingID: "binding-after-reclaim",
				RequestedAt:       output.NewTimestamp(time.Now()),
			})).To(HaveOccurred())
		})
	})

	Describe("absence with no admitted delete", func() {
		It("is an out-of-band lifetime violation and never a reclamation", func() {
			ref := reclaimable(hangarDigest(65), 1725830823000065)

			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordOutOfBandAbsence(ctx, tx, ref)).To(Succeed())
			})
			Expect(lifecycleStateOf(ref)).To(Equal("missing_out_of_band"))

			// It is not a reclamation, and nothing may later call it one: the
			// state is terminal and a reclaim job over it is refused.
			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.AdmitReclaim(ctx, tx, ref, owner, 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)).
				To(MatchError(output.ErrConflict))
		})

		It("is refused for a generation this plane admitted a delete for", func() {
			ref := reclaimable(hangarDigest(66), 1725830823000066)
			job := admit(ref)
			in(func(tx db.HangarOutputTx) {
				_, err := repository.AdmitDelete(ctx, tx, job, deleteTimeout)
				Expect(err).NotTo(HaveOccurred())
			})

			tx := begin()
			defer db.Rollback(tx)
			err := repository.RecordOutOfBandAbsence(ctx, tx, ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("admitted delete"))
		})
	})

	Describe("a crash between the record and its effect", func() {
		It("replays the admitted delete rather than losing that it asked", func() {
			ref := reclaimable(hangarDigest(67), 1725830823000067)
			job := admit(ref)

			// The admitted-delete record is written and the transaction dies
			// before the commit. Nothing durable says this plane asked, so an
			// absence found afterwards is an out-of-band violation -- which is
			// the honest answer, and exactly why the record must precede the
			// external call rather than follow it.
			tx := begin()
			_, err := repository.AdmitDelete(ctx, tx, job, deleteTimeout)
			Expect(err).NotTo(HaveOccurred())
			db.Rollback(tx)

			in(func(tx db.HangarOutputTx) {
				replayed, err := repository.LoadReclaimJob(ctx, tx, ref)
				Expect(err).NotTo(HaveOccurred())
				Expect(replayed.AdmittedDeletes).To(Equal(0),
					"an admitted delete outlived the transaction that was going to justify it")
			})

			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordOutOfBandAbsence(ctx, tx, ref)).To(Succeed())
			})
			Expect(lifecycleStateOf(ref)).To(Equal("missing_out_of_band"))
		})
	})

	Describe("the work query", func() {
		It("returns open jobs and stops returning finalized ones", func() {
			first := reclaimable(hangarDigest(68), 1725830823000068)
			second := reclaimable(hangarDigest(69), 1725830823000069)
			firstJob := admit(first)
			admit(second)

			in(func(tx db.HangarOutputTx) {
				jobs, err := repository.DueReclaimJobs(ctx, tx, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(jobs).To(HaveLen(2))
			})

			in(func(tx db.HangarOutputTx) {
				Expect(repository.FinalizeReclaim(ctx, tx, firstJob, output.ReclaimAbandoned,
					false)).To(Succeed())
			})

			in(func(tx db.HangarOutputTx) {
				jobs, err := repository.DueReclaimJobs(ctx, tx, 10)
				Expect(err).NotTo(HaveOccurred())
				Expect(jobs).To(HaveLen(1))
				Expect(jobs[0].Ref).To(Equal(second))
			})
		})

		It("is bounded", func() {
			tx := begin()
			defer db.Rollback(tx)
			_, err := repository.DueReclaimJobs(ctx, tx, 0)
			Expect(err).To(MatchError(output.ErrIncomplete))
		})
	})

	// A read lease is the reader's protection and it outlives the claim; an
	// EXPIRED one is not protection at all, which is what stops one crashed
	// materializer pinning a generation forever.
	Describe("read leases against admission", func() {
		It("blocks admission while live and permits it once closed", func() {
			ref := reclaimable(hangarDigest(70), 1725830823000070)
			claimID := output.ClaimID(uuid.NewString())
			leaseID := output.ReadLeaseID(uuid.NewString())

			in(func(tx db.HangarOutputTx) {
				Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
					ProtocolVersion:   output.ProtocolVersion,
					ClaimID:           claimID,
					Ref:               ref,
					ConsumerBindingID: "binding-reader",
					RequestedAt:       output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})
			in(func(tx db.HangarOutputTx) {
				_, err := repository.AcquireReadLease(ctx, tx,
					hangarReadLeaseRequest(leaseID, claimID, ref))
				Expect(err).NotTo(HaveOccurred())
			})
			in(func(tx db.HangarOutputTx) {
				Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
					ProtocolVersion: output.ProtocolVersion,
					ClaimID:         claimID,
					Ref:             ref,
					RequestedAt:     output.NewTimestamp(time.Now()),
				})).To(Succeed())
			})

			// The last claim is gone and the reader is still transferring.
			tx := begin()
			err := repository.AdmitReclaim(ctx, tx, ref, owner, 1, output.MinLeaseTerm,
				output.DefaultPublicationGrace)
			db.Rollback(tx)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("read lease"))

			closed := 0
			in(func(tx db.HangarOutputTx) {
				_, err := dbConn.Exec(`
					UPDATE hangar_read_leases
					   SET granted_at = now() - interval '2 hours',
					       expires_at = now() - interval '1 minute'
					 WHERE read_lease_id = $1`, string(leaseID))
				Expect(err).NotTo(HaveOccurred())
				closed, err = repository.CloseAbandonedReadLeases(ctx, tx, 10)
				Expect(err).NotTo(HaveOccurred())
			})
			Expect(closed).To(Equal(1))

			admit(ref)
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"))
		})
	})
})

// A release intent implies a terminal capture, enforced by the row.
//
// The rule used to be an extra WHERE clause in the acknowledgement statement
// and it could refuse nothing: all three writers of release_intent_id set a
// terminal state in the same statement, so deleting the clause entirely left
// every suite green. It is a CHECK now, which is a rule about the row rather
// than about one path to it -- and which a spec can actually put a row in front
// of.
//
// The writes here are deliberately RAW. Every production path satisfies the
// invariant already; what has to be proved is that a path which did not would
// be stopped, and the only way to arrange that is to be the path.
var _ = Describe("a release intent on a capture reservation", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	BeforeEach(func() {
		ctx = context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("release-intent-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)
		hangarActivateEpoch(ctx, repository)
	})

	It("is refused while the capture is still live, and admitted once it is terminal", func() {
		capture := hangarPublishAt(ctx, repository, hangarDigest(70), 1725830823000070,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

		// The control: the row exists and is terminal, so an intent on it is
		// legal. Without this the refusal below could be the row being missing.
		_, err := dbConn.Exec(`
			UPDATE hangar_capture_reservations SET release_intent_id = gen_random_uuid()
			 WHERE handoff_id = $1`, string(capture.HandoffID))
		Expect(err).NotTo(HaveOccurred())

		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM hangar_capture_reservations WHERE handoff_id = $1`,
			string(capture.HandoffID)).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("registered"))

		// And now a live one. A capture still being written owes no release:
		// its source is held BECAUSE it is being written, and an unsealed tree
		// released mid-write is the seam Phase 4 closed.
		live := hangarReserve(ctx, repository, hangarDigest(71),
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		_, err = dbConn.Exec(`
			UPDATE hangar_capture_reservations SET release_intent_id = gen_random_uuid()
			 WHERE handoff_id = $1`, string(live.HandoffID))
		Expect(err).To(HaveOccurred(),
			"a release intent was recorded for a capture that is not terminal at all")
		Expect(err.Error()).To(ContainSubstring("hangar_release_intent_implies_terminal"))
	})
})
