package db_test

// The web's reclaim pass, driven end to end against real PostgreSQL: admission,
// the conditional delete, and finalization, reading the database and the store
// afterwards.
//
// The store is the tier-1 memory substrate on purpose: what is under test here
// is the composition -- which record precedes which effect, what a pass does
// with an answer -- and the store contract itself is proved against a real GCS
// API server in hangar/output/conformance.

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/hangaroutput/reclaim"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
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
		owner      string
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
				Version:         output.MarkerVersion,
				Scope:           "team-a",
				Digest:          digest,
				ReservationID:   output.ReservationID(uuid.NewString()),
				ActivationEpoch: 1,
				CreatedAt:       output.NewTimestamp(time.Now().UTC()),
			}.Metadata())

		capture := hangarPublishAt(ctx, repository, digest, attrs.Generation,
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)
		hangarReleaseSource(ctx, repository, capture)
		hangarAgeCapture(capture, 48*time.Hour)

		return capture.Ref
	}

	// Grace measured on the database clock, arranged by moving the row into the
	// past rather than by waiting eight days.
	ageRegistration := func(ref hangar.TreeRef, by time.Duration) {
		GinkgoHelper()
		_, err := dbConn.Exec(`
			UPDATE hangar_exact_lifecycles SET registered_at = registered_at - $4::interval
			 WHERE scope = $1 AND digest = $2 AND generation = $3`,
			string(ref.Scope), string(ref.Digest), ref.Generation,
			by.Round(time.Second).String())
		Expect(err).NotTo(HaveOccurred())
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

	policyStateOf := func(epoch int64) string {
		GinkgoHelper()
		var count int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM hangar_integrity_findings WHERE resolved_at IS NULL AND violation IN ('out_of_band_absence', 'runtime_principal_denied')`).Scan(&count)).To(Succeed())
		if count > 0 {
			return "at_risk"
		}
		return "healthy"
	}

	// admissionRefusal asks the GATE rather than the snapshot table: a state
	// column nothing reads is not a plane that stopped. The claim is taken on a
	// healthy registered generation, so nothing but the policy gate can refuse
	// it, and the refusal arrives at COMMIT because the trigger is deferred.
	admissionRefusal := func(ref hangar.TreeRef) string {
		GinkgoHelper()
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		Expect(repository.AcquireClaim(ctx, db.HangarOutputTx{Tx: tx}, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           output.ClaimID(uuid.NewString()),
			Ref:               ref,
			ConsumerBindingID: "binding-at-risk",
			RequestedAt:       output.NewTimestamp(time.Now()),
		})).To(Succeed())

		commitErr := tx.Commit()
		if commitErr == nil {
			return ""
		}

		return commitErr.Error()
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

	BeforeEach(func() {
		ctx = context.Background()
		owner = uuid.NewString()
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
			ActivationEpoch:  executioncontrol.ActivationEpoch(1),
		})
		Expect(err).NotTo(HaveOccurred())

		// THE BUCKET EXISTS, because a deployment's does. The tier-1 store
		// started modelling bucket existence when a missing bucket stopped
		// being indistinguishable from an empty one (GCS-F2), and a fixture
		// that never creates one makes every sweep over a bucket with no
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
			OwnerID:       owner,
		}
	}

	Describe("reclamation, end to end", func() {
		It("collects a published, settled, grace-elapsed generation without anyone creating a job by hand", func() {
			ref := published(hangarDigest(80))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)
			key, err := hangar.TreeKey(namespace.Prefix(), ref.Scope, ref.Digest)
			Expect(err).NotTo(HaveOccurred())
			Expect(store.Keys(namespace.Bucket())).To(ContainElement(key))

			// The control: the delete pass alone does nothing, because
			// admission is what fills its queue. This is the state the phase
			// shipped in -- a reclaimer draining a queue nothing filled,
			// reporting 0/ok forever.
			advanced, _, err := newPass().DeleteDue(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(0))
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))

			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1),
				"the admission pass admitted nothing for a published, settled generation whose "+
					"publication grace has elapsed and which nothing protects")
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"),
				"admission did not mark the generation reclaiming durably before any external "+
					"delete")

			advanced, _, err = newPass().DeleteDue(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(1))

			Expect(lifecycleStateOf(ref)).To(Equal("reclaimed_confirmed"))
			Expect(store.Keys(namespace.Bucket())).NotTo(ContainElement(key),
				"the plane says the generation is reclaimed_confirmed and the object is still "+
					"in the bucket")
		})

		It("waits for reconciliation before admitting another delete beside an unexplained one", func() {
			// R1-F15's ruling, in the half where the blip reading is wrong. A
			// fresh safe attestation reopens captures, claims and warrants,
			// because a twenty-minute network problem must not need a human
			// before the plane resumes. It does NOT reopen deletion while a
			// violation says something else may be removing this bucket's
			// objects: resuming there is how a plane finishes a job something
			// else started.
			gone := published(hangarDigest(89))
			candidate := published(hangarDigest(90))
			// A third, untouched generation for the admission probe. The probe
			// takes a CLAIM, and a claim is one of reclaim admission's
			// exclusions -- probing on the candidate would block the very
			// admission this spec is about, for the wrong reason.
			probe := published(hangarDigest(91))
			ageRegistration(candidate, output.DefaultPublicationGrace+time.Hour)

			// Something else removed it: an absence nothing this plane did
			// explains, recorded as the integrity finding it is.
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.RecordOutOfBandAbsence(ctx, db.HangarOutputTx{Tx: tx}, gone)).To(Succeed())
			Expect(tx.Commit()).To(Succeed())
			Expect(openViolations()).To(ContainElement(string(output.ViolationOutOfBandAbsence)))

			Expect(policyStateOf(1)).To(Equal("at_risk"))
			Expect(admissionRefusal(probe)).NotTo(BeEmpty())

			// And reclaim admission, which is the half that waits.
			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred(),
				"a refused admission is a fact about the epoch, not a failed pass")
			Expect(admitted).To(BeZero(),
				"the plane resumed deleting objects beside an unexplained deleter after nothing "+
					"more than a fresh reading")
			Expect(lifecycleStateOf(candidate)).To(Equal("registered"))

			// Reconciled, and deletion resumes. Without this the refusal above
			// could be a plane that had simply stopped reclaiming.
			var finding int64
			Expect(dbConn.QueryRow(`SELECT id FROM hangar_integrity_findings
				WHERE resolved_at IS NULL AND violation = 'out_of_band_absence'`).Scan(&finding)).To(Succeed())
			tx, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			Expect(repository.ResolveIntegrityFinding(ctx, db.HangarOutputTx{Tx: tx}, finding)).To(Succeed())
			Expect(tx.Commit()).To(Succeed())

			admitted, err = newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))
			Expect(lifecycleStateOf(candidate)).To(Equal("reclaiming"))
		})

		It("leaves a generation inside its publication grace alone", func() {
			ref := published(hangarDigest(81))

			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(0))
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))
		})
		It("stops reporting a healthy class once a job cannot be advanced", func() {
			ref := published(hangarDigest(85))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)

			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			// The control: with a store that answers, the pass is ok and the
			// count is one. Both halves matter -- the assertion below is that a
			// STUCK pass looks different, not that this pass ever looks bad.
			deletes := newPass()
			advanced, _, err := deletes.DeleteDue(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(1))

			// And now a plane whose store will not answer. A pass that reported
			// class=ok here is the exact state the Reporter's own doc says the
			// processed count exists to make distinguishable -- and the count
			// cannot say it, because a controller that did nothing and a
			// controller that is stuck both report zero.
			second := published(hangarDigest(86))
			ageRegistration(second, output.DefaultPublicationGrace+time.Hour)
			admitted, err = newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			store.Inject(gcstest.Faults{DeleteTimeout: true})
			advanced, _, err = deletes.DeleteDue(ctx)
			Expect(advanced).To(BeZero())
			Expect(err).To(HaveOccurred(),
				"a reclaim pass whose every job failed reported no error at all, so the "+
					"controller above it reports class=ok for an unreachable store")
			Expect(err).To(MatchError(output.ErrTimeout))
			Expect(lifecycleStateOf(second)).To(Equal("reclaiming"),
				"a job the store did not answer for was closed rather than left open")
		})

		It("refuses to call an absence it never deleted its own successful deletion", func() {
			// GCS-F2. The object API cannot tell "gone because we deleted it
			// and lost the answer" from "gone because something else removed
			// it" -- a 404 is a 404, and a delete in a bucket that does not
			// exist at all answers already_absent too. The distinction is not
			// in the store's answer and never can be; it is in this job's own
			// attempt history, which is why it is decided here.
			//
			// Before this, the pass inferred on the FIRST attempt, because the
			// attempt row was committed microseconds before the call and the
			// schema asked only whether one existed. "A prior admitted delete"
			// was true by construction, so a wrong --output-prefix or a deleted
			// bucket finalized every registered generation as this plane's own
			// successful deletions, with the epoch never leaving healthy.
			ref := published(hangarDigest(87))
			healthy := published(hangarDigest(91))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)
			key, err := hangar.TreeKey(namespace.Prefix(), ref.Scope, ref.Digest)
			Expect(err).NotTo(HaveOccurred())

			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			// Somebody else's deletion, between the admission and the delete.
			// This plane has admitted a job and made no call at all.
			current, statErr := store.StatCurrent(ctx, namespace.Bucket(), key)
			Expect(statErr).NotTo(HaveOccurred())
			Expect(store.DeleteExact(ctx, namespace.Bucket(), key, current.Generation)).To(Succeed())

			_, _, err = newPass().DeleteDue(ctx)
			Expect(err).To(MatchError(output.ErrAtRisk),
				"a pass that found its object already gone on its FIRST attempt reported a "+
					"healthy reclamation")

			Expect(lifecycleStateOf(ref)).NotTo(Equal("reclaimed_inferred"),
				"the plane claimed a deletion it never made: nothing it did can explain this "+
					"absence, and inferring from an attempt whose response never went missing "+
					"is inferring from the attempt it just wrote itself")
			Expect(lifecycleStateOf(ref)).To(Equal("missing_out_of_band"))
			Expect(openViolations()).To(ConsistOf(string(output.ViolationOutOfBandAbsence)))
			Expect(policyStateOf(1)).To(Equal("at_risk"))
			Expect(admissionRefusal(healthy)).To(ContainSubstring("storage integrity"),
				"the plane went on admitting new protection into a bucket whose objects are "+
					"disappearing")
		})

		It("still infers a deletion whose response it really did lose", func() {
			// The other side of the branch above, and the thing that stops it
			// being "never infer anything". The attempt that timed out is what
			// makes the later absence this plane's own: a delete WAS issued
			// and its answer never arrived, which is the exact state that
			// is inferred rather than confirmed.
			ref := published(hangarDigest(92))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)
			key, err := hangar.TreeKey(namespace.Prefix(), ref.Scope, ref.Digest)
			Expect(err).NotTo(HaveOccurred())

			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			// The delete lands and the answer is lost. The job stays open.
			store.Inject(gcstest.Faults{DeleteTimeout: true})
			_, _, err = newPass().DeleteDue(ctx)
			Expect(err).To(MatchError(output.ErrTimeout))
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"))

			// It really did land: the object is gone, and the next pass finds
			// it absent with a lost response of its own behind it.
			store.Inject(gcstest.Faults{})
			current, statErr := store.StatCurrent(ctx, namespace.Bucket(), key)
			Expect(statErr).NotTo(HaveOccurred())
			Expect(store.DeleteExact(ctx, namespace.Bucket(), key, current.Generation)).To(Succeed())

			advanced, _, err := newPass().DeleteDue(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(1))
			Expect(lifecycleStateOf(ref)).To(Equal("reclaimed_inferred"))
			Expect(openViolations()).To(BeEmpty(),
				"a deletion this plane issued and lost the answer to was reported as somebody "+
					"else removing the object")
			Expect(policyStateOf(1)).NotTo(Equal("at_risk"))
		})

		It("puts the epoch at risk when the store refuses the reclaimer a delete it is configured to make", func() {
			ref := published(hangarDigest(83))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)

			admitted, err := newPass().Admit(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			// The store says 403 for the one call this principal exists to
			// make. That is the platform-principal mismatch arriving at
			// runtime rather than in an IAM reading: the matrix says what the
			// bindings claim, and this is the store saying otherwise.
			store.Inject(gcstest.Faults{Unauthorized: true})

			_, _, err = newPass().DeleteDue(ctx)
			Expect(err).To(MatchError(output.ErrUnauthorized),
				"a reclaimer whose store refused it the one call its role exists to make "+
					"reported a healthy pass")

			Expect(lifecycleStateOf(ref)).To(Equal("registered"),
				"a generation the plane could not delete was left reclaiming or worse; an "+
					"unauthorized delete goes back to being protected rather than being "+
					"deleted on a guess")
			Expect(openViolations()).
				To(ConsistOf(string(output.ViolationRuntimePrincipalDenied)))
			Expect(policyStateOf(1)).To(Equal("at_risk"))
			Expect(admissionRefusal(ref)).To(ContainSubstring("storage integrity"),
				"the plane finalized one job and carried on admitting new work under an "+
					"identity the store had just refused")
		})
	})
})
