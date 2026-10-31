package gc_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbtest"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/metric"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
)

// reclaimMetrics registers the metric collectors with a registry of its
// own, so a spec reads what the /metrics page would carry.
func reclaimMetrics() *prometheus.Registry {
	GinkgoHelper()

	reg := prometheus.NewRegistry()
	Expect(metric.Register(reg, nil)).To(Succeed())
	return reg
}

// reclaimValue reads the backlog gauge, or the duration histogram's pass
// count.
func reclaimValue(reg *prometheus.Registry, name string) float64 {
	GinkgoHelper()

	families, err := reg.Gather()
	Expect(err).NotTo(HaveOccurred())
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		m := family.GetMetric()[0]
		if m.GetHistogram() != nil {
			return float64(m.GetHistogram().GetSampleCount())
		}
		return m.GetGauge().GetValue()
	}
	Fail("no family " + name)
	return 0
}

const (
	backlogMetric  = "concourse_gc_pipeline_run_reclaim_backlog"
	durationMetric = "concourse_gc_pipeline_run_reclaim_duration"
)

var _ = Describe("PipelineRunReclaimer metrics", func() {
	var (
		lifecycle db.PipelineRunReclaimLifecycle
		template  db.Pipeline
	)

	// Build a template retaining only its newest run, then a run graph whose
	// older runs are all terminal. Every run but the newest is a candidate.
	createTerminalRuns := func(count int) {
		GinkgoHelper()

		keepLast := 1
		var err error
		template, _, err = defaultTeam.SavePipeline(atc.PipelineRef{Name: "reclaim-metrics-template"}, atc.Config{
			Template:     true,
			RunRetention: &atc.RunRetentionConfig{KeepLast: &keepLast},
			Jobs:         atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())

		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		for i := 0; i < count; i++ {
			creation, err := dbtest.CreateRun(dbConn, factory, context.Background(), template, db.RunParams{}, "creator")
			Expect(err).NotTo(HaveOccurred())

			_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE pipeline_run_id = $1`, creation.Run.ID())
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = 'succeeded', completed_at = now(), result_manifest = '{}' WHERE id = $1`, creation.Run.ID())
			Expect(err).NotTo(HaveOccurred())
		}
	}

	BeforeEach(func() {
		lifecycle = db.NewPipelineRunReclaimLifecycle(dbConn)
	})

	It("reports the whole backlog, not just the batch it took", func() {
		createTerminalRuns(22)
		reg := reclaimMetrics()
		passes := reclaimValue(reg, durationMetric)

		reclaimer := gc.NewPipelineRunReclaimer(lifecycle, time.Now)
		Expect(reclaimer.Run(context.Background())).To(Succeed())

		Expect(reclaimValue(reg, backlogMetric)).
			To(Equal(float64(21)), "twenty-one of twenty-two runs are past the retention window")
		Expect(reclaimValue(reg, durationMetric)).To(Equal(passes + 1))
	})

	It("drains the backlog across batches", func() {
		createTerminalRuns(22)
		reg := reclaimMetrics()

		reclaimer := gc.NewPipelineRunReclaimer(lifecycle, time.Now)
		backlogs := []float64{}
		for range 3 {
			Expect(reclaimer.Run(context.Background())).To(Succeed())
			backlogs = append(backlogs, reclaimValue(reg, backlogMetric))
		}

		Expect(backlogs).To(Equal([]float64{21, 1, 0}), "each pass must measure the backlog it found, and reclaiming must shrink it")
	})

	It("reports an empty backlog when there is nothing to reclaim", func() {
		reg := reclaimMetrics()
		metric.PipelineRunReclaimBacklog(5) // a stale level a pass must overwrite

		reclaimer := gc.NewPipelineRunReclaimer(lifecycle, time.Now)
		Expect(reclaimer.Run(context.Background())).To(Succeed())

		Expect(reclaimValue(reg, backlogMetric)).
			To(Equal(float64(0)), "an idle reclaimer must publish zero rather than nothing at all")
	})

	It("counts every candidate the bounded batch could not name", func() {
		createTerminalRuns(22)

		ids, err := lifecycle.ReclaimCandidateRunIDs(2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(HaveLen(2), "the batch stays bounded")

		backlog, err := lifecycle.ReclaimBacklog()
		Expect(err).NotTo(HaveOccurred())
		Expect(backlog).To(Equal(21), "the backlog is not capped by the batch size")
	})
})
