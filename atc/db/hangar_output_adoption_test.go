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

// Orphan adoption: the four things that must all be true, and the one that
// overrides the rest.
//
// Real PostgreSQL, because every precondition here is a correlation across
// three tables read on the database clock, and because AC 14's race is two
// transactions arriving in an order neither of them chose. The classification
// half -- what an object IS -- lives beside the role that reads it
// (hangar/output/conformance/inventory_test.go); this file is what the control
// plane does about it.
var _ = Describe("adopting a managed orphan", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	long := 30 * 24 * time.Hour

	begin := func() db.HangarOutputTx {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())

		return db.HangarOutputTx{Tx: tx}
	}

	// adopt runs one adoption in its own transaction and reports both halves.
	adopt := func(request output.AdoptionRequest) (output.AdoptionOutcome, error) {
		GinkgoHelper()
		tx := begin()
		defer db.Rollback(tx)

		outcome, err := repository.AdoptManagedOrphan(ctx, tx, request)
		if err != nil {
			return outcome, err
		}

		return outcome, tx.Commit()
	}

	lifecycleStateOf := func(ref hangar.TreeRef) (string, string) {
		GinkgoHelper()
		var state, origin string
		Expect(dbConn.QueryRow(`
			SELECT state, origin FROM hangar_exact_lifecycles
			WHERE scope = $1 AND digest = $2 AND generation = $3`,
			string(ref.Scope), string(ref.Digest), ref.Generation).Scan(&state, &origin)).
			To(Succeed())

		return state, origin
	}

	countRows := func(table, where string, args ...any) int {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(
			`SELECT count(*) FROM `+table+` WHERE `+where, args...).Scan(&count)).To(Succeed())

		return count
	}

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(3)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("adoption-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	Describe("an object with no correlated reservation at all", func() {
		// The commonest orphan: a capture crashed before it wrote anything
		// durable, or the deployment lost a row. There is nothing to wait for
		// except grace.
		orphan := hangar.TreeRef{
			Scope:      "team-a",
			Digest:     hangarDigest(40),
			Generation: 1725830823000040,
		}

		It("is adopted once its publication grace has elapsed", func() {
			// The control first: inside grace it is refused, so the success
			// below cannot pass for a method that adopts anything it is given.
			outcome, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-time.Hour)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionWithinPublicationGrace))
			Expect(countRows("hangar_exact_lifecycles", "generation = $1", orphan.Generation)).
				To(Equal(0))

			outcome, err = adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(output.AdoptionAdopted))

			state, origin := lifecycleStateOf(orphan)
			Expect(state).To(Equal("adopted"))
			Expect(origin).To(Equal("adopted"))
		})

		It("records lifecycle state and invents no capture, receipt or binding", func() {
			_, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())

			// Adoption enters LIFECYCLE bookkeeping only. A receipt is
			// daemon-signed (Req 25) and registration on retry belongs to the
			// fenced capture owner (Req 40); the inventory principal that calls
			// this holds list and get and nothing else (Req 54(b)).
			Expect(countRows("hangar_output_receipts", "lifecycle_id IN "+
				"(SELECT id FROM hangar_exact_lifecycles WHERE generation = $1)",
				orphan.Generation)).To(Equal(0),
				"adoption signed a receipt it has no key for")
			Expect(countRows("hangar_logical_reservations", "scope = $1 AND digest = $2",
				string(orphan.Scope), string(orphan.Digest))).To(Equal(0),
				"adoption invented a logical reservation")
			Expect(countRows("hangar_claims", "lifecycle_id IN "+
				"(SELECT id FROM hangar_exact_lifecycles WHERE generation = $1)",
				orphan.Generation)).To(Equal(0), "adoption invented a claim")
		})

		It("is idempotent, and says so rather than rewriting the row", func() {
			first, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(first).To(Equal(output.AdoptionAdopted))

			second, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(second).To(Equal(output.AdoptionAlreadyRegistered))
			Expect(countRows("hangar_exact_lifecycles", "generation = $1", orphan.Generation)).
				To(Equal(1))
		})

		It("never rewrites a REGISTERED generation's origin", func() {
			capture := hangarPublishAt(ctx, repository, hangarDigest(41), 1725830823000041,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

			outcome, err := adopt(hangarAdoptionFor(capture.Ref, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(output.AdoptionAlreadyRegistered))

			state, origin := lifecycleStateOf(capture.Ref)
			Expect(state).To(Equal("registered"))
			Expect(origin).To(Equal("registered"),
				"adoption relabelled a generation this deployment had registered through a "+
					"signed receipt")
		})

		It("refuses an object marked for another activation epoch, and touches nothing", func() {
			request := hangarAdoptionFor(orphan, time.Now().Add(-long))
			request.Marker.ActivationEpoch = 9

			outcome, err := adopt(request)
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionForeignEpoch))
			Expect(countRows("hangar_exact_lifecycles", "generation = $1", orphan.Generation)).
				To(Equal(0))
		})

		It("refuses a marker that does not describe the object it was read off", func() {
			request := hangarAdoptionFor(orphan, time.Now().Add(-long))
			request.Marker.Digest = hangarDigest(42)

			_, err := adopt(request)
			Expect(err).To(MatchError(output.ErrCorrupt))
			Expect(countRows("hangar_exact_lifecycles", "generation = $1", orphan.Generation)).
				To(Equal(0))
		})
	})

	Describe("an unresolved reservation is a shield, not a licence", func() {
		It("blocks adoption however much grace has elapsed, before any generation is known", func() {
			digest := hangarDigest(43)
			hangarReserve(ctx, repository, digest,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

			orphan := hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: 1725830823000043}

			// A YEAR of grace, and the answer is the same. Grace reduces work
			// and provides recovery margin; it is never the claim/reclaim
			// mutex, and a rule that weakened with age would be one.
			outcome, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-365*24*time.Hour)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionProtectedByReservation))
			Expect(countRows("hangar_exact_lifecycles", "generation = $1", orphan.Generation)).
				To(Equal(0))
		})

		It("keeps blocking after the capture deadline has passed", func() {
			digest := hangarDigest(44)
			capture := hangarReserve(ctx, repository, digest,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
			hangarAgeCapture(capture, 48*time.Hour)

			orphan := hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: 1725830823000044}
			outcome, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionProtectedByReservation),
				"an elapsed deadline turned an unresolved reservation into an adoptable orphan; "+
					"the reservation is still unresolved and the fenced owner may still register "+
					"that exact generation")
		})
	})

	Describe("a resolved reservation still owes three things", func() {
		var capture HangarCapture

		BeforeEach(func() {
			capture = hangarPublishAt(ctx, repository, hangarDigest(45), 1725830823000045,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		})

		// A SECOND generation at the same correlation: the orphan whose
		// preconditions are the correlated capture's, not its own.
		second := func() hangar.TreeRef {
			return hangar.TreeRef{
				Scope: "team-a", Digest: hangarDigest(45), Generation: 1725830823000046,
			}
		}

		It("waits for the capture deadline plus the safety margin", func() {
			hangarReleaseSource(ctx, repository, capture)

			outcome, err := adopt(hangarAdoptionFor(second(), time.Now().Add(-long)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionBeforeCaptureDeadline))

			hangarAgeCapture(capture, 48*time.Hour)

			outcome, err = adopt(hangarAdoptionFor(second(), time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(output.AdoptionAdopted))
		})

		It("waits for the source release, not only the decision", func() {
			hangarAgeCapture(capture, 48*time.Hour)

			// Terminal and past its deadline, and the source is still held on a
			// node: Req 40 wants the release too.
			outcome, err := adopt(hangarAdoptionFor(second(), time.Now().Add(-long)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionCaptureNotSettled))

			hangarReleaseSource(ctx, repository, capture)

			outcome, err = adopt(hangarAdoptionFor(second(), time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(output.AdoptionAdopted))
		})

		It("waits for the object's own publication grace as well", func() {
			hangarAgeCapture(capture, 48*time.Hour)
			hangarReleaseSource(ctx, repository, capture)

			// Everything the CAPTURE owes is settled; the OBJECT is new. The
			// two clocks measure different things and both have to pass.
			outcome, err := adopt(hangarAdoptionFor(second(), time.Now().Add(-time.Hour)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionWithinPublicationGrace))
		})
	})

	Describe("no reservation is extended past its original deadline to avoid collection", func() {
		It("refuses to move a predeclared capture deadline at all", func() {
			capture := hangarReserve(ctx, repository, hangarDigest(47),
				output.NewTimestamp(time.Now().Add(2*time.Hour)))

			_, err := dbConn.Exec(`
				UPDATE hangar_handoff_predeclarations
				   SET capture_deadline_at = capture_deadline_at + interval '6 days'
				 WHERE handoff_id = $1`, string(capture.HandoffID))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable"))

			// And the reservation's copy cannot drift from it either, which is
			// the other half: a deadline extended on one side only would be a
			// capture with two deadlines.
			_, err = dbConn.Exec(`
				UPDATE hangar_capture_reservations
				   SET capture_deadline_at = capture_deadline_at + interval '6 days'
				 WHERE reservation_id = $1`, string(capture.ReservationID))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("does not match the predeclared"))
		})
	})

	// AC 14, both arrival orders, with the consumer watching.
	Describe("a publication created after reservation and before registration", func() {
		var (
			capture HangarCapture
			orphan  hangar.TreeRef
		)

		BeforeEach(func() {
			digest := hangarDigest(48)
			capture = hangarReserve(ctx, repository, digest,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
			orphan = hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: 1725830823000048}
		})

		claimIsRefused := func(ref hangar.TreeRef) {
			GinkgoHelper()
			tx := begin()
			defer db.Rollback(tx)
			err := repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
				ProtocolVersion:   output.ProtocolVersion,
				ClaimID:           output.ClaimID(uuid.NewString()),
				Ref:               ref,
				ConsumerBindingID: "binding-ac14",
				RequestedAt:       output.NewTimestamp(time.Now()),
			})
			Expect(err).To(MatchError(output.ErrNotFound),
				"a consumer saw a result in the gap between reservation and registration")
		}

		It("is protected before the capture deadline, and no consumer result appears", func() {
			outcome, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionProtectedByReservation))
			claimIsRefused(orphan)
		})

		It("lets the fenced owner register the exact generation after the deadline", func() {
			hangarAgeCapture(capture, 48*time.Hour)

			// The owner's retry wins: it registers the exact generation the
			// inventory was looking at, and adoption then has nothing to do.
			capture.Ref = orphan
			nonce, issuedAt := hangarIssueChallenge(capture.HandoffID, capture.ReservationID, orphan)
			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.RegisterReceipt(ctx, tx, hangarAdmissionFor(capture.HandoffID,
				capture.Execution, capture.ReservationID, orphan, nonce, issuedAt))).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			outcome, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(output.AdoptionAlreadyRegistered))

			state, origin := lifecycleStateOf(orphan)
			Expect(state).To(Equal("registered"))
			Expect(origin).To(Equal("registered"))
		})

		It("permits marked-orphan adoption once the capture settles terminally", func() {
			hangarAgeCapture(capture, 48*time.Hour)

			// The other arm: the owner records terminal failure instead, and
			// then releases the source it held.
			tx := begin()
			defer db.Rollback(tx)
			Expect(repository.RecordTerminalCaptureFailure(ctx, tx,
				capture.ReservationID, 1, "seal_unconfirmed")).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			// Decided and unsettled: still not adoptable, because the source is
			// still held.
			outcome, err := adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).To(MatchError(output.ErrConflict))
			Expect(outcome).To(Equal(output.AdoptionCaptureNotSettled))
			claimIsRefused(orphan)

			hangarReleaseSource(ctx, repository, capture)

			outcome, err = adopt(hangarAdoptionFor(orphan, time.Now().Add(-long)))
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(output.AdoptionAdopted))
		})
	})

	// The Req 33 lifecycle boundary: adoption and a late receipt registration
	// are one winner, because both take the same exact-lifecycle lock.
	Describe("adoption and a late receipt registration serialize", func() {
		It("yields one lifecycle row whichever arrives first", func() {
			dbConn.SetMaxOpenConns(4)

			digest := hangarDigest(49)
			capture := hangarReserve(ctx, repository, digest,
				output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
			ref := hangar.TreeRef{Scope: "team-a", Digest: digest, Generation: 1725830823000049}
			hangarAgeCapture(capture, 48*time.Hour)

			nonce, issuedAt := hangarIssueChallenge(capture.HandoffID, capture.ReservationID, ref)

			// The registration opens first and holds the exact row.
			registering := begin()
			defer db.Rollback(registering)
			Expect(repository.RegisterReceipt(ctx, registering, hangarAdmissionFor(
				capture.HandoffID, capture.Execution, capture.ReservationID, ref,
				nonce, issuedAt))).To(Succeed())

			// The adoption arrives while it is open and blocks on the lock the
			// registration holds.
			adopted := make(chan struct {
				outcome output.AdoptionOutcome
				err     error
			}, 1)
			go func() {
				defer GinkgoRecover()
				outcome, err := adopt(hangarAdoptionFor(ref, time.Now().Add(-long)))
				adopted <- struct {
					outcome output.AdoptionOutcome
					err     error
				}{outcome, err}
			}()

			Consistently(adopted, 300*time.Millisecond).ShouldNot(Receive(),
				"adoption did not block on the exact-lifecycle lock the registration holds; "+
					"two writers over one generation is two records of one object")

			Expect(registering.Commit()).To(Succeed())

			var result struct {
				outcome output.AdoptionOutcome
				err     error
			}
			Eventually(adopted, 5*time.Second).Should(Receive(&result))
			Expect(result.err).NotTo(HaveOccurred())
			Expect(result.outcome).To(Equal(output.AdoptionAlreadyRegistered))

			Expect(countRows("hangar_exact_lifecycles", "generation = $1", ref.Generation)).
				To(Equal(1))
			state, origin := lifecycleStateOf(ref)
			Expect(state).To(Equal("registered"))
			Expect(origin).To(Equal("registered"))
		})
	})
})
