package emitter

import (
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/concourse/concourse/atc/metric"
)

var _ = Describe("PrometheusEmitter garbage collector", func() {
	var (
		prometheusEmitter *PrometheusEmitter
		logger            *lagertest.TestLogger

		labelsLong    prometheus.Labels
		labelsUnknown prometheus.Labels
		labelsTasks   prometheus.Labels
	)

	workerGaugeVec := func(name string, labelNames ...string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "concoursedev",
				Subsystem: "workers",
				Name:      name,
			},
			labelNames,
		)
	}

	BeforeEach(func() {
		logger = lagertest.NewTestLogger("test")

		prometheusEmitter = &PrometheusEmitter{
			workerContainers:        workerGaugeVec("containers", "worker", "platform", "team", "tags"),
			workerUnknownContainers: workerGaugeVec("unknown_containers", "worker"),
			workerVolumes:           workerGaugeVec("volumes", "worker", "platform", "team", "tags"),
			workerUnknownVolumes:    workerGaugeVec("unknown_volumes", "worker"),
			workerTasks:             workerGaugeVec("tasks", "worker", "platform"),

			workerContainersLabels:        map[string]map[string]prometheus.Labels{},
			workerUnknownContainersLabels: map[string]map[string]prometheus.Labels{},
			workerVolumesLabels:           map[string]map[string]prometheus.Labels{},
			workerUnknownVolumesLabels:    map[string]map[string]prometheus.Labels{},
			workerTasksLabels:             map[string]map[string]prometheus.Labels{},
			workerLastSeen:                map[string]time.Time{},
		}

		labelsLong = prometheus.Labels{
			"worker":   "foo",
			"platform": "linux",
			"team":     "main",
			"tags":     "",
		}

		labelsUnknown = prometheus.Labels{
			"worker": "foo",
		}

		labelsTasks = prometheus.Labels{
			"worker":   "foo",
			"platform": "linux",
		}
	})

	JustBeforeEach(func() {
		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "worker containers",
			Value: 42,
			Attributes: map[string]string{
				"worker":    "foo",
				"platform":  "linux",
				"team_name": "main",
				"tags":      "",
			},
		})

		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "worker unknown containers",
			Value: 42,
			Attributes: map[string]string{
				"worker": "foo",
			},
		})

		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "worker volumes",
			Value: 42,
			Attributes: map[string]string{
				"worker":    "foo",
				"platform":  "linux",
				"team_name": "main",
				"tags":      "",
			},
		})

		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "worker unknown volumes",
			Value: 42,
			Attributes: map[string]string{
				"worker": "foo",
			},
		})

		prometheusEmitter.Emit(logger, metric.Event{
			Name:  "worker tasks",
			Value: 42,
			Attributes: map[string]string{
				"worker":   "foo",
				"platform": "linux",
			},
		})
	})

	It("removes every series belonging to the worker", func() {
		Expect(prometheusEmitter.workerContainersLabels).To(HaveLen(1))
		Expect(prometheusEmitter.workerUnknownContainersLabels).To(HaveLen(1))
		Expect(prometheusEmitter.workerVolumesLabels).To(HaveLen(1))
		Expect(prometheusEmitter.workerUnknownVolumesLabels).To(HaveLen(1))
		Expect(prometheusEmitter.workerTasksLabels).To(HaveLen(1))

		prometheusEmitter.doGarbageCollection("foo")

		Expect(prometheusEmitter.workerContainersLabels).To(HaveLen(0))
		Expect(prometheusEmitter.workerUnknownContainersLabels).To(HaveLen(0))
		Expect(prometheusEmitter.workerVolumesLabels).To(HaveLen(0))
		Expect(prometheusEmitter.workerUnknownVolumesLabels).To(HaveLen(0))
		Expect(prometheusEmitter.workerTasksLabels).To(HaveLen(0))

		// Delete returns false if the series no longer exists
		Expect(prometheusEmitter.workerContainers.Delete(labelsLong)).To(BeFalse())
		Expect(prometheusEmitter.workerUnknownContainers.Delete(labelsUnknown)).To(BeFalse())
		Expect(prometheusEmitter.workerVolumes.Delete(labelsLong)).To(BeFalse())
		Expect(prometheusEmitter.workerUnknownVolumes.Delete(labelsUnknown)).To(BeFalse())
		Expect(prometheusEmitter.workerTasks.Delete(labelsTasks)).To(BeFalse())
	})

	// There is no easy way to detect whether metrics are REALLY garbage collected due to the
	// limitations of the Prometheus client library, so here we verify that the metrics that were
	// deleted in the previous spec were actually present from the beginning.
	It("only removes series that were there to begin with", func() {
		// Delete returns true if the series is actually deleted
		Expect(prometheusEmitter.workerContainers.Delete(labelsLong)).To(BeTrue())
		Expect(prometheusEmitter.workerUnknownContainers.Delete(labelsUnknown)).To(BeTrue())
		Expect(prometheusEmitter.workerVolumes.Delete(labelsLong)).To(BeTrue())
		Expect(prometheusEmitter.workerUnknownVolumes.Delete(labelsUnknown)).To(BeTrue())
		Expect(prometheusEmitter.workerTasks.Delete(labelsTasks)).To(BeTrue())

		prometheusEmitter.doGarbageCollection("foo")

		Expect(prometheusEmitter.workerContainers.Delete(labelsLong)).To(BeFalse())
		Expect(prometheusEmitter.workerUnknownContainers.Delete(labelsUnknown)).To(BeFalse())
		Expect(prometheusEmitter.workerVolumes.Delete(labelsLong)).To(BeFalse())
		Expect(prometheusEmitter.workerUnknownVolumes.Delete(labelsUnknown)).To(BeFalse())
		Expect(prometheusEmitter.workerTasks.Delete(labelsTasks)).To(BeFalse())
	})
})
