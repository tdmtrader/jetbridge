package gc_test

import (
	"context"
	"slices"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/gc"
	"github.com/concourse/concourse/atc/metric"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingEmitter keeps every event handed to it. The shipped emitters are
// write-only sinks that rewrite what they are given, so a spec that wants to
// see what the reclaimer measured has to supply the sink itself.
type recordingEmitter struct {
	mu     sync.Mutex
	events []metric.Event
}

func (e *recordingEmitter) Emit(_ lager.Logger, event metric.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *recordingEmitter) Events() []metric.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

type recordingEmitterFactory struct{ emitter *recordingEmitter }

func (f recordingEmitterFactory) Description() string { return "recording" }
func (f recordingEmitterFactory) IsConfigured() bool  { return true }
func (f recordingEmitterFactory) NewEmitter(map[string]string) (metric.Emitter, error) {
	return f.emitter, nil
}

// captureMetrics swaps the process-wide Monitor for a recording one and
// restores it afterwards, because a metric Event emits to that global.
func captureMetrics() *recordingEmitter {
	GinkgoHelper()

	emitter := &recordingEmitter{}
	monitor := metric.NewMonitor()
	monitor.RegisterEmitter(recordingEmitterFactory{emitter: emitter})
	Expect(monitor.Initialize(logger, "test", map[string]string{}, 1000)).To(Succeed())

	previous := metric.Metrics
	metric.Metrics = monitor
	DeferCleanup(func() { metric.Metrics = previous })

	return emitter
}

func eventValues(emitter *recordingEmitter, name string) []float64 {
	values := []float64{}
	for _, event := range emitter.Events() {
		if event.Name == name {
			values = append(values, event.Value)
		}
	}
	return values
}

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
			creation, err := factory.CreateRun(context.Background(), template, db.RunParams{}, "creator")
			Expect(err).NotTo(HaveOccurred())

			_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded', completed = true, end_time = now() WHERE pipeline_run_id = $1`, creation.Run.ID())
			Expect(err).NotTo(HaveOccurred())
			_, err = dbConn.Exec(`UPDATE pipeline_runs SET status = 'succeeded', completed_at = now() WHERE id = $1`, creation.Run.ID())
			Expect(err).NotTo(HaveOccurred())
		}
	}

	BeforeEach(func() {
		lifecycle = db.NewPipelineRunReclaimLifecycle(dbConn)
	})

	It("reports the whole backlog, not just the batch it took", func() {
		createTerminalRuns(4)
		emitter := captureMetrics()

		reclaimer := gc.NewPipelineRunReclaimer(lifecycle, time.Now, 2)
		Expect(reclaimer.Run(context.Background())).To(Succeed())

		Eventually(func() []float64 { return eventValues(emitter, "pipeline run reclaim backlog") }).
			Should(Equal([]float64{3}), "three of four runs are past the retention window")

		Eventually(func() []float64 { return eventValues(emitter, "gc: pipeline run reclaim duration (ms)") }).
			Should(HaveLen(1))
	})

	It("drains the backlog across batches", func() {
		createTerminalRuns(4)
		emitter := captureMetrics()

		reclaimer := gc.NewPipelineRunReclaimer(lifecycle, time.Now, 2)
		Expect(reclaimer.Run(context.Background())).To(Succeed())
		Expect(reclaimer.Run(context.Background())).To(Succeed())
		Expect(reclaimer.Run(context.Background())).To(Succeed())

		Eventually(func() []float64 { return eventValues(emitter, "pipeline run reclaim backlog") }).
			Should(Equal([]float64{3, 1, 0}), "each pass must measure the backlog it found, and reclaiming must shrink it")
	})

	It("reports an empty backlog when there is nothing to reclaim", func() {
		emitter := captureMetrics()

		reclaimer := gc.NewPipelineRunReclaimer(lifecycle, time.Now, 20)
		Expect(reclaimer.Run(context.Background())).To(Succeed())

		Eventually(func() []float64 { return eventValues(emitter, "pipeline run reclaim backlog") }).
			Should(Equal([]float64{0}), "an idle reclaimer must publish zero rather than nothing at all")
	})

	It("counts every candidate the bounded batch could not name", func() {
		createTerminalRuns(4)

		ids, err := lifecycle.ReclaimCandidateRunIDs(2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(HaveLen(2), "the batch stays bounded")

		backlog, err := lifecycle.ReclaimBacklog()
		Expect(err).NotTo(HaveOccurred())
		Expect(backlog).To(Equal(3), "the backlog is not capped by the batch size")
	})
})
