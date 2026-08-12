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
		var (
			emitter *recordingEmitter
			monitor *metric.Monitor
		)

		BeforeEach(func() {
			monitor, emitter = monitorWithRecorder()
		})

		It("emits a value for every state", func() {
			givenNoWorkers().Emit(testLogger, monitor)

			waitForEvents(emitter)

			for _, state := range db.AllWorkerStates() {
				event := eventWithState(emitter, state)
				Expect(event.Value).To(Equal(float64(0)))
			}
		})

		It("correctly emits the number of running workers", func() {
			givenOneWorkerWithState(db.WorkerStateRunning).
				Emit(testLogger, monitor)

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

func givenNoWorkers() metric.WorkersState {
	return metric.WorkersState{
		WorkerStateByName: make(map[string]db.WorkerState),
	}
}

func givenOneWorkerWithState(state db.WorkerState) metric.WorkersState {
	workersState := givenNoWorkers()
	workersState.WorkerStateByName["my-worker"] = state
	return workersState
}

func waitForEvents(emitter *recordingEmitter) {
	numberOfWorkerStates := len(db.AllWorkerStates())
	Eventually(emitter.EventCount).Should(Equal(numberOfWorkerStates))
}
