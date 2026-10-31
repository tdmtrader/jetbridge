package metric_test

import (
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/tracing"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/propagation"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Metrics", func() {
	var reg *prometheus.Registry

	BeforeEach(func() {
		reg = freshRegistry()
	})

	Describe("worker state metric", func() {
		It("publishes a value for every state, zeroes included", func() {
			metric.WorkersRegistered(map[string]db.WorkerState{})

			for _, state := range db.AllWorkerStates() {
				Expect(gathered(reg, "concourse_workers_registered", map[string]string{"state": string(state)})).To(Equal(float64(0)))
			}
		})

		It("counts the workers in each state", func() {
			metric.WorkersRegistered(map[string]db.WorkerState{"my-worker": db.WorkerStateRunning})

			Expect(gathered(reg, "concourse_workers_registered", map[string]string{"state": "running"})).To(Equal(float64(1)))
		})
	})

	Describe("pipeline run reclaimer", func() {
		It("publishes the backlog as a level and the pass duration as a histogram", func() {
			passes := gathered(reg, "concourse_gc_pipeline_run_reclaim_duration", nil)

			metric.PipelineRunReclaimBacklog(7)
			metric.PipelineRunReclaimDuration(42 * time.Millisecond)
			Expect(gathered(reg, "concourse_gc_pipeline_run_reclaim_backlog", nil)).To(Equal(float64(7)))
			Expect(gathered(reg, "concourse_gc_pipeline_run_reclaim_duration", nil)).To(Equal(passes + 1))

			// A gauge, not a counter: a shrinking backlog must be able to say so.
			metric.PipelineRunReclaimBacklog(2)
			Expect(gathered(reg, "concourse_gc_pipeline_run_reclaim_backlog", nil)).To(Equal(float64(2)))
		})
	})

	Describe("finished builds", func() {
		It("counts the build by status and attaches its trace to the duration", func() {
			labels := map[string]string{"team": "main", "pipeline": "pipeline", "job": "job", "status": "succeeded"}
			before := gathered(reg, "concourse_builds_finished", labels)

			metric.BuildFinished(tracedBuild{})

			Expect(gathered(reg, "concourse_builds_finished", labels)).To(Equal(before + 1))
			Expect(exemplarTraceIDs(reg, "concourse_builds_duration_seconds")).To(ContainElement("4bf92f3577b34da6a3ce929d0e0e4736"))
		})
	})

	Describe("latest completed build status", func() {
		It("publishes the four terminal statuses and ignores the rest", func() {
			labels := map[string]string{"teamName": "team1", "pipelineName": "pipeline1", "jobName": "job1"}

			metric.LatestCompletedBuildStatus("team1", "pipeline1", "job1", "errored")
			Expect(gathered(reg, "concourse_builds_latest_completed_build_status", labels)).To(Equal(float64(3)))

			metric.LatestCompletedBuildStatus("team1", "pipeline1", "job1", "started")
			Expect(gathered(reg, "concourse_builds_latest_completed_build_status", labels)).To(Equal(float64(3)))
		})
	})

	Describe("constant labels", func() {
		It("sanitizes attribute names into valid label names", func() {
			reg := prometheus.NewRegistry()
			Expect(metric.Register(reg, map[string]string{
				"invalid-label":     "foo",
				"__prefix__test__":  "bar",
				"_prefix_testtwo__": "baz",
			})).To(Succeed())

			Expect(gathered(reg, "concourse_builds_started_total", map[string]string{
				"invalid_label":  "foo",
				"prefix_test":    "bar",
				"prefix_testtwo": "baz",
			})).To(BeNumerically(">=", 0))

			families, err := reg.Gather()
			Expect(err).NotTo(HaveOccurred())
			for _, family := range families {
				for _, m := range family.GetMetric() {
					names := []string{}
					for _, label := range m.GetLabel() {
						names = append(names, label.GetName())
					}
					Expect(names).To(ContainElements("invalid_label", "prefix_test", "prefix_testtwo"), family.GetName())
				}
			}
		})
	})
})

// tracedBuild is a finished pipeline build with a trace, the shape
// BuildFinished reads.
type tracedBuild struct{ db.Build }

func (tracedBuild) TracingAttrs() tracing.Attrs {
	return tracing.Attrs{"team_name": "main", "pipeline": "pipeline", "job": "job"}
}
func (tracedBuild) Status() db.BuildStatus { return db.BuildStatusSucceeded }
func (tracedBuild) StartTime() time.Time   { return time.Unix(0, 0) }
func (tracedBuild) EndTime() time.Time     { return time.Unix(5, 0) }
func (tracedBuild) SpanContext() propagation.TextMapCarrier {
	return propagation.MapCarrier{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
}
