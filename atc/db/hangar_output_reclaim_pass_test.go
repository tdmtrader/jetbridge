package db_test

// The web's reclaim pass, driven end to end against real PostgreSQL: the
// candidate query, the hold under the locks, the conditional delete, and the
// stamp, reading the database and the store afterwards.
//
// The store is the tier-1 memory substrate on purpose: what is under test here
// is the composition -- what the pass does with each answer the store gives,
// and what it leaves behind -- and the store contract itself is proved against
// a real GCS API server in hangar/output/conformance.

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/hangaroutput/reclaim"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

var _ = Describe("the web's reclaim pass", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		store      *gcstest.Memory
		namespace  output.OutputNamespace
		transactor hangaroutput.Transactor
	)

	const deleteTimeout = 2 * time.Minute

	// A generation the plane says exists and the store really holds.
	//
	// The store comes FIRST and the lifecycle row is registered at the
	// generation the store handed back, because that is the order production
	// has: an object is created, and the publication that names it follows. A
	// fixture that chose the generation itself would be a fixture in which the
	// two could never disagree.
	published := func(digest hangar.Digest) hangar.TreeRef {
		GinkgoHelper()
		key, err := hangar.TreeKey(namespace.Prefix(), "team-a", digest)
		Expect(err).NotTo(HaveOccurred())
		attrs := store.Seed(namespace.Bucket(), key, []byte("published tree"),
			output.ObjectMarker{
				Scope:         "team-a",
				Digest:        digest,
				ReservationID: output.ReservationID(uuid.NewString()),
				CreatedAt:     output.NewTimestamp(time.Now().UTC()),
			}.Metadata())

		capture := hangarPublishAt(ctx, repository, digest, attrs.Generation,
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)
		hangarReleaseSource(ctx, repository, capture)
		hangarAgeCapture(capture, 48*time.Hour)

		return capture.Ref
	}

	// reclaimable is published with its grace elapsed on the database clock,
	// arranged by moving the row into the past rather than by waiting eight
	// days.
	reclaimable := func(digest hangar.Digest) hangar.TreeRef {
		GinkgoHelper()
		ref := published(digest)
		hangarAgePublication(ref, hangarGraceElapsed)

		return ref
	}

	keyOf := func(ref hangar.TreeRef) string {
		GinkgoHelper()
		key, err := hangar.TreeKey(namespace.Prefix(), ref.Scope, ref.Digest)
		Expect(err).NotTo(HaveOccurred())

		return key
	}

	// openViolations is what is still unreconciled, read straight out of the
	// table rather than through the repository: the assertion is about the row
	// that survives the next attestation.
	openViolations := func() []string {
		GinkgoHelper()
		rows, err := dbConn.Query(`
			SELECT violation FROM hangar_integrity_findings
			 WHERE resolved_at IS NULL ORDER BY id`)
		Expect(err).NotTo(HaveOccurred())
		defer db.Close(rows)

		var violations []string
		for rows.Next() {
			var violation string
			Expect(rows.Scan(&violation)).To(Succeed())
			violations = append(violations, violation)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())

		return violations
	}

	// admissionRefusal asks the GATE rather than the findings table: a row
	// nothing reads is not a plane that stopped. The claim is taken on a
	// healthy registered generation, so nothing but the gate can refuse it,
	// and the refusal arrives at COMMIT because the trigger is deferred.
	admissionRefusal := func(ref hangar.TreeRef) string {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		Expect(hangarAcquireClaim(ctx, repository, db.HangarOutputTx{Tx: tx},
			output.ClaimID(uuid.NewString()), ref, "binding-at-risk")).To(Succeed())

		commitErr := tx.Commit()
		if commitErr == nil {
			return ""
		}

		return commitErr.Error()
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

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(6)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("controller-pass-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)
		transactor = hangarComponentTransactor{conn: dbConn}
		store = gcstest.NewMemory()

		namespace, err = output.DeriveNamespace(output.NamespaceConfig{
			Store:            output.StoreGCS,
			Bucket:           "output-bucket",
			DeploymentPrefix: "deployments/blue",
			TenantID:         "tenant-a",
		})
		Expect(err).NotTo(HaveOccurred())

		// THE BUCKET EXISTS, because a deployment's does. The tier-1 store
		// started modelling bucket existence when a missing bucket stopped
		// being indistinguishable from an empty one (GCS-F2), and a fixture
		// that never creates one makes every delete over a bucket with no
		// objects in it yet answer "bucket not found" -- which is a true
		// answer to a question this fixture did not mean to ask.
		store.CreateBucket(namespace.Bucket())

		hangarActivateEpoch(ctx, repository)
	})

	newPass := func() *reclaim.Pass {
		GinkgoHelper()
		role, err := reclaimer.New(namespace, reclaimer.Restrict(store))
		Expect(err).NotTo(HaveOccurred())

		return &reclaim.Pass{
			Transactor:    transactor,
			Repository:    repository,
			Reclaimer:     role,
			Grace:         output.DefaultPublicationGrace,
			DeleteTimeout: deleteTimeout,
			Batch:         10,
		}
	}

	Describe("reclamation, end to end", func() {
		It("deletes a published, settled, grace-elapsed generation and stamps it reclaimed", func() {
			ref := reclaimable(hangarDigest(80))
			Expect(store.Keys(namespace.Bucket())).To(ContainElement(keyOf(ref)))

			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{1, 0, 0}),
				"the pass reclaimed nothing for a published, settled generation whose "+
					"publication grace has elapsed and which nothing protects")

			Expect(reclaimedAt(ref).Valid).To(BeTrue())
			Expect(store.Keys(namespace.Bucket())).NotTo(ContainElement(keyOf(ref)),
				"the plane says the generation is reclaimed and the object is still in the bucket")

			// Nothing left to do: a second pass is empty, not a second delete.
			reclaimed, deferred, failed, err = newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{0, 0, 0}))
		})

		It("leaves a generation inside its publication grace, or under a live claim, alone", func() {
			inGrace := published(hangarDigest(81))
			claimed := reclaimable(hangarDigest(82))
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			Expect(hangarAcquireClaim(ctx, repository, tx, output.ClaimID(uuid.NewString()), claimed,
				"binding-live")).To(Succeed())
			Expect(db.HangarOutputTx{Tx: tx}.Commit()).To(Succeed())

			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{0, 0, 0}))
			Expect(reclaimedAt(inGrace).Valid).To(BeFalse())
			Expect(reclaimedAt(claimed).Valid).To(BeFalse())
			Expect(store.Keys(namespace.Bucket())).To(ConsistOf(keyOf(inGrace), keyOf(claimed)))
		})

		It("defers every delete while an unexplained absence is open, and resumes once it is resolved", func() {
			// A fresh reading reopens captures and claims, because a
			// twenty-minute network problem must not need a human before the
			// plane resumes. It does NOT reopen deletion while a finding says
			// something else may be removing this bucket's objects: resuming
			// there is how a plane finishes a job something else started.
			gone := reclaimable(hangarDigest(89))
			candidate := reclaimable(hangarDigest(90))
			// A third, untouched generation for the admission probe. The probe
			// takes a CLAIM, and a claim is one of the pass's exclusions --
			// probing on the candidate would protect the very generation this
			// spec is about, for the wrong reason.
			probe := published(hangarDigest(91))

			// Something else removed it: a read found it absent, and recorded
			// the integrity finding that is.
			Expect(db.HangarAbsences{Conn: dbConn}.RecordUnexpectedAbsence(ctx, gone)).To(Succeed())
			Expect(openViolations()).To(ConsistOf(string(output.ViolationOutOfBandAbsence)))
			Expect(admissionRefusal(probe)).To(ContainSubstring("storage integrity"))

			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred(),
				"a refused hold is a fact about the plane, not a failed pass")
			Expect(reclaimed).To(BeZero(),
				"the plane resumed deleting objects beside an unexplained deleter")
			Expect(deferred).To(Equal(2))
			Expect(failed).To(BeZero())
			Expect(reclaimedAt(candidate).Valid).To(BeFalse())
			Expect(store.Keys(namespace.Bucket())).To(ContainElement(keyOf(candidate)))

			// Resolved, and deletion resumes. Without this the refusal above
			// could be a plane that had simply stopped reclaiming. The
			// generation that was found missing is absent in the store, and
			// absent is reclaimed: its exact generation is gone either way.
			var finding int64
			Expect(dbConn.QueryRow(`SELECT id FROM hangar_integrity_findings
				WHERE resolved_at IS NULL AND violation = 'out_of_band_absence'`).Scan(&finding)).To(Succeed())
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.ResolveIntegrityFinding(ctx, db.HangarOutputTx{Tx: tx}, finding)).To(Succeed())
			Expect(tx.Commit()).To(Succeed())
			Expect(store.DeleteExact(ctx, namespace.Bucket(), keyOf(gone), gone.Generation)).To(Succeed())

			reclaimed, deferred, failed, err = newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{2, 0, 0}))
			Expect(reclaimedAt(candidate).Valid).To(BeTrue())
			Expect(reclaimedAt(gone).Valid).To(BeTrue())
			Expect(openViolations()).To(BeEmpty())
		})

		It("stamps a generation the store already lost as reclaimed", func() {
			// The object API cannot tell "gone because we deleted it and lost
			// the answer" from "gone because something else removed it": a
			// 404 is a 404. The exact generation is gone either way, and a
			// registered row for an object that is not there is a row the
			// pass retries forever; the stamp is what ends that. A loss that
			// matters is reported by the READ that found it, as the finding
			// the spec above waits on.
			ref := reclaimable(hangarDigest(87))
			Expect(store.DeleteExact(ctx, namespace.Bucket(), keyOf(ref), ref.Generation)).To(Succeed())

			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{1, 0, 0}))
			Expect(reclaimedAt(ref).Valid).To(BeTrue())
			Expect(openViolations()).To(BeEmpty())
		})

		It("stamps a generation another object has replaced as reclaimed, and leaves the replacement", func() {
			ref := reclaimable(hangarDigest(88))
			replacement := store.Seed(namespace.Bucket(), keyOf(ref), []byte("someone else's tree"), nil)
			Expect(replacement.Generation).NotTo(Equal(ref.Generation))

			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{1, 0, 0}))
			Expect(reclaimedAt(ref).Valid).To(BeTrue())

			current, err := store.StatCurrent(ctx, namespace.Bucket(), keyOf(ref))
			Expect(err).NotTo(HaveOccurred())
			Expect(current.Generation).To(Equal(replacement.Generation),
				"a refused conditional delete was retried, and the only retry available to it "+
					"would be a broader one")
		})

		It("leaves a generation registered when the store does not answer, and reports the pass failed", func() {
			ref := reclaimable(hangarDigest(85))

			// The control: with a store that answers, the pass is ok and the
			// count is one. Both halves matter -- the assertion below is that a
			// STUCK pass looks different, not that this pass ever looks bad.
			reclaimed, _, failed, err := newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(reclaimed).To(Equal(1))
			Expect(failed).To(BeZero())
			Expect(reclaimedAt(ref).Valid).To(BeTrue())

			// And now a plane whose store will not answer. A pass that reported
			// no error here is the exact state a controller cannot tell from
			// one that had nothing to do.
			second := reclaimable(hangarDigest(86))
			store.Inject(gcstest.Faults{DeleteTimeout: true})
			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{0, 0, 1}))
			Expect(err).To(MatchError(output.ErrTimeout),
				"a reclaim pass whose every delete failed reported no error at all, so the "+
					"controller above it reports class=ok for an unreachable store")
			Expect(reclaimedAt(second).Valid).To(BeFalse(),
				"a generation the store did not answer for was stamped reclaimed on a guess")

			// It really did land and the answer was lost: the next pass finds
			// it absent and stamps it then, with nothing to reconcile.
			store.Inject(gcstest.Faults{})
			Expect(store.DeleteExact(ctx, namespace.Bucket(), keyOf(second), second.Generation)).To(Succeed())
			reclaimed, deferred, failed, err = newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{1, 0, 0}))
			Expect(reclaimedAt(second).Valid).To(BeTrue())
			Expect(openViolations()).To(BeEmpty())
		})

		It("records a principal denial when the store refuses the reclaimer a delete it is configured to make", func() {
			ref := reclaimable(hangarDigest(83))

			// The store says 403 for the one call this principal exists to
			// make. That is the platform-principal mismatch arriving at
			// runtime rather than in an IAM reading: the matrix says what the
			// bindings claim, and this is the store saying otherwise.
			store.Inject(gcstest.Faults{Unauthorized: true})

			reclaimed, deferred, failed, err := newPass().Reclaim(ctx)
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{0, 0, 1}))
			Expect(err).To(MatchError(output.ErrUnauthorized),
				"a reclaimer whose store refused it the one call its role exists to make "+
					"reported a healthy pass")

			Expect(reclaimedAt(ref).Valid).To(BeFalse(),
				"a generation the plane could not delete was stamped reclaimed; an "+
					"unauthorized delete goes back to being protected rather than being "+
					"deleted on a guess")
			Expect(openViolations()).To(ConsistOf(string(output.ViolationRuntimePrincipalDenied)))
			Expect(admissionRefusal(ref)).To(ContainSubstring("storage integrity"),
				"the plane carried on admitting new work under an identity the store had just "+
					"refused")

			// And the finding holds the next pass too, with the store healthy
			// again, until an operator resolves it.
			store.Inject(gcstest.Faults{})
			reclaimed, deferred, failed, err = newPass().Reclaim(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect([]int{reclaimed, deferred, failed}).To(Equal([]int{0, 1, 0}))
			Expect(reclaimedAt(ref).Valid).To(BeFalse())
			Expect(store.Keys(namespace.Bucket())).To(ConsistOf(keyOf(ref)))
		})
	})
})

// hangarComponentTransactor adapts the suite's connection to the ATC-side
// component's port.
type hangarComponentTransactor struct{ conn db.DbConn }

func (transactor hangarComponentTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	return db.HangarOutputTx{Tx: tx}, nil
}
