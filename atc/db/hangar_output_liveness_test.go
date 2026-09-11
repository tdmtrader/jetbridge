package db_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/output"
)

// Bounded-worker liveness, with every operation queue continuously busy.
//
// Real PostgreSQL and real concurrent controllers, because "all queues busy" is
// concurrency by definition and because the guarantee under test is about
// leases and cursors that outlive a process. What is proved here is the
// negative that matters: no kind can be starved by another kind's backlog, a
// controller that loses its lease does not spin reporting failures, and a
// restart plus a missed notification still finds the work -- because every
// worker has a nonzero periodic wake and not only a NOTIFY.
var _ = Describe("the bounded output-plane workers", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(12)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("liveness-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	newRunner := func(kind output.OperationKind, pass controller.Pass) *controller.Runner {
		return &controller.Runner{
			Kind:            kind,
			ActivationEpoch: 1,
			OwnerID:         uuid.NewString(),
			Term:            output.MinLeaseTerm,
			Transactor:      hangarLivenessTransactor{conn: dbConn},
			Leases:          repository,
			Pass:            pass,
		}
	}

	It("never lets one kind's backlog starve another", func() {
		// Every kind, busy at once, each doing a bounded unit per pass. The
		// assertion is that EVERY kind makes progress in the same window --
		// not that the total is high. A shared lease, a shared cursor or a
		// single queue would show up as one kind at zero.
		var mutex sync.Mutex
		passes := map[output.OperationKind]int{}

		runners := make([]*controller.Runner, 0, len(output.OperationKinds()))
		for _, kind := range output.OperationKinds() {
			kind := kind
			runners = append(runners, newRunner(kind, controller.PassFunc(
				func(context.Context, output.OperationLease) (int, error) {
					mutex.Lock()
					defer mutex.Unlock()
					passes[kind]++

					// Every unit is bounded. A pass that drove its whole
					// backlog would hold every other kind behind whatever its
					// slowest item is.
					return 1, nil
				})))
		}

		var group sync.WaitGroup
		for _, runner := range runners {
			runner := runner
			group.Add(1)
			go func() {
				defer GinkgoRecover()
				defer group.Done()
				for pass := 0; pass < 5; pass++ {
					Expect(runner.Run(ctx)).To(Succeed())
				}
			}()
		}
		group.Wait()

		mutex.Lock()
		defer mutex.Unlock()
		for _, kind := range output.OperationKinds() {
			Expect(passes[kind]).To(Equal(5),
				fmt.Sprintf("the %s kind ran %d of 5 passes while every other kind was busy; "+
					"one operation cannot advance another's work and cannot starve it either",
					kind, passes[kind]))
		}

		// And every kind owns its own lease row, which is what makes the above
		// true rather than lucky.
		var kinds int
		Expect(dbConn.QueryRow(`
			SELECT count(DISTINCT kind) FROM hangar_operation_leases WHERE activation_epoch = 1`).
			Scan(&kinds)).To(Succeed())
		Expect(kinds).To(Equal(len(output.OperationKinds())))
	})

	It("gives one kind to one owner and lets the loser keep waking without failing", func() {
		ran := 0
		first := newRunner(output.OperationInventory, controller.PassFunc(
			func(context.Context, output.OperationLease) (int, error) {
				ran++

				return 1, nil
			}))
		loserRan := 0
		second := newRunner(output.OperationInventory, controller.PassFunc(
			func(context.Context, output.OperationLease) (int, error) {
				loserRan++

				return 1, nil
			}))

		Expect(first.Run(ctx)).To(Succeed())
		Expect(ran).To(Equal(1))

		// The loser wakes, finds it is not the owner, and reports success. A
		// controller that treated "somebody else has the lease" as a failure
		// would make a correctly configured pair of replicas look broken every
		// minute of their lives.
		for pass := 0; pass < 3; pass++ {
			Expect(second.Run(ctx)).To(Succeed())
		}
		Expect(loserRan).To(Equal(0),
			"the runner that does not hold the lease still did work")
		Expect(second.Holds()).To(BeFalse())
		Expect(first.Holds()).To(BeTrue())
	})

	It("forgets a lease it has been told it lost, rather than renewing under a dead fence", func() {
		runner := newRunner(output.OperationReclaimDelete, controller.PassFunc(
			func(context.Context, output.OperationLease) (int, error) { return 1, nil }))
		Expect(runner.Run(ctx)).To(Succeed())
		Expect(runner.Holds()).To(BeTrue())

		// A takeover while this runner was asleep: the lease expires on the
		// database clock and somebody else claims it, advancing the fence.
		_, err := dbConn.Exec(`
			UPDATE hangar_operation_leases
			   SET renewed_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
			 WHERE kind = 'reclaim_delete' AND activation_epoch = 1`)
		Expect(err).NotTo(HaveOccurred())

		other := newRunner(output.OperationReclaimDelete, controller.PassFunc(
			func(context.Context, output.OperationLease) (int, error) { return 1, nil }))
		Expect(other.Run(ctx)).To(Succeed())
		Expect(other.Holds()).To(BeTrue())

		// The old owner wakes. Its renewal is refused, and it must not hold on
		// to the lease it no longer has: a runner that kept it would present a
		// fence somebody else advanced on every write it made.
		Expect(runner.Run(ctx)).To(Succeed())
		Expect(runner.Holds()).To(BeFalse(),
			"a fenced-out controller kept the lease it was told it had lost")
	})

	It("finds work after a restart with no notification at all", func() {
		// A controller that only woke on NOTIFY would strand this: the work was
		// created before the process existed, so there is no notification left
		// to receive. The periodic wake is what makes the restart find it.
		capture := hangarPublishAt(ctx, repository, hangarDigest(90), 1725830823000090,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		hangarReleaseSource(ctx, repository, capture)
		hangarAgeCapture(capture, 48*time.Hour)

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(repository.AdmitReclaim(ctx, db.HangarOutputTx{Tx: tx}, capture.Ref,
			uuid.NewString(), 1, output.MinLeaseTerm)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		found := 0
		restarted := newRunner(output.OperationReclaimDelete, controller.PassFunc(
			func(ctx context.Context, lease output.OperationLease) (int, error) {
				tx, err := dbConn.Begin()
				if err != nil {
					return 0, err
				}
				defer db.Rollback(tx)
				jobs, err := repository.DueReclaimJobs(ctx, db.HangarOutputTx{Tx: tx}, 10)
				if err != nil {
					return 0, err
				}
				found = len(jobs)

				return len(jobs), nil
			}))

		Expect(restarted.Run(ctx)).To(Succeed())
		Expect(found).To(Equal(1),
			"a restarted controller did not find work that was created before it started")
	})

	It("has a nonzero wake no slower than a minute, whatever it is configured with", func() {
		// component.Runner with a zero interval wakes only on NOTIFY, so a zero
		// here is a worker that a missed notification silences until the next
		// restart. There is one spelling of the bound and it is nonzero by
		// construction.
		Expect(controller.Interval(0)).To(Equal(output.WorkerFallbackInterval))
		Expect(controller.Interval(-time.Second)).To(Equal(output.WorkerFallbackInterval))
		Expect(controller.Interval(time.Hour)).To(Equal(output.WorkerFallbackInterval),
			"a configured interval slower than the bound was accepted")
		Expect(controller.Interval(10 * time.Second)).To(Equal(10 * time.Second))
		Expect(output.WorkerFallbackInterval).To(Equal(time.Minute))
	})

	// The carry-forward: read-lease recovery had no worker at all, so a crashed
	// materializer's lease was closed by nothing and its generation was pinned
	// against reclaim until somebody noticed.
	It("closes abandoned read leases on a bounded periodic pass and leaves live ones alone", func() {
		capture := hangarPublishAt(ctx, repository, hangarDigest(91), 1725830823000091,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		hangarReleaseSource(ctx, repository, capture)

		claimID := output.ClaimID(uuid.NewString())
		abandoned := output.ReadLeaseID(uuid.NewString())
		live := output.ReadLeaseID(uuid.NewString())

		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(repository.AcquireClaim(ctx, db.HangarOutputTx{Tx: tx}, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           claimID,
			Ref:               capture.Ref,
			ConsumerBindingID: "binding-liveness",
			RequestedAt:       output.NewTimestamp(time.Now()),
		})).To(Succeed())
		for _, id := range []output.ReadLeaseID{abandoned, live} {
			_, err := repository.AcquireReadLease(ctx, db.HangarOutputTx{Tx: tx},
				hangarReadLeaseRequest(id, claimID, capture.Ref))
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(tx.Commit()).To(Succeed())

		_, err = dbConn.Exec(`
			UPDATE hangar_read_leases
			   SET granted_at = now() - interval '2 hours', expires_at = now() - interval '1 minute'
			 WHERE read_lease_id = $1`, string(abandoned))
		Expect(err).NotTo(HaveOccurred())

		closed := 0
		cleanup := newRunner(output.OperationReadLeaseCleanup, controller.PassFunc(
			func(ctx context.Context, lease output.OperationLease) (int, error) {
				tx, err := dbConn.Begin()
				if err != nil {
					return 0, err
				}
				defer db.Rollback(tx)
				count, err := repository.CloseAbandonedReadLeases(ctx,
					db.HangarOutputTx{Tx: tx}, 10)
				if err != nil {
					return 0, err
				}
				if err := tx.Commit(); err != nil {
					return 0, err
				}
				closed += count

				return count, nil
			}))

		Expect(cleanup.Run(ctx)).To(Succeed())
		Expect(closed).To(Equal(1))

		var abandonedReleased, liveReleased bool
		Expect(dbConn.QueryRow(`
			SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id = $1`,
			string(abandoned)).Scan(&abandonedReleased)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id = $1`,
			string(live)).Scan(&liveReleased)).To(Succeed())
		Expect(abandonedReleased).To(BeTrue())
		Expect(liveReleased).To(BeFalse(),
			"the cleanup pass closed a live reader's lease, which is a materialization deleted "+
				"out from under a running task")

		// The pass is idempotent and stays bounded: a second run closes nothing
		// and does not spin.
		closed = 0
		Expect(cleanup.Run(ctx)).To(Succeed())
		Expect(closed).To(Equal(0))
	})

	// The production component, not only the repository method it calls. The
	// carry-forward was that this method had no caller at all, so a spec that
	// exercised the method would prove exactly what was already true when the
	// gap was found.
	It("runs the production read-lease cleanup component against real leases", func() {
		capture := hangarPublishAt(ctx, repository, hangarDigest(92), 1725830823000092,
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))
		hangarReleaseSource(ctx, repository, capture)

		claimID := output.ClaimID(uuid.NewString())
		leaseID := output.ReadLeaseID(uuid.NewString())
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		Expect(repository.AcquireClaim(ctx, db.HangarOutputTx{Tx: tx}, output.ClaimAcquisition{
			ProtocolVersion:   output.ProtocolVersion,
			ClaimID:           claimID,
			Ref:               capture.Ref,
			ConsumerBindingID: "binding-component",
			RequestedAt:       output.NewTimestamp(time.Now()),
		})).To(Succeed())
		_, err = repository.AcquireReadLease(ctx, db.HangarOutputTx{Tx: tx},
			hangarReadLeaseRequest(leaseID, claimID, capture.Ref))
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		cleaner := &hangaroutput.ReadLeaseCleaner{
			Transactor: hangarComponentTransactor{conn: dbConn},
			Leases:     repository,
		}

		// The control: a live lease survives a pass, so the close below is the
		// expiry rather than a component that closes everything.
		Expect(cleaner.Run(ctx)).To(Succeed())
		var released bool
		Expect(dbConn.QueryRow(`
			SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id = $1`,
			string(leaseID)).Scan(&released)).To(Succeed())
		Expect(released).To(BeFalse())

		_, err = dbConn.Exec(`
			UPDATE hangar_read_leases
			   SET granted_at = now() - interval '2 hours', expires_at = now() - interval '1 minute'
			 WHERE read_lease_id = $1`, string(leaseID))
		Expect(err).NotTo(HaveOccurred())

		Expect(cleaner.Run(ctx)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id = $1`,
			string(leaseID)).Scan(&released)).To(Succeed())
		Expect(released).To(BeTrue(),
			"the production component did not close a lease the database clock had expired; the "+
				"generation stays protected against reclaim for the life of the deployment")

		// And with the reader gone, the generation is reclaimable again.
		hangarAgeCapture(capture, 48*time.Hour)
		in := func(work func(tx db.HangarOutputTx)) {
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			work(db.HangarOutputTx{Tx: tx})
			Expect(tx.Commit()).To(Succeed())
		}
		in(func(tx db.HangarOutputTx) {
			Expect(repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
				ProtocolVersion: output.ProtocolVersion,
				ClaimID:         claimID,
				Ref:             capture.Ref,
				RequestedAt:     output.NewTimestamp(time.Now()),
			})).To(Succeed())
		})
		in(func(tx db.HangarOutputTx) {
			Expect(repository.AdmitReclaim(ctx, tx, capture.Ref, uuid.NewString(), 1,
				output.MinLeaseTerm)).To(Succeed())
		})
	})

	It("reports a bounded class per pass and never an identity", func() {
		var reported []string
		runner := newRunner(output.OperationAdoption, controller.PassFunc(
			func(context.Context, output.OperationLease) (int, error) {
				return 0, fmt.Errorf("%w: the object store did not answer", output.ErrTimeout)
			}))
		runner.Reporter = controller.ReporterFunc(
			func(_ context.Context, kind output.OperationKind, processed int, class string) {
				reported = append(reported, fmt.Sprintf("%s:%d:%s", kind, processed, class))
			})

		Expect(runner.Run(ctx)).To(Succeed(),
			"a failing pass stopped the controller; one unreachable node must not stop a plane")
		Expect(reported).To(Equal([]string{"adoption:0:timeout"}))
	})
})

// hangarLivenessTransactor adapts the suite's connection to the controller's
// port. It lives here rather than in atc/db because the port belongs to the
// controller, and a package that satisfies an interface should not have to know
// it exists.
type hangarLivenessTransactor struct{ conn db.DbConn }

func (transactor hangarLivenessTransactor) Begin() (controller.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	return db.HangarOutputTx{Tx: tx}, nil
}

// hangarComponentTransactor adapts the suite's connection to the ATC-side
// component's port, which is the coordinator's rather than the controller's.
type hangarComponentTransactor struct{ conn db.DbConn }

func (transactor hangarComponentTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := transactor.conn.Begin()
	if err != nil {
		return nil, err
	}

	return db.HangarOutputTx{Tx: tx}, nil
}
