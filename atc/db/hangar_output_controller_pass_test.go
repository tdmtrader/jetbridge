package db_test

// The composition: the three controller passes, driven end to end against real
// PostgreSQL.
//
// This is where the Phase 7 review found four capabilities with no production
// caller at all -- AdmitReclaim, RecordOutOfBandAbsence, inventory.RecoverCursor
// and a dead classifier. Every one of them had specs. What none of them had was
// a spec that ran the thing a Pod runs, and a suite that drives a repository
// method directly cannot tell a wired capability from an unwired one.
//
// So these specs start at Pass.Run and finish by reading the database and the
// store. The store is the tier-1 memory substrate on purpose: what is under test
// here is the composition -- which record precedes which effect, what a pass
// does with an answer -- and the store contract itself is proved against a real
// GCS API server in hangar/output/conformance.

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/atc/hangaroutput/inventorypass"
	"github.com/concourse/concourse/atc/hangaroutput/reclaimpass"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

var _ = Describe("the output-plane controller passes", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
		store      *gcstest.Memory
		namespace  output.OutputNamespace
		transactor controller.Transactor
		owner      string
	)

	const deleteTimeout = 2 * time.Minute

	// A generation the plane says exists and the store really holds.
	//
	// The store comes FIRST and the lifecycle row is registered at the
	// generation the store handed back, because that is the order production
	// has: an object is created, and the receipt that names it follows. A
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
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
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
			SELECT violation FROM hangar_policy_violations
			 WHERE activation_epoch = 1 AND resolved_at IS NULL ORDER BY id`)
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
		var state string
		Expect(dbConn.QueryRow(`
			SELECT state FROM hangar_policy_snapshots WHERE activation_epoch = $1
			 ORDER BY observed_at DESC, id DESC LIMIT 1`, epoch).Scan(&state)).To(Succeed())

		return state
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

	// One kind, one owner. The lease is claimed once per kind and handed to
	// every pass of that kind, because a second owner is exactly what the
	// lease refuses -- and a fixture that claimed afresh each time would be
	// testing that refusal rather than the pass.
	leases := map[output.OperationKind]output.OperationLease{}
	leaseFor := func(kind output.OperationKind) output.OperationLease {
		GinkgoHelper()
		if held, ok := leases[kind]; ok {
			return held
		}
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		lease, err := repository.ClaimOperationLease(ctx, db.HangarOutputTx{Tx: tx},
			kind, 1, owner, output.MinLeaseTerm)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		leases[kind] = lease

		return lease
	}

	BeforeEach(func() {
		ctx = context.Background()
		owner = uuid.NewString()
		dbConn.SetMaxOpenConns(6)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("controller-pass-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)
		transactor = hangarLivenessTransactor{conn: dbConn}
		leases = map[output.OperationKind]output.OperationLease{}
		store = gcstest.NewMemory()

		namespace, err = output.DeriveNamespace(output.NamespaceConfig{
			Store:            output.StoreGCS,
			Bucket:           "output-bucket",
			DeploymentPrefix: "deployments/blue",
			TenantID:         "tenant-a",
			ActivationEpoch:  executioncontrol.ActivationEpoch(1),
		})
		Expect(err).NotTo(HaveOccurred())

		hangarActivateEpoch(ctx, repository)
	})

	newAdmission := func() *reclaimpass.AdmissionPass {
		return &reclaimpass.AdmissionPass{
			Repository: repository,
			Transactor: transactor,
			Grace:      output.DefaultPublicationGrace,
			Term:       output.LeaseTermFor(deleteTimeout),
			Batch:      10,
			OwnerID:    owner,
		}
	}

	newDeletes := func() *reclaimpass.DeletePass {
		role, err := reclaimer.New(namespace, reclaimer.Restrict(store))
		Expect(err).NotTo(HaveOccurred())

		return &reclaimpass.DeletePass{
			Reclaimer:     role,
			Repository:    repository,
			Transactor:    transactor,
			DeleteTimeout: deleteTimeout,
			Batch:         10,
			OwnerID:       owner,
			Term:          output.LeaseTermFor(deleteTimeout),
		}
	}

	newSweep := func() *inventorypass.Pass {
		sweep, err := inventory.New(namespace, inventory.Restrict(store),
			output.ClockFunc(func() time.Time { return time.Now().UTC() }))
		Expect(err).NotTo(HaveOccurred())

		return &inventorypass.Pass{
			Namespace:  namespace,
			Inventory:  sweep,
			Repository: repository,
			Transactor: transactor,
			Grace:      output.DefaultPublicationGrace,
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
			advanced, err := newDeletes().Run(ctx, leaseFor(output.OperationReclaimDelete))
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(0))
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))

			admitted, err := newAdmission().Run(ctx, leaseFor(output.OperationReclaimAdmission))
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1),
				"the admission pass admitted nothing for a published, settled generation whose "+
					"publication grace has elapsed and which nothing protects")
			Expect(lifecycleStateOf(ref)).To(Equal("reclaiming"),
				"admission did not mark the generation reclaiming durably before any external "+
					"delete")

			advanced, err = newDeletes().Run(ctx, leaseFor(output.OperationReclaimDelete))
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(1))

			Expect(lifecycleStateOf(ref)).To(Equal("reclaimed_confirmed"))
			Expect(store.Keys(namespace.Bucket())).NotTo(ContainElement(key),
				"the plane says the generation is reclaimed_confirmed and the object is still "+
					"in the bucket")
		})

		It("wakes the delete controller on the channel its admission notifies", func() {
			// Req 50's other half. The fallback is what makes work always
			// FOUND; this is what makes it found promptly, and it was absent:
			// there was no NOTIFY anywhere in the output plane, so a reclaim
			// job admitted a moment after a pass waited a whole interval for
			// the pass that would act on it.
			//
			// The listener is a real LISTEN on a real connection, and the
			// notification is issued inside the admitting transaction --
			// PostgreSQL delivers it only when that transaction commits, which
			// is what "after the work exists" has to mean.
			listener, err := pgx.Connect(ctx, postgresRunner.DataSourceName())
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = listener.Close(ctx) }()

			channel := output.NotifyChannel(output.OperationReclaimDelete)
			_, err = listener.Exec(ctx, "LISTEN "+channel)
			Expect(err).NotTo(HaveOccurred())

			ref := published(hangarDigest(88))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)

			admitted, err := newAdmission().Run(ctx, leaseFor(output.OperationReclaimAdmission))
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			waiting, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			notification, err := listener.WaitForNotification(waiting)
			Expect(err).NotTo(HaveOccurred(),
				"admitting a reclaim job notified nobody, so the controller that acts on it "+
					"waits a whole periodic interval for work that already exists")
			Expect(notification.Channel).To(Equal(channel),
				"the notification went to a channel no controller listens on, which is a "+
					"producer notifying nobody and fails silently")
		})

		It("leaves a generation inside its publication grace alone", func() {
			ref := published(hangarDigest(81))

			admitted, err := newAdmission().Run(ctx, leaseFor(output.OperationReclaimAdmission))
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(0))
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))
		})
	})

	Describe("the inventory sweep", func() {
		It("reports an adoption that FAILED rather than counting it as a refusal", func() {
			// The two are opposite facts and the pass told them apart by
			// asking Adopted() before asking err. AdoptManagedOrphan returns
			// ("", err) for a validation failure, a lock failure or a query
			// error, so every one of them arrived as "not adopted, nothing
			// wrong" -- and the page's debt arm, which exists for exactly this,
			// was unreachable.
			//
			// The failure is arranged by taking the connection away, which is
			// the shape of the real thing: the repository cannot say anything
			// about the object because it never got to look.
			key, err := hangar.TreeKey(namespace.Prefix(), "team-a", hangarDigest(87))
			Expect(err).NotTo(HaveOccurred())
			store.Seed(namespace.Bucket(), key, []byte("an orphan"), output.ObjectMarker{
				Version:         output.MarkerVersion,
				Scope:           "team-a",
				Digest:          hangarDigest(87),
				ReservationID:   output.ReservationID(uuid.NewString()),
				ActivationEpoch: 1,
				CreatedAt:       output.NewTimestamp(time.Now().UTC()),
			}.Metadata())

			// The control: the sweep adopts nothing here and says nothing is
			// wrong, because a marked object inside its publication grace is a
			// typed refusal about one object.
			lease := leaseFor(output.OperationInventory)
			sweep := newSweep()
			_, err = sweep.Run(ctx, lease)
			Expect(err).NotTo(HaveOccurred())

			// And now a failure, in the adoption transaction and nowhere else.
			// The pass reads the cursor first and commits the page last; only
			// the transaction in between is poisoned, so a pass that reports an
			// error here is reporting THIS one.
			broken := newSweep()
			broken.Transactor = &hangarPoisonedTransactor{conn: dbConn, poisonAt: 2}
			_, err = broken.Run(ctx, lease)
			Expect(err).NotTo(HaveOccurred(),
				"one object's failure stopped the page; the keys behind it are stranded")

			// The debt row is the whole assertion. It is the arm the pass has
			// for exactly this -- "recorded against the object and not fatal to
			// the page" -- and it was UNREACHABLE: AdoptManagedOrphan returns an
			// empty outcome for a validation, lock or query failure, so asking
			// Adopted() before asking err reported every one of them as a
			// refusal, and the arm's own comment described behaviour that never
			// happened.
			var reason, detail string
			Expect(dbConn.QueryRow(`
				SELECT reason, detail FROM hangar_inventory_debt
				 WHERE bucket_fingerprint = $1 AND object_key = $2`,
				namespace.Bucket(), key).Scan(&reason, &detail)).To(Succeed(),
				"an adoption that failed was recorded nowhere at all")
			Expect(reason).To(Equal(string(output.DebtStatFailure)))
			Expect(detail).NotTo(BeEmpty(),
				"the debt row does not say what went wrong")
		})

		It("recovers a corrupt cursor on a later pass rather than stalling on it forever", func() {
			// The cursor's own CHECK is what makes this shape unwritable, so
			// the fixture suspends exactly that constraint for the length of
			// the spec and restores it after. The suspension is the only way a
			// durable row can carry the shape at all, and the constraint is
			// proved on its own by the specs that write through the repository.
			//
			// The branch is therefore defence in depth rather than a state
			// today's schema can reach: a cursor corrupted by something the
			// CHECK does not encode -- a restored backup, a cohort at another
			// protocol version, a column a later migration adds -- still has to
			// be recovered rather than propagated, because a pass that
			// propagates it stalls that bucket for the life of the deployment.
			// That is the outcome Req 44 exists to prevent, and it is what this
			// pass did until the reachability guard found RecoverCursor had no
			// production caller at all.
			lease := leaseFor(output.OperationInventory)
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			_, err = repository.LoadInventoryCursor(ctx, db.HangarOutputTx{Tx: tx},
				namespace.Bucket(), 1, int64(lease.LeaseFence))
			Expect(err).NotTo(HaveOccurred())
			Expect(tx.Commit()).To(Succeed())

			_, err = dbConn.Exec(`ALTER TABLE hangar_inventory_cursors
				DROP CONSTRAINT hangar_cursor_generation_needs_key`)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				// NOT VALID, so the restore cannot itself fail on a row a
				// failing spec left behind: the guard is back on for every
				// write, which is what the specs after this one need.
				_, err := dbConn.Exec(`ALTER TABLE hangar_inventory_cursors
					ADD CONSTRAINT hangar_cursor_generation_needs_key
					CHECK (after_key <> '' OR after_generation = 0) NOT VALID`)
				Expect(err).NotTo(HaveOccurred())
			})

			_, err = dbConn.Exec(`
				UPDATE hangar_inventory_cursors SET after_key = '', after_generation = 4242
				 WHERE bucket_fingerprint = $1`, namespace.Bucket())
			Expect(err).NotTo(HaveOccurred())

			// The control: the cursor really is corrupt, and reading it really
			// does refuse. Without this the pass below could be succeeding
			// because nothing was ever wrong.
			tx, err = dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			_, loadErr := repository.LoadInventoryCursor(ctx, db.HangarOutputTx{Tx: tx},
				namespace.Bucket(), 1, int64(lease.LeaseFence))
			Expect(loadErr).To(MatchError(output.ErrCorrupt))
			Expect(tx.Rollback()).To(Succeed())

			_, err = newSweep().Run(ctx, lease)
			Expect(err).NotTo(HaveOccurred(),
				"the sweep propagated a corrupt cursor instead of recovering it; a cursor that "+
					"does not validate is a fact about the cursor, and a pass that stops on it "+
					"stalls that bucket for the life of the deployment")

			var afterKey string
			var afterGeneration int64
			Expect(dbConn.QueryRow(`
				SELECT after_key, after_generation FROM hangar_inventory_cursors
				 WHERE bucket_fingerprint = $1`, namespace.Bucket()).
				Scan(&afterKey, &afterGeneration)).To(Succeed())
			Expect(afterKey).To(BeEmpty())
			Expect(afterGeneration).To(BeZero())

			var reason string
			Expect(dbConn.QueryRow(`
				SELECT reason FROM hangar_inventory_debt WHERE bucket_fingerprint = $1`,
				namespace.Bucket()).Scan(&reason)).To(Succeed())
			Expect(reason).To(Equal(string(output.DebtCorruptCursor)),
				"the corruption was repaired and not recorded; the debt row is what stops a "+
					"cursor being silently reset every pass with nobody the wiser")
		})

		It("reports an exact generation the store no longer has as an out-of-band lifetime violation", func() {
			ref := published(hangarDigest(82))
			// A second, healthy generation, published BEFORE anything goes
			// wrong. It is what the admission probe below takes a claim on:
			// the one that vanished is terminal and would be refused for its
			// own reasons.
			healthy := published(hangarDigest(84))
			key, err := hangar.TreeKey(namespace.Prefix(), ref.Scope, ref.Digest)
			Expect(err).NotTo(HaveOccurred())

			// The control: while the object is there, the audit says nothing.
			sweep := newSweep()
			_, err = sweep.Run(ctx, leaseFor(output.OperationInventory))
			Expect(err).NotTo(HaveOccurred())
			Expect(lifecycleStateOf(ref)).To(Equal("registered"))

			// Somebody else's lifecycle rule, which is the case Req 52 names.
			// Nothing in this plane admitted a delete for it.
			Expect(store.ObjectToDelete(namespace.Bucket(), key).Delete(ctx)).To(Succeed())

			reconciled, err := sweep.Reconcile(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(reconciled).To(Equal(1))
			Expect(lifecycleStateOf(ref)).To(Equal("missing_out_of_band"),
				"an exact generation vanished with no admitted delete behind it and the plane "+
					"did not record a lifetime violation; absence rewritten as reclamation is "+
					"exactly how a bucket losing objects to somebody else looks like this "+
					"system working")

			// And the epoch, which is the half Req 52 is actually about.
			// Moving one lifecycle row says one object is gone; entering
			// at-risk is what stops the plane publishing into a bucket
			// something else is deleting from.
			Expect(openViolations()).To(ConsistOf(string(output.ViolationOutOfBandAbsence)))
			Expect(policyStateOf(1)).To(Equal("at_risk"))
			Expect(admissionRefusal(healthy)).To(ContainSubstring("at_risk"),
				"the plane went on admitting new protection beside an unexplained deleter")
		})

		It("stops reporting a healthy class once a job cannot be advanced", func() {
			ref := published(hangarDigest(85))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)

			admitted, err := newAdmission().Run(ctx, leaseFor(output.OperationReclaimAdmission))
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			// The control: with a store that answers, the pass is ok and the
			// count is one. Both halves matter -- the assertion below is that a
			// STUCK pass looks different, not that this pass ever looks bad.
			deletes := newDeletes()
			advanced, err := deletes.Run(ctx, leaseFor(output.OperationReclaimDelete))
			Expect(err).NotTo(HaveOccurred())
			Expect(advanced).To(Equal(1))

			// And now a plane whose store will not answer. A pass that reported
			// class=ok here is the exact state the Reporter's own doc says the
			// processed count exists to make distinguishable -- and the count
			// cannot say it, because a controller that did nothing and a
			// controller that is stuck both report zero.
			second := published(hangarDigest(86))
			ageRegistration(second, output.DefaultPublicationGrace+time.Hour)
			admitted, err = newAdmission().Run(ctx, leaseFor(output.OperationReclaimAdmission))
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			store.Inject(gcstest.Faults{DeleteTimeout: true})
			advanced, err = deletes.Run(ctx, leaseFor(output.OperationReclaimDelete))
			Expect(advanced).To(BeZero())
			Expect(err).To(HaveOccurred(),
				"a reclaim pass whose every job failed reported no error at all, so the "+
					"controller above it reports class=ok for an unreachable store")
			Expect(err).To(MatchError(output.ErrTimeout))
			Expect(lifecycleStateOf(second)).To(Equal("reclaiming"),
				"a job the store did not answer for was closed rather than left open")
		})

		It("puts the epoch at risk when the store refuses the reclaimer a delete it is configured to make", func() {
			ref := published(hangarDigest(83))
			ageRegistration(ref, output.DefaultPublicationGrace+time.Hour)

			admitted, err := newAdmission().Run(ctx, leaseFor(output.OperationReclaimAdmission))
			Expect(err).NotTo(HaveOccurred())
			Expect(admitted).To(Equal(1))

			// The store says 403 for the one call this principal exists to
			// make. That is Req 52's platform-principal mismatch arriving at
			// runtime rather than in an IAM reading: the matrix says what the
			// bindings claim, and this is the store saying otherwise.
			store.Inject(gcstest.Faults{Unauthorized: true})

			_, err = newDeletes().Run(ctx, leaseFor(output.OperationReclaimDelete))
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
			Expect(admissionRefusal(ref)).To(ContainSubstring("at_risk"),
				"the plane finalized one job and carried on admitting new work under an "+
					"identity the store had just refused")
		})
	})
})

// hangarPoisonedTransactor hands out real transactions, except for the one at
// poisonAt, which it rolls back before handing over.
//
// It is a transactor and not a fake repository on purpose: what has to be
// arranged is the case where the repository cannot say anything about the
// object at all -- a lock failure, a query error -- and everything below this
// is the production repository against real PostgreSQL, failing for a real
// reason.
//
// Exactly ONE transaction is poisoned, and that precision is the point. If the
// pass's own commit failed too, the pass would report an error either way and
// the spec would be green whether or not adoption errors are surfaced.
type hangarPoisonedTransactor struct {
	conn     db.DbConn
	poisonAt int
	handed   int
}

func (transactor *hangarPoisonedTransactor) Begin() (controller.Transaction, error) {
	transactor.handed++
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}
	if transactor.handed == transactor.poisonAt {
		_ = tx.Rollback()
	}

	return db.HangarOutputTx{Tx: tx}, nil
}
