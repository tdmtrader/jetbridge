package db_test

import (
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkerTaskCache", func() {
	var workerTaskCache db.WorkerTaskCache

	BeforeEach(func() {
		taskCache, err := taskCacheFactory.FindOrCreate(defaultJob.ID(), "some-step", "some-path")
		Expect(err).ToNot(HaveOccurred())

		workerTaskCache = db.WorkerTaskCache{
			WorkerName: defaultWorker.Name(),
			TaskCache:  taskCache,
		}
	})

	Describe("FindOrCreate", func() {
		Context("when there is no existing worker task cache", func() {
			It("creates worker task cache", func() {
				usedWorkerTaskCache, err := workerTaskCacheFactory.FindOrCreate(workerTaskCache)
				Expect(err).ToNot(HaveOccurred())

				Expect(usedWorkerTaskCache.ID).ToNot(BeZero())
			})
		})

		Context("when there is existing worker task caches", func() {
			var existingWorkerTaskCache *db.UsedWorkerTaskCache

			BeforeEach(func() {
				var err error
				existingWorkerTaskCache, err = workerTaskCacheFactory.FindOrCreate(workerTaskCache)
				Expect(err).ToNot(HaveOccurred())
			})

			It("finds worker task cache", func() {
				usedWorkerTaskCache, err := workerTaskCacheFactory.FindOrCreate(workerTaskCache)
				Expect(err).ToNot(HaveOccurred())

				Expect(usedWorkerTaskCache.ID).To(Equal(existingWorkerTaskCache.ID))
			})
		})
	})

	Describe("Find", func() {
		var uwtc *db.UsedWorkerTaskCache
		var found bool
		var findErr error

		JustBeforeEach(func() {
			uwtc, found, findErr = workerTaskCacheFactory.Find(workerTaskCache)
		})

		Context("when there are no existing worker task caches", func() {
			It("returns false and no error", func() {
				Expect(findErr).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())
				Expect(uwtc).To(BeNil())
			})
		})

		Context("when there is existing worker task caches", func() {
			var createdWorkerTaskCache *db.UsedWorkerTaskCache

			BeforeEach(func() {
				var err error
				createdWorkerTaskCache, err = workerTaskCacheFactory.FindOrCreate(workerTaskCache)
				Expect(err).ToNot(HaveOccurred())
			})

			It("finds worker task cache", func() {
				Expect(findErr).ToNot(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(uwtc.ID).To(Equal(createdWorkerTaskCache.ID))
			})
		})
	})
})
