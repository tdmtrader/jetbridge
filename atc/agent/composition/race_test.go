package composition_test

import (
	"context"
	"sync"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/agent/composition"
	"github.com/concourse/concourse/atc/runs"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// gatedAdmitter is the real port with a barrier in front of Begin.
//
// It embeds the real Admitter and overrides one method -- the house pattern
// for injecting a condition without a double. AdmitRun, the claim, the
// transaction and the connection underneath are all the production ones; the
// only thing added is that neither goroutine issues its claim until both have
// a transaction open.
//
// The gate is at Begin and not at the claim, and that is forced rather than
// chosen. Under READ COMMITTED the loser's INSERT ... ON CONFLICT DO NOTHING
// does not return until the winner's transaction ends, so a barrier that
// waited for both claims to be *attempted* before releasing the winner's
// commit would deadlock: the loser is parked inside Postgres and the winner
// inside Go.
//
// What the gate buys, stated so nobody mistakes it for more: it makes the
// blocking path expected, not guaranteed. Once both Begins have returned,
// scheduling can still let the winner admit and commit before the loser issues
// its claim, in which case the loser finds the committed iteration row on its
// first read and never blocks. That is acceptable -- every outcome asserted
// below is required of both schedules -- and it is not what makes this
// falsifiable. The falsifier is dropping the unique constraint, which fails
// under any schedule at all, including a fully serial one.
type gatedAdmitter struct {
	runs.Admitter

	opened     chan struct{}
	peerOpened chan struct{}
}

func (a gatedAdmitter) Begin(ctx context.Context) (runs.Transaction, error) {
	tx, err := a.Admitter.Begin(ctx)
	if err != nil {
		return nil, err
	}

	close(a.opened)
	<-a.peerOpened

	return tx, nil
}

// A2.
var _ = Describe("two admissions of the same call, racing on two connections", func() {
	var (
		ctx      context.Context
		services [2]*composition.Service
	)

	BeforeEach(func() {
		ctx = context.Background()

		gates := [2]chan struct{}{make(chan struct{}), make(chan struct{})}

		for i := range services {
			// A connection of its own, not a second handle on the suite's:
			// two goroutines sharing one pool would serialize on it and the
			// race would never happen. One connection each, which is also the
			// budget an admission has to fit in -- so this is two racing
			// admissions on exactly two connections, not two on a slack pool.
			conn := postgresRunner.OpenConn()
			DeferCleanup(func() {
				Expect(conn.Close()).To(Succeed())
			})

			services[i] = composition.NewService(gatedAdmitter{
				Admitter:   newAdmitter(conn),
				opened:     gates[i],
				peerOpened: gates[1-i],
			})
		}
	})

	// Deliberately no lock.NewBuildTrackingLockID anywhere in this file. The
	// lock is what makes the two-tracker version of this test pass vacuously,
	// and it is released while a draining web's goroutine is still live -- so
	// the consumer has to be correct with it out of the picture entirely.
	It("admits exactly one run, and both callers get it", func() {
		req := composition.Request{
			BuildID:     buildID,
			PlanID:      atc.PlanID("race/1"),
			Template:    templateRef,
			Principal:   principal,
			InputDigest: "sha256:raced",
		}

		var (
			results [2]composition.Result
			errs    [2]error
			wg      sync.WaitGroup
		)

		wg.Add(len(services))
		for i, service := range services {
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				results[i], errs[i] = service.Admit(ctx, req)
			}()
		}
		wg.Wait()

		Expect(errs[0]).NotTo(HaveOccurred())
		Expect(errs[1]).NotTo(HaveOccurred())

		Expect(countOf("SELECT count(*) FROM pipeline_runs")).To(Equal(1))
		Expect(countOf("SELECT count(*) FROM composition_calls")).To(Equal(1))
		Expect(countOf("SELECT count(*) FROM composition_iterations")).To(Equal(1))

		Expect(results[0].RunID).To(Equal(results[1].RunID))
		Expect(results[0].RunID).To(Equal(iterationRunID(buildID, "race/1")))

		// Exactly one of them admitted; the other re-attached. Which one is
		// the schedule's business, not the contract's.
		Expect(results[0].Replayed).NotTo(Equal(results[1].Replayed))
	})
})
