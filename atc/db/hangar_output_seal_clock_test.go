package db_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/output"
)

// THE ONE CLOCK `now()` CAN ACTUALLY MOVE.
//
// PostgreSQL's now() is transaction_timestamp(), so a transaction open for N
// seconds reads every now() as N seconds early. The plane rules on that
// imprecision rather than fixing it -- the note is at the top of
// hangar_output_reclaim.go -- and the ruling holds for LEASES, because every
// lease in this schema is floored at fifteen minutes by a CHECK and N is
// milliseconds.
//
// The seal deadline is not a lease. Requirement 17 makes it configurable down
// to THIRTY SECONDS, and a slow check transaction that reads its own start
// instant judges a seal in-time that is not -- which is the exact failure
// seal_deadline_at was added to prevent. So the comparison is
// clock_timestamp(), as hangarStatProofFresh already is for the same reason.
//
// The spec holds a transaction open across the deadline, which is the only way
// to tell the two readings apart: every other spec in this suite asks the
// question in a transaction that opened a moment ago, where the two agree.
var _ = Describe("the seal deadline against a slow transaction", func() {
	var (
		ctx        context.Context
		repository *db.HangarOutputRepository
	)

	BeforeEach(func() {
		ctx = context.Background()

		consumer, err := db.HangarConsumerPrefixHeld("seal-clock-spec")
		Expect(err).NotTo(HaveOccurred())
		repository = db.NewHangarOutputRepository(consumer)
		hangarActivateEpoch(ctx, repository)
	})

	It("has passed for a transaction that opened before it and asked after it", func() {
		capture := hangarReserve(ctx, repository, hangarDigest(81),
			output.NewTimestamp(time.Now().Add(output.DefaultCaptureDeadline)))

		// The checking transaction, on its own connection, opened FIRST: its
		// transaction_timestamp() is fixed here and every now() it reads for
		// the rest of its life is this instant.
		checking := postgresRunner.OpenConn()
		DeferCleanup(func() { Expect(checking.Close()).To(Succeed()) })
		slow, err := checking.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(slow)

		var opened time.Time
		Expect(slow.QueryRow(`SELECT transaction_timestamp()`).Scan(&opened)).To(Succeed())

		// The control, asked before anything has elapsed: the deadline has not
		// passed. Without it a predicate that answered true for everything
		// would look like an enforced deadline.
		Expect(repository.SealDeadlinePassed(ctx, slow, capture.ReservationID)).To(BeFalse())

		// A deadline just past the checking transaction's own start instant --
		// arranged, the way every elapsed-time fixture in this suite arranges
		// time, and on the database's clock rather than this process's.
		deadline := opened.Add(300 * time.Millisecond)
		result, err := dbConn.Exec(`
			UPDATE hangar_capture_reservations SET seal_deadline_at = $2
			WHERE reservation_id = $1`, string(capture.ReservationID), deadline)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RowsAffected()).To(BeEquivalentTo(1))

		// Still not passed: the deadline is genuinely in the future for
		// everybody, so a true answer here would be the predicate answering
		// from the wrong column rather than from the wrong clock.
		Expect(repository.SealDeadlinePassed(ctx, slow, capture.ReservationID)).To(BeFalse())

		Eventually(func() bool {
			var past bool
			Expect(dbConn.QueryRow(`SELECT clock_timestamp() > $1`, deadline).Scan(&past)).
				To(Succeed())

			return past
		}, 10*time.Second, 20*time.Millisecond).Should(BeTrue(),
			"the database clock never passed the deadline this spec set")

		Expect(repository.SealDeadlinePassed(ctx, slow, capture.ReservationID)).To(BeTrue(),
			"the seal deadline was judged against the checking transaction's START instant, "+
				"so a transaction that has been open longer than the seal had left reports a "+
				"seal still in time. At Req 17's thirty-second floor that is the whole bound")

		// And the transaction really was the slow one, so the reading above is
		// the difference between the two clocks and not a coincidence.
		var stale bool
		Expect(slow.QueryRow(`SELECT now() < $1`, deadline).Scan(&stale)).To(Succeed())
		Expect(stale).To(BeTrue(),
			"the checking transaction's own now() had already passed the deadline, so this "+
				"spec would pass against either reading")
	})
})
