package gc_test

import (
	"context"

	"github.com/concourse/concourse/atc/gc"

	"errors"

	"github.com/concourse/concourse/atc/db/dbfakes"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("WorkerCollector", func() {
	var (
		workerCollector     GcCollector
		fakeWorkerLifecycle *dbfakes.FakeWorkerLifecycle
	)

	BeforeEach(func() {
		fakeWorkerLifecycle = new(dbfakes.FakeWorkerLifecycle)

		workerCollector = gc.NewWorkerCollector(fakeWorkerLifecycle)

		fakeWorkerLifecycle.DeleteUnresponsiveEphemeralWorkersReturns(nil, nil)
	})

	Describe("Run", func() {
		It("tells the worker factory to delete unresponsive ephemeral workers", func() {
			err := workerCollector.Run(context.TODO())
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeWorkerLifecycle.DeleteUnresponsiveEphemeralWorkersCallCount()).To(Equal(1))
		})

		It("returns an error if deleting unresponsive ephemeral workers fails", func() {
			returnedErr := errors.New("some-error")
			fakeWorkerLifecycle.DeleteUnresponsiveEphemeralWorkersReturns(nil, returnedErr)

			err := workerCollector.Run(context.TODO())
			Expect(err).To(MatchError(returnedErr))
		})

	})
})
