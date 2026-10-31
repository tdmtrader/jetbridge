package metric_test

import (
	"os"
	"time"

	"github.com/concourse/concourse/atc/metric"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tedsuo/ifrit"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Periodic emission of metrics", func() {
	var (
		reg     *prometheus.Registry
		process ifrit.Process
	)

	BeforeEach(func() {
		reg = freshRegistry()
	})

	JustBeforeEach(func() {
		process = ifrit.Invoke(metric.PeriodicallyEmit(250 * time.Millisecond))
	})

	AfterEach(func() {
		process.Signal(os.Interrupt)
		<-process.Wait()
	})

	Context("database-related metrics", func() {
		BeforeEach(func() {
			useEmptyTestDB()

			metric.InstrumentDB(openTestConn("A"))
			metric.InstrumentDB(openTestConn("B"))
		})

		It("publishes the queries counted and each pool's open connections", func() {
			before := gathered(reg, "concourse_db_queries_total", nil)
			metric.Metrics.DatabaseQueries.Inc()

			Eventually(func() float64 { return gathered(reg, "concourse_db_queries_total", nil) }).
				Should(BeNumerically(">=", before+1))

			By("publishing one connection gauge per pool")
			Eventually(func() int { return seriesCount(reg, "concourse_db_connections", map[string]string{"dbname": "A"}) }).Should(Equal(1))
			Eventually(func() int { return seriesCount(reg, "concourse_db_connections", map[string]string{"dbname": "B"}) }).Should(Equal(1))
		})
	})

	It("publishes the highest level a sampled gauge reached since the last tick", func() {
		metric.Metrics.BuildsRunning.Inc()
		metric.Metrics.BuildsRunning.Inc()
		metric.Metrics.BuildsRunning.Dec()
		metric.Metrics.BuildsRunning.Dec()

		Eventually(func() float64 { return gathered(reg, "concourse_builds_running", nil) }).Should(Equal(float64(2)))
		Eventually(func() float64 { return gathered(reg, "concourse_builds_running", nil) }).Should(Equal(float64(0)))
	})
})

// seriesCount counts the series of family name whose labels include want.
func seriesCount(reg *prometheus.Registry, name string, want map[string]string) int {
	GinkgoHelper()

	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())

	count := 0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
	series:
		for _, m := range family.GetMetric() {
			for _, label := range m.GetLabel() {
				if v, ok := want[label.GetName()]; ok && v != label.GetValue() {
					continue series
				}
			}
			count++
		}
	}
	return count
}
