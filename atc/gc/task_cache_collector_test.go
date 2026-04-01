package gc_test

import (
	"context"

	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/gc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TaskCacheCollector", func() {
	var collector GcCollector
	var fakeTaskCacheLifecycle *dbfakes.FakeTaskCacheLifecycle

	BeforeEach(func() {
		fakeTaskCacheLifecycle = new(dbfakes.FakeTaskCacheLifecycle)

		collector = gc.NewTaskCacheCollector(fakeTaskCacheLifecycle)
	})

	Describe("Run", func() {
		It("tells the task cache lifecycle to remove invalid task caches", func() {
			err := collector.Run(context.TODO())
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeTaskCacheLifecycle.CleanUpInvalidTaskCachesCallCount()).To(Equal(1))
		})
	})
})
