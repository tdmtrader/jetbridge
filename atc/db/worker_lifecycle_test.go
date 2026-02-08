package db_test

import (
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Worker Lifecycle", func() {
	var (
		atcWorker atc.Worker
	)

	BeforeEach(func() {
		atcWorker = atc.Worker{
			ActiveContainers: 140,
			ResourceTypes: []atc.WorkerResourceType{
				{
					Type:    "some-resource-type",
					Image:   "some-image",
					Version: "some-version",
				},
				{
					Type:    "other-resource-type",
					Image:   "other-image",
					Version: "other-version",
				},
			},
			Platform:  "some-platform",
			Tags:      atc.Tags{"some", "tags"},
			Ephemeral: true,
			Name:      "some-name",
			StartTime: 55,
		}
	})

	Describe("DeleteUnresponsiveEphemeralWorkers", func() {
		Context("when the worker has heartbeated recently", func() {
			BeforeEach(func() {
				_, err := workerFactory.SaveWorker(atcWorker, 5*time.Minute)
				Expect(err).ToNot(HaveOccurred())
			})

			It("leaves the worker alone", func() {
				deletedWorkers, err := workerLifecycle.DeleteUnresponsiveEphemeralWorkers()
				Expect(err).ToNot(HaveOccurred())
				Expect(deletedWorkers).To(BeEmpty())
			})
		})

		Context("when the worker has not heartbeated recently", func() {
			BeforeEach(func() {
				_, err := workerFactory.SaveWorker(atcWorker, -1*time.Minute)
				Expect(err).ToNot(HaveOccurred())
			})

			It("deletes the ephemeral worker", func() {
				deletedWorkers, err := workerLifecycle.DeleteUnresponsiveEphemeralWorkers()
				Expect(err).ToNot(HaveOccurred())
				Expect(len(deletedWorkers)).To(Equal(1))
				Expect(deletedWorkers[0]).To(Equal("some-name"))
			})
		})
	})

	Describe("GetWorkersState", func() {

		JustBeforeEach(func() {
			atcWorker.State = string(db.WorkerStateStalled)
			_, err := workerFactory.SaveWorker(atcWorker, 5*time.Minute)
			Expect(err).ToNot(HaveOccurred())
		})

		It("gets the workers' state", func() {
			countByState, err := workerLifecycle.GetWorkerStateByName()
			Expect(err).ToNot(HaveOccurred())
			expectedState := map[string]db.WorkerState{
				"default-worker": db.WorkerStateRunning,
				"other-worker":   db.WorkerStateRunning,
				"some-name":      db.WorkerStateStalled,
			}
			Expect(countByState).To(Equal(expectedState))
		})

	})
})
