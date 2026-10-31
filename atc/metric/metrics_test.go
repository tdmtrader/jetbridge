package metric_test

import (
	"fmt"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	Describe("worker state metric", func() {
		var emitter *recordingEmitter

		BeforeEach(func() {
			emitter = captureMetrics()
		})

		It("emits a value for every state", func() {
			metric.WorkersRegistered(givenNoWorkers())

			waitForEvents(emitter)

			for _, state := range db.AllWorkerStates() {
				event := eventWithState(emitter, state)
				Expect(event.Value).To(Equal(float64(0)))
			}
		})

		It("correctly emits the number of running workers", func() {
			metric.WorkersRegistered(givenOneWorkerWithState(db.WorkerStateRunning))

			waitForEvents(emitter)

			event := eventWithState(emitter, db.WorkerStateRunning)
			Expect(event.Value).To(Equal(float64(1)))
		})
	})
})

func eventWithState(emitter *recordingEmitter, state db.WorkerState) metric.Event {
	GinkgoHelper()

	for _, event := range emitter.Events() {
		if event.Attributes["state"] == string(state) {
			return event
		}
	}

	Fail(fmt.Sprintf("no event emitted for worker state %s", state))
	return metric.Event{}
}

func givenNoWorkers() map[string]db.WorkerState {
	return map[string]db.WorkerState{}
}

func givenOneWorkerWithState(state db.WorkerState) map[string]db.WorkerState {
	return map[string]db.WorkerState{"my-worker": state}
}

func waitForEvents(emitter *recordingEmitter) {
	numberOfWorkerStates := len(db.AllWorkerStates())
	Eventually(emitter.EventCount).Should(Equal(numberOfWorkerStates))
}
