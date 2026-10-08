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
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Reclamation's durable half: which registered generations the pass may
// select, what it rechecks under the locks before it asks the store, and the
// one stamp that records the store's answer.
//
// Real PostgreSQL, because every case is either two transactions arriving in an
// order neither chose, or a crash between a record and its effect. The typed
// conditional delete itself -- the answers a real store gives -- lives beside
// the reclaimer role in hangar/output/conformance.
var _ = Describe("reclaiming an exact generation", func() {
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

	in := func(work func(tx db.HangarOutputTx)) {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)
		work(tx)
		Expect(tx.Commit()).To(Succeed())
	}

	// published is a settled capture whose generation is still inside its
	// publication grace: no claim, no reader, a terminal and released capture
	// past its deadline. reclaimable is the same with the grace elapsed.
	// Elapsed grace is a precondition in its own right and one spec below is
	// about exactly that, so the two are separate helpers.
	published := func(digest hangar.Digest, generation int64) hangar.TreeRef {
		GinkgoHelper()
		capture := hangarPublishAt(ctx, repository, digest, generation,
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)
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

	reclaimedAt := func(ref hangar.TreeRef) sql.NullTime {
		GinkgoHelper()
		var at sql.NullTime
		Expect(dbConn.QueryRow(`
			SELECT reclaimed_at FROM hangar_exact_lifecycles
			WHERE scope = $1 AND digest = $2 AND generation = $3`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&at)).To(Succeed())

		return at
	}

	// candidates is the pass's work query at the default grace, wide enough
	// to hold every generation a spec publishes.
	candidates := func() []hangar.TreeRef {
		GinkgoHelper()
		var refs []hangar.TreeRef
		in(func(tx db.HangarOutputTx) {
			var err error
			refs, err = repository.ReclaimableGenerations(ctx, tx, output.DefaultPublicationGrace, 100)
			Expect(err).NotTo(HaveOccurred())
		})

		return refs
	}

	// hold is HoldForReclaim in a transaction that is then rolled back: the
	// answer, with nothing left behind.
	hold := func(ref hangar.TreeRef) error {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)

		return repository.HoldForReclaim(ctx, tx, ref, output.DefaultPublicationGrace)
	}

	// reclaim is the pass's transaction with the store call elided: hold the
	// generation and stamp it, in one commit.
	reclaim := func(ref hangar.TreeRef) {
		GinkgoHelper()
		in(func(tx db.HangarOutputTx) {
			Expect(repository.HoldForReclaim(ctx, tx, ref, output.DefaultPublicationGrace)).To(Succeed())
			Expect(repository.StampReclaimed(ctx, tx, ref)).To(Succeed())
		})
	}

	consumersClaim := func(ref hangar.TreeRef, binding string) output.ClaimID {
		GinkgoHelper()
		id := output.ClaimID(uuid.NewString())
		in(func(tx db.HangarOutputTx) {
			Expect(hangarAcquireClaim(ctx, repository, tx, id, ref, binding)).To(Succeed())
		})

		return id
	}

	readersClaim := func(ref hangar.TreeRef, term time.Duration) output.ClaimRecord {
		GinkgoHelper()
		var record output.ClaimRecord
		in(func(tx db.HangarOutputTx) {
			var err error
			record, err = repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           output.ClaimID(uuid.NewString()),
				Ref:               ref,
				ConsumerBindingID: "input-read:task-handle/input-0",
				RequestedAt:       output.NewTimestamp(time.Now()),
				Term:              term,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		return record
	}

	release := func(ref hangar.TreeRef, id output.ClaimID) {
		GinkgoHelper()
		in(func(tx db.HangarOutputTx) {
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         id,
				Ref:             ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
		})
	}

	// expire moves a reader's claim past its term on the database clock. What
	// is being arranged is time passing: the row keeps its term, and the
	// comparison the repository makes is still the database's own.
	expire := func(id output.ClaimID) {
		GinkgoHelper()
		result, err := dbConn.Exec(`
			UPDATE hangar_claims SET expires_at = now() - interval '1 second' WHERE claim_id = $1`,
			string(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RowsAffected()).To(BeEquivalentTo(1))
	}

	openClaims := func() int {
		GinkgoHelper()
		var counts output.PlaneCounts
		in(func(tx db.HangarOutputTx) {
			var err error
			counts, err = repository.CountOutputPlaneState(ctx, tx)
			Expect(err).NotTo(HaveOccurred())
		})

		return counts.OpenClaims
	}

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(4)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("reclaim-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	Describe("what the pass may select", func() {
		It("names a registered, unclaimed generation once its publication grace has elapsed", func() {
			// Elapsed grace is one of the preconditions. Without it a
			// generation is reclaimable the instant it publishes: the capture
			// that made the object has settled, so nothing else here objects,
			// and the plane would delete a freshly published tree because no
			// claim had been taken yet.
			ref := published(hangarDigest(60), 1725830823000060)
			Expect(candidates()).NotTo(ContainElement(ref))

			err := hold(ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("publication grace"))
			Expect(reclaimedAt(ref).Valid).To(BeFalse())

			// And the control: the same generation, once its grace has
			// elapsed on the database clock, is selected and may be held.
			hangarAgePublication(ref, hangarGraceElapsed)
			Expect(candidates()).To(ConsistOf(ref))
			Expect(hold(ref)).To(Succeed())
		})

		It("excludes a generation under a consumer's live claim, until it is released", func() {
			ref := reclaimable(hangarDigest(51), 1725830823000051)
			claimID := consumersClaim(ref, "binding-reclaim")

			Expect(candidates()).NotTo(ContainElement(ref))
			err := hold(ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 live claim(s)"),
				"the refusal is about something other than the live claim; a precondition "+
					"added later must not quietly become the reason this spec is green")

			release(ref, claimID)
			Expect(candidates()).To(ConsistOf(ref))
			Expect(hold(ref)).To(Succeed())
		})

		// A reader's claim is the reader's protection and it outlives the
		// consumer's; an EXPIRED one is not protection at all, which is what
		// stops one crashed materializer pinning a generation for the life of
		// the deployment.
		It("excludes a reader's live claim and not an expired one", func() {
			ref := reclaimable(hangarDigest(70), 1725830823000070)
			reader := readersClaim(ref, 30*time.Minute)
			Expect(reader.ExpiresAt).NotTo(BeNil())

			Expect(candidates()).NotTo(ContainElement(ref))
			err := hold(ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("1 live claim(s)"))
			Expect(openClaims()).To(Equal(1), "a reader's live claim is an open claim")

			expire(reader.ClaimID)
			Expect(openClaims()).To(BeZero(),
				"an expired reader's claim was counted as residue a drain would wait on")
			Expect(candidates()).To(ConsistOf(ref))
			Expect(hold(ref)).To(Succeed())
		})

		It("excludes a generation while a pending or publishing capture names the same tree", func() {
			digest := hangarDigest(53)
			ref := reclaimable(digest, 1725830823000053)

			// A second capture of the same content opens and does not publish.
			hangarReserve(ctx, repository, digest, output.DefaultCaptureDeadline)

			Expect(candidates()).NotTo(ContainElement(ref))
			err := hold(ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("pending or publishing capture"))
		})

		It("excludes a generation while an unregistered input publication names the same tree", func() {
			digest := hangarDigest(54)
			ref := reclaimable(digest, 1725830823000054)

			// A Run input of the same content is reserved and the node has not
			// yet answered with the publication, so no generation is known for
			// it. It could be this one. The reservation row is immutable and
			// the schema refuses one reserved already lapsed, so the lapsed
			// case is the schema's: an unanswered reservation lapses within
			// two minutes of its creation and protects nothing after.
			Expect(candidates()).To(ConsistOf(ref))
			now := time.Now().UTC()
			in(func(tx db.HangarOutputTx) {
				Expect(repository.ReserveInputPublication(ctx, tx, output.InputStage{
					Version:       output.InputPublicationVersion,
					ReservationID: output.ReservationID(uuid.NewString()),
					NodeUID:       executioncontrol.NodeUID("node-uid"),
					Scope:         ref.Scope,
					Digest:        digest,
					Bytes:         1024,
					CreatedAt:     output.NewTimestamp(now),
					ExpiresAt:     output.NewTimestamp(now.Add(90 * time.Second)),
				}, uuid.NewString())).To(Succeed())
			})

			Expect(candidates()).NotTo(ContainElement(ref))
			err := hold(ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("unregistered input publication"))
		})

		It("refuses the hold while a blocking integrity finding is open", func() {
			ref := reclaimable(hangarDigest(55), 1725830823000055)

			in(func(tx db.HangarOutputTx) {
				Expect(repository.RecordRuntimeAtRisk(ctx, tx, output.IntegrityFindingRecord{
					Violation: output.ViolationOutOfBandAbsence, Subject: "missing-generation",
					Detail: "unexpected object loss",
				})).To(Succeed())
			})

			// The query is the bound and not the decision: the finding is
			// about the plane, not this generation, and the pass learns it at
			// the hold, before any store call.
			Expect(candidates()).To(ConsistOf(ref))
			err := hold(ref)
			Expect(err).To(MatchError(output.ErrAtRisk))
			Expect(err.Error()).To(ContainSubstring("storage integrity"))
			Expect(reclaimedAt(ref).Valid).To(BeFalse())

			var finding int64
			Expect(dbConn.QueryRow(`
				SELECT id FROM hangar_integrity_findings WHERE resolved_at IS NULL`).Scan(&finding)).
				To(Succeed())
			in(func(tx db.HangarOutputTx) {
				Expect(repository.ResolveIntegrityFinding(ctx, tx, finding)).To(Succeed())
			})
			Expect(hold(ref)).To(Succeed())
		})

		It("stops naming a generation once it is stamped reclaimed", func() {
			ref := reclaimable(hangarDigest(56), 1725830823000056)
			Expect(candidates()).To(ConsistOf(ref))

			reclaim(ref)

			Expect(candidates()).To(BeEmpty())
			err := hold(ref)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("already reclaimed"))
		})

		It("is bounded, oldest first, and names its grace", func() {
			newer := reclaimable(hangarDigest(57), 1725830823000057)
			older := reclaimable(hangarDigest(58), 1725830823000058)
			hangarAgePublication(older, time.Hour)

			in(func(tx db.HangarOutputTx) {
				refs, err := repository.ReclaimableGenerations(ctx, tx, output.DefaultPublicationGrace, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(refs).To(Equal([]hangar.TreeRef{older}))

				refs, err = repository.ReclaimableGenerations(ctx, tx, output.DefaultPublicationGrace, 2)
				Expect(err).NotTo(HaveOccurred())
				Expect(refs).To(Equal([]hangar.TreeRef{older, newer}))

				_, err = repository.ReclaimableGenerations(ctx, tx, output.DefaultPublicationGrace, 0)
				Expect(err).To(MatchError(output.ErrIncomplete))
				_, err = repository.ReclaimableGenerations(ctx, tx, 0, 10)
				Expect(err).To(MatchError(output.ErrIncomplete))
				Expect(repository.HoldForReclaim(ctx, tx, older, 0)).To(MatchError(output.ErrIncomplete))
			})
		})
	})

	Describe("the stamp", func() {
		It("records the delete once, and a second stamp is a conflict", func() {
			ref := reclaimable(hangarDigest(61), 1725830823000061)
			Expect(reclaimedAt(ref).Valid).To(BeFalse())

			reclaim(ref)
			stamped := reclaimedAt(ref)
			Expect(stamped.Valid).To(BeTrue())

			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.StampReclaimed(ctx, tx, ref)).To(MatchError(output.ErrConflict))
			Expect(reclaimedAt(ref)).To(Equal(stamped), "a second stamp moved the first")
		})

		It("refuses to stamp a generation no publication registered", func() {
			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.StampReclaimed(ctx, tx, hangar.TreeRef{
				Scope: "team-a", Digest: hangarDigest(62), Generation: 1725830823000062,
			})).To(MatchError(output.ErrNotFound))
		})

		It("never resurrects a reclaimed generation", func() {
			digest := hangarDigest(63)
			ref := reclaimable(digest, 1725830823000063)
			reclaim(ref)

			// A claim on it is refused: the stamped row is the tombstone that
			// stops a stale resurrection.
			tx := begin()
			err := repository.HoldForReclaim(ctx, tx, ref, output.DefaultPublicationGrace)
			Expect(err).To(MatchError(output.ErrConflict))
			err = hangarAcquireClaim(ctx, repository, tx, output.ClaimID(uuid.NewString()), ref,
				"binding-after-reclaim")
			Expect(err).To(MatchError(output.ErrNotFound))
			Expect(err.Error()).To(ContainSubstring("reclaimed"))
			db.Rollback(tx)

			// And so is registering it again: a second capture of the same
			// content that publishes at the same generation finds the row
			// stamped and is refused rather than reusing it.
			again := hangarReserve(ctx, repository, digest, output.DefaultCaptureDeadline)
			tx = begin()
			defer db.Rollback(tx)
			_, err = repository.CASPublishingToPublished(ctx, tx, output.PublishedCapture{
				Key: again.Key, Generation: ref.Generation,
			})
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(err.Error()).To(ContainSubstring("never resurrects"))
		})
	})

	Describe("a crash between the delete and the stamp", func() {
		It("leaves the generation registered for the next pass to retry", func() {
			ref := reclaimable(hangarDigest(67), 1725830823000067)

			// The pass holds the generation, asks the store, and dies before
			// the commit. Nothing durable changed: the row is registered, the
			// next pass selects it again, and the store answers already-absent
			// to a delete that already landed.
			tx := begin()
			Expect(repository.HoldForReclaim(ctx, tx, ref, output.DefaultPublicationGrace)).To(Succeed())
			Expect(repository.StampReclaimed(ctx, tx, ref)).To(Succeed())
			db.Rollback(tx)

			Expect(reclaimedAt(ref).Valid).To(BeFalse(),
				"a stamp outlived the transaction that was going to justify it")
			Expect(candidates()).To(ConsistOf(ref))
			reclaim(ref)
			Expect(reclaimedAt(ref).Valid).To(BeTrue())
		})
	})

	Describe("an absence a read found", func() {
		absences := func() db.HangarAbsences { return db.HangarAbsences{Conn: dbConn} }

		openFindings := func() []string {
			GinkgoHelper()
			rows, err := dbConn.Query(`
				SELECT violation || ':' || subject FROM hangar_integrity_findings
				WHERE resolved_at IS NULL ORDER BY id`)
			Expect(err).NotTo(HaveOccurred())
			defer db.Close(rows)
			var findings []string
			for rows.Next() {
				var finding string
				Expect(rows.Scan(&finding)).To(Succeed())
				findings = append(findings, finding)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())

			return findings
		}

		It("is a lifetime violation for a registered generation, which stays registered", func() {
			ref := reclaimable(hangarDigest(65), 1725830823000065)

			Expect(absences().RecordUnexpectedAbsence(ctx, ref)).To(Succeed())
			Expect(openFindings()).To(ConsistOf(fmt.Sprintf("%s:%s/%s/%d",
				output.ViolationOutOfBandAbsence, ref.Scope, ref.Digest, ref.Generation)))
			Expect(reclaimedAt(ref).Valid).To(BeFalse(),
				"an absence nothing this plane did explains was recorded as a reclamation")

			// Recorded once: the finding is the operator's to resolve, and a
			// second read of the same absence is the same finding.
			Expect(absences().RecordUnexpectedAbsence(ctx, ref)).To(Succeed())
			Expect(openFindings()).To(HaveLen(1))

			// The pass finds the object absent once nothing claims it and the
			// finding is resolved, and stamps it then; until then the finding
			// blocks it.
			Expect(hold(ref)).To(MatchError(output.ErrAtRisk))
		})

		It("is not news for a reclaimed generation, nor for one never registered", func() {
			ref := reclaimable(hangarDigest(66), 1725830823000066)
			reclaim(ref)

			Expect(absences().RecordUnexpectedAbsence(ctx, ref)).To(Succeed())
			Expect(absences().RecordUnexpectedAbsence(ctx, hangar.TreeRef{
				Scope: "team-a", Digest: hangarDigest(68), Generation: 1725830823000068,
			})).To(Succeed())
			Expect(openFindings()).To(BeEmpty(),
				"this plane's own delete, or a generation it never managed, was reported as "+
					"somebody else removing an object")
		})
	})
})

