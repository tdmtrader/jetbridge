package metric_test

import (
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	Describe("worker state metric", func() {
		var (
			output  *metricLogOutput
			monitor *metric.Monitor
			logger  lager.Logger
		)

		BeforeEach(func() {
			monitor, output, logger = monitorWithLager()
		})

		It("emits a value for every state", func() {
			givenNoWorkers().Emit(logger, monitor)

			waitForEvents(output)

			for _, state := range db.AllWorkerStates() {
				event := eventWithState(output, state)
				Expect(event).To(HaveKeyWithValue("value", float64(0)))
			}
		})

		It("correctly emits the number of running workers", func() {
			givenOneWorkerWithState(db.WorkerStateRunning).
				Emit(logger, monitor)

			waitForEvents(output)

			event := eventWithState(output, db.WorkerStateRunning)
			Expect(event).To(HaveKeyWithValue("value", float64(1)))
		})
	})
})

func eventWithState(output *metricLogOutput, state db.WorkerState) lager.Data {
	GinkgoHelper()

	for _, event := range output.MetricEvents() {
		if event["state"] == string(state) {
			return event
		}
	}

	Fail(fmt.Sprintf("no event emitted for worker state %s", state))
	return nil
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

func waitForEvents(output *metricLogOutput) {
	numberOfWorkerStates := len(db.AllWorkerStates())
	Eventually(output.EventCount).Should(Equal(numberOfWorkerStates))
}
