package db_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

// Read-lease recovery: the web's periodic cleanup closes leases whose readers
// are gone, on the database clock, and leaves live ones alone.
var _ = Describe("the read-lease cleanup", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbConn.SetMaxOpenConns(12)
		DeferCleanup(func() { dbConn.SetMaxOpenConns(1) })

		consumer, err := db.HangarConsumerPrefixHeld("read-lease-cleanup-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)

		hangarActivateEpoch(ctx, repository)
	})

	// The carry-forward: read-lease recovery had no worker at all, so a crashed
	// materializer's lease was closed by nothing and its generation was pinned
	// against reclaim until somebody noticed.
	It("closes abandoned read leases on a bounded periodic pass and leaves live ones alone", func() {
		capture := hangarPublishAt(ctx, repository, hangarDigest(91), 1725830823000091,
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)
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

		cleanup := &hangaroutput.ReadLeaseCleaner{
			Transactor: hangarComponentTransactor{conn: dbConn},
			Leases:     repository,
			BatchSize:  10,
		}
		Expect(cleanup.Run(ctx)).To(Succeed())

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

		// The pass is idempotent: a second run closes nothing more and leaves
		// the live lease open.
		Expect(cleanup.Run(ctx)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT released_at IS NOT NULL FROM hangar_read_leases WHERE read_lease_id = $1`,
			string(live)).Scan(&liveReleased)).To(Succeed())
		Expect(liveReleased).To(BeFalse())
	})

	// The production component, not only the repository method it calls. The
	// carry-forward was that this method had no caller at all, so a spec that
	// exercised the method would prove exactly what was already true when the
	// gap was found.
	It("runs the production read-lease cleanup component against real leases", func() {
		capture := hangarPublishAt(ctx, repository, hangarDigest(92), 1725830823000092,
			output.DefaultCaptureDeadline)
		hangarReleaseCaptureClaim(ctx, repository, capture)
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
			GinkgoHelper()
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
		hangarAgePublication(capture.Ref, hangarGraceElapsed)
		in(func(tx db.HangarOutputTx) {
			Expect(repository.AdmitReclaim(ctx, tx, capture.Ref, uuid.NewString(), 1,
				output.MinLeaseTerm,
				output.DefaultPublicationGrace)).To(Succeed())
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
