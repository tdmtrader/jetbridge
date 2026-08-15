package metric_test

import (
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/tedsuo/ifrit"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Periodic emission of metrics", func() {
	var (
		output  *metricLogOutput
		monitor *metric.Monitor
		logger  lager.Logger

		process ifrit.Process
	)

	BeforeEach(func() {
		monitor, output, logger = monitorWithLager()
	})

	JustBeforeEach(func() {
		runner := metric.PeriodicallyEmit(
			logger,
			monitor,
			250*time.Millisecond,
		)

		process = ifrit.Invoke(runner)
	})

	AfterEach(func() {
		process.Signal(os.Interrupt)
		<-process.Wait()
	})

	Context("database-related metrics", func() {
		BeforeEach(func() {
			useEmptyTestDB()

			monitor.Databases = []db.DbConn{openTestConn("A"), openTestConn("B")}
		})

		It("emits database queries", func() {
			Eventually(output.MetricEvents).Should(
				ContainElement(
					HaveKeyWithValue("name", "database queries"),
				),
			)

			By("emits database connections for each pool")
			Eventually(output.MetricEvents).Should(
				ContainElement(
					And(
						HaveKeyWithValue("name", "database connections"),
						HaveKeyWithValue("ConnectionName", "A"),
					),
				),
			)
			Eventually(output.MetricEvents).Should(
				ContainElement(
					And(
						HaveKeyWithValue("name", "database connections"),
						HaveKeyWithValue("ConnectionName", "B"),
					),
				),
			)
		})
	})

	Context("concurrent requests", func() {
		const action = "ListAllSomething"

		BeforeEach(func() {
			gauge := &metric.Gauge{}
			gauge.Set(123)

			counter := &metric.Counter{}
			counter.IncDelta(10)

			monitor.ConcurrentRequests[action] = gauge
			monitor.ConcurrentRequestsLimitHit[action] = counter
		})

		It("emits", func() {
			Eventually(output.MetricEvents).Should(
				ContainElement(
					And(
						HaveKeyWithValue("name", "concurrent requests"),
						HaveKeyWithValue("value", float64(123)),
						HaveKeyWithValue("action", action),
					),
				),
			)

			Eventually(output.MetricEvents).Should(
				ContainElement(
					And(
						HaveKeyWithValue("name", "concurrent requests limit hit"),
						HaveKeyWithValue("value", float64(10)),
						HaveKeyWithValue("action", action),
					),
				),
			)
		})
	})

	Context("waiting steps metrics", func() {
		labels := metric.StepsWaitingLabels{
			TeamId:   "42",
			TeamName: "teamdev",
			Type:     "task",
		}

		BeforeEach(func() {
			gauge := &metric.Gauge{}
			gauge.Set(123)
			monitor.StepsWaiting[labels] = gauge
		})

		It("emits", func() {
			Eventually(output.MetricEvents).Should(
				ContainElement(
					And(
						HaveKeyWithValue("name", "steps waiting"),
						HaveKeyWithValue("value", float64(123)),
						HaveKeyWithValue("teamId", labels.TeamId),
						HaveKeyWithValue("teamName", labels.TeamName),
						HaveKeyWithValue("type", labels.Type),
					),
				),
			)
		})
	})
})