// A release implies a terminal capture, enforced by the row.
//
// Every production writer of released_at (SetReleased) names the terminal
// states in its WHERE clause, so deleting that clause would leave every
// repository spec green. It is a CHECK on the row, which is a rule about the
// row rather than about one path to it -- and which a spec can actually put a
// row in front of.
//
// The writes here are deliberately RAW. Every production path satisfies the
// invariant already; what has to be proved is that a path which did not would
// be stopped, and the only way to arrange that is to be the path.
var _ = Describe("a release on a capture row", func() {
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
			output.DefaultCaptureDeadline)

		// The control: the row exists and is terminal, so a release on it is
		// legal. Without this the refusal below could be the row being missing.
		result, err := dbConn.Exec(`
			UPDATE hangar_captures SET released_at = now()
			 WHERE execution_id = $1 AND output_name = $2`,
			string(capture.Key.ExecutionID), string(capture.Key.Output))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RowsAffected()).To(BeEquivalentTo(1))

		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM hangar_captures WHERE execution_id = $1 AND output_name = $2`,
			string(capture.Key.ExecutionID), string(capture.Key.Output)).Scan(&state)).To(Succeed())
		Expect(state).To(Equal("published"))

		// And now a live one. A capture still being written owes no release:
		// its source is held BECAUSE it is being written, and an unsealed tree
		// released mid-write is the seam Phase 4 closed.
		live := hangarReserve(ctx, repository, hangarDigest(71),
			output.DefaultCaptureDeadline)
		_, err = dbConn.Exec(`
			UPDATE hangar_captures SET released_at = now()
			 WHERE execution_id = $1 AND output_name = $2`,
			string(live.Key.ExecutionID), string(live.Key.Output))
		Expect(err).To(HaveOccurred(),
			"a release was recorded for a capture that is not terminal at all")
		Expect(err.Error()).To(ContainSubstring("violates check constraint"))
	})
})
