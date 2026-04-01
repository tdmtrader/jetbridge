package gc_test

import (
	"context"
	"errors"

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
		It("succeeds when cleanup completes without error", func() {
			fakeTaskCacheLifecycle.CleanUpInvalidTaskCachesReturns([]int{1, 2, 3}, nil)

			err := collector.Run(context.TODO())
			Expect(err).NotTo(HaveOccurred())
		})

		It("propagates errors from the lifecycle", func() {
			fakeTaskCacheLifecycle.CleanUpInvalidTaskCachesReturns(nil, errors.New("db gone"))

			err := collector.Run(context.TODO())
			Expect(err).To(MatchError("db gone"))
		})
	})
})
