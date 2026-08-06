package gc_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkerCollector", func() {
	var (
		workerCollector GcCollector
		workerFactory   db.WorkerFactory
	)

	// worker is the shape DeleteUnresponsiveEphemeralWorkers selects on:
	// `WHERE ephemeral AND expires < NOW()`. A negative ttl backdates expires, so
	// a worker can be unresponsive without the test waiting for it to become so.
	worker := func(name string, ephemeral bool) atc.Worker {
		return atc.Worker{
			Name:             name,
			Ephemeral:        ephemeral,
			Platform:         "some-platform",
			ActiveContainers: 1,
			StartTime:        55,
		}
	}

	BeforeEach(func() {
		workerFactory = db.NewWorkerFactory(dbConn, db.NewStaticWorkerCache(logger, dbConn, 0))
		workerCollector = gc.NewWorkerCollector(db.NewWorkerLifecycle(dbConn))
	})

	Describe("Run", func() {
		It("removes an ephemeral worker that has stopped heartbeating", func() {
			_, err := workerFactory.SaveWorker(worker("expired-ephemeral", true), -time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(workerCollector.Run(context.TODO())).To(Succeed())

			_, found, err := workerFactory.GetWorker("expired-ephemeral")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse(), "expired ephemeral worker should have been collected")
		})

		It("leaves an ephemeral worker that is still heartbeating", func() {
			_, err := workerFactory.SaveWorker(worker("live-ephemeral", true), 5*time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(workerCollector.Run(context.TODO())).To(Succeed())

			_, found, err := workerFactory.GetWorker("live-ephemeral")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "unexpired ephemeral worker should have survived")
		})

		It("leaves a non-ephemeral worker even once it has expired", func() {
			_, err := workerFactory.SaveWorker(worker("expired-persistent", false), -time.Minute)
			Expect(err).NotTo(HaveOccurred())

			Expect(workerCollector.Run(context.TODO())).To(Succeed())

			_, found, err := workerFactory.GetWorker("expired-persistent")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue(), "a non-ephemeral worker is stalled, not garbage")
		})

		It("returns the error when the delete fails", func() {
			// A closed connection is a real failure the database can be made to
			// produce on demand. It is opened separately so the suite's own conn
			// -- which AfterEach closes -- is left intact.
			doomed := postgresRunner.OpenConn()
			Expect(doomed.Close()).To(Succeed())

			err := gc.NewWorkerCollector(db.NewWorkerLifecycle(doomed)).Run(context.TODO())
			Expect(err).To(HaveOccurred())
		})
	})
})
