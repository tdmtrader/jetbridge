package runs_test

import (
	"context"
	"sync"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/skymarshal/skycmd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The port's connection budget, asserted at the limit.
//
// AdmitRun runs inside a transaction the caller opened, and that transaction is
// holding a connection for as long as the caller holds it. So every read
// admission needs has to go through that same transaction. A read that went to
// the pool instead would want a *second* connection while the first is still
// checked out -- and N concurrent admissions against a pool of N would then
// each hold one connection and wait for another that no one is going to
// release. Nothing times out: the factories' pool reads take no context, so
// they wait on context.Background() forever. The process does not fail, it
// stops.
//
// postgresrunner pins a pool to one connection for exactly this reason -- "only
// allow one connection so that we can detect any code paths that require more
// than one, which will deadlock if it's at the limit". This spec takes it at
// its word: one connection, a transaction open on it, and an admission that
// must complete anyway.
//
// Why the bound is a wall clock and not only the context deadline: a pool wait
// inside database/sql is interruptible only by the context of the query that is
// waiting, and a read that takes no context waits on context.Background(). A
// port that reaches for a second connection therefore hangs rather than
// returning a deadline error, so the deadline below is a backstop and the
// select is what turns the hang into a failure.
var _ = Describe("admission's connection budget", func() {
	It("admits with the caller's transaction as its only connection", func() {
		// A pool of its own, left at postgresrunner's one-connection default,
		// so this spec states its own precondition rather than inheriting it
		// from the suite.
		conn := postgresRunner.OpenConn()

		// Closed exactly once: the timeout branch below closes the pool to
		// unpark whatever is waiting on it, and closing this connection twice
		// panics on its notification bus rather than reporting an error.
		var closeOnce sync.Once
		closeConn := func() error {
			var err error
			closeOnce.Do(func() { err = conn.Close() })

			return err
		}
		DeferCleanup(func() {
			Expect(closeConn()).To(Succeed())
		})

		displayUserIds, err := skycmd.NewSkyDisplayUserIdGenerator(map[string]string{"local": "user_id"})
		Expect(err).NotTo(HaveOccurred())

		port := runs.NewAdmitter(conn,
			db.NewPipelineRunFactory(conn, lockFactory),
			db.NewTeamFactory(conn, lockFactory),
			displayUserIds, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		DeferCleanup(cancel)

		admitted := make(chan error, 1)
		go func() {
			defer GinkgoRecover()

			tx, err := port.Begin(ctx)
			if err != nil {
				admitted <- err

				return
			}
			defer tx.Rollback()

			// The pool's one connection is checked out from here until the
			// caller finishes the transaction. That is what Begin publishes,
			// and admission has to live within it.
			_, err = port.AdmitRun(ctx, tx, runs.Admission{
				Template:    templateRef,
				Principal:   memberPrincipal,
				ContractKey: "connection-budget/one",
			})
			if err != nil {
				admitted <- err

				return
			}

			admitted <- tx.Commit()
		}()

		select {
		case err := <-admitted:
			Expect(err).NotTo(HaveOccurred())
		case <-time.After(30 * time.Second):
			// Closing the pool releases whatever is parked on it, so the
			// goroutine unwinds and the suite's teardown drops the database
			// instead of blocking behind a transaction nobody can finish.
			Expect(closeConn()).To(Succeed())
			Eventually(admitted, 30*time.Second).Should(Receive())

			Fail("admission did not finish within 30s while the caller held the " +
				"port's transaction on a one-connection pool. It is waiting for a " +
				"second connection, and the caller is holding the only one.")
		}
	})
})
