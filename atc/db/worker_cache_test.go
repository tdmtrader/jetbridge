package db_test

import (
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkerCache", func() {
	var (
		workerCache *db.WorkerCache
		scenario    *dbtest.Scenario
	)

	BeforeEach(func() {
		_, err := dbConn.Exec("DELETE FROM workers")
		Expect(err).ToNot(HaveOccurred())

		// The fallback interval exceeds every assertion window, so only notifications
		// can make insertion, update and deletion visible after the initial read.
		workerCache, err = db.NewWorkerCache(logger, dbConn, time.Hour)
		Expect(err).ToNot(HaveOccurred())

		scenario = dbtest.Setup()
	})

	getWorkers := func() []db.Worker {
		defer GinkgoRecover()

		workers, err := workerCache.Workers()
		Expect(err).ToNot(HaveOccurred())
		return workers
	}

	getWorkerNames := func() []string {
		workers := getWorkers()
		names := make([]string, len(workers))
		for i, worker := range workers {
			names[i] = worker.Name()
		}
		return names
	}

	getWorkerActiveVolumes := func() []int {
		workers := getWorkers()
		volumes := make([]int, len(workers))
		for i, worker := range workers {
			volumes[i] = worker.ActiveVolumes()
		}
		return volumes
	}

	It("keeps its data up-to-date by listening to triggers", func() {
		By("ensuring the data starts out as empty")
		Expect(getWorkers()).To(BeEmpty())

		By("adding a new worker")
		atcWorker := dbtest.BaseWorker("some-worker")
		scenario.Run(builder.WithWorker(atcWorker))

		By("ensuring the cache eventually adds the new worker")
		Eventually(getWorkerNames, 10*time.Second).Should(ConsistOf("some-worker"))

		By("updating the worker")
		atcWorker.ActiveVolumes = 50
		scenario.Run(builder.WithWorker(atcWorker))
		Eventually(getWorkerActiveVolumes, 10*time.Second).Should(ConsistOf(50))

		By("removing the worker")
		_, err := dbConn.Exec("DELETE FROM workers")
		Expect(err).ToNot(HaveOccurred())

		Eventually(getWorkers, 10*time.Second).Should(BeEmpty())
	})
})
